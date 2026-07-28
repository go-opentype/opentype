// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"fmt"
	"math"
	"strconv"
)

// This file adds Compact Font Format (CFF) / Type2 charstring outline support,
// so OpenType "OTTO" fonts carry renderable outlines in the same contour/point
// representation that glyf.go produces. Type2 charstrings describe outlines
// with cubic Béziers; the interpreter flattens each cubic into on-curve line
// segments, yielding []contour whose points are all on-curve — exactly what the
// rasteriser (raster.go) consumes for a polygon. It is deliberately standalone:
// nothing here is wired into Font/Face; a caller drives parseCFF + outline.

// cffTable is a parsed CFF table: the CharStrings INDEX (one charstring per
// glyph id), the global and local subroutine INDEXes with their computed
// biases, and the charstring type (only Type2 is supported).
type cffTable struct {
	charStrings    [][]byte
	globalSubrs    [][]byte
	localSubrs     [][]byte
	gsubrBias      int
	lsubrBias      int
	charstringType int
	sidToGid       map[int]int // charset: string id -> glyph id (for seac)
	priv           *cffPrivate // Private-DICT hint parameters (blue zones, std widths)
}

// maxT2Depth bounds callsubr/callgsubr recursion; deeper nesting is rejected as
// malformed.
const maxT2Depth = 10

// maxSeacDepth bounds endchar-seac accent composition recursion (a seac glyph
// whose base or accent is itself a seac), rejecting cycles as malformed.
const maxSeacDepth = 4

// maxT2Stack is the Type2 operand-stack limit (the spec permits 48 arguments).
const maxT2Stack = 48

// cffCurveSteps is the number of line segments a cubic Bézier is flattened into
// in font units. A fixed count keeps the interpreter branch-free here; the
// segments are re-scaled to device space by the rasteriser.
const cffCurveSteps = 8

// readOffset reads a size-byte (1..4) big-endian integer from r.
func readOffset(r *reader, size int) int {
	v := 0
	for k := 0; k < size; k++ {
		v = v<<8 | int(r.u8())
	}
	return v
}

// parseIndex decodes a CFF INDEX at r's current position, returning each stored
// object as a sub-slice of r.b and leaving r positioned just past the INDEX.
func parseIndex(r *reader) ([][]byte, error) {
	count := int(r.u16())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cff index count: %w", r.err)
	}
	if count == 0 {
		return nil, nil
	}
	offSize := int(r.u8())
	if offSize < 1 || offSize > 4 {
		return nil, fmt.Errorf("opentype: cff index: bad offSize %d", offSize)
	}
	offsets := make([]int, count+1)
	for k := range offsets {
		offsets[k] = readOffset(r, offSize)
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cff index offsets: %w", r.err)
	}
	base := r.pos - offsets[0]
	items := make([][]byte, count)
	for k := 0; k < count; k++ {
		start, end := base+offsets[k], base+offsets[k+1]
		if start < 0 || end > len(r.b) || start > end {
			return nil, fmt.Errorf("opentype: cff index: item %d out of range", k)
		}
		items[k] = r.b[start:end]
	}
	r.pos = base + offsets[count]
	return items, nil
}

// parseDict decodes a CFF DICT into a map from operator to its operand list.
// Two-byte operators (escape byte 12) are keyed as 1200+second-byte.
func parseDict(b []byte) (map[int][]float64, error) {
	d := map[int][]float64{}
	var operands []float64
	i := 0
	for i < len(b) {
		b0 := b[i]
		if b0 <= 21 {
			op := int(b0)
			i++
			if b0 == 12 {
				if i >= len(b) {
					return nil, fmt.Errorf("opentype: cff dict: truncated operator")
				}
				op = 1200 + int(b[i])
				i++
			}
			d[op] = operands
			operands = nil
			continue
		}
		v, n, err := parseDictOperand(b, i)
		if err != nil {
			return nil, err
		}
		operands = append(operands, v)
		i += n
	}
	return d, nil
}

// parseDictOperand decodes one DICT operand starting at b[i], returning the
// value and the number of bytes it occupied.
func parseDictOperand(b []byte, i int) (float64, int, error) {
	b0 := b[i]
	switch {
	case b0 == 28:
		if i+3 > len(b) {
			return 0, 0, fmt.Errorf("opentype: cff dict operand: %w", errTruncated)
		}
		return float64(int16(uint16(b[i+1])<<8 | uint16(b[i+2]))), 3, nil
	case b0 == 29:
		if i+5 > len(b) {
			return 0, 0, fmt.Errorf("opentype: cff dict operand: %w", errTruncated)
		}
		u := uint32(b[i+1])<<24 | uint32(b[i+2])<<16 | uint32(b[i+3])<<8 | uint32(b[i+4])
		return float64(int32(u)), 5, nil
	case b0 == 30:
		return parseDictReal(b, i)
	case b0 >= 32 && b0 <= 246:
		return float64(int(b0) - 139), 1, nil
	case b0 >= 247 && b0 <= 250:
		if i+2 > len(b) {
			return 0, 0, fmt.Errorf("opentype: cff dict operand: %w", errTruncated)
		}
		return float64((int(b0)-247)*256 + int(b[i+1]) + 108), 2, nil
	case b0 >= 251 && b0 <= 254:
		if i+2 > len(b) {
			return 0, 0, fmt.Errorf("opentype: cff dict operand: %w", errTruncated)
		}
		return float64(-(int(b0)-251)*256 - int(b[i+1]) - 108), 2, nil
	default:
		return 0, 0, fmt.Errorf("opentype: cff dict: reserved operand byte %d", b0)
	}
}

