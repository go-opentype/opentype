// Copyright (c) 2026, the go-opentype authors
// All rights reserved.
//
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
)

// type1Font is a PostScript Type 1 font program: a dictionary of charstrings
// addressed by name, the subroutines they call, and the encoding the program
// was written with. It is the format PDF calls FontFile, and the one every
// PostScript printer of the nineteen-nineties spoke.
type type1Font struct {
	// names is every glyph, in the order they were read, which is the order
	// their indices are handed out in. .notdef is put first when the font has
	// one, so that glyph zero means what it means everywhere else.
	names       []string
	charstrings map[string][]byte
	byName      map[string]GlyphIndex
	subrs       [][]byte
	// encoding is what the program itself says a byte stands for; nil means
	// it said to use the standard one.
	encoding   map[byte]string
	fontMatrix [6]float64
}

// The three constants the eexec cipher is built on, and the two keys: one for
// the program's private half, one for each charstring inside it.
const (
	eexecC1      = 52845
	eexecC2      = 22719
	eexecKey     = 55665
	charstringR  = 4330
	defaultLenIV = 4
)

// ParseType1 decodes a PostScript Type 1 font program — what a PDF carries as
// a FontFile, and what a .pfb or .pfa file holds. Such a program addresses its
// glyphs by name and carries an encoding of its own, so the result answers
// [Font.GlyphIndexByName] and [Font.GlyphIndexByCode] and reports no character
// map.
//
// The bytes are not retained: a Type 1 program is decrypted on the way in, so
// what is kept is the plaintext.
func ParseType1(b []byte) (*Font, error) {
	clear, enc, err := type1Halves(b)
	if err != nil {
		return nil, err
	}
	private := eexecDecrypt(enc, eexecKey, 4)
	if len(private) == 0 {
		return nil, fmt.Errorf("opentype: type1: the private half is empty")
	}
	t1 := &type1Font{
		charstrings: map[string][]byte{},
		byName:      map[string]GlyphIndex{},
		fontMatrix:  defaultFontMatrix,
	}
	t1.readClear(clear)
	if err := t1.readPrivate(private); err != nil {
		return nil, err
	}
	if len(t1.names) == 0 {
		return nil, fmt.Errorf("opentype: type1: the font has no glyphs")
	}
	f := &Font{
		t1:               t1,
		numGlyphs:        len(t1.names),
		unitsPerEm:       unitsPerEmOf(t1.fontMatrix),
		numberOfHMetrics: len(t1.names),
		glyphNames:       t1.byName,
	}
	f.ascender = f.unitsPerEm
	f.advances, f.lsbs = t1.metrics()
	return f, nil
}

// type1Halves separates the program's readable half from its encrypted one,
// whichever container they arrived in: the segments of a .pfb file, the hex of
// a .pfa one, or the plain concatenation a PDF stream holds.
func type1Halves(b []byte) (clear, enc []byte, err error) {
	if len(b) >= 2 && b[0] == 0x80 {
		if b, err = joinPFB(b); err != nil {
			return nil, nil, err
		}
	}
	i := bytes.Index(b, []byte("eexec"))
	if i < 0 {
		return nil, nil, fmt.Errorf("opentype: type1: the program has no eexec section")
	}
	clear = b[:i]
	rest := b[i+len("eexec"):]
	for len(rest) > 0 && isPSSpace(rest[0]) {
		rest = rest[1:]
	}
	if isHexSection(rest) {
		return clear, unhex(rest), nil
	}
	return clear, rest, nil
}

// joinPFB concatenates the segments of a .pfb file, which wraps each half in a
// six-byte header rather than running them together.
func joinPFB(b []byte) ([]byte, error) {
	var out []byte
	for len(b) >= 2 && b[0] == 0x80 {
		switch b[1] {
		case 3:
			return out, nil
		case 1, 2:
			if len(b) < 6 {
				return nil, fmt.Errorf("opentype: type1: a pfb segment header is cut short")
			}
			n := int(b[2]) | int(b[3])<<8 | int(b[4])<<16 | int(b[5])<<24
			if n < 0 || 6+n > len(b) {
				return nil, fmt.Errorf("opentype: type1: a pfb segment runs past the end")
			}
			out = append(out, b[6:6+n]...)
			b = b[6+n:]
		default:
			return nil, fmt.Errorf("opentype: type1: unknown pfb segment %d", b[1])
		}
	}
	return out, nil
}

// hexLookahead is how many bytes must all be hexadecimal digits before the
// section is read as hexadecimal. The specification says four, which is not
// enough: a binary section begins with four random bytes, and four random
// bytes are all hexadecimal digits about once in twenty thousand times — often
// enough to happen across a corpus, and when it does the whole font is lost.
// Sixteen makes that impossible while still holding for every real one, whose
// hexadecimal runs are lines long.
const hexLookahead = 16

