// Copyright (c) the go-opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/andybalholm/brotli"
)

// WOFF2 is the container the web serves fonts in: an SFNT whose tables are
// concatenated into one brotli stream, with `glyf` and `loca` optionally
// rewritten into a denser form that has to be reconstructed. It is not a
// compressed SFNT — the bytes of a transformed glyf are not the bytes of a
// glyf — which is why this is a decoder and not an unzip.
//
// The algorithm is the W3C WOFF2 specification's; where the spec describes a
// table of 128 triplet encodings, the arithmetic form of Google's reference
// decoder (woff2_dec.cc, MIT) is followed instead, being the same mapping in
// closed form and far easier to get right.
//
// The test for this reads a real subsetted WOFF2 from a font service and
// compares every glyph it decodes — outline point by outline point, and
// advance — against the plain TrueType of the SAME face. Nothing about the
// container can be verified by decoding alone: a decoder that quietly drops a
// contour still produces a font that parses.

const (
	sigWOFF2 = 0x774F4632 // 'wOF2'
	sigWOFF  = 0x774F4646 // 'wOFF'

	woff2HeaderSize = 48
	// A transformed glyf carries seven substreams after its own 36-byte
	// header (four fixed uint16 plus seven uint32 lengths).
	glyfNumSubstreams = 7
	// flagOverlapSimpleBitmap says a per-glyph bitmap follows the substreams,
	// marking the glyphs whose first point takes OVERLAP_SIMPLE.
	flagOverlapSimpleBitmap = 1
)

// woff2KnownTags is the table the 6-bit tag index in a directory entry indexes
// (WOFF2 §5.2); index 63 means a 4-byte tag follows instead.
var woff2KnownTags = [63]string{
	"cmap", "head", "hhea", "hmtx", "maxp", "name", "OS/2", "post",
	"cvt ", "fpgm", "glyf", "loca", "prep", "CFF ", "VORG", "EBDT",
	"EBLC", "gasp", "hdmx", "kern", "LTSH", "PCLT", "VDMX", "vhea",
	"vmtx", "BASE", "GDEF", "GPOS", "GSUB", "EBSC", "JSTF", "MATH",
	"CBDT", "CBLC", "COLR", "CPAL", "SVG ", "sbix", "acnt", "avar",
	"bdat", "bloc", "bsln", "cvar", "fdsc", "feat", "fmtx", "fvar",
	"gvar", "hsty", "just", "lcar", "mort", "morx", "opbd", "prop",
	"trak", "Zapf", "Silf", "Glat", "Gloc", "Feat", "Sill",
}

var errNotWOFF = errors.New("opentype: not a WOFF or WOFF2 container")

// isWOFFContainer reports whether b starts with a WOFF or WOFF2 signature.
func isWOFFContainer(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	switch be32(b) {
	case sigWOFF, sigWOFF2:
		return true
	}
	return false
}

// decodeWOFFContainer unwraps a WOFF or WOFF2 file to the SFNT inside it.
func decodeWOFFContainer(b []byte) ([]byte, error) {
	if len(b) < 4 {
		return nil, errNotWOFF
	}
	switch be32(b) {
	case sigWOFF:
		return decodeWOFF1(b)
	case sigWOFF2:
		return decodeWOFF2(b)
	}
	return nil, errNotWOFF
}

// woff2Table is one entry of the directory, with where its data landed in the
// decompressed stream and where it will land in the SFNT.
type woff2Table struct {
	tag        string
	transform  uint8  // transformation version, bits 6-7 of the entry's flags
	origLength uint32 // the table's length once reconstructed
	xformLen   uint32 // its length in the stream, when transformed
	data       []byte // its bytes in the decompressed stream
	out        []byte // its reconstructed bytes
}

// transformed reports whether this entry's data needs reconstructing rather
// than copying. The convention is inverted for glyf and loca: version 0 is
// the transform for them and the null transform for everything else.
func (t woff2Table) transformed() bool {
	if t.tag == "glyf" || t.tag == "loca" {
		return t.transform == 0
	}
	return t.transform != 0
}

