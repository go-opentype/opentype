// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"errors"
	"fmt"
)

// A font declares counts, and Parse allocates from them. Two defences keep a
// few hundred bytes of font from asking for gigabytes (issue #75):
//
//  1. Every allocation sized by a declared count first checks, with
//     [reader.room], that the bytes the count promises are present. The check
//     divides (count <= remaining/size) instead of multiplying, so a 32-bit
//     count cannot overflow it on a 32-bit build. That bounds each single
//     allocation by the length of the table it sits in.
//
//  2. A single allocation bounded by the file is still not a bound on the
//     total: a thousand offsets may point at one table, two 16-bit counts may
//     multiply, and a coverage range may cover 65536 glyphs in six bytes. So
//     one [allocBudget] per Parse counts every element the tables allocate,
//     each time a shared sub-table is decoded again, and refuses past the
//     budget.

// errDeclaredCount is the refusal of a count that promises more elements than
// the bytes present can hold. It wraps errTruncated: the table is short of the
// data it declared, which is what a caller that tests for truncation expects.
var errDeclaredCount = fmt.Errorf("a declared count exceeds the data present: %w", errTruncated)

// errAllocBudget is the refusal of a font whose tables, taken together, ask the
// parser to build more than allocBudgetFloor + allocBudgetPerInputByte times its
// own length.
var errAllocBudget = errors.New("the tables declare more structure than the font's length allows")

const (
	// allocBudgetPerInputByte is how many bytes of decoded structure Parse
	// grants for each byte of input.
	//
	// It was set from a measurement: bounds_measure_test.go (run with
	// OPENTYPE_MEASURE_DIRS) parses every font found on a machine and reports the
	// largest ratio of charged bytes to input bytes. On the 227 fonts of
	// /System/Library/Fonts, /Library/Fonts and this repository's testdata that
	// parse, the largest ratio was 0.20 (NotoSansUgaritic-Regular.ttf); the
	// ratio is low because the decoders that charge the budget (cmap, fvar) build
	// roughly what they read. The worst a well-formed font can charge is the
	// 12x of an fvar instance (a 4-byte coordinate becomes a 48-byte map entry),
	// so 64 is 5x above that and 320x above the largest font measured. The
	// shared-offset amplifications (a table referenced a thousand times) do not
	// rely on this constant: each shared table is decoded once (memoOnce), and
	// the budget is the backstop for products no offset can share.
	allocBudgetPerInputByte = 64

	// allocBudgetFloor is added to the proportional part so that a tiny, valid
	// font (a subset of a few hundred bytes) never meets a budget smaller than
	// its own fixed-size structures: a 256-entry cmap format 0 or the 8 KiB
	// bitmap of a cmap format 8 are charged in full whatever the input length.
	// 1 MiB is also the most a few-hundred-byte hostile font can make a charging
	// decoder build, which is what the bomb tests assert under.
	allocBudgetFloor = 1 << 20

	// mapEntryCost is what a decoded map[GlyphIndex]int entry is charged. A Go
	// map holds 2 + 8 bytes of key and value, plus its control bytes and the
	// slack the table keeps free; 48 is a deliberate overestimate.
	mapEntryCost = 48
)

// allocBudget counts what one Parse has asked its decoders to build. It is not
// safe for concurrent use: Parse is sequential and owns it.
type allocBudget struct {
	left  int64 // bytes still available
	spent int64 // bytes charged so far, for the measurement in bounds_measure_test.go
}

// newAllocBudget returns the budget for a font (or table) of inputLen bytes.
func newAllocBudget(inputLen int) *allocBudget {
	return &allocBudget{left: allocBudgetFloor + allocBudgetPerInputByte*int64(inputLen)}
}

// budgetFor picks the budget a decoder was handed, or a fresh one sized from the
// bytes it is decoding when it was called on its own: the decoders are also
// entered directly, with no Parse above them.
func budgetFor(given []*allocBudget, inputLen int) *allocBudget {
	if len(given) > 0 {
		return given[0]
	}
	return newAllocBudget(inputLen)
}

// spend charges count elements of size bytes each, and refuses when the budget
// cannot cover them. count is the 64-bit form of whatever the font declared, so
// the product count*size cannot overflow: size is a small constant.
func (a *allocBudget) spend(table, field string, count uint64, size int) error {
	// count <= 2^32 for every field a font can declare; a product of two 16-bit
	// counts is passed as the product, still below 2^32.
	if count > uint64(a.left)/uint64(size) {
		return fmt.Errorf("opentype: %s %s of %d elements of %d bytes: %w", table, field, count, size, errAllocBudget)
	}
	n := int64(count) * int64(size)
	a.left -= n
	a.spent += n
	return nil
}

// claim is what a decoder calls before it allocates count elements of size
// bytes: the bytes must be present (room) and the budget must cover them (spend).
func (r *reader) claim(bud *allocBudget, table, field string, count uint64, size int) error {
	if err := r.room(table, field, count, size); err != nil {
		return err
	}
	return bud.spend(table, field, count, size)
}

// has reports whether count elements of size bytes each can still be read from
// the bytes the reader has left, dividing so that nothing is multiplied.
func (r *reader) has(count uint64, size int) bool {
	remaining := len(r.b) - r.pos
	if remaining < 0 {
		remaining = 0 // a reader positioned past the end has nothing left
	}
	return count <= uint64(remaining)/uint64(size)
}

// room is has with the refusal spelled out: it names the table and the field
// whose declared count did not fit.
func (r *reader) room(table, field string, count uint64, size int) error {
	if !r.has(count, size) {
		return fmt.Errorf("opentype: %s %s declares %d elements of %d bytes but %d bytes remain: %w",
			table, field, count, size, max(len(r.b)-r.pos, 0), errDeclaredCount)

	}
	return nil
}

// memoOnce returns the value decoded at off, decoding it with f only the first
// time: offsets in a font may share a target, and decoding a shared sub-table
// once per reference is what multiplies a small font into gigabytes. A decoded
// table is immutable, so one copy serves every reference.
func memoOnce[T any](memo map[int]T, off int, f func() (T, error)) (T, error) {
	if v, ok := memo[off]; ok {
		return v, nil
	}
	v, err := f()
	if err != nil {
		var zero T
		return zero, err
	}
	memo[off] = v
	return v, nil
}