// parseDictReal decodes a real-number DICT operand (leading byte 30, BCD nibble
// encoding) starting at b[i].
func parseDictReal(b []byte, i int) (float64, int, error) {
	var s []byte
	j := i + 1
	done := false
	for !done {
		if j >= len(b) {
			return 0, 0, fmt.Errorf("opentype: cff dict real: %w", errTruncated)
		}
		by := b[j]
		j++
		for _, nib := range [2]byte{by >> 4, by & 0x0f} {
			switch {
			case nib <= 9:
				s = append(s, '0'+nib)
			case nib == 0x0a:
				s = append(s, '.')
			case nib == 0x0b:
				s = append(s, 'E')
			case nib == 0x0c:
				s = append(s, 'E', '-')
			case nib == 0x0e:
				s = append(s, '-')
			case nib == 0x0f:
				done = true
			default:
				return 0, 0, fmt.Errorf("opentype: cff dict real: bad nibble %d", nib)
			}
			if done {
				break
			}
		}
	}
	v, err := strconv.ParseFloat(string(s), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("opentype: cff dict real %q: %w", s, err)
	}
	return v, j - i, nil
}

// subrBias returns the Type2 subroutine-index bias for a subr INDEX of n
// entries (CFF spec: 107, 1131, or 32768).
func subrBias(n int) int {
	if n < 1240 {
		return 107
	}
	if n < 33900 {
		return 1131
	}
	return 32768
}

// dictInt returns operand idx of operator op in d as an int, or ok=false when
// the operator is absent or has too few operands.
func dictInt(d map[int][]float64, op, idx int) (int, bool) {
	v, ok := d[op]
	if !ok || idx >= len(v) {
		return 0, false
	}
	return int(v[idx]), true
}

// parseCFF decodes a CFF table: Header, Name INDEX, Top DICT INDEX, String
// INDEX, Global Subr INDEX, then from the Top DICT the CharStrings INDEX,
// Private DICT (and its Local Subr INDEX) and CharstringType.
func parseCFF(data []byte) (*cffTable, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("opentype: cff header: %w", errTruncated)
	}
	hdrSize := int(data[2])
	if hdrSize < 4 || hdrSize > len(data) {
		return nil, fmt.Errorf("opentype: cff: bad hdrSize %d", hdrSize)
	}
	r := &reader{b: data, pos: hdrSize}
	if _, err := parseIndex(r); err != nil { // Name INDEX
		return nil, err
	}
	topDicts, err := parseIndex(r) // Top DICT INDEX
	if err != nil {
		return nil, err
	}
	if len(topDicts) < 1 {
		return nil, fmt.Errorf("opentype: cff: empty Top DICT INDEX")
	}
	if _, err := parseIndex(r); err != nil { // String INDEX
		return nil, err
	}
	gsubrs, err := parseIndex(r) // Global Subr INDEX
	if err != nil {
		return nil, err
	}

	top, err := parseDict(topDicts[0])
	if err != nil {
		return nil, err
	}

	cst := 2
	if t, ok := dictInt(top, 1206, 0); ok {
		cst = t
	}
	if cst != 2 {
		return nil, fmt.Errorf("opentype: cff: unsupported CharstringType %d", cst)
	}

	csOff, ok := dictInt(top, 17, 0)
	if !ok {
		return nil, fmt.Errorf("opentype: cff: missing CharStrings offset")
	}
	if csOff < 0 || csOff > len(data) {
		return nil, fmt.Errorf("opentype: cff: CharStrings offset out of range")
	}
	charStrings, err := parseIndex(&reader{b: data, pos: csOff})
	if err != nil {
		return nil, err
	}

	localSubrs, priv, err := parseLocalSubrs(data, top)
	if err != nil {
		return nil, err
	}

	charsetOff, _ := dictInt(top, 15, 0) // absent -> 0 (ISOAdobe predefined)
	sidToGid, err := parseCharset(data, charsetOff, len(charStrings))
	if err != nil {
		return nil, err
	}

	return &cffTable{
		charStrings:    charStrings,
		globalSubrs:    gsubrs,
		localSubrs:     localSubrs,
		gsubrBias:      subrBias(len(gsubrs)),
		lsubrBias:      subrBias(len(localSubrs)),
		charstringType: cst,
		sidToGid:       sidToGid,
		priv:           priv,
	}, nil
}

