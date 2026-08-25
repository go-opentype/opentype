// Copyright (c) 2026, the go-opentype authors
// All rights reserved.
//
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// The Type 1 charstring operators. They are not the Type 2 ones: the two
// formats share a lineage and very little else.
const (
	t1HStem     = 1
	t1VStem     = 3
	t1VMoveTo   = 4
	t1RLineTo   = 5
	t1HLineTo   = 6
	t1VLineTo   = 7
	t1RRCurveTo = 8
	t1ClosePath = 9
	t1CallSubr  = 10
	t1Return    = 11
	t1Escape    = 12
	t1HSbw      = 13
	t1EndChar   = 14
	t1RMoveTo   = 21
	t1HMoveTo   = 22
	t1VHCurveTo = 30
	t1HVCurveTo = 31
)

// The escaped operators, reached through operator 12.
const (
	t1DotSection        = 0
	t1VStem3            = 1
	t1HStem3            = 2
	t1Seac              = 6
	t1Sbw               = 7
	t1Div               = 12
	t1CallOtherSubr     = 16
	t1Pop               = 17
	t1SetCurrentPoint   = 33
	maxT1Depth          = 30
	maxT1Stack          = 48
	maxT1PostScriptSize = 32
)

// t1machine runs one Type 1 charstring.
type t1machine struct {
	f         *type1Font
	stack     []float64
	ps        []float64 // the interpreter's other stack, which othersubrs use
	contours  []contour
	cur       contour
	x, y      float64
	sbx, sby  float64
	width     float64
	haveWidth bool
	// flex collects the seven points an othersubr-driven curve is built from;
	// while it is running, a move records a point instead of starting a
	// contour.
	inFlex    bool
	flexPts   []outlinePoint
	seacDepth int
	done      bool
}

// outline draws a glyph of a Type 1 program.
func (t *type1Font) outline(gid int) ([]contour, error) {
	name, ok := t.glyphName(gid)
	if !ok {
		return nil, fmt.Errorf("opentype: type1: glyph index %d out of range", gid)
	}
	return t.outlineByName(name, 0)
}

// outlineByName draws the glyph a name stands for.
func (t *type1Font) outlineByName(name string, seacDepth int) ([]contour, error) {
	cs, ok := t.charstrings[name]
	if !ok {
		return nil, fmt.Errorf("opentype: type1: no glyph called %q", name)
	}
	m := &t1machine{f: t, seacDepth: seacDepth}
	if err := m.run(cs, 0); err != nil {
		return nil, err
	}
	m.finishContour()
	return m.contours, nil
}

// type1Width reads what a charstring says its glyph is wide, which is the
// operand of hsbw or sbw — always the first operator of a Type 1 glyph.
func type1Width(cs []byte) (sb, w float64, ok bool) {
	var stack []float64
	for i := 0; i < len(cs); {
		v, next, isNum, valid := t1Operand(cs, i)
		if !valid {
			return 0, 0, false
		}
		if isNum {
			stack = append(stack, v)
			i = next
			continue
		}
		switch cs[i] {
		case t1HSbw:
			if len(stack) < 2 {
				return 0, 0, false
			}
			return stack[0], stack[1], true
		case t1Escape:
			if i+1 < len(cs) && cs[i+1] == t1Sbw {
				if len(stack) < 4 {
					return 0, 0, false
				}
				return stack[0], stack[2], true
			}
			return 0, 0, false
		default:
			return 0, 0, false
		}
	}
	return 0, 0, false
}

// t1Operand reads one charstring byte, saying whether it was a number and
// where the next one is.
func t1Operand(cs []byte, i int) (v float64, next int, isNum, ok bool) {
	b := cs[i]
	switch {
	case b >= 32 && b <= 246:
		return float64(int(b) - 139), i + 1, true, true
	case b >= 247 && b <= 250:
		if i+1 >= len(cs) {
			return 0, 0, false, false
		}
		return float64((int(b)-247)*256 + int(cs[i+1]) + 108), i + 2, true, true
	case b >= 251 && b <= 254:
		if i+1 >= len(cs) {
			return 0, 0, false, false
		}
		return float64(-(int(b)-251)*256 - int(cs[i+1]) - 108), i + 2, true, true
	case b == 255:
		if i+4 >= len(cs) {
			return 0, 0, false, false
		}
		n := int32(uint32(cs[i+1])<<24 | uint32(cs[i+2])<<16 | uint32(cs[i+3])<<8 | uint32(cs[i+4]))
		return float64(n), i + 5, true, true
	}
	return 0, i, false, true
}

