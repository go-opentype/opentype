// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// This file implements CFF (Type2 charstring) subsetting: rebuilding a 'CFF '
// table that keeps the outlines of a requested set of glyphs and replaces every
// other glyph's charstring with an empty one (a lone endchar). It is a real
// charstring subset — the CharStrings INDEX, the bulk of a CFF program, no longer
// carries the dropped glyphs' outlines — while glyph numbering is preserved, so a
// PDF embeds the result with an Identity CIDToGIDMap and the font's charset,
// encoding, Private DICT and subroutines all stay valid unchanged.
//
// Scope (documented, deliberate): the global and local subroutine INDEXes are
// retained whole rather than tree-shaken; CID-keyed CFF (a ROS/FDArray/FDSelect
// font) and CFF2 are rejected with an error rather than subset; and endchar-seac
// accent composition (a deprecated CFF feature) is not followed, so a caller
// embedding a seac-built accented glyph should also request its base and accent
// components. These edges keep the subsetter tractable and exact for the common
// name-keyed OTF, which is what PDF and EPUB embedders overwhelmingly handle.

// dictRawEntry is one operator of a CFF DICT together with the raw bytes of its
// operands and their decoded values. The raw bytes let a DICT be re-emitted
// verbatim except for the operators whose operands the subsetter rewrites (the
// offset operators of the Top DICT); copying them avoids decoding and re-encoding
// real-number operands (such as FontMatrix), which would not round-trip exactly.
// The decoded values let the subsetter read the offset and flag operators it acts
// on without a second parse pass.
type dictRawEntry struct {
	operands []byte
	values   []float64
	op       int
}

// parseDictRaw splits a CFF DICT into its operator entries, each carrying the raw
// operand bytes that preceded the operator and their decoded values. It reuses
// parseDictOperand for the per-operand byte lengths and values, so it accepts
// exactly the operands parseDict does.
func parseDictRaw(b []byte) ([]dictRawEntry, error) {
	var entries []dictRawEntry
	opStart := 0
	var values []float64
	i := 0
	for i < len(b) {
		b0 := b[i]
		if b0 <= 21 {
			operands := b[opStart:i]
			op := int(b0)
			i++
			if b0 == 12 {
				if i >= len(b) {
					return nil, fmt.Errorf("opentype: cff dict: truncated operator")
				}
				op = 1200 + int(b[i])
				i++
			}
			entries = append(entries, dictRawEntry{operands: operands, values: values, op: op})
			opStart = i
			values = nil
			continue
		}
		v, n, err := parseDictOperand(b, i)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
		i += n
	}
	return entries, nil
}

// findEntry returns the DICT entry for operator op, or nil when the operator is
// absent.
func findEntry(entries []dictRawEntry, op int) *dictRawEntry {
	for i := range entries {
		if entries[i].op == op {
			return &entries[i]
		}
	}
	return nil
}

