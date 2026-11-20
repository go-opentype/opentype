// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"fmt"
	"sort"
)

// This file parses the structures shared by the OpenType Layout tables GSUB
// and GPOS: the ScriptList, FeatureList and LookupList, plus the Coverage and
// ClassDef tables their subtables point at. The lookup subtables themselves are
// table-specific and decoded in gsub.go and gpos.go.
//
// All offsets inside a Layout table are unsigned 16-bit values measured from
// the start of the structure that contains them, so parsing proceeds by
// re-slicing at each offset with subslice and recursing.

// subslice returns b[off:], or nil when off is past the end of b. A nil result
// makes any subsequent bounded read record errTruncated, so callers can defer
// the error check to a single site.
func subslice(b []byte, off int) []byte {
	if off > len(b) {
		return nil
	}
	return b[off:]
}

// tag reads a 4-byte OpenType tag as a string, recording errTruncated when the
// four bytes are not available.
func (r *reader) tag() string {
	if r.pos+4 > len(r.b) {
		r.err = errTruncated
		return ""
	}
	s := string(r.b[r.pos : r.pos+4])
	r.pos += 4
	return s
}

// langSys is a decoded LangSys table: the (optional) required feature index and
// the list of feature indices enabled for a script/language.
type langSys struct {
	required uint16 // 0xFFFF means "no required feature"
	features []uint16
}

// langSysRecord binds a 4-byte language tag to its LangSys table.
type langSysRecord struct {
	tag string
	ls  *langSys
}

// scriptRecord is a decoded Script table: its default LangSys (may be nil) and
// any language-specific LangSys tables, keyed by tag.
type scriptRecord struct {
	tag         string
	defaultLang *langSys
	langs       []langSysRecord
}

// featureRecord is a decoded Feature table: the lookup indices it activates.
type featureRecord struct {
	tag     string
	lookups []uint16
}

// layoutHeader holds the script and feature tables shared by a GSUB or GPOS
// table. It is embedded into gsub and gpos to provide selectLookups.
type layoutHeader struct {
	scripts  []scriptRecord
	features []featureRecord
}

// parseLayoutCommon decodes the version header of a GSUB/GPOS table and its
// ScriptList, FeatureList and LookupList, returning the shared header plus the
// raw bytes of each Lookup table (decoded by the caller per lookup type).
func parseLayoutCommon(data []byte) (layoutHeader, [][]byte, error) {
	r := reader{b: data}
	r.skip(4) // majorVersion, minorVersion (1.1's featureVariations is ignored)
	scriptOff := int(r.u16())
	featureOff := int(r.u16())
	lookupOff := int(r.u16())
	if r.err != nil {
		return layoutHeader{}, nil, fmt.Errorf("opentype: layout header: %w", r.err)
	}
	scripts, err := parseScriptList(subslice(data, scriptOff))
	if err != nil {
		return layoutHeader{}, nil, err
	}
	features, err := parseFeatureList(subslice(data, featureOff))
	if err != nil {
		return layoutHeader{}, nil, err
	}
	lookups, err := parseLookupList(subslice(data, lookupOff))
	if err != nil {
		return layoutHeader{}, nil, err
	}
	return layoutHeader{scripts: scripts, features: features}, lookups, nil
}

// parseScriptList decodes a ScriptList table.
func parseScriptList(b []byte) ([]scriptRecord, error) {
	r := reader{b: b}
	count := int(r.u16())
	type rec struct {
		tag string
		off int
	}
	if err := r.room("scriptList", "scriptCount", uint64(count), 6); err != nil {
		return nil, err
	}
	recs := make([]rec, count)
	for i := 0; i < count; i++ {
		recs[i].tag = r.tag()
		recs[i].off = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: scriptList: %w", r.err)
	}
	out := make([]scriptRecord, count)
	// Records may share one Script table: decode each distinct offset once, so
	// 65535 records pointing at one Script cost one Script (issue #75).
	seen := map[int]scriptRecord{}
	for i, rc := range recs {
		sc, ok := seen[rc.off]
		if !ok {
			var err error
			sc, err = parseScript(subslice(b, rc.off))
			if err != nil {
				return nil, err
			}
			seen[rc.off] = sc
		}
		sc.tag = rc.tag
		out[i] = sc
	}
	return out, nil
}

// parseScript decodes a Script table (its default and language LangSys tables).
func parseScript(b []byte) (scriptRecord, error) {
	r := reader{b: b}
	defOff := int(r.u16())
	langCount := int(r.u16())
	type lr struct {
		tag string
		off int
	}
	if err := r.room("script", "langSysCount", uint64(langCount), 6); err != nil {
		return scriptRecord{}, err
	}
	lrs := make([]lr, langCount)
	for i := 0; i < langCount; i++ {
		lrs[i].tag = r.tag()
		lrs[i].off = int(r.u16())
	}
	if r.err != nil {
		return scriptRecord{}, fmt.Errorf("opentype: script: %w", r.err)
	}
	var sc scriptRecord
	if defOff != 0 {
		ls, err := parseLangSys(subslice(b, defOff))
		if err != nil {
			return scriptRecord{}, err
		}
		sc.defaultLang = ls
	}
	sc.langs = make([]langSysRecord, langCount)
	seen := map[int]*langSys{} // records may share one LangSys
	for i, l := range lrs {
		ls, ok := seen[l.off]
		if !ok {
			var err error
			ls, err = parseLangSys(subslice(b, l.off))
			if err != nil {
				return scriptRecord{}, err
			}
			seen[l.off] = ls
		}
		sc.langs[i] = langSysRecord{tag: l.tag, ls: ls}
	}
	return sc, nil
}