// parseCharset decodes the CFF charset (Top DICT operator 15) into a string-id
// -> glyph-id map, used to resolve endchar-seac base/accent glyph references.
// A predefined charset (offset 0, 1 or 2) or an absent operator is treated as
// the identity mapping (glyph i has string id i), which is exact for the
// ISOAdobe charset. Formats 0, 1 and 2 of an embedded charset are decoded.
func parseCharset(data []byte, off, nGlyphs int) (map[int]int, error) {
	m := map[int]int{0: 0} // glyph 0 (.notdef) is always string id 0
	if off <= 2 {
		for gid := 0; gid < nGlyphs; gid++ {
			m[gid] = gid
		}
		return m, nil
	}
	if off > len(data) {
		return nil, fmt.Errorf("opentype: cff charset offset out of range")
	}
	r := &reader{b: data, pos: off}
	format := r.u8()
	gid := 1
	switch format {
	case 0:
		for gid < nGlyphs {
			m[int(r.u16())] = gid
			gid++
		}
	case 1:
		for gid < nGlyphs {
			first := int(r.u16())
			nLeft := int(r.u8())
			for i := 0; i <= nLeft && gid < nGlyphs; i++ {
				m[first+i] = gid
				gid++
			}
		}
	case 2:
		for gid < nGlyphs {
			first := int(r.u16())
			nLeft := int(r.u16())
			for i := 0; i <= nLeft && gid < nGlyphs; i++ {
				m[first+i] = gid
				gid++
			}
		}
	default:
		return nil, fmt.Errorf("opentype: cff charset: unsupported format %d", format)
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cff charset: %w", r.err)
	}
	return m, nil
}

// parseLocalSubrs resolves the Private DICT referenced by the Top DICT: it
// parses the Private-DICT hint parameters (blue zones, standard/snap stem
// widths) and, if the DICT declares a Subrs offset, decodes the Local Subr
// INDEX. A font with no Private DICT yields nil hints and no local subrs.
func parseLocalSubrs(data []byte, top map[int][]float64) ([][]byte, *cffPrivate, error) {
	pv, ok := top[18]
	if !ok {
		return nil, nil, nil
	}
	if len(pv) < 2 {
		return nil, nil, fmt.Errorf("opentype: cff: bad Private DICT entry")
	}
	psize, poff := int(pv[0]), int(pv[1])
	if psize < 0 || poff < 0 || poff+psize > len(data) {
		return nil, nil, fmt.Errorf("opentype: cff: Private DICT out of range")
	}
	priv, err := parseDict(data[poff : poff+psize])
	if err != nil {
		return nil, nil, err
	}
	hints := parsePrivateHints(priv)
	soff, ok := dictInt(priv, 19, 0)
	if !ok {
		return nil, hints, nil
	}
	soff += poff
	if soff < 0 || soff > len(data) {
		return nil, nil, fmt.Errorf("opentype: cff: Local Subrs offset out of range")
	}
	ls, err := parseIndex(&reader{b: data, pos: soff})
	if err != nil {
		return nil, nil, err
	}
	return ls, hints, nil
}

// t2machine is the Type2 charstring interpreter state for one glyph: the
// operand stack, the pen position, the contour under construction and the
// finished contours, the accumulated stem-hint count (for hintmask byte
// sizing), and the width/endchar flags.
type t2machine struct {
	c         *cffTable
	stack     []float64
	x, y      float64
	cur       contour
	contours  []contour
	nStems    int
	widthDone bool
	done      bool
	seacDepth int // endchar-seac accent-composition recursion depth

	// Hinting data recorded during interpretation for the grid-fitter (cffhint.go):
	// the resolved stem-edge positions in font units (in declaration order, which
	// is the order hintmask/cntrmask bits index), the per-hintmask activation, the
	// counter-control (cntrmask) stem groups, and the flex-region point ranges.
	stemHints  []cffStemHint
	hintMasks  []cffHintMask
	cntrGroups [][]int
	flexRanges []flexRange
}

// outline interprets glyph gid's Type2 charstring into its outline as a set of
// contours in font units (all points on-curve, cubics pre-flattened).
func (c *cffTable) outline(gid int) ([]contour, error) {
	return c.outlineSeac(gid, 0)
}