// isHexSection reports whether the encrypted half was written as hexadecimal,
// which is what a .pfa file does.
func isHexSection(b []byte) bool {
	n := 0
	for _, c := range b {
		if isPSSpace(c) {
			continue
		}
		if !isHexDigit(c) {
			return false
		}
		if n++; n == hexLookahead {
			return true
		}
	}
	return false
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func isPSSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == 0
}

// unhex reads a hexadecimal section, ignoring whatever whitespace it was laid
// out with.
func unhex(b []byte) []byte {
	out := make([]byte, 0, len(b)/2)
	var hi byte
	have := false
	for _, c := range b {
		if !isHexDigit(c) {
			continue
		}
		v := hexVal(c)
		if !have {
			hi, have = v, true
			continue
		}
		out = append(out, hi<<4|v)
		have = false
	}
	return out
}

func hexVal(c byte) byte {
	switch {
	case c <= '9':
		return c - '0'
	case c <= 'F':
		return c - 'A' + 10
	default:
		return c - 'a' + 10
	}
}

// eexecDecrypt undoes the cipher a Type 1 program is written in, throwing away
// the leading bytes that exist only to randomise it.
func eexecDecrypt(data []byte, key uint16, skip int) []byte {
	r := key
	out := make([]byte, len(data))
	for i, c := range data {
		out[i] = c ^ byte(r>>8)
		r = (uint16(c)+r)*eexecC1 + eexecC2
	}
	if len(out) < skip {
		return nil
	}
	return out[skip:]
}

// readClear takes what the readable half says: the font's matrix, and the
// encoding it was written with.
func (t *type1Font) readClear(b []byte) {
	if m, ok := readMatrix(b); ok {
		t.fontMatrix = m
	}
	t.encoding = readType1Encoding(b)
}

// readMatrix reads /FontMatrix, which says how big the program's own units are.
func readMatrix(b []byte) ([6]float64, bool) {
	var m [6]float64
	i := bytes.Index(b, []byte("/FontMatrix"))
	if i < 0 {
		return m, false
	}
	open := bytes.IndexByte(b[i:], '[')
	if open < 0 {
		return m, false
	}
	close := bytes.IndexByte(b[i+open:], ']')
	if close < 0 {
		return m, false
	}
	fields := bytes.Fields(b[i+open+1 : i+open+close])
	if len(fields) < 6 {
		return m, false
	}
	for k := 0; k < 6; k++ {
		v, err := strconv.ParseFloat(string(fields[k]), 64)
		if err != nil {
			return m, false
		}
		m[k] = v
	}
	if m[0] == 0 {
		return m, false
	}
	return m, true
}