// run executes a charstring or a subroutine.
func (m *t1machine) run(cs []byte, depth int) error {
	if depth > maxT1Depth {
		return fmt.Errorf("opentype: type1: subroutine nesting too deep")
	}
	for i := 0; i < len(cs); {
		v, next, isNum, ok := t1Operand(cs, i)
		if !ok {
			return fmt.Errorf("opentype: type1: a charstring operand runs past the end")
		}
		if isNum {
			if len(m.stack) >= maxT1Stack {
				return fmt.Errorf("opentype: type1: too many operands")
			}
			m.stack = append(m.stack, v)
			i = next
			continue
		}
		op := cs[i]
		i++
		if op == t1Escape {
			if i >= len(cs) {
				return fmt.Errorf("opentype: type1: a charstring ends in an escape")
			}
			esc := cs[i]
			i++
			stop, err := m.escaped(esc, depth)
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
			continue
		}
		stop, err := m.operator(op, depth)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

// operator runs one plain operator, reporting whether the charstring is over.
func (m *t1machine) operator(op byte, depth int) (bool, error) {
	s := m.stack
	switch op {
	case t1HStem, t1VStem:
		m.clear()
	case t1HSbw:
		if len(s) >= 2 {
			m.sbx, m.width, m.haveWidth = s[0], s[1], true
			m.x, m.y = m.sbx, 0
		}
		m.clear()
	case t1RMoveTo:
		if len(s) >= 2 {
			m.moveTo(s[len(s)-2], s[len(s)-1])
		}
		m.clear()
	case t1HMoveTo:
		if len(s) >= 1 {
			m.moveTo(s[len(s)-1], 0)
		}
		m.clear()
	case t1VMoveTo:
		if len(s) >= 1 {
			m.moveTo(0, s[len(s)-1])
		}
		m.clear()
	case t1RLineTo:
		if len(s) >= 2 {
			m.lineTo(s[0], s[1])
		}
		m.clear()
	case t1HLineTo:
		if len(s) >= 1 {
			m.lineTo(s[0], 0)
		}
		m.clear()
	case t1VLineTo:
		if len(s) >= 1 {
			m.lineTo(0, s[0])
		}
		m.clear()
	case t1RRCurveTo:
		if len(s) >= 6 {
			m.rrcurveTo(s[0], s[1], s[2], s[3], s[4], s[5])
		}
		m.clear()
	case t1HVCurveTo:
		if len(s) >= 4 {
			m.rrcurveTo(s[0], 0, s[1], s[2], 0, s[3])
		}
		m.clear()
	case t1VHCurveTo:
		if len(s) >= 4 {
			m.rrcurveTo(0, s[0], s[1], s[2], s[3], 0)
		}
		m.clear()
	case t1ClosePath:
		m.closePath()
		m.clear()
	case t1CallSubr:
		return false, m.callSubr(depth)
	case t1Return:
		return true, nil
	case t1EndChar:
		m.done = true
		return true, nil
	default:
		// An operator this format has not got: the rest of the charstring
		// cannot be trusted, but what has been drawn so far can.
		return true, nil
	}
	return false, nil
}

// escaped runs one of the operators reached through the escape byte.
func (m *t1machine) escaped(esc byte, depth int) (bool, error) {
	s := m.stack
	switch esc {
	case t1DotSection, t1VStem3, t1HStem3:
		m.clear()
	case t1Sbw:
		if len(s) >= 4 {
			m.sbx, m.sby, m.width, m.haveWidth = s[0], s[1], s[2], true
			m.x, m.y = m.sbx, m.sby
		}
		m.clear()
	case t1Div:
		if len(s) >= 2 && s[len(s)-1] != 0 {
			m.stack = append(s[:len(s)-2], s[len(s)-2]/s[len(s)-1])
		}
	case t1Seac:
		return true, m.seac()
	case t1CallOtherSubr:
		return false, m.callOtherSubr()
	case t1Pop:
		if len(m.ps) > 0 {
			m.stack = append(m.stack, m.ps[len(m.ps)-1])
			m.ps = m.ps[:len(m.ps)-1]
		} else {
			m.stack = append(m.stack, 0)
		}
	case t1SetCurrentPoint:
		if len(s) >= 2 {
			m.x, m.y = s[0], s[1]
		}
		m.clear()
	default:
		return true, nil
	}
	return false, nil
}

// callSubr runs a numbered subroutine.
func (m *t1machine) callSubr(depth int) error {
	if len(m.stack) == 0 {
		return nil
	}
	n := int(m.stack[len(m.stack)-1])
	m.stack = m.stack[:len(m.stack)-1]
	if n < 0 || n >= len(m.f.subrs) || m.f.subrs[n] == nil {
		return nil
	}
	return m.run(m.f.subrs[n], depth+1)
}

// callOtherSubr handles the four subroutines a Type 1 program calls out to the
// interpreter for. Numbers 0 to 2 build a flex — a curve so shallow it is
// drawn as one — and number 3 asks which hints to use, which nothing here
// needs. Anything else is answered by handing its arguments straight back, so
// that a program using an othersubr of its own carries on rather than stops.
func (m *t1machine) callOtherSubr() error {
	if len(m.stack) < 2 {
		m.clear()
		return nil
	}
	which := int(m.stack[len(m.stack)-1])
	n := int(m.stack[len(m.stack)-2])
	m.stack = m.stack[:len(m.stack)-2]
	if n < 0 || n > len(m.stack) {
		m.clear()
		return nil
	}
	args := m.stack[len(m.stack)-n:]
	m.stack = m.stack[:len(m.stack)-n]

	switch which {
	case 0: // the flex is over: draw the two curves its seven points describe
		m.endFlex()
		// The convention is that the two coordinates it ends at are read back
		// with two pops.
		m.ps = append(m.ps[:0], m.y, m.x)
	case 1: // a flex begins
		m.inFlex = true
		m.flexPts = m.flexPts[:0]
	case 2: // a flex point has been collected by the move that followed
	case 3:
		// Hint replacement. The program pushes the number of the subroutine
		// holding the hints it wants, and expects to read it straight back
		// with a pop and then call it. Handing back anything else — the
		// othersubr's own number, say — calls the wrong subroutine, and the
		// glyph comes out empty with nothing to say why.
		if len(args) > 0 {
			m.ps = append(m.ps[:0], args[0])
		} else {
			m.ps = append(m.ps[:0], 3)
		}
	default:
		// Hand the arguments back in the order the pops will take them.
		m.ps = m.ps[:0]
		for i := len(args) - 1; i >= 0; i-- {
			m.ps = append(m.ps, args[i])
		}
		if len(m.ps) > maxT1PostScriptSize {
			m.ps = m.ps[:maxT1PostScriptSize]
		}
	}
	return nil
}

// endFlex turns the seven collected points into the two curves they stand for.
func (m *t1machine) endFlex() {
	m.inFlex = false
	if len(m.flexPts) < 7 {
		return
	}
	p := m.flexPts[len(m.flexPts)-7:]
	// The first of the seven is the reference point the shape is measured
	// against and is not drawn.
	m.curveTo(p[1].x, p[1].y, p[2].x, p[2].y, p[3].x, p[3].y)
	m.curveTo(p[4].x, p[4].y, p[5].x, p[5].y, p[6].x, p[6].y)
}

// seac composes an accented glyph out of two others, named by their Standard
// Encoding codes.
func (m *t1machine) seac() error {
	if m.seacDepth >= maxSeacDepth {
		return fmt.Errorf("opentype: type1: accent composition nesting too deep")
	}
	s := m.stack
	if len(s) < 5 {
		m.clear()
		return nil
	}
	asb, adx, ady, bchar, achar := s[0], s[1], s[2], int(s[3]), int(s[4])
	m.clear()
	base, err := m.component(bchar)
	if err != nil {
		return err
	}
	accent, err := m.component(achar)
	if err != nil {
		return err
	}
	m.finishContour()
	m.contours = append(m.contours, base...)
	// The accent is placed relative to the base's own left side bearing.
	dx, dy := m.sbx-asb+adx, ady
	for _, c := range accent {
		nc := make(contour, len(c))
		for i, p := range c {
			nc[i] = outlinePoint{x: p.x + dx, y: p.y + dy, on: p.on}
		}
		m.contours = append(m.contours, nc)
	}
	m.done = true
	return nil
}

// component draws one half of an accented glyph, named by a Standard Encoding
// code.
func (m *t1machine) component(code int) ([]contour, error) {
	sid, ok := standardEncodingSID(code)
	if !ok || int(sid) >= nStdStrings {
		return nil, fmt.Errorf("opentype: type1: code %d is not in the standard encoding", code)
	}
	return m.f.outlineByName(cffStandardStrings[sid], m.seacDepth+1)
}

// clear empties the operand stack, which every operator that consumes one does.
func (m *t1machine) clear() { m.stack = m.stack[:0] }

// moveTo begins a contour, or collects a flex point while one is being built.
func (m *t1machine) moveTo(dx, dy float64) {
	m.x += dx
	m.y += dy
	if m.inFlex {
		m.flexPts = append(m.flexPts, outlinePoint{x: m.x, y: m.y, on: true})
		return
	}
	m.finishContour()
	m.cur = contour{{x: m.x, y: m.y, on: true}}
}

// lineTo adds a straight segment.
func (m *t1machine) lineTo(dx, dy float64) {
	m.x += dx
	m.y += dy
	m.cur = append(m.cur, outlinePoint{x: m.x, y: m.y, on: true})
}

// rrcurveTo adds a curve given by three relative steps.
func (m *t1machine) rrcurveTo(dx1, dy1, dx2, dy2, dx3, dy3 float64) {
	x1, y1 := m.x+dx1, m.y+dy1
	x2, y2 := x1+dx2, y1+dy2
	m.curveTo(x1, y1, x2, y2, x2+dx3, y2+dy3)
}

// curveTo flattens a cubic curve into the on-curve points a contour is made of.
func (m *t1machine) curveTo(x1, y1, x2, y2, x3, y3 float64) {
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

// closePath ends the contour in progress. A Type 1 program says so out loud,
// where later formats leave it implied.
func (m *t1machine) closePath() {
	m.finishContour()
}

// finishContour puts the contour under construction with the others.
func (m *t1machine) finishContour() {
	if len(m.cur) > 0 {
		m.contours = append(m.contours, m.cur)
		m.cur = nil
	}
}