// runGlyph interprets glyph gid's Type2 charstring and returns the finished
// interpreter, from which the outline (m.contours) and the recorded hinting
// data (m.stemHints, m.hintMasks) can both be read. seacDepth carries the
// endchar-seac recursion depth so a seac's components can be composed.
func (c *cffTable) runGlyph(gid, seacDepth int) (*t2machine, error) {
	if gid < 0 || gid >= len(c.charStrings) {
		return nil, fmt.Errorf("opentype: cff glyph %d out of range", gid)
	}
	m := &t2machine{c: c, seacDepth: seacDepth}
	if err := m.run(c.charStrings[gid], 0); err != nil {
		return nil, err
	}
	m.finishContour()
	return m, nil
}

// outlineSeac is outline with an explicit endchar-seac recursion depth so that
// a seac's base and accent components (themselves glyphs) can be composed.
func (c *cffTable) outlineSeac(gid, seacDepth int) ([]contour, error) {
	m, err := c.runGlyph(gid, seacDepth)
	if err != nil {
		return nil, err
	}
	return m.contours, nil
}

// outlineHints interprets glyph gid and returns its outline together with the
// hinting data (recorded stems, per-hintmask activation and the font's parsed
// Private-DICT hint parameters) that cffhint.go's grid-fitter consumes.
func (c *cffTable) outlineHints(gid int) ([]contour, *cffGlyphHints, error) {
	m, err := c.runGlyph(gid, 0)
	if err != nil {
		return nil, nil, err
	}
	return m.contours, &cffGlyphHints{
		stems:      m.stemHints,
		hintMasks:  m.hintMasks,
		cntrGroups: m.cntrGroups,
		flexRanges: m.flexRanges,
		priv:       c.priv,
	}, nil
}

// push appends v to the operand stack, guarding the stack-depth limit.
func (m *t2machine) push(v float64) error {
	if len(m.stack) >= maxT2Stack {
		return fmt.Errorf("opentype: cff: charstring stack overflow")
	}
	m.stack = append(m.stack, v)
	return nil
}

// clear empties the operand stack (every operator is stack-clearing in Type2).
func (m *t2machine) clear() { m.stack = m.stack[:0] }

// width consumes a leading width operand on the operand stack the first time a
// width-bearing operator runs. expectEven states the parity of the operator's
// own argument count; a mismatch means a width value precedes the arguments.
func (m *t2machine) width(expectEven bool) {
	if m.widthDone {
		return
	}
	m.widthDone = true
	if len(m.stack) > 0 && (len(m.stack)%2 == 0) != expectEven {
		m.stack = m.stack[1:]
	}
}

// finishContour appends the contour under construction (if any) to the result.
func (m *t2machine) finishContour() {
	if len(m.cur) > 0 {
		m.contours = append(m.contours, m.cur)
		m.cur = nil
	}
}

// moveTo closes the current contour and starts a new one at the relative point.
func (m *t2machine) moveTo(dx, dy float64) {
	m.finishContour()
	m.x += dx
	m.y += dy
	m.cur = contour{{x: m.x, y: m.y, on: true}}
}

// lineTo adds a straight segment to the relative point.
func (m *t2machine) lineTo(dx, dy float64) {
	m.x += dx
	m.y += dy
	m.cur = append(m.cur, outlinePoint{x: m.x, y: m.y, on: true})
}

// curveTo flattens a cubic Bézier from the pen position through control points
// (x1,y1),(x2,y2) to the endpoint (x3,y3), appending on-curve sample points.
func (m *t2machine) curveTo(x1, y1, x2, y2, x3, y3 float64) {
	x0, y0 := m.x, m.y
	for k := 1; k <= cffCurveSteps; k++ {
		t := float64(k) / float64(cffCurveSteps)
		u := 1 - t
		bx := u*u*u*x0 + 3*u*u*t*x1 + 3*u*t*t*x2 + t*t*t*x3
		by := u*u*u*y0 + 3*u*u*t*y1 + 3*u*t*t*y2 + t*t*t*y3
		m.cur = append(m.cur, outlinePoint{x: bx, y: by, on: true})
	}
	m.x, m.y = x3, y3
}

// run executes a charstring (or subroutine) body. depth guards recursion.
func (m *t2machine) run(code []byte, depth int) error {
	if depth > maxT2Depth {
		return fmt.Errorf("opentype: cff: subroutine recursion too deep")
	}
	i := 0
	for i < len(code) {
		b0 := code[i]
		i++
		if b0 >= 32 || b0 == 28 {
			v, n, err := m.operand(code, i-1)
			if err != nil {
				return err
			}
			if err := m.push(v); err != nil {
				return err
			}
			i += n - 1
			continue
		}
		stop, ni, err := m.operator(b0, code, i, depth)
		if err != nil {
			return err
		}
		i = ni
		if stop {
			return nil
		}
	}
	return nil
}