func decodeWOFF2(b []byte) ([]byte, error) {
	if len(b) < woff2HeaderSize {
		return nil, fmt.Errorf("opentype: woff2: short header: %w", errTruncated)
	}
	flavor := be32(b[4:])
	if flavor == 0x74746366 { // 'ttcf'
		return nil, errors.New("opentype: woff2: font collections are not supported")
	}
	numTables := int(be16(b[12:]))
	if numTables == 0 {
		return nil, errors.New("opentype: woff2: no tables")
	}
	totalCompressed := be32(b[20:])

	r := &woff2Reader{b: b, i: woff2HeaderSize}
	tables := make([]woff2Table, numTables)
	for i := range tables {
		t, err := r.tableEntry()
		if err != nil {
			return nil, err
		}
		tables[i] = t
	}
	if uint64(r.i)+uint64(totalCompressed) > uint64(len(b)) {
		return nil, fmt.Errorf("opentype: woff2: compressed stream past end: %w", errTruncated)
	}
	stream, err := io.ReadAll(brotli.NewReader(bytes.NewReader(b[r.i : r.i+int(totalCompressed)])))
	if err != nil {
		return nil, fmt.Errorf("opentype: woff2: brotli: %w", err)
	}
	// The stream is the tables' data concatenated in directory order, each at
	// its transformed length where it has one.
	off := 0
	for i := range tables {
		n := int(tables[i].origLength)
		if tables[i].transformed() {
			n = int(tables[i].xformLen)
		}
		if n < 0 || off+n > len(stream) {
			return nil, fmt.Errorf("opentype: woff2: %q runs past the stream: %w", tables[i].tag, errTruncated)
		}
		tables[i].data = stream[off : off+n]
		off += n
	}
	if err := reconstructTables(tables); err != nil {
		return nil, err
	}
	return assembleSFNT(flavor, tables)
}

// tableEntry reads one directory entry.
func (r *woff2Reader) tableEntry() (woff2Table, error) {
	flags, err := r.u8()
	if err != nil {
		return woff2Table{}, err
	}
	var t woff2Table
	t.transform = flags >> 6
	if idx := flags & 0x3f; idx == 0x3f {
		tag, err := r.bytes(4)
		if err != nil {
			return woff2Table{}, err
		}
		t.tag = string(tag)
	} else {
		// A six-bit index runs 0..63 and 63 is the escape above, so every
		// remaining value indexes the 63-entry table.
		t.tag = woff2KnownTags[idx]
	}
	if t.origLength, err = r.base128(); err != nil {
		return woff2Table{}, err
	}
	if t.transformed() {
		if t.xformLen, err = r.base128(); err != nil {
			return woff2Table{}, err
		}
	}
	return t, nil
}

// reconstructTables fills each table's out from its data: a copy for a null
// transform, and a rebuild for glyf/loca and a transformed hmtx.
func reconstructTables(tables []woff2Table) error {
	byTag := map[string]*woff2Table{}
	for i := range tables {
		byTag[tables[i].tag] = &tables[i]
	}
	for i := range tables {
		t := &tables[i]
		if !t.transformed() {
			if int(t.origLength) != len(t.data) {
				return fmt.Errorf("opentype: woff2: %q is %d bytes, the directory says %d", t.tag, len(t.data), t.origLength)
			}
			t.out = t.data
		}
	}
	glyf, loca := byTag["glyf"], byTag["loca"]
	var xMins []int16
	var numGlyphs int
	if glyf != nil && glyf.transformed() {
		if loca == nil {
			return errors.New("opentype: woff2: a transformed glyf needs a loca")
		}
		g, l, mins, n, err := reconstructGlyf(glyf.data, loca.origLength)
		if err != nil {
			return err
		}
		glyf.out, loca.out, xMins, numGlyphs = g, l, mins, n
	} else if loca != nil && loca.transformed() {
		return errors.New("opentype: woff2: loca is transformed but glyf is not")
	}
	if hmtx := byTag["hmtx"]; hmtx != nil && hmtx.transformed() {
		hhea := byTag["hhea"]
		if hhea == nil || hhea.out == nil || len(hhea.out) < 36 {
			return errors.New("opentype: woff2: a transformed hmtx needs an hhea")
		}
		numHMetrics := int(be16(hhea.out[34:]))
		if numGlyphs == 0 {
			// The hmtx transform recovers a bearing from its glyph's xMin, and
			// those are collected while glyf is reconstructed — so a font that
			// transforms hmtx but NOT glyf leaves none behind. Read the count
			// from maxp and the bearings out of the glyf the stream carried
			// as it was.
			var err error
			if numGlyphs, xMins, err = xMinsFromGlyf(byTag["maxp"], byTag["head"], byTag["glyf"], byTag["loca"]); err != nil {
				return err
			}
		}
		out, err := reconstructHmtx(hmtx.data, numGlyphs, numHMetrics, xMins)
		if err != nil {
			return err
		}
		hmtx.out = out
	}
	for i := range tables {
		if tables[i].out == nil {
			return fmt.Errorf("opentype: woff2: %q was left unreconstructed (transform version %d)", tables[i].tag, tables[i].transform)
		}
	}
	return nil
}