// encodeCFFInt5 encodes v as a five-byte CFF DICT integer operand (marker 29 and
// a 32-bit big-endian value). A fixed five-byte width keeps the Top DICT the same
// size no matter what offset value it holds, which lets the layout be computed in
// one pass: the offsets an operand carries do not change the size of the DICT that
// carries them.
func encodeCFFInt5(v int) []byte {
	return []byte{29, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
}

// encodeCFFOp encodes a DICT operator (a one-byte operator, or the escape byte 12
// followed by the second byte for a two-byte operator keyed as 1200+second).
func encodeCFFOp(op int) []byte {
	if op >= 1200 {
		return []byte{12, byte(op - 1200)}
	}
	return []byte{byte(op)}
}

// topDictOffsets carries the rebuilt Top DICT's offset operands. charset is -1
// when the original charset is predefined (its operand is copied unchanged);
// otherwise it is the new absolute offset of the relocated charset. hasPrivate is
// false when the font has no Private DICT (no op-18 entry is emitted then).
type topDictOffsets struct {
	charset      int
	charStrings  int
	privateSize  int
	privateOff   int
	hasPrivate   bool
	charsetIsPre bool
}

// encodeTopDict re-emits the Top DICT, copying every operator verbatim except the
// ones that carry absolute offsets: CharStrings (17) and Private (18) get the new
// offsets, charset (15) gets its new offset unless it is predefined, and Encoding
// (16) is forced to the predefined Standard encoding (0) since a custom encoding
// is neither relocated nor needed for embedding. All rewritten offset operands use
// the fixed five-byte form, so the encoded size does not depend on the offsets.
func encodeTopDict(entries []dictRawEntry, off topDictOffsets) []byte {
	var out []byte
	for _, e := range entries {
		switch e.op {
		case 15: // charset
			if off.charsetIsPre {
				out = append(out, e.operands...)
			} else {
				out = append(out, encodeCFFInt5(off.charset)...)
			}
			out = append(out, encodeCFFOp(15)...)
		case 16: // Encoding -> Standard
			out = append(out, encodeCFFInt5(0)...)
			out = append(out, encodeCFFOp(16)...)
		case 17: // CharStrings
			out = append(out, encodeCFFInt5(off.charStrings)...)
			out = append(out, encodeCFFOp(17)...)
		case 18: // Private [size offset]
			out = append(out, encodeCFFInt5(off.privateSize)...)
			out = append(out, encodeCFFInt5(off.privateOff)...)
			out = append(out, encodeCFFOp(18)...)
		default:
			out = append(out, e.operands...)
			out = append(out, encodeCFFOp(e.op)...)
		}
	}
	return out
}

// writeCFFIndex encodes items as a CFF INDEX (a count, an offset size, count+1
// relative offsets, then the concatenated items). An empty item list encodes as a
// count of zero, the shortest valid INDEX.
func writeCFFIndex(items [][]byte) []byte {
	count := len(items)
	out := []byte{byte(count >> 8), byte(count)}
	if count == 0 {
		return out
	}
	offs := make([]int, count+1)
	offs[0] = 1
	for i, it := range items {
		offs[i+1] = offs[i] + len(it)
	}
	offSize := 1
	for v := offs[count]; v > 0xFF; v >>= 8 {
		offSize++
	}
	out = append(out, byte(offSize))
	for _, o := range offs {
		for b := offSize - 1; b >= 0; b-- {
			out = append(out, byte(o>>(8*b)))
		}
	}
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

// cffIndexEnd returns the byte offset just past the CFF INDEX beginning at pos.
func cffIndexEnd(data []byte, pos int) (int, error) {
	r := &reader{b: data, pos: pos}
	if _, err := parseIndex(r); err != nil {
		return 0, err
	}
	return r.pos, nil
}

// cffCharsetLength returns the byte length of the (non-predefined) charset at off
// for a font of nGlyphs glyphs, decoding format 0, 1 or 2 exactly as parseCharset
// walks them.
func cffCharsetLength(data []byte, off, nGlyphs int) (int, error) {
	if off > len(data) {
		return 0, fmt.Errorf("opentype: cff charset offset out of range")
	}
	r := &reader{b: data, pos: off}
	format := r.u8()
	gid := 1
	switch format {
	case 0:
		for gid < nGlyphs {
			r.u16()
			gid++
		}
	case 1:
		for gid < nGlyphs {
			r.u16()
			gid += int(r.u8()) + 1
		}
	case 2:
		for gid < nGlyphs {
			r.u16()
			gid += int(r.u16()) + 1
		}
	default:
		return 0, fmt.Errorf("opentype: cff charset: unsupported format %d", format)
	}
	if r.err != nil {
		return 0, fmt.Errorf("opentype: cff charset: %w", r.err)
	}
	return r.pos - off, nil
}

// cffPrivateBlob returns a copy of the Private DICT at [privOff, privOff+privSize)
// together with its Local Subr INDEX when it declares one, as a single contiguous
// blob. The blob preserves the byte distance between the Private DICT and its
// local subrs, so the DICT's (relative) Subrs offset stays valid after the blob is
// relocated as a whole.
func cffPrivateBlob(data []byte, privOff, privSize int) ([]byte, error) {
	if privOff < 0 || privSize < 0 || privOff+privSize > len(data) {
		return nil, fmt.Errorf("opentype: cff: Private DICT out of range")
	}
	priv, err := parseDict(data[privOff : privOff+privSize])
	if err != nil {
		return nil, err
	}
	soff, ok := dictInt(priv, 19, 0)
	if !ok {
		return append([]byte(nil), data[privOff:privOff+privSize]...), nil
	}
	subrAbs := privOff + soff
	if subrAbs < 0 || subrAbs > len(data) {
		return nil, fmt.Errorf("opentype: cff: Local Subrs offset out of range")
	}
	end, err := cffIndexEnd(data, subrAbs)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), data[privOff:end]...), nil
}