// operand decodes a numeric charstring operand beginning at code[i], returning
// the value and its total byte length (including the leading byte).
func (m *t2machine) operand(code []byte, i int) (float64, int, error) {
	b0 := code[i]
	switch {
	case b0 == 28:
		if i+3 > len(code) {
			return 0, 0, fmt.Errorf("opentype: cff charstring: %w", errTruncated)
		}
		return float64(int16(uint16(code[i+1])<<8 | uint16(code[i+2]))), 3, nil
	case b0 == 255:
		if i+5 > len(code) {
			return 0, 0, fmt.Errorf("opentype: cff charstring: %w", errTruncated)
		}
		u := uint32(code[i+1])<<24 | uint32(code[i+2])<<16 | uint32(code[i+3])<<8 | uint32(code[i+4])
		return float64(int32(u)) / 65536.0, 5, nil
	case b0 < 247: // 32..246
		return float64(int(b0) - 139), 1, nil
	case b0 < 251: // 247..250
		if i+2 > len(code) {
			return 0, 0, fmt.Errorf("opentype: cff charstring: %w", errTruncated)
		}
		return float64((int(b0)-247)*256 + int(code[i+1]) + 108), 2, nil
	default: // 251..254
		if i+2 > len(code) {
			return 0, 0, fmt.Errorf("opentype: cff charstring: %w", errTruncated)
		}
		return float64(-(int(b0)-251)*256 - int(code[i+1]) - 108), 2, nil
	}
}

// operator executes one Type2 operator byte b0. code/i give the stream position
// just past b0 (for hintmask bytes and the escape second byte); it returns the
// updated stream index, whether interpretation should stop (return/endchar),
// and any error.
func (m *t2machine) operator(b0 byte, code []byte, i, depth int) (stop bool, ni int, err error) {
	switch b0 {
	case 1, 3, 18, 23: // hstem, vstem, hstemhm, vstemhm
		m.stems(b0 == 1 || b0 == 18)
	case 19, 20: // hintmask, cntrmask
		m.stems(false) // any operands here are implicit vstem hints
		ni, herr := m.readHintMask(code, i, b0 == 19)
		if herr != nil {
			return false, i, herr
		}
		i = ni
	case 21: // rmoveto
		m.width(true)
		if len(m.stack) < 2 {
			return false, i, fmt.Errorf("opentype: cff: rmoveto needs 2 args")
		}
		n := len(m.stack)
		m.moveTo(m.stack[n-2], m.stack[n-1])
		m.clear()
	case 22: // hmoveto
		m.width(false)
		if len(m.stack) < 1 {
			return false, i, fmt.Errorf("opentype: cff: hmoveto needs 1 arg")
		}
		m.moveTo(m.stack[len(m.stack)-1], 0)
		m.clear()
	case 4: // vmoveto
		m.width(false)
		if len(m.stack) < 1 {
			return false, i, fmt.Errorf("opentype: cff: vmoveto needs 1 arg")
		}
		m.moveTo(0, m.stack[len(m.stack)-1])
		m.clear()
	case 5: // rlineto
		s := m.stack
		for j := 0; j+2 <= len(s); j += 2 {
			m.lineTo(s[j], s[j+1])
		}
		m.clear()
	case 6: // hlineto
		m.altLine(true)
	case 7: // vlineto
		m.altLine(false)
	case 8: // rrcurveto
		m.rrcurveto(m.stack)
		m.clear()
	case 24: // rcurveline
		m.rcurveline(m.stack)
		m.clear()
	case 25: // rlinecurve
		m.rlinecurve(m.stack)
		m.clear()
	case 26: // vvcurveto
		m.vvcurveto(m.stack)
		m.clear()
	case 27: // hhcurveto
		m.hhcurveto(m.stack)
		m.clear()
	case 30: // vhcurveto
		m.altCurve(m.stack, false)
		m.clear()
	case 31: // hvcurveto
		m.altCurve(m.stack, true)
		m.clear()
	case 10: // callsubr
		return m.callSubr(m.c.localSubrs, m.c.lsubrBias, depth, i)
	case 29: // callgsubr
		return m.callSubr(m.c.globalSubrs, m.c.gsubrBias, depth, i)
	case 11: // return
		return true, i, nil
	case 14: // endchar
		m.width(true)
		if len(m.stack) >= 4 { // deprecated seac: adx ady bchar achar
			if err := m.seac(); err != nil {
				return false, i, err
			}
		}
		m.done = true
		return true, i, nil
	case 12: // escape: two-byte operator
		if i >= len(code) {
			return false, i, fmt.Errorf("opentype: cff charstring: truncated escape")
		}
		b1 := code[i]
		i++
		if err := m.escape(b1); err != nil {
			return false, i, err
		}
	default:
		return false, i, fmt.Errorf("opentype: cff: unknown operator %d", b0)
	}
	return false, i, nil
}

// stems handles a stem-hint operator: strip a leading width once, then record
// the declared stem pairs (which also advances the hint count and clears the
// stack). horizontal selects hstem (edges are Y positions) versus vstem (X).
func (m *t2machine) stems(horizontal bool) {
	m.width(true)
	m.recordStems(horizontal)
}