// assembleSFNT writes the reconstructed tables as an SFNT: a directory sorted
// by tag, table data 4-byte aligned in directory order, and correct checksums
// including head's checkSumAdjustment.
func assembleSFNT(flavor uint32, tables []woff2Table) ([]byte, error) {
	sorted := make([]*woff2Table, len(tables))
	for i := range tables {
		sorted[i] = &tables[i]
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].tag < sorted[j].tag })

	n := len(sorted)
	headerSize := 12 + 16*n
	total := headerSize
	for _, t := range sorted {
		total += (len(t.out) + 3) &^ 3
	}
	out := make([]byte, headerSize, total)
	binary.BigEndian.PutUint32(out[0:], flavor)
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	// searchRange, entrySelector, rangeShift: the largest power of two not
	// above n, times 16, and the remainder — a binary-search hint every
	// writer fills in and no reader depends on, kept correct anyway.
	pow, sel := 1, 0
	for pow*2 <= n {
		pow *= 2
		sel++
	}
	binary.BigEndian.PutUint16(out[6:], uint16(pow*16))
	binary.BigEndian.PutUint16(out[8:], uint16(sel))
	binary.BigEndian.PutUint16(out[10:], uint16(n*16-pow*16))

	offsets := make([]int, n)
	for i, t := range sorted {
		offsets[i] = len(out)
		out = append(out, t.out...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	headIdx := -1
	for i, t := range sorted {
		e := 12 + 16*i
		copy(out[e:], t.tag)
		binary.BigEndian.PutUint32(out[e+4:], tableChecksum(t.out))
		binary.BigEndian.PutUint32(out[e+8:], uint32(offsets[i]))
		binary.BigEndian.PutUint32(out[e+12:], uint32(len(t.out)))
		if t.tag == "head" {
			headIdx = i
		}
	}
	// head.checkSumAdjustment is 0xB1B0AFBA minus the whole font's checksum,
	// computed with that field itself zero. A reader that verifies it — a
	// validator, a printer's RIP — rejects the font outright when it is
	// stale, so it is recomputed rather than carried over.
	if headIdx >= 0 && len(sorted[headIdx].out) >= 12 {
		adj := offsets[headIdx] + 8
		binary.BigEndian.PutUint32(out[adj:], 0)
		binary.BigEndian.PutUint32(out[12+16*headIdx+4:], tableChecksum(out[offsets[headIdx]:offsets[headIdx]+len(sorted[headIdx].out)]))
		binary.BigEndian.PutUint32(out[adj:], 0xB1B0AFBA-tableChecksum(out))
	}
	return out, nil
}

// tableChecksum is the SFNT checksum: the big-endian uint32 sum of the bytes,
// zero-padded to a multiple of four.
func tableChecksum(b []byte) uint32 {
	var sum uint32
	for i := 0; i+4 <= len(b); i += 4 {
		sum += be32(b[i:])
	}
	if rem := len(b) % 4; rem != 0 {
		var tail [4]byte
		copy(tail[:], b[len(b)-rem:])
		sum += be32(tail[:])
	}
	return sum
}

// xMinsFromGlyf reads the glyph count and each glyph's xMin straight out of an
// untransformed glyf, which is what a transformed hmtx needs when glyf itself
// was stored as it is. A glyph with no outline has no xMin and contributes
// zero, which is also what its bearing is.
func xMinsFromGlyf(maxp, head, glyf, loca *woff2Table) (int, []int16, error) {
	if maxp == nil || len(maxp.out) < 6 {
		return 0, nil, errors.New("opentype: woff2: a transformed hmtx needs a maxp")
	}
	if head == nil || len(head.out) < 52 {
		return 0, nil, errors.New("opentype: woff2: a transformed hmtx needs a head")
	}
	if glyf == nil || loca == nil {
		return 0, nil, errors.New("opentype: woff2: a transformed hmtx needs a glyf and a loca")
	}
	numGlyphs := int(be16(maxp.out[4:]))
	long := be16(head.out[50:]) != 0
	offset := func(i int) (int, bool) {
		if long {
			if 4*i+4 > len(loca.out) {
				return 0, false
			}
			return int(be32(loca.out[4*i:])), true
		}
		if 2*i+2 > len(loca.out) {
			return 0, false
		}
		return 2 * int(be16(loca.out[2*i:])), true
	}
	xMins := make([]int16, numGlyphs)
	for g := 0; g < numGlyphs; g++ {
		start, ok1 := offset(g)
		end, ok2 := offset(g + 1)
		if !ok1 || !ok2 {
			return 0, nil, fmt.Errorf("opentype: woff2: loca is too short for %d glyphs", numGlyphs)
		}
		if end <= start || start+4 > len(glyf.out) {
			continue // an empty glyph: no outline, no bearing of its own
		}
		xMins[g] = int16(be16(glyf.out[start+2:]))
	}
	return numGlyphs, xMins, nil
}