// SubsetCFF builds a subset 'CFF ' table (the raw font program, as a PDF /FontFile3
// CIDFontType0C stream embeds it) that keeps the charstrings of the glyphs in gids
// plus glyph 0, replacing every other glyph's charstring with an empty one. Glyph
// numbering is preserved, so the font's charset, encoding, Private DICT and
// subroutines remain valid and a PDF embeds the result with an Identity CIDToGIDMap
// (no glyph-id remap is needed, unlike SubsetTrueType).
//
// It returns an error for a font with no CFF table (a TrueType font — use
// SubsetTrueType), for a CFF2 (variable) font, for a CID-keyed CFF (one with a ROS,
// FDArray or FDSelect operator), or when a requested glyph id is out of range. See
// the file-level comment for the retained-subroutine and endchar-seac scope notes.
func (f *Font) SubsetCFF(gids []GlyphIndex) ([]byte, error) {
	if f.cff2 != nil {
		return nil, fmt.Errorf("opentype: SubsetCFF: CFF2 (variable) fonts are not supported; bake an instance first")
	}
	if f.cff == nil {
		return nil, fmt.Errorf("opentype: SubsetCFF: font has no CFF table (not a CFF/OpenType font)")
	}
	// f.cff being non-nil means Parse decoded a 'CFF ' table, which it also
	// retained in f.tables, so the lookup always succeeds here.
	return subsetCFFTable(f.tables["CFF "], f.cff, gids)
}