// recordStems resolves the operand pairs into absolute stem-edge positions in
// font units and appends them to m.stemHints, advancing the hint count and
// clearing the stack. Each pair is (edgeDelta, width); the first edge is
// relative to the glyph origin and each subsequent edge is relative to the
// previous stem's far edge, per the Type2 stem-hint encoding. It records the
// positions without the leading-width handling that stems does, so it is safe
// to reuse from CFF2 (which has no per-glyph width).
func (m *t2machine) recordStems(horizontal bool) {
	s := m.stack
	pos := 0.0
	for j := 0; j+1 < len(s); j += 2 {
		lo := pos + s[j]
		hi := lo + s[j+1]
		m.stemHints = append(m.stemHints, cffStemHint{horizontal: horizontal, min: lo, max: hi})
		pos = hi
	}
	m.nStems += len(s) / 2
	m.clear()
}

// readHintMask consumes a hintmask/cntrmask operator's mask bytes at code[i]
// (one bit per declared stem, high bit first) and returns the stream index just
// past them. When record is true (a hintmask, operator 19) the activated stem
// set is captured together with the point count reached so far, so the
// grid-fitter can apply the hints active at each point (including a mask that
// changes mid-subpath). A cntrmask (operator 20, record false) records the set
// of stems it selects as one counter-control group, whose counters (the gaps
// between the group's stems) the grid-fitter equalises (see cffhint.go).
func (m *t2machine) readHintMask(code []byte, i int, record bool) (int, error) {
	nb := (m.nStems + 7) / 8
	if i+nb > len(code) {
		return i, fmt.Errorf("opentype: cff hintmask: %w", errTruncated)
	}
	active := make([]bool, m.nStems)
	for k := 0; k < m.nStems; k++ {
		if code[i+k/8]&(0x80>>(uint(k)%8)) != 0 {
			active[k] = true
		}
	}
	if record {
		m.hintMasks = append(m.hintMasks, cffHintMask{pointIndex: m.pointCount(), active: active})
	} else {
		var group []int
		for k, on := range active {
			if on {
				group = append(group, k)
			}
		}
		m.cntrGroups = append(m.cntrGroups, group)
	}
	return i + nb, nil
}

// pointCount returns the number of outline points emitted so far (finished
// contours plus the contour under construction), used to stamp each hintmask
// with the subpath position at which it takes effect.
func (m *t2machine) pointCount() int {
	n := len(m.cur)
	for _, c := range m.contours {
		n += len(c)
	}
	return n
}

// callSubr executes the subroutine selected by the top-of-stack index (plus the
// bias) from subrs, returning the endchar-propagating stop flag.
func (m *t2machine) callSubr(subrs [][]byte, bias, depth, i int) (bool, int, error) {
	if len(m.stack) < 1 {
		return false, i, fmt.Errorf("opentype: cff: callsubr needs an index")
	}
	idx := int(m.stack[len(m.stack)-1]) + bias
	m.stack = m.stack[:len(m.stack)-1]
	if idx < 0 || idx >= len(subrs) {
		return false, i, fmt.Errorf("opentype: cff: subr index %d out of range", idx)
	}
	if err := m.run(subrs[idx], depth+1); err != nil {
		return false, i, err
	}
	return m.done, i, nil
}

