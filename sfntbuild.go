// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"sort"
)

// This file holds the shared machinery for writing a fresh sfnt container: the
// table directory, per-table checksums and the head checkSumAdjustment. Both the
// TrueType subsetter (subset.go) and the variable-instance serialiser
// (instance.go) rebuild a font from a set of tables and emit it through
// buildSFNT, so a single, tested writer covers every produced font.

// buildSFNT packs tables into an sfnt container with the given sfnt version tag,
// laying out the table directory (records sorted by tag, 4-byte-aligned bodies),
// filling in each record's checksum, offset and length, and patching head's
// checkSumAdjustment so the emitted font is a valid, self-consistent sfnt. A
// "head" table, when present, must be at least 12 bytes (its checkSumAdjustment
// field lives at offset 8); the callers always supply the full 54-byte table.
func buildSFNT(version uint32, tables map[string][]byte) []byte {
	tags := make([]string, 0, len(tables))
	for t := range tables {
		tags = append(tags, t)
	}
	sort.Strings(tags)

	n := len(tags)
	// searchRange/entrySelector/rangeShift describe the largest power of two not
	// exceeding n, per the sfnt spec's binary-search hint fields.
	entrySelector := 0
	for (1 << (entrySelector + 1)) <= n {
		entrySelector++
	}
	searchRange := (1 << entrySelector) * 16
	rangeShift := n*16 - searchRange

	headerLen := 12 + 16*n
	offsets := make(map[string]int, n)
	var body []byte
	for _, t := range tags {
		offsets[t] = headerLen + len(body)
		body = append(body, tables[t]...)
		for len(body)%4 != 0 {
			body = append(body, 0)
		}
	}

	out := make([]byte, headerLen)
	binary.BigEndian.PutUint32(out[0:], version)
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	binary.BigEndian.PutUint16(out[6:], uint16(searchRange))
	binary.BigEndian.PutUint16(out[8:], uint16(entrySelector))
	binary.BigEndian.PutUint16(out[10:], uint16(rangeShift))
	for i, t := range tags {
		rec := 12 + i*16
		copy(out[rec:], t)
		binary.BigEndian.PutUint32(out[rec+4:], sfntChecksum(tables[t]))
		binary.BigEndian.PutUint32(out[rec+8:], uint32(offsets[t]))
		binary.BigEndian.PutUint32(out[rec+12:], uint32(len(tables[t])))
	}
	out = append(out, body...)

	// head.checkSumAdjustment = 0xB1B0AFBA - checksum(whole file), computed with
	// the field itself read as zero (it is still zero in out at this point).
	if headOff, ok := offsets["head"]; ok {
		adj := 0xB1B0AFBA - sfntChecksum(out)
		binary.BigEndian.PutUint32(out[headOff+8:], adj)
	}
	return out
}

// sfntChecksum is the sfnt table checksum: the sum modulo 2^32 of the data read
// as big-endian uint32 words, the final word zero-padded when the length is not a
// multiple of four.
func sfntChecksum(b []byte) uint32 {
	var sum uint32
	var i int
	for ; i+4 <= len(b); i += 4 {
		sum += binary.BigEndian.Uint32(b[i:])
	}
	if rem := len(b) - i; rem > 0 {
		var tail [4]byte
		copy(tail[:], b[i:])
		sum += binary.BigEndian.Uint32(tail[:])
	}
	return sum
}

// minimalCmap returns a structurally valid, essentially empty cmap table: a
// single format-4 (3,1 Unicode BMP) subtable whose only segment is the mandatory
// 0xFFFF sentinel, so it maps no character to a real glyph. A subset embedded in
// a PDF as a CIDFontType2 is addressed by glyph id through the CIDToGIDMap and
// never consults a cmap, but opentype.Parse (and other consumers) require a cmap
// table to be present and decodable; this satisfies that without carrying the
// original, now-misindexed mappings.
func minimalCmap() []byte {
	var sub []byte
	put16 := func(v uint16) { sub = binary.BigEndian.AppendUint16(sub, v) }
	put16(4)      // format
	put16(24)     // length (4-header + 8 fixed + one segment of 8)
	put16(0)      // language
	put16(2)      // segCountX2 (segCount 1)
	put16(2)      // searchRange
	put16(0)      // entrySelector
	put16(0)      // rangeShift
	put16(0xFFFF) // endCode[0] (sentinel)
	put16(0)      // reservedPad
	put16(0xFFFF) // startCode[0]
	put16(1)      // idDelta[0]
	put16(0)      // idRangeOffset[0]

	var t []byte
	putT16 := func(v uint16) { t = binary.BigEndian.AppendUint16(t, v) }
	putT32 := func(v uint32) { t = binary.BigEndian.AppendUint32(t, v) }
	putT16(0) // version
	putT16(1) // numTables
	putT16(3) // platformID (Windows)
	putT16(1) // encodingID (Unicode BMP)
	putT32(12)
	return append(t, sub...)
}