// subsetCFFTable rebuilds the CFF table bytes: header and the Name, String and
// Global Subr INDEXes are copied verbatim; the Top DICT is re-emitted with new
// offsets; the CharStrings INDEX is rebuilt keeping the requested glyphs; and the
// (relocated) charset and Private+Local-Subr blobs are appended. The layout is
// header, Name INDEX, Top DICT INDEX, String INDEX, Global Subr INDEX, charset,
// CharStrings INDEX, Private+Local Subrs.
func subsetCFFTable(data []byte, cff *cffTable, gids []GlyphIndex) ([]byte, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("opentype: cff header: %w", errTruncated)
	}
	hdrSize := int(data[2])
	if hdrSize < 4 || hdrSize > len(data) {
		return nil, fmt.Errorf("opentype: cff: bad hdrSize %d", hdrSize)
	}

	// Re-walk the top-level INDEXes to recover their raw byte spans (Name, Top
	// DICT, String, Global Subr), which are copied unchanged into the subset.
	r := &reader{b: data, pos: hdrSize}
	nameStart := r.pos
	if _, err := parseIndex(r); err != nil {
		return nil, err
	}
	topStart := r.pos
	topDicts, err := parseIndex(r)
	if err != nil {
		return nil, err
	}
	if len(topDicts) < 1 {
		return nil, fmt.Errorf("opentype: cff: empty Top DICT INDEX")
	}
	strStart := r.pos
	if _, err := parseIndex(r); err != nil {
		return nil, err
	}
	gsubrStart := r.pos
	if _, err := parseIndex(r); err != nil {
		return nil, err
	}
	gsubrEnd := r.pos
	nameRaw := data[nameStart:topStart]
	strRaw := data[strStart:gsubrStart]
	gsubrRaw := data[gsubrStart:gsubrEnd]

	entries, err := parseDictRaw(topDicts[0])
	if err != nil {
		return nil, err
	}
	// CID-keyed CFF (ROS/FDArray/FDSelect) is out of scope: its charset and
	// FDSelect index the same glyph ids, and its Font DICTs carry their own
	// Private offsets, none of which this preserve-numbering subsetter rewrites.
	if findEntry(entries, 1230) != nil {
		return nil, fmt.Errorf("opentype: SubsetCFF: CID-keyed CFF is not supported")
	}
	if findEntry(entries, 1236) != nil {
		return nil, fmt.Errorf("opentype: SubsetCFF: CID-keyed CFF (FDArray) is not supported")
	}

	n := len(cff.charStrings)
	kept := map[GlyphIndex]bool{0: true}
	for _, g := range gids {
		if int(g) >= n {
			return nil, fmt.Errorf("opentype: SubsetCFF: glyph index %d out of range", int(g))
		}
		kept[g] = true
	}
	items := make([][]byte, n)
	for gid := 0; gid < n; gid++ {
		if kept[GlyphIndex(gid)] {
			items[gid] = cff.charStrings[gid]
		} else {
			items[gid] = []byte{14} // endchar: an empty glyph
		}
	}
	newCharStrings := writeCFFIndex(items)

	// Resolve the charset: predefined (offset <= 2) charsets are copied by operand
	// and need no blob; a custom charset is relocated and its length measured.
	var charsetBlob []byte
	offsets := topDictOffsets{charset: -1, charsetIsPre: true}
	if e := findEntry(entries, 15); e != nil && len(e.values) > 0 && int(e.values[0]) > 2 {
		csOff := int(e.values[0])
		csLen, err := cffCharsetLength(data, csOff, n)
		if err != nil {
			return nil, err
		}
		charsetBlob = append([]byte(nil), data[csOff:csOff+csLen]...)
		offsets.charsetIsPre = false
	}

	// Resolve the Private DICT and its Local Subrs into one relocatable blob.
	var privBlob []byte
	if pv := findEntry(entries, 18); pv != nil {
		if len(pv.values) < 2 {
			return nil, fmt.Errorf("opentype: cff: bad Private DICT entry")
		}
		privSize, privOff := int(pv.values[0]), int(pv.values[1])
		privBlob, err = cffPrivateBlob(data, privOff, privSize)
		if err != nil {
			return nil, err
		}
		offsets.hasPrivate = true
		offsets.privateSize = privSize
	}

	// Measure the Top DICT INDEX (fixed size thanks to five-byte offset operands),
	// then compute every downstream offset and assemble.
	topDictSize := len(writeCFFIndex([][]byte{encodeTopDict(entries, offsets)}))

	pos := hdrSize + len(nameRaw) + topDictSize + len(strRaw) + len(gsubrRaw)
	if !offsets.charsetIsPre {
		offsets.charset = pos
		pos += len(charsetBlob)
	}
	offsets.charStrings = pos
	pos += len(newCharStrings)
	if offsets.hasPrivate {
		offsets.privateOff = pos
	}

	topDict := encodeTopDict(entries, offsets)
	out := append([]byte(nil), data[:hdrSize]...)
	out = append(out, nameRaw...)
	out = append(out, writeCFFIndex([][]byte{topDict})...)
	out = append(out, strRaw...)
	out = append(out, gsubrRaw...)
	out = append(out, charsetBlob...)
	out = append(out, newCharStrings...)
	out = append(out, privBlob...)
	return out, nil
}