// escape executes a two-byte (escape) operator: the flex family. Every flex
// variant draws two shallow cubic curves; the point range they emit is recorded
// as a flexRange so the grid-fitter can keep the flex smooth instead of snapping
// its near-flat span to a stem edge (see cffhint.go).
func (m *t2machine) escape(b1 byte) error {
	s := m.stack
	startIdx := m.pointCount()
	switch b1 {
	case 34: // hflex: dx1 dx2 dy2 dx3 dx4 dx5 dx6
		if len(s) < 7 {
			return fmt.Errorf("opentype: cff: hflex needs 7 args")
		}
		x1, y1 := m.x+s[0], m.y
		x2, y2 := x1+s[1], y1+s[2]
		x3, y3 := x2+s[3], y2
		m.curveTo(x1, y1, x2, y2, x3, y3)
		x4, y4 := m.x+s[4], m.y
		x5, y5 := x4+s[5], y4-s[2]
		x6, y6 := x5+s[6], y5
		m.curveTo(x4, y4, x5, y5, x6, y6)
	case 35: // flex: 12 deltas + fd
		if len(s) < 12 {
			return fmt.Errorf("opentype: cff: flex needs 13 args")
		}
		x1, y1 := m.x+s[0], m.y+s[1]
		x2, y2 := x1+s[2], y1+s[3]
		x3, y3 := x2+s[4], y2+s[5]
		m.curveTo(x1, y1, x2, y2, x3, y3)
		x4, y4 := m.x+s[6], m.y+s[7]
		x5, y5 := x4+s[8], y4+s[9]
		x6, y6 := x5+s[10], y5+s[11]
		m.curveTo(x4, y4, x5, y5, x6, y6)
	case 36: // hflex1: dx1 dy1 dx2 dy2 dx3 dx4 dx5 dy5 dx6
		if len(s) < 9 {
			return fmt.Errorf("opentype: cff: hflex1 needs 9 args")
		}
		sy := m.y
		x1, y1 := m.x+s[0], m.y+s[1]
		x2, y2 := x1+s[2], y1+s[3]
		x3, y3 := x2+s[4], y2
		m.curveTo(x1, y1, x2, y2, x3, y3)
		x4, y4 := m.x+s[5], m.y
		x5, y5 := x4+s[6], y4+s[7]
		x6, y6 := x5+s[8], sy
		m.curveTo(x4, y4, x5, y5, x6, y6)
	case 37: // flex1: 10 deltas + d6
		if len(s) < 11 {
			return fmt.Errorf("opentype: cff: flex1 needs 11 args")
		}
		sx, sy := m.x, m.y
		dx := s[0] + s[2] + s[4] + s[6] + s[8]
		dy := s[1] + s[3] + s[5] + s[7] + s[9]
		x1, y1 := m.x+s[0], m.y+s[1]
		x2, y2 := x1+s[2], y1+s[3]
		x3, y3 := x2+s[4], y2+s[5]
		m.curveTo(x1, y1, x2, y2, x3, y3)
		x4, y4 := m.x+s[6], m.y+s[7]
		x5, y5 := x4+s[8], y4+s[9]
		var x6, y6 float64
		if math.Abs(dx) > math.Abs(dy) {
			x6, y6 = x5+s[10], sy
		} else {
			x6, y6 = sx, y5+s[10]
		}
		m.curveTo(x4, y4, x5, y5, x6, y6)
	default:
		return fmt.Errorf("opentype: cff: unknown escape operator %d", b1)
	}
	m.flexRanges = append(m.flexRanges, flexRange{start: startIdx, end: m.pointCount() - 1})
	m.clear()
	return nil
}

// rrcurveto draws successive cubic curves from groups of six relative deltas.
func (m *t2machine) rrcurveto(s []float64) {
	for j := 0; j+6 <= len(s); j += 6 {
		m.relCurve(s[j], s[j+1], s[j+2], s[j+3], s[j+4], s[j+5])
	}
}

// rcurveline draws cubic curves then a final straight line (trailing pair).
func (m *t2machine) rcurveline(s []float64) {
	j := 0
	for ; j+6 <= len(s)-2; j += 6 {
		m.relCurve(s[j], s[j+1], s[j+2], s[j+3], s[j+4], s[j+5])
	}
	m.lineTo(s[j], s[j+1])
}

// rlinecurve draws straight lines then a final cubic curve (trailing six).
func (m *t2machine) rlinecurve(s []float64) {
	j := 0
	for ; j+2 <= len(s)-6; j += 2 {
		m.lineTo(s[j], s[j+1])
	}
	m.relCurve(s[j], s[j+1], s[j+2], s[j+3], s[j+4], s[j+5])
}

// relCurve draws one cubic from six relative deltas (two control points then
// the endpoint), each relative to the previous.
func (m *t2machine) relCurve(dxa, dya, dxb, dyb, dxc, dyc float64) {
	x1, y1 := m.x+dxa, m.y+dya
	x2, y2 := x1+dxb, y1+dyb
	x3, y3 := x2+dxc, y2+dyc
	m.curveTo(x1, y1, x2, y2, x3, y3)
}

// hhcurveto draws curves whose start and end tangents are horizontal, with an
// optional leading dy1 for the first curve.
func (m *t2machine) hhcurveto(s []float64) {
	j := 0
	dy1 := 0.0
	if len(s)%4 == 1 {
		dy1 = s[0]
		j = 1
	}
	for ; j+4 <= len(s); j += 4 {
		x1, y1 := m.x+s[j], m.y+dy1
		x2, y2 := x1+s[j+1], y1+s[j+2]
		x3, y3 := x2+s[j+3], y2
		m.curveTo(x1, y1, x2, y2, x3, y3)
		dy1 = 0
	}
}

// vvcurveto draws curves whose start and end tangents are vertical, with an
// optional leading dx1 for the first curve.
func (m *t2machine) vvcurveto(s []float64) {
	j := 0
	dx1 := 0.0
	if len(s)%4 == 1 {
		dx1 = s[0]
		j = 1
	}
	for ; j+4 <= len(s); j += 4 {
		x1, y1 := m.x+dx1, m.y+s[j]
		x2, y2 := x1+s[j+1], y1+s[j+2]
		x3, y3 := x2, y2+s[j+3]
		m.curveTo(x1, y1, x2, y2, x3, y3)
		dx1 = 0
	}
}

