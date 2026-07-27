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
}

// maxT2Depth bounds callsubr/callgsubr recursion; deeper nesting is rejected as
// malformed.
const maxT2Depth = 10

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

	localSubrs, err := parseLocalSubrs(data, top)
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
	}, nil
}

// parseLocalSubrs resolves the Private DICT referenced by the Top DICT and, if
// it declares a Subrs offset, decodes the Local Subr INDEX.
func parseLocalSubrs(data []byte, top map[int][]float64) ([][]byte, error) {
	pv, ok := top[18]
	if !ok {
		return nil, nil
	}
	if len(pv) < 2 {
		return nil, fmt.Errorf("opentype: cff: bad Private DICT entry")
	}
	psize, poff := int(pv[0]), int(pv[1])
	if psize < 0 || poff < 0 || poff+psize > len(data) {
		return nil, fmt.Errorf("opentype: cff: Private DICT out of range")
	}
	priv, err := parseDict(data[poff : poff+psize])
	if err != nil {
		return nil, err
	}
	soff, ok := dictInt(priv, 19, 0)
	if !ok {
		return nil, nil
	}
	soff += poff
	if soff < 0 || soff > len(data) {
		return nil, fmt.Errorf("opentype: cff: Local Subrs offset out of range")
	}
	return parseIndex(&reader{b: data, pos: soff})
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
}

// outline interprets glyph gid's Type2 charstring into its outline as a set of
// contours in font units (all points on-curve, cubics pre-flattened).
func (c *cffTable) outline(gid int) ([]contour, error) {
	if gid < 0 || gid >= len(c.charStrings) {
		return nil, fmt.Errorf("opentype: cff glyph %d out of range", gid)
	}
	m := &t2machine{c: c}
	if err := m.run(c.charStrings[gid], 0); err != nil {
		return nil, err
	}
	m.finishContour()
	return m.contours, nil
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
		m.stems()
	case 19, 20: // hintmask, cntrmask
		m.stems()
		nb := (m.nStems + 7) / 8
		if i+nb > len(code) {
			return false, i, fmt.Errorf("opentype: cff charstring: %w", errTruncated)
		}
		i += nb
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

// stems handles a stem-hint operator: strip a leading width once, add the
// declared stem pairs to the hint count, and clear the stack.
func (m *t2machine) stems() {
	m.width(true)
	m.nStems += len(m.stack) / 2
	m.clear()
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

// escape executes a two-byte (escape) operator: the flex family.
func (m *t2machine) escape(b1 byte) error {
	s := m.stack
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