// parseLangSys decodes a LangSys table.
func parseLangSys(b []byte) (*langSys, error) {
	r := reader{b: b}
	r.skip(2) // lookupOrderOffset (reserved, always 0)
	required := r.u16()
	n := int(r.u16())
	if err := r.room("langSys", "featureIndexCount", uint64(n), 2); err != nil {
		return nil, err
	}
	feats := make([]uint16, n)
	for i := 0; i < n; i++ {
		feats[i] = r.u16()
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: langSys: %w", r.err)
	}
	return &langSys{required: required, features: feats}, nil
}

// parseFeatureList decodes a FeatureList table.
func parseFeatureList(b []byte) ([]featureRecord, error) {
	r := reader{b: b}
	count := int(r.u16())
	type rec struct {
		tag string
		off int
	}
	if err := r.room("featureList", "featureCount", uint64(count), 6); err != nil {
		return nil, err
	}
	recs := make([]rec, count)
	for i := 0; i < count; i++ {
		recs[i].tag = r.tag()
		recs[i].off = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: featureList: %w", r.err)
	}
	out := make([]featureRecord, count)
	seen := map[int]featureRecord{} // records may share one Feature table
	for i, rc := range recs {
		fr, ok := seen[rc.off]
		if !ok {
			var err error
			fr, err = parseFeature(subslice(b, rc.off))
			if err != nil {
				return nil, err
			}
			seen[rc.off] = fr
		}
		fr.tag = rc.tag
		out[i] = fr
	}
	return out, nil
}

// parseFeature decodes a Feature table (its lookup index list).
func parseFeature(b []byte) (featureRecord, error) {
	r := reader{b: b}
	r.skip(2) // featureParamsOffset (unused here)
	n := int(r.u16())
	if err := r.room("feature", "lookupIndexCount", uint64(n), 2); err != nil {
		return featureRecord{}, err
	}
	idx := make([]uint16, n)
	for i := 0; i < n; i++ {
		idx[i] = r.u16()
	}
	if r.err != nil {
		return featureRecord{}, fmt.Errorf("opentype: feature: %w", r.err)
	}
	return featureRecord{lookups: idx}, nil
}

// parseLookupList decodes a LookupList table into the raw bytes of each Lookup
// table. Type-specific decoding happens in gsub.go and gpos.go.
func parseLookupList(b []byte) ([][]byte, error) {
	r := reader{b: b}
	count := int(r.u16())
	if err := r.room("lookupList", "lookupCount", uint64(count), 2); err != nil {
		return nil, err
	}
	offs := make([]int, count)
	for i := 0; i < count; i++ {
		offs[i] = int(r.u16())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: lookupList: %w", r.err)
	}

	out := make([][]byte, count)
	for i, off := range offs {
		out[i] = subslice(b, off)
	}
	return out, nil
}

// findScript resolves a script tag, falling back to "DFLT" and then to the
// first script present, matching the behaviour of common shapers.
func (h *layoutHeader) findScript(tag string) *scriptRecord {
	for i := range h.scripts {
		if h.scripts[i].tag == tag {
			return &h.scripts[i]
		}
	}
	for i := range h.scripts {
		if h.scripts[i].tag == "DFLT" {
			return &h.scripts[i]
		}
	}
	if len(h.scripts) > 0 {
		return &h.scripts[0]
	}
	return nil
}

// findLang resolves a language tag within a script, falling back to the script's
// default LangSys.
func (s *scriptRecord) findLang(tag string) *langSys {
	for i := range s.langs {
		if s.langs[i].tag == tag {
			return s.langs[i].ls
		}
	}
	return s.defaultLang
}

// selectLookups returns the lookup indices activated by the wanted feature tags
// for a (script, language) pair, in ascending lookup order and de-duplicated.
// The required feature (if any) is included when its tag is wanted.
func (h *layoutHeader) selectLookups(script, lang string, want map[string]bool) []int {
	sr := h.findScript(script)
	if sr == nil {
		return nil
	}
	ls := sr.findLang(lang)
	if ls == nil {
		return nil
	}
	seen := map[int]bool{}
	var out []int
	add := func(featIdx uint16) {
		if int(featIdx) >= len(h.features) {
			return
		}
		fr := h.features[featIdx]
		if !want[fr.tag] {
			return
		}
		for _, li := range fr.lookups {
			if !seen[int(li)] {
				seen[int(li)] = true
				out = append(out, int(li))
			}
		}
	}
	if ls.required != 0xFFFF {
		add(ls.required)
	}
	for _, fi := range ls.features {
		add(fi)
	}
	sort.Ints(out)
	return out
}
