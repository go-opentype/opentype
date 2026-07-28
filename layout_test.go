// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"errors"
	"reflect"
	"testing"
)

// This file synthesises minimal-but-valid GSUB/GPOS/kern tables in memory (in
// the spirit of synth_test.go) so the layout tests never depend on a font file
// and can exercise every parse and apply branch deterministically.

// --- ScriptList / FeatureList / LookupList builders -------------------------

type tLangSys struct {
	required uint16
	features []uint16
}

type tLang struct {
	tag string
	ls  tLangSys
}

type tScript struct {
	tag   string
	def   *tLangSys
	langs []tLang
}

type tFeature struct {
	tag     string
	lookups []uint16
}

func buildLangSys(ls tLangSys) []byte {
	w := &bw{}
	w.u16(0) // lookupOrderOffset
	w.u16(ls.required)
	w.u16(uint16(len(ls.features)))
	for _, f := range ls.features {
		w.u16(f)
	}
	return w.bytes()
}

func buildScript(s tScript) []byte {
	header := 4 + 6*len(s.langs)
	body := &bw{}
	defOff := 0
	if s.def != nil {
		defOff = header + len(body.b)
		body.b = append(body.b, buildLangSys(*s.def)...)
	}
	type lrec struct {
		tag string
		off int
	}
	var lrecs []lrec
	for _, l := range s.langs {
		off := header + len(body.b)
		body.b = append(body.b, buildLangSys(l.ls)...)
		lrecs = append(lrecs, lrec{l.tag, off})
	}
	w := &bw{}
	w.u16(uint16(defOff))
	w.u16(uint16(len(s.langs)))
	for _, lr := range lrecs {
		w.b = append(w.b, []byte(lr.tag)...)
		w.u16(uint16(lr.off))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

func buildScriptList(scripts []tScript) []byte {
	header := 2 + 6*len(scripts)
	body := &bw{}
	type srec struct {
		tag string
		off int
	}
	var srecs []srec
	for _, s := range scripts {
		off := header + len(body.b)
		body.b = append(body.b, buildScript(s)...)
		srecs = append(srecs, srec{s.tag, off})
	}
	w := &bw{}
	w.u16(uint16(len(scripts)))
	for _, sr := range srecs {
		w.b = append(w.b, []byte(sr.tag)...)
		w.u16(uint16(sr.off))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

func buildFeature(f tFeature) []byte {
	w := &bw{}
	w.u16(0) // featureParamsOffset
	w.u16(uint16(len(f.lookups)))
	for _, l := range f.lookups {
		w.u16(l)
	}
	return w.bytes()
}

func buildFeatureList(feats []tFeature) []byte {
	header := 2 + 6*len(feats)
	body := &bw{}
	type frec struct {
		tag string
		off int
	}
	var frecs []frec
	for _, f := range feats {
		off := header + len(body.b)
		body.b = append(body.b, buildFeature(f)...)
		frecs = append(frecs, frec{f.tag, off})
	}
	w := &bw{}
	w.u16(uint16(len(feats)))
	for _, fr := range frecs {
		w.b = append(w.b, []byte(fr.tag)...)
		w.u16(uint16(fr.off))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

func buildLookupList(lookups [][]byte) []byte {
	header := 2 + 2*len(lookups)
	body := &bw{}
	var offs []int
	for _, lk := range lookups {
		offs = append(offs, header+len(body.b))
		body.b = append(body.b, lk...)
	}
	w := &bw{}
	w.u16(uint16(len(lookups)))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// buildLayoutTable assembles a GSUB/GPOS table (version 1.0) from a scriptList,
// featureList and lookupList.
func buildLayoutTable(scripts []tScript, feats []tFeature, lookups [][]byte) []byte {
	sl := buildScriptList(scripts)
	fl := buildFeatureList(feats)
	ll := buildLookupList(lookups)
	scriptOff := 10
	featureOff := scriptOff + len(sl)
	lookupOff := featureOff + len(fl)
	w := &bw{}
	w.u16(1) // majorVersion
	w.u16(0) // minorVersion
	w.u16(uint16(scriptOff))
	w.u16(uint16(featureOff))
	w.u16(uint16(lookupOff))
	w.b = append(w.b, sl...)
	w.b = append(w.b, fl...)
	w.b = append(w.b, ll...)
	return w.bytes()
}

// buildLookup wraps subtables of one lookup type into a Lookup table.
func buildLookup(lookupType uint16, subtables [][]byte) []byte {
	header := 6 + 2*len(subtables)
	body := &bw{}
	var offs []int
	for _, st := range subtables {
		offs = append(offs, header+len(body.b))
		body.b = append(body.b, st...)
	}
	w := &bw{}
	w.u16(lookupType)
	w.u16(0) // lookupFlag
	w.u16(uint16(len(subtables)))
	for _, o := range offs {
		w.u16(uint16(o))
	}
	w.b = append(w.b, body.b...)
	return w.bytes()
}

// --- Coverage / ClassDef builders -------------------------------------------

func buildCoverage1(glyphs ...GlyphIndex) []byte {
	w := &bw{}
	w.u16(1)
	w.u16(uint16(len(glyphs)))
	for _, g := range glyphs {
		w.u16(uint16(g))
	}
	return w.bytes()
}

// buildCoverage2 takes {start, end, startCoverageIndex} ranges.
func buildCoverage2(ranges ...[3]int) []byte {
	w := &bw{}
	w.u16(2)
	w.u16(uint16(len(ranges)))
	for _, r := range ranges {
		w.u16(uint16(r[0]))
		w.u16(uint16(r[1]))
		w.u16(uint16(r[2]))
	}
	return w.bytes()
}

func buildClassDef1(start int, classes ...uint16) []byte {
	w := &bw{}
	w.u16(1)
	w.u16(uint16(start))
	w.u16(uint16(len(classes)))
	for _, c := range classes {
		w.u16(c)
	}
	return w.bytes()
}

// buildClassDef2 takes {start, end, class} ranges.
func buildClassDef2(ranges ...[3]int) []byte {
	w := &bw{}
	w.u16(2)
	w.u16(uint16(len(ranges)))
	for _, r := range ranges {
		w.u16(uint16(r[0]))
		w.u16(uint16(r[1]))
		w.u16(uint16(r[2]))
	}
	return w.bytes()
}

// --- layout common parse tests ----------------------------------------------

// simpleScripts returns a DFLT script whose default LangSys enables the given
// feature indices, with no required feature.
func simpleScripts(featureIdx ...uint16) []tScript {
	return []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: featureIdx}}}
}

func TestParseCoverageFormats(t *testing.T) {
	// Format 1.
	m1, err := parseCoverage(buildCoverage1(10, 20, 30))
	if err != nil {
		t.Fatalf("coverage1: %v", err)
	}
	if m1[10] != 0 || m1[20] != 1 || m1[30] != 2 {
		t.Errorf("coverage1 indices = %v", m1)
	}
	// Format 2.
	m2, err := parseCoverage(buildCoverage2([3]int{5, 7, 0}, [3]int{100, 100, 3}))
	if err != nil {
		t.Fatalf("coverage2: %v", err)
	}
	if m2[5] != 0 || m2[6] != 1 || m2[7] != 2 || m2[100] != 3 {
		t.Errorf("coverage2 indices = %v", m2)
	}
}

func TestParseCoverageErrors(t *testing.T) {
	if _, err := parseCoverage([]byte{0, 9}); err == nil { // format 9
		t.Error("bad coverage format should error")
	}
	if _, err := parseCoverage([]byte{0, 1, 0}); err == nil { // truncated count
		t.Error("truncated coverage should error")
	}
}

func TestParseClassDefFormats(t *testing.T) {
	c1, err := parseClassDef(buildClassDef1(10, 1, 2, 0))
	if err != nil {
		t.Fatalf("classdef1: %v", err)
	}
	if c1[10] != 1 || c1[11] != 2 || c1[12] != 0 || c1[99] != 0 {
		t.Errorf("classdef1 = %v", c1)
	}
	c2, err := parseClassDef(buildClassDef2([3]int{5, 6, 3}, [3]int{20, 20, 7}))
	if err != nil {
		t.Fatalf("classdef2: %v", err)
	}
	if c2[5] != 3 || c2[6] != 3 || c2[20] != 7 {
		t.Errorf("classdef2 = %v", c2)
	}
}

func TestParseClassDefErrors(t *testing.T) {
	if _, err := parseClassDef([]byte{0, 9}); err == nil { // format 9
		t.Error("bad classdef format should error")
	}
	if _, err := parseClassDef([]byte{0, 1, 0, 0, 0}); err == nil { // truncated
		t.Error("truncated classdef should error")
	}
}

func TestParseLayoutHeaderErrors(t *testing.T) {
	// Truncated header.
	if _, _, err := parseLayoutCommon([]byte{0, 1, 0}); err == nil {
		t.Error("truncated layout header should error")
	}
	// Corrupt each of the three list offsets (script@4, feature@6, lookup@8) in
	// the 10-byte header in turn so it points past the end.
	good := buildLayoutTable(simpleScripts(0),
		[]tFeature{{tag: "liga", lookups: []uint16{0}}}, [][]byte{buildLookup(1, nil)})
	for name, off := range map[string]int{"script": 4, "feature": 6, "lookup": 8} {
		bad := append([]byte(nil), good...)
		bad[off] = 0xFF
		bad[off+1] = 0xFF // offset 0xFFFF, past the end
		if _, _, err := parseLayoutCommon(bad); err == nil {
			t.Errorf("%s: bad offset should error", name)
		}
	}
}

func TestSelectLookups(t *testing.T) {
	// One script (DFLT) with a required feature (idx 0 = "liga") plus two
	// listed features (idx 1 = "calt", idx 2 = "kern"); liga and calt share
	// lookup 0 to exercise de-duplication, calt also has lookup 1.
	scripts := []tScript{{tag: "DFLT", def: &tLangSys{
		required: 0,
		features: []uint16{1, 2, 99}, // 99 is an out-of-range feature index
	}}}
	feats := []tFeature{
		{tag: "liga", lookups: []uint16{0}},
		{tag: "calt", lookups: []uint16{0, 1}},
		{tag: "kern", lookups: []uint16{2}},
	}
	h := layoutHeader{
		scripts:  mustScripts(t, scripts),
		features: mustFeatures(t, feats),
	}
	got := h.selectLookups("DFLT", "", map[string]bool{"liga": true, "calt": true})
	if !reflect.DeepEqual(got, []int{0, 1}) {
		t.Errorf("selectLookups = %v want [0 1]", got)
	}
	// A wanted tag that is not present selects nothing.
	if got := h.selectLookups("DFLT", "", map[string]bool{"zzzz": true}); got != nil {
		t.Errorf("unknown feature selected %v", got)
	}
}

func TestSelectLookupsScriptAndLangFallback(t *testing.T) {
	scripts := []tScript{
		{tag: "latn", def: &tLangSys{required: 0xFFFF, features: []uint16{0}},
			langs: []tLang{{tag: "TRK ", ls: tLangSys{required: 0xFFFF, features: []uint16{0}}}}},
	}
	feats := []tFeature{{tag: "liga", lookups: []uint16{5}}}
	h := layoutHeader{scripts: mustScripts(t, scripts), features: mustFeatures(t, feats)}

	// Requested script "grek" is absent and there is no DFLT, so the first
	// script (latn) is used; requested language "XXX " is absent so the
	// default LangSys is used.
	if got := h.selectLookups("grek", "XXX ", map[string]bool{"liga": true}); !reflect.DeepEqual(got, []int{5}) {
		t.Errorf("fallback selectLookups = %v want [5]", got)
	}
	// An explicit, present language tag is honoured.
	if got := h.selectLookups("latn", "TRK ", map[string]bool{"liga": true}); !reflect.DeepEqual(got, []int{5}) {
		t.Errorf("lang selectLookups = %v want [5]", got)
	}
}

func TestSelectLookupsNoScript(t *testing.T) {
	h := layoutHeader{} // no scripts at all
	if got := h.selectLookups("DFLT", "", map[string]bool{"liga": true}); got != nil {
		t.Errorf("empty header selected %v", got)
	}
}

func TestSelectLookupsNoLang(t *testing.T) {
	// A script with neither a default LangSys nor the requested language.
	scripts := mustScripts(t, []tScript{{tag: "DFLT", langs: []tLang{
		{tag: "AAA ", ls: tLangSys{required: 0xFFFF, features: []uint16{0}}},
	}}})
	h := layoutHeader{scripts: scripts, features: mustFeatures(t, []tFeature{{tag: "liga", lookups: []uint16{0}}})}
	if got := h.selectLookups("DFLT", "ZZZ ", map[string]bool{"liga": true}); got != nil {
		t.Errorf("no-lang selected %v", got)
	}
}

func TestParseScriptDefaultAbsent(t *testing.T) {
	// A script whose defaultLangSysOffset is 0 (no default) but which carries a
	// language record, to cover the defOff==0 path in parseScript.
	scripts := mustScripts(t, []tScript{{tag: "DFLT", langs: []tLang{
		{tag: "AAA ", ls: tLangSys{required: 0xFFFF, features: []uint16{0}}},
	}}})
	if scripts[0].defaultLang != nil {
		t.Error("expected nil default LangSys")
	}
	if len(scripts[0].langs) != 1 || scripts[0].langs[0].tag != "AAA " {
		t.Errorf("langs = %+v", scripts[0].langs)
	}
}

func TestFindScriptDFLTFallback(t *testing.T) {
	// An absent requested script falls back to the DFLT script (not the first).
	scripts := mustScripts(t, []tScript{
		{tag: "latn", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}},
		{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}},
	})
	h := layoutHeader{scripts: scripts, features: mustFeatures(t, []tFeature{{tag: "liga", lookups: []uint16{7}}})}
	if got := h.selectLookups("grek", "", map[string]bool{"liga": true}); !reflect.DeepEqual(got, []int{7}) {
		t.Errorf("DFLT fallback = %v want [7]", got)
	}
}

func TestParseScriptLangRecordError(t *testing.T) {
	// A Script table with no default LangSys but a language record whose
	// LangSys offset is past the end.
	w := &bw{}
	w.u16(0) // defaultLangSysOffset (none)
	w.u16(1) // langSysCount
	w.b = append(w.b, []byte("AAA ")...)
	w.u16(0xFFFF) // bad LangSys offset
	if _, err := parseScript(w.bytes()); err == nil {
		t.Error("bad language LangSys offset should error")
	}
}

// mustScripts parses a ScriptList blob, failing the test on error.
func mustScripts(t *testing.T, s []tScript) []scriptRecord {
	t.Helper()
	out, err := parseScriptList(buildScriptList(s))
	if err != nil {
		t.Fatalf("parseScriptList: %v", err)
	}
	return out
}

// mustFeatures parses a FeatureList blob, failing the test on error.
func mustFeatures(t *testing.T, f []tFeature) []featureRecord {
	t.Helper()
	out, err := parseFeatureList(buildFeatureList(f))
	if err != nil {
		t.Fatalf("parseFeatureList: %v", err)
	}
	return out
}

func TestLayoutParseTruncations(t *testing.T) {
	// Each of these blobs is a structurally valid outer table with one inner
	// structure truncated, exercising the deferred error checks.
	cases := map[string][]byte{
		"scriptRecord": buildLayoutTableTrunc(t, "scriptRecord"),
		"script":       buildLayoutTableTrunc(t, "script"),
		"langSys":      buildLayoutTableTrunc(t, "langSys"),
		"featureList":  buildLayoutTableTrunc(t, "featureList"),
		"feature":      buildLayoutTableTrunc(t, "feature"),
		"lookupList":   buildLayoutTableTrunc(t, "lookupList"),
	}
	for name, blob := range cases {
		if _, _, err := parseLayoutCommon(blob); err == nil {
			t.Errorf("%s: truncation should error", name)
		} else if !errors.Is(err, errTruncated) {
			t.Errorf("%s: err = %v want errTruncated", name, err)
		}
	}
}

// buildLayoutTableTrunc builds a layout table and then truncates it so a chosen
// inner structure runs off the end.
func buildLayoutTableTrunc(t *testing.T, which string) []byte {
	t.Helper()
	scripts := []tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}}
	feats := []tFeature{{tag: "liga", lookups: []uint16{0}}}
	lookups := [][]byte{buildLookup(1, nil)}
	full := buildLayoutTable(scripts, feats, lookups)
	switch which {
	case "scriptRecord":
		// Keep the header and scriptList count but cut the record.
		return full[:12]
	case "script":
		// Cut inside the Script table (after the scriptList records).
		return full[:20]
	case "langSys":
		// Cut inside the LangSys table.
		return full[:24]
	case "feature", "featureList":
		featOff := int(be16(full[6:]))
		if which == "featureList" {
			return full[:featOff+1] // count byte only
		}
		// Keep the feature count and record (8 bytes) but cut the feature body
		// they point at, so parseFeature reads off the end.
		return full[:featOff+8]
	case "lookupList":
		lkOff := int(be16(full[8:]))
		return full[:lkOff+1]
	}
	t.Fatalf("unknown truncation %q", which)
	return nil
}
