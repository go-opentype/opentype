// Copyright (c) the go-opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"fmt"
)

// tripletDecode turns one glyph's flag bytes and the glyph stream's triplets
// into absolute points.
//
// Each point has a flag byte — high bit clear for an on-curve point, the low
// seven bits selecting how many data bytes its deltas take and how they are
// laid out — and then 1 to 4 bytes of data. The WOFF2 specification presents
// this as a table of 128 encodings; the arithmetic below is Google's reference
// decoder's closed form of the same table, which is what makes it checkable by
// reading rather than by trusting a transcription of 128 rows.
//
// The low bit of the flag is the X sign and the next bit up the Y sign, which
// is why the sign helper takes the flag itself and the Y calls shift it once.
func tripletDecode(flags []byte, glyphStream *woff2Reader) ([]glyfPoint, error) {
	points := make([]glyfPoint, len(flags))
	x, y := 0, 0
	for i, f := range flags {
		onCurve := f>>7 == 0
		flag := int(f & 0x7f)
		var n int
		switch {
		case flag < 84:
			n = 1
		case flag < 120:
			n = 2
		case flag < 124:
			n = 3
		default:
			n = 4
		}
		d, err := glyphStream.bytes(n)
		if err != nil {
			return nil, fmt.Errorf("point %d: %w", i, err)
		}
		var dx, dy int
		switch {
		case flag < 10:
			dx = 0
			dy = withSign(flag, ((flag&14)<<7)+int(d[0]))
		case flag < 20:
			dx = withSign(flag, (((flag-10)&14)<<7)+int(d[0]))
			dy = 0
		case flag < 84:
			b0 := flag - 20
			b1 := int(d[0])
			dx = withSign(flag, 1+(b0&0x30)+(b1>>4))
			dy = withSign(flag>>1, 1+((b0&0x0c)<<2)+(b1&0x0f))
		case flag < 120:
			b0 := flag - 84
			dx = withSign(flag, 1+((b0/12)<<8)+int(d[0]))
			dy = withSign(flag>>1, 1+(((b0%12)>>2)<<8)+int(d[1]))
		case flag < 124:
			dx = withSign(flag, int(d[0])<<4+int(d[1])>>4)
			dy = withSign(flag>>1, (int(d[1])&0x0f)<<8+int(d[2]))
		default:
			dx = withSign(flag, int(d[0])<<8+int(d[1]))
			dy = withSign(flag>>1, int(d[2])<<8+int(d[3]))
		}
		x += dx
		y += dy
		points[i] = glyfPoint{x: x, y: y, onCurve: onCurve}
	}
	return points, nil
}

// withSign applies a delta's sign: the relevant bit set means positive.
func withSign(flag, v int) int {
	if flag&1 != 0 {
		return v
	}
	return -v
}