// readType1Encoding reads the /Encoding the program was written with: either a
// name saying to use the standard one, or an array built up by putting a name
// at a code at a time.
func readType1Encoding(b []byte) map[byte]string {
	i := bytes.Index(b, []byte("/Encoding"))
	if i < 0 {
		return nil
	}
	rest := b[i+len("/Encoding"):]
	// Whatever comes first decides: a name, or an array being built.
	if j := bytes.Index(rest, []byte("StandardEncoding")); j >= 0 && j < 40 {
		return nil
	}
	out := map[byte]string{}
	for {
		j := bytes.Index(rest, []byte("dup "))
		if j < 0 {
			break
		}
		rest = rest[j+4:]
		fields := bytes.Fields(rest)
		if len(fields) < 3 || len(fields[1]) < 2 || fields[1][0] != '/' || string(fields[2]) != "put" {
			continue
		}
		code, err := strconv.Atoi(string(fields[0]))
		if err != nil || code < 0 || code > 255 {
			continue
		}
		out[byte(code)] = string(fields[1][1:])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// readPrivate takes what the encrypted half holds: how much of each charstring
// is padding, the subroutines, and the charstrings themselves.
func (t *type1Font) readPrivate(b []byte) error {
	lenIV := defaultLenIV
	if i := bytes.Index(b, []byte("/lenIV")); i >= 0 {
		if fields := bytes.Fields(b[i+len("/lenIV"):]); len(fields) > 0 {
			if v, err := strconv.Atoi(string(fields[0])); err == nil && v >= 0 && v < 16 {
				lenIV = v
			}
		}
	}
	t.readSubrs(b, lenIV)
	return t.readCharstrings(b, lenIV)
}

// readSubrs reads the /Subrs array: a numbered list of shared charstrings.
func (t *type1Font) readSubrs(b []byte, lenIV int) {
	i := bytes.Index(b, []byte("/Subrs"))
	if i < 0 {
		return
	}
	rest := b[i+len("/Subrs"):]
	fields := bytes.Fields(rest)
	if len(fields) == 0 {
		return
	}
	n, err := strconv.Atoi(string(fields[0]))
	if err != nil || n < 0 || n > 65535 {
		return
	}
	t.subrs = make([][]byte, n)
	for k := 0; k < n; k++ {
		j := bytes.Index(rest, []byte("dup "))
		if j < 0 {
			return
		}
		rest = rest[j+4:]
		idx, data, next, ok := readBinaryEntry(rest, lenIV)
		if !ok {
			return
		}
		if num, err := strconv.Atoi(string(idx)); err == nil && num >= 0 && num < n {
			t.subrs[num] = data
		}
		rest = next
	}
}

// readCharstrings reads the /CharStrings dictionary: a glyph name and its
// charstring, over and over.
func (t *type1Font) readCharstrings(b []byte, lenIV int) error {
	i := bytes.Index(b, []byte("/CharStrings"))
	if i < 0 {
		return fmt.Errorf("opentype: type1: the program has no CharStrings")
	}
	rest := b[i+len("/CharStrings"):]
	// Past the dictionary's own preamble, every entry begins with a name.
	if j := bytes.Index(rest, []byte("begin")); j >= 0 {
		rest = rest[j+len("begin"):]
	}
	for {
		j := bytes.IndexByte(rest, '/')
		if j < 0 {
			break
		}
		rest = rest[j+1:]
		raw, data, next, ok := readBinaryEntry(rest, lenIV)
		if !ok {
			break
		}
		name := string(raw)
		if _, seen := t.charstrings[name]; !seen && name != "" {
			t.charstrings[name] = data
			t.names = append(t.names, name)
		}
		rest = next
	}
	t.order()
	return nil
}

// readBinaryEntry reads "<key> <length> RD <length bytes> ..." — the shape
// both a subroutine and a charstring arrive in. The operator that introduces
// the bytes is spelt RD by some programs and -| by others, and neither is a
// keyword worth insisting on: what matters is that a number is followed by
// something that is not a number, and then that many bytes.
func readBinaryEntry(b []byte, lenIV int) (key, data []byte, rest []byte, ok bool) {
	name, after := psToken(b)
	if name == nil {
		return nil, nil, nil, false
	}
	lengthTok, after := psToken(after)
	n, err := strconv.Atoi(string(lengthTok))
	if err != nil || n < 0 || n > len(after) {
		return nil, nil, nil, false
	}
	_, after = psToken(after) // RD, -|, or whatever this program calls it
	if len(after) < 1+n {
		return nil, nil, nil, false
	}
	// Exactly one space separates the operator from the bytes.
	raw := after[1 : 1+n]
	return name, eexecDecrypt(raw, charstringR, lenIV), after[1+n:], true
}

// psToken reads one whitespace-delimited token.
func psToken(b []byte) (tok, rest []byte) {
	i := 0
	for i < len(b) && isPSSpace(b[i]) {
		i++
	}
	start := i
	for i < len(b) && !isPSSpace(b[i]) {
		i++
	}
	if start == i {
		return nil, b[i:]
	}
	return b[start:i], b[i:]
}

// order puts .notdef first, so that glyph zero means what it means in every
// other format, and indexes the rest where they fell.
func (t *type1Font) order() {
	for i, n := range t.names {
		if n == ".notdef" && i != 0 {
			t.names = append([]string{n}, append(t.names[:i:i], t.names[i+1:]...)...)
			break
		}
	}
	for i, n := range t.names {
		t.byName[n] = GlyphIndex(i)
	}
}

// metrics is what each glyph's charstring says it is wide, which for a Type 1
// program is the operand of its very first operator.
func (t *type1Font) metrics() (advances, lsbs []int) {
	advances = make([]int, len(t.names))
	lsbs = make([]int, len(t.names))
	for i, name := range t.names {
		sb, w, ok := type1Width(t.charstrings[name])
		if !ok {
			continue
		}
		advances[i] = int(math.Round(w))
		lsbs[i] = int(math.Round(sb))
	}
	return advances, lsbs
}

// glyphByCode maps a byte through the encoding the program was written with.
func (t *type1Font) glyphByCode(code byte) (GlyphIndex, bool) {
	name, ok := t.encoding[code]
	if !ok {
		if t.encoding != nil {
			return 0, false
		}
		sid, in := standardEncodingSID(int(code))
		if !in || int(sid) >= nStdStrings {
			return 0, false
		}
		name = cffStandardStrings[sid]
	}
	gid, ok := t.byName[name]
	return gid, ok
}

// glyphName is what the program calls a glyph.
func (t *type1Font) glyphName(gid int) (string, bool) {
	if gid < 0 || gid >= len(t.names) {
		return "", false
	}
	return t.names[gid], true
}
