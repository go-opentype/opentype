// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "encoding/binary"

// reader is a big-endian sequential reader over a byte slice with sticky
// error tracking: any read past the end records errTruncated and returns zero,
// so a parser can read a run of fields and check err once at the end.
type reader struct {
	b   []byte
	pos int
	err error
}

// u8 reads one unsigned byte.
func (r *reader) u8() uint8 {
	if r.pos+1 > len(r.b) {
		r.err = errTruncated
		return 0
	}
	v := r.b[r.pos]
	r.pos++
	return v
}

// u16 reads a big-endian uint16.
func (r *reader) u16() uint16 {
	if r.pos+2 > len(r.b) {
		r.err = errTruncated
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.pos:])
	r.pos += 2
	return v
}

// u32 reads a big-endian uint32.
func (r *reader) u32() uint32 {
	if r.pos+4 > len(r.b) {
		r.err = errTruncated
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	return v
}

// i16 reads a big-endian signed int16.
func (r *reader) i16() int16 { return int16(r.u16()) }

// f2dot14 reads an F2Dot14 fixed-point value as a float64.
func (r *reader) f2dot14() float64 { return float64(r.i16()) / 16384.0 }

// skip advances the position by n bytes, recording an error if that runs past
// the end of the slice.
func (r *reader) skip(n int) {
	if r.pos+n > len(r.b) {
		r.err = errTruncated
		return
	}
	r.pos += n
}