// altCurve implements hvcurveto (startHoriz true) and vhcurveto (false), whose
// curves alternate between horizontal and vertical start tangents, with an
// optional extra final delta on the last curve.
func (m *t2machine) altCurve(s []float64, startHoriz bool) {
	j := 0
	horiz := startHoriz
	for len(s)-j >= 4 {
		last := 0.0
		if len(s)-j == 5 {
			last = s[j+4]
		}
		if horiz {
			x1, y1 := m.x+s[j], m.y
			x2, y2 := x1+s[j+1], y1+s[j+2]
			x3, y3 := x2+last, y2+s[j+3]
			m.curveTo(x1, y1, x2, y2, x3, y3)
		} else {
			x1, y1 := m.x, m.y+s[j]
			x2, y2 := x1+s[j+1], y1+s[j+2]
			x3, y3 := x2+s[j+3], y2+last
			m.curveTo(x1, y1, x2, y2, x3, y3)
		}
		j += 4
		horiz = !horiz
	}
}

// altLine implements hlineto (startHoriz true) and vlineto (false), drawing
// alternating horizontal/vertical segments, one per operand.
func (m *t2machine) altLine(startHoriz bool) {
	horiz := startHoriz
	for _, d := range m.stack {
		if horiz {
			m.lineTo(d, 0)
		} else {
			m.lineTo(0, d)
		}
		horiz = !horiz
	}
	m.clear()
}

// seac composes the accented glyph described by the four endchar-seac operands
// (adx ady bchar achar) left on the stack: the base glyph's outline plus the
// accent glyph's outline shifted by (adx, ady). bchar and achar are Standard
// Encoding codes, resolved to glyphs via the Standard Encoding and the CFF
// charset. Any outline the seac charstring drew itself is retained.
func (m *t2machine) seac() error {
	if m.seacDepth >= maxSeacDepth {
		return fmt.Errorf("opentype: cff seac: composition nesting too deep")
	}
	n := len(m.stack)
	adx, ady := m.stack[n-4], m.stack[n-3]
	bchar, achar := int(m.stack[n-2]), int(m.stack[n-1])
	base, err := m.c.seacComponent(bchar, m.seacDepth+1)
	if err != nil {
		return err
	}
	accent, err := m.c.seacComponent(achar, m.seacDepth+1)
	if err != nil {
		return err
	}
	m.finishContour()
	m.contours = append(m.contours, base...)
	for _, cont := range accent {
		nc := make(contour, len(cont))
		for i, p := range cont {
			nc[i] = outlinePoint{x: p.x + adx, y: p.y + ady, on: p.on}
		}
		m.contours = append(m.contours, nc)
	}
	m.clear()
	return nil
}

// seacComponent resolves a Standard Encoding code to a glyph id (via the
// standard encoding and the CFF charset) and returns that glyph's outline.
func (c *cffTable) seacComponent(code, seacDepth int) ([]contour, error) {
	sid, ok := standardEncodingSID(code)
	if !ok {
		return nil, fmt.Errorf("opentype: cff seac: code %d not in standard encoding", code)
	}
	gid, ok := c.sidToGid[int(sid)]
	if !ok {
		return nil, fmt.Errorf("opentype: cff seac: no glyph for string id %d", sid)
	}
	return c.outlineSeac(gid, seacDepth)
}

// standardEncodingSID maps a Standard Encoding character code to its CFF
// standard-string id (SID). For the printable ASCII range 32..126 the SID is
// simply code-31; the punctuation/accent codes 161..251 use a lookup table.
// ok is false for codes with no Standard Encoding glyph.
func standardEncodingSID(code int) (uint16, bool) {
	if code >= 32 && code <= 126 {
		return uint16(code - 31), true
	}
	sid, ok := stdEncodingHigh[code]
	return sid, ok
}

// stdEncodingHigh holds the Standard Encoding codes above the printable ASCII
// range (161..251) and their CFF standard-string ids.
var stdEncodingHigh = map[int]uint16{
	161: 96, 162: 97, 163: 98, 164: 99, 165: 100, 166: 101, 167: 102, 168: 103,
	169: 104, 170: 105, 171: 106, 172: 107, 173: 108, 174: 109, 175: 110,
	177: 111, 178: 112, 179: 113, 180: 114, 182: 115, 183: 116, 184: 117,
	185: 118, 186: 119, 187: 120, 188: 121, 189: 122, 191: 123, 193: 124,
	194: 125, 195: 126, 196: 127, 197: 128, 198: 129, 199: 130, 200: 131,
	202: 132, 203: 133, 205: 134, 206: 135, 207: 136, 208: 137, 225: 138,
	227: 139, 232: 140, 233: 141, 234: 142, 235: 143, 241: 144, 245: 145,
	248: 146, 249: 147, 250: 148, 251: 149,
}
