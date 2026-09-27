// Copyright (c) the go-opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"fmt"
)

// woff2Reader is a bounded big-endian reader over one buffer, with the two
// variable-length integer encodings WOFF2 uses.
type woff2Reader struct {
	b []byte
	i int
}

func (r *woff2Reader) remaining() int { return len(r.b) - r.i }

func (r *woff2Reader) bytes(n int) ([]byte, error) {
	if n < 0 || r.remaining() < n {
		return nil, fmt.Errorf("opentype: woff2: %w", errTruncated)
	}
	b := r.b[r.i : r.i+n]
	r.i += n
	return b, nil
}

func (r *woff2Reader) u8() (uint8, error) {
	b, err := r.bytes(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (r *woff2Reader) u16() (uint16, error) {
	b, err := r.bytes(2)
	if err != nil {
		return 0, err
	}
	return be16(b), nil
}

func (r *woff2Reader) u32() (uint32, error) {
	b, err := r.bytes(4)
	if err != nil {
		return 0, err
	}
	return be32(b), nil
}

// base128 reads a UIntBase128: up to five 7-bit groups, most significant
// first, the last without its continuation bit. A leading 0x80 (a value with
// a leading zero group) and anything that would overflow 32 bits are invalid,
// so that one value has exactly one encoding.
func (r *woff2Reader) base128() (uint32, error) {
	var v uint32
	for i := 0; i < 5; i++ {
		c, err := r.u8()
		if err != nil {
			return 0, err
		}
		if i == 0 && c == 0x80 {
			return 0, fmt.Errorf("opentype: woff2: UIntBase128 with a leading zero")
		}
		if v&0xfe000000 != 0 {
			return 0, fmt.Errorf("opentype: woff2: UIntBase128 overflows 32 bits")
		}
		v = v<<7 | uint32(c&0x7f)
		if c&0x80 == 0 {
			return v, nil
		}
	}
	return 0, fmt.Errorf("opentype: woff2: UIntBase128 longer than five bytes")
}

// read255 reads a 255UInt16: one byte for 0..252, or an escape for the rest.
func (r *woff2Reader) read255() (int, error) {
	c, err := r.u8()
	if err != nil {
		return 0, err
	}
	switch c {
	case 253: // a whole uint16 follows
		v, err := r.u16()
		return int(v), err
	case 255: // one byte, offset by 253
		v, err := r.u8()
		return int(v) + 253, err
	case 254: // one byte, offset by twice 253
		v, err := r.u8()
		return int(v) + 253*2, err
	}
	return int(c), nil
}

// glyfPoint is one decoded outline point, in font units.
type glyfPoint struct {
	x, y    int
	onCurve bool
}

// reconstructGlyf rebuilds the glyf and loca tables from a transformed glyf
// substream set. It returns the two tables, each glyph's xMin (which a
// transformed hmtx needs to recover left side bearings) and the glyph count.
//
// locaLength is what the directory says the reconstructed loca must be, and
// it is checked rather than trusted: it is what fixes whether loca holds
// 16-bit or 32-bit offsets, and getting that wrong shifts every glyph.
func reconstructGlyf(data []byte, locaLength uint32) (glyf, loca []byte, xMins []int16, numGlyphs int, err error) {
	h := &woff2Reader{b: data}
	if _, err = h.u16(); err != nil { // reserved
		return nil, nil, nil, 0, err
	}
	flags, err := h.u16()
	if err != nil {
		return nil, nil, nil, 0, err
	}
	hasOverlapBitmap := flags&flagOverlapSimpleBitmap != 0
	nGlyphs16, err := h.u16()
	if err != nil {
		return nil, nil, nil, 0, err
	}
	indexFormat, err := h.u16()
	if err != nil {
		return nil, nil, nil, 0, err
	}
	numGlyphs = int(nGlyphs16)
	wantLoca := uint32(2)
	if indexFormat != 0 {
		wantLoca = 4
	}
	if want := wantLoca * uint32(numGlyphs+1); want != locaLength {
		return nil, nil, nil, 0, fmt.Errorf("opentype: woff2: loca is %d bytes, a %d-glyph font in index format %d needs %d", locaLength, numGlyphs, indexFormat, want)
	}

	subs := make([]*woff2Reader, glyfNumSubstreams)
	sizes := make([]int, glyfNumSubstreams)
	for i := range sizes {
		n, err := h.u32()
		if err != nil {
			return nil, nil, nil, 0, err
		}
		sizes[i] = int(n)
	}
	for i := range subs {
		b, err := h.bytes(sizes[i])
		if err != nil {
			return nil, nil, nil, 0, fmt.Errorf("opentype: woff2: glyf substream %d: %w", i, err)
		}
		subs[i] = &woff2Reader{b: b}
	}
	nContours, nPoints, flagStream, glyphStream, composites, bboxes, instructions :=
		subs[0], subs[1], subs[2], subs[3], subs[4], subs[5], subs[6]

	var overlapBitmap []byte
	if hasOverlapBitmap {
		if overlapBitmap, err = h.bytes((numGlyphs + 7) >> 3); err != nil {
			return nil, nil, nil, 0, err
		}
	}
	// The bbox substream opens with a bitmap of the glyphs that carry an
	// explicit bounding box, padded to a multiple of four bytes.
	bboxBitmapLen := ((numGlyphs + 31) >> 5) << 2
	bboxBitmap, err := bboxes.bytes(bboxBitmapLen)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("opentype: woff2: bbox bitmap: %w", err)
	}

	xMins = make([]int16, numGlyphs)
	locaValues := make([]uint32, numGlyphs+1)
	var out []byte
	for g := 0; g < numGlyphs; g++ {
		locaValues[g] = uint32(len(out))
		haveBbox := bboxBitmap[g>>3]&(0x80>>(g&7)) != 0
		nc, err := nContours.u16()
		if err != nil {
			return nil, nil, nil, 0, err
		}
		var glyph []byte
		switch {
		case nc == 0xffff:
			if !haveBbox {
				return nil, nil, nil, 0, fmt.Errorf("opentype: woff2: composite glyph %d has no bbox", g)
			}
			glyph, err = woff2CompositeGlyph(composites, glyphStream, instructions, bboxes)
		case nc > 0:
			glyph, err = woff2SimpleGlyph(int(nc), nPoints, flagStream, glyphStream, instructions, bboxes, haveBbox,
				hasOverlapBitmap && overlapBitmap[g>>3]&(0x80>>(g&7)) != 0)
		default:
			// An empty glyph occupies no bytes, and must not claim a bbox.
			if haveBbox {
				return nil, nil, nil, 0, fmt.Errorf("opentype: woff2: empty glyph %d has a bbox", g)
			}
		}
		if err != nil {
			return nil, nil, nil, 0, fmt.Errorf("opentype: woff2: glyph %d: %w", g, err)
		}
		if nc > 0 && nc != 0xffff && len(glyph) >= 4 {
			xMins[g] = int16(be16(glyph[2:]))
		}
		out = append(out, glyph...)
		for len(out)%4 != 0 { // every glyph starts on a four-byte boundary
			out = append(out, 0)
		}
	}
	locaValues[numGlyphs] = uint32(len(out))
	loca, err = storeLoca(locaValues, indexFormat)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return out, loca, xMins, numGlyphs, nil
}

// storeLoca writes the offsets in the index format the font declared. The
// short format stores each offset halved, so an odd one cannot be represented
// — which is why every glyph is padded to four bytes above.
func storeLoca(values []uint32, indexFormat uint16) ([]byte, error) {
	if indexFormat != 0 {
		out := make([]byte, 4*len(values))
		for i, v := range values {
			binary.BigEndian.PutUint32(out[4*i:], v)
		}
		return out, nil
	}
	out := make([]byte, 2*len(values))
	for i, v := range values {
		if v%2 != 0 || v/2 > 0xffff {
			return nil, fmt.Errorf("opentype: woff2: offset %d does not fit the short loca format", v)
		}
		binary.BigEndian.PutUint16(out[2*i:], uint16(v/2))
	}
	return out, nil
}

// woff2CompositeGlyph assembles one composite glyph: its component records
// come over verbatim, its bbox from the bbox stream, and its instructions —
// whose length lives in the glyph stream, not beside them — from the
// instruction stream.
func woff2CompositeGlyph(composites, glyphStream, instructions, bboxes *woff2Reader) ([]byte, error) {
	start := composites.i
	haveInstructions := false
	for more := true; more; {
		flags, err := composites.u16()
		if err != nil {
			return nil, err
		}
		const (
			argsAreWords    = 1 << 0
			weHaveAScale    = 1 << 3
			moreComponents  = 1 << 5
			xAndYScale      = 1 << 6
			twoByTwo        = 1 << 7
			weHaveInstructs = 1 << 8
		)
		haveInstructions = haveInstructions || flags&weHaveInstructs != 0
		more = flags&moreComponents != 0
		skip := 2 // the component's glyph index
		if flags&argsAreWords != 0 {
			skip += 4
		} else {
			skip += 2
		}
		switch {
		case flags&weHaveAScale != 0:
			skip += 2
		case flags&xAndYScale != 0:
			skip += 4
		case flags&twoByTwo != 0:
			skip += 8
		}
		if _, err := composites.bytes(skip); err != nil {
			return nil, err
		}
	}
	body := composites.b[start:composites.i]

	var instrLen int
	if haveInstructions {
		var err error
		if instrLen, err = glyphStream.read255(); err != nil {
			return nil, err
		}
	}
	bbox, err := bboxes.bytes(8)
	if err != nil {
		return nil, fmt.Errorf("bbox: %w", err)
	}
	out := make([]byte, 0, 10+len(body)+2+instrLen)
	out = append(out, 0xff, 0xff) // numberOfContours = -1
	out = append(out, bbox...)
	out = append(out, body...)
	if haveInstructions {
		out = append(out, byte(instrLen>>8), byte(instrLen))
		instr, err := instructions.bytes(instrLen)
		if err != nil {
			return nil, fmt.Errorf("instructions: %w", err)
		}
		out = append(out, instr...)
	}
	return out, nil
}

// woff2SimpleGlyph assembles one simple glyph, decoding its points from the
// triplet encoding and re-encoding them in the glyf format.
func woff2SimpleGlyph(nContours int, nPoints, flagStream, glyphStream, instructions, bboxes *woff2Reader, haveBbox, overlap bool) ([]byte, error) {
	ends := make([]int, nContours)
	total := 0
	for i := 0; i < nContours; i++ {
		n, err := nPoints.read255()
		if err != nil {
			return nil, err
		}
		total += n
		if total > 0xffff {
			return nil, fmt.Errorf("more than 65535 points")
		}
		ends[i] = total - 1
	}
	flags, err := flagStream.bytes(total)
	if err != nil {
		return nil, fmt.Errorf("flags: %w", err)
	}
	points, err := tripletDecode(flags, glyphStream)
	if err != nil {
		return nil, err
	}
	instrLen, err := glyphStream.read255()
	if err != nil {
		return nil, err
	}
	instr, err := instructions.bytes(instrLen)
	if err != nil {
		return nil, fmt.Errorf("instructions: %w", err)
	}

	out := make([]byte, 10, 10+2*nContours+2+instrLen+5*total)
	binary.BigEndian.PutUint16(out[0:], uint16(nContours))
	if haveBbox {
		bbox, err := bboxes.bytes(8)
		if err != nil {
			return nil, fmt.Errorf("bbox: %w", err)
		}
		copy(out[2:], bbox)
	} else {
		writeBbox(out[2:], points)
	}
	for _, e := range ends {
		out = append(out, byte(e>>8), byte(e))
	}
	out = append(out, byte(instrLen>>8), byte(instrLen))
	out = append(out, instr...)
	return appendPoints(out, points, overlap), nil
}

// writeBbox stores the points' bounding box into a glyf record's xMin..yMax.
// A glyph whose bbox the stream omitted is one whose bbox is exactly this, so
// computing it is not an approximation.
func writeBbox(dst []byte, points []glyfPoint) {
	if len(points) == 0 {
		return
	}
	xMin, yMin, xMax, yMax := points[0].x, points[0].y, points[0].x, points[0].y
	for _, p := range points[1:] {
		if p.x < xMin {
			xMin = p.x
		}
		if p.x > xMax {
			xMax = p.x
		}
		if p.y < yMin {
			yMin = p.y
		}
		if p.y > yMax {
			yMax = p.y
		}
	}
	for i, v := range []int{xMin, yMin, xMax, yMax} {
		binary.BigEndian.PutUint16(dst[2*i:], uint16(int16(v)))
	}
}