// appendPoints writes the points in the glyf format — a run-length-encoded
// flag array, then all the X deltas, then all the Y deltas, each delta one
// byte where it fits and two where it does not.
func appendPoints(out []byte, points []glyfPoint, overlap bool) []byte {
	const (
		onCurve    = 1 << 0
		xShort     = 1 << 1
		yShort     = 1 << 2
		repeat     = 1 << 3
		xSameOrPos = 1 << 4
		ySameOrPos = 1 << 5
		overlapBit = 1 << 6
	)
	lastFlag, repeatCount := -1, 0
	lastX, lastY := 0, 0
	for i, p := range points {
		flag := 0
		if p.onCurve {
			flag |= onCurve
		}
		// OVERLAP_SIMPLE rides on the FIRST point's flag; the transform
		// carries it in a per-glyph bitmap instead of in the flags.
		if overlap && i == 0 {
			flag |= overlapBit
		}
		dx, dy := p.x-lastX, p.y-lastY
		switch {
		case dx == 0:
			flag |= xSameOrPos
		case dx > -256 && dx < 256:
			flag |= xShort
			if dx > 0 {
				flag |= xSameOrPos
			}
		}
		switch {
		case dy == 0:
			flag |= ySameOrPos
		case dy > -256 && dy < 256:
			flag |= yShort
			if dy > 0 {
				flag |= ySameOrPos
			}
		}
		if flag == lastFlag && repeatCount < 255 {
			out[len(out)-1] |= repeat
			repeatCount++
		} else {
			if repeatCount != 0 {
				out = append(out, byte(repeatCount))
			}
			out = append(out, byte(flag))
			repeatCount = 0
		}
		lastX, lastY, lastFlag = p.x, p.y, flag
	}
	if repeatCount != 0 {
		out = append(out, byte(repeatCount))
	}
	// The coordinates: every X first, then every Y, each written only when
	// its flag said it is neither zero nor already implied by the sign bit.
	lastX = 0
	for _, p := range points {
		dx := p.x - lastX
		switch {
		case dx == 0:
		case dx > -256 && dx < 256:
			out = append(out, byte(abs16(dx)))
		default:
			out = append(out, byte(dx>>8), byte(dx))
		}
		lastX = p.x
	}
	lastY = 0
	for _, p := range points {
		dy := p.y - lastY
		switch {
		case dy == 0:
		case dy > -256 && dy < 256:
			out = append(out, byte(abs16(dy)))
		default:
			out = append(out, byte(dy>>8), byte(dy))
		}
		lastY = p.y
	}
	return out
}

func abs16(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// reconstructHmtx rebuilds hmtx from the transformed form, which drops the
// left side bearings it can recover: for a glyph with an outline, lsb is its
// xMin, so the transform stores the advances and only those bearings it could
// not derive.
func reconstructHmtx(data []byte, numGlyphs, numHMetrics int, xMins []int16) ([]byte, error) {
	r := &woff2Reader{b: data}
	flags, err := r.u8()
	if err != nil {
		return nil, err
	}
	if flags&0xfc != 0 {
		return nil, fmt.Errorf("opentype: woff2: hmtx flags 0x%02x: bits 2-7 are reserved", flags)
	}
	proportionalLSBs := flags&1 == 0
	monospaceLSBs := flags&2 == 0
	if proportionalLSBs && monospaceLSBs {
		return nil, fmt.Errorf("opentype: woff2: hmtx claims a transform but drops no bearings")
	}
	if numHMetrics < 1 || numHMetrics > numGlyphs || len(xMins) < numGlyphs {
		return nil, fmt.Errorf("opentype: woff2: hmtx: %d metrics for %d glyphs", numHMetrics, numGlyphs)
	}
	advances := make([]uint16, numHMetrics)
	for i := range advances {
		if advances[i], err = r.u16(); err != nil {
			return nil, err
		}
	}
	lsbs := make([]int16, numGlyphs)
	for i := 0; i < numHMetrics; i++ {
		if proportionalLSBs {
			v, err := r.u16()
			if err != nil {
				return nil, err
			}
			lsbs[i] = int16(v)
		} else {
			lsbs[i] = xMins[i]
		}
	}
	for i := numHMetrics; i < numGlyphs; i++ {
		if monospaceLSBs {
			v, err := r.u16()
			if err != nil {
				return nil, err
			}
			lsbs[i] = int16(v)
		} else {
			lsbs[i] = xMins[i]
		}
	}
	out := make([]byte, 4*numHMetrics+2*(numGlyphs-numHMetrics))
	for i := 0; i < numHMetrics; i++ {
		binary.BigEndian.PutUint16(out[4*i:], advances[i])
		binary.BigEndian.PutUint16(out[4*i+2:], uint16(lsbs[i]))
	}
	for i := numHMetrics; i < numGlyphs; i++ {
		binary.BigEndian.PutUint16(out[4*numHMetrics+2*(i-numHMetrics):], uint16(lsbs[i]))
	}
	return out, nil
}
