// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"errors"
	"fmt"
	"math"
)

// This file is a self-contained TrueType instruction (hinting) bytecode
// interpreter: a stack machine over the TrueType virtual machine with graphics
// state, a storage area, the Control Value Table, and function / instruction
// definitions. It is standalone: it exposes newInterp and (*interp).run and is
// not yet wired into the glyph-rendering pipeline (that is a caller's job).
//
// # Coordinate model
//
// All interpreter coordinates are F26Dot6 (26.6 fixed point, 64 units per
// pixel) held in int32, matching the on-wire stack representation. run accepts
// and returns outlinePoint values whose x/y are fractional pixels; it converts
// to and from 26.6 at the boundary. The "original" (pre-hinting) coordinates
// used by relative-measurement opcodes are a snapshot of the scaled input, and
// IUP/IP/SHP treat the whole input point slice as a single closed contour.
//
// # Implemented opcodes
//
// Push:        NPUSHB NPUSHW PUSHB[0-7] PUSHW[0-7]
// Stack:       DUP POP CLEAR SWAP DEPTH CINDEX MINDEX ROLL
// Arithmetic:  ADD SUB DIV MUL ABS NEG FLOOR CEILING MAX MIN
// Logic:       AND OR NOT LT LTEQ GT GTEQ EQ NEQ
// Control:     IF ELSE EIF JMPR JROT JROF
// Storage:     RS WS
// CVT:         RCVT WCVTP WCVTF
// State:       SVTCA SPVTCA SFVTCA SRP0 SRP1 SRP2 SZP0 SZP1 SZP2 SZPS
//              RTG RTHG RTDG RUTG RDTG ROFF SROUND S45ROUND SLOOP SMD SCVTCI
//              SDB SDS
// Functions:   FDEF ENDF CALL LOOPCALL IDEF
// Points:      GC SCFS MDAP MIAP MDRP MIRP SHP SHPIX IP ALIGNRP IUP
// Delta:       DELTAP1 DELTAP2 DELTAP3 DELTAC1 DELTAC2 DELTAC3
// Info:        MPPEM MPS GETINFO
//
// # Not implemented
//
// The remaining ~80 opcodes are deliberately out of scope for this core:
// SPVTL/SFVTL/SDPVTL/SPVFS/SFVFS/GPV/GFV (arbitrary vectors from lines/stack),
// ISECT, MSIRP, ALIGNPTS, UTP, FLIPPT/FLIPRGON/FLIPRGOFF/FLIPON/FLIPOFF,
// SANGW, AA, SDPVTL, MIN/MAX are covered but SCANCTRL/SCANTYPE/INSTCTRL,
// GETVARIATION, GETDATA, ODD/EVEN, SxxxW single-width, and the debug DEBUG
// opcode are not. Any opcode with no handler (and no matching IDEF) reaches a
// single covered fallback that reports "unimplemented opcode". Because every
// unimplemented opcode is a single byte (only the push family carries inline
// operands, and those are implemented), skipping such an opcode is a simple
// one-byte advance; the fallback errors rather than silently skipping so that
// callers learn a program used something outside this core.

// Interpreter guards. maxHintBudget bounds total instructions executed to stop
// runaway loops; maxCallDepth bounds CALL/LOOPCALL/IDEF recursion.
const (
	maxHintBudget = 1_000_000
	maxCallDepth  = 64
)

var (
	errStackUnderflow = errors.New("opentype: hint: stack underflow")
	errStackOverflow  = errors.New("opentype: hint: stack overflow")
	errHintBudget     = errors.New("opentype: hint: instruction budget exhausted")
	errCallDepth      = errors.New("opentype: hint: call nesting too deep")
	errBadProgram     = errors.New("opentype: hint: truncated instruction stream")
)

// gpoint is one interpreter point in F26Dot6. cur is the working position,
// orig the scaled pre-hinting position used for relative measurement; tx/ty
// record whether the point has been touched in x/y (for IUP).
type gpoint struct {
	curX, curY   int32
	origX, origY int32
	on           bool
	tx, ty       bool
}

// vec is a projection or freedom unit vector.
type vec struct{ x, y float64 }

// round modes.
const (
	rndGrid = iota // period / phase / threshold rounding (RTG, RTHG, RTDG, SROUND…)
	rndUp          // round up to grid (RUTG)
	rndDown        // round down to grid (RDTG)
	rndOff         // no rounding (ROFF)
)

// roundState captures the current rounding behaviour. For rndGrid the value is
// rounded with the given period/phase/threshold (all in F26Dot6 pixels).
type roundState struct {
	mode                     int
	period, phase, threshold float64
}

// gstate is the interpreter graphics state that run resets to the post-prep
// default before each glyph.
type gstate struct {
	pv, fv, dv    vec
	rp0, rp1, rp2 int
	zp0, zp1, zp2 int
	loop          int
	round         roundState
	minDist       int32
	cvtCutIn      int32
	deltaBase     int
	deltaShift    int
}

// funcDef is a defined function's body (the bytes between FDEF and ENDF).
type funcDef struct{ body []byte }

// interp is a TrueType bytecode interpreter bound to a font and pixel size.
type interp struct {
	font  *Font
	ppem  int
	scale float64 // pixels per font unit

	stack    []int32
	storage  []int32
	cvt      []int32 // F26Dot6, scaled to ppem
	maxStack int

	funcs map[int32]funcDef
	idefs map[uint8]funcDef

	gstate        // live graphics state
	dflt   gstate // post-prep default, restored per glyph

	glyphZone []gpoint
	twilight  []gpoint
	scratch   gpoint // returned for out-of-range point access after setErr

	budget int
	err    error
}

// setErr records the first error encountered.
func (it *interp) setErr(e error) {
	if it.err == nil {
		it.err = e
	}
}

// --- stack helpers ----------------------------------------------------------

func (it *interp) push(v int32) {
	if len(it.stack) >= it.maxStack {
		it.setErr(errStackOverflow)
		return
	}
	it.stack = append(it.stack, v)
}

func (it *interp) pop() int32 {
	n := len(it.stack)
	if n == 0 {
		it.setErr(errStackUnderflow)
		return 0
	}
	v := it.stack[n-1]
	it.stack = it.stack[:n-1]
	return v
}

// --- construction -----------------------------------------------------------

// newInterp builds an interpreter for font at ppem pixels per em and runs the
// font program (fpgm) then the control-value program (prep) once, capturing
// the resulting graphics state as the per-glyph default. ppem must be positive.
func newInterp(font *Font, ppem int) (*interp, error) {
	if ppem <= 0 {
		return nil, fmt.Errorf("opentype: hint: non-positive ppem %d", ppem)
	}
	it := &interp{
		font:     font,
		ppem:     ppem,
		scale:    float64(ppem) / float64(font.unitsPerEm),
		maxStack: font.maxStackElements,
		storage:  make([]int32, font.maxStorage),
		funcs:    make(map[int32]funcDef, font.maxFunctionDefs),
		idefs:    make(map[uint8]funcDef),
	}
	if it.maxStack < 32 {
		it.maxStack = 32 // keep room for the interpreter's own working values
	}
	// Scale the CVT into F26Dot6 pixels.
	it.cvt = make([]int32, len(font.cvt))
	for i, v := range font.cvt {
		it.cvt[i] = f26(float64(v) * it.scale)
	}
	it.twilight = make([]gpoint, font.maxTwilightPts)

	it.resetState()
	if err := it.exec(font.fpgm, 0); err != nil {
		return nil, err
	}
	it.resetState()
	if err := it.exec(font.prep, 0); err != nil {
		return nil, err
	}
	it.dflt = it.gstate
	return it, nil
}

// resetState restores the TrueType default graphics state.
func (it *interp) resetState() {
	it.gstate = gstate{
		pv:         vec{1, 0},
		fv:         vec{1, 0},
		dv:         vec{1, 0},
		zp0:        1, // default reference zone is the glyph zone
		zp1:        1,
		zp2:        1,
		loop:       1,
		round:      roundState{mode: rndGrid, period: 64, phase: 0, threshold: 32},
		minDist:    64, // 1 pixel
		cvtCutIn:   f26(17.0 / 16.0),
		deltaBase:  9,
		deltaShift: 3,
	}
}

// f26 converts fractional pixels to F26Dot6, rounding to nearest.
func f26(px float64) int32 { return int32(math.Round(px * 64)) }

// --- run --------------------------------------------------------------------

// run hints one glyph. pts are the glyph's points in fractional pixels; it
// returns a new slice with the hinted positions. The graphics state is reset to
// the post-prep default first. The whole slice is treated as a single closed
// contour for IUP/IP/SHP.
func (it *interp) run(pts []outlinePoint, instructions []byte) ([]outlinePoint, error) {
	it.gstate = it.dflt
	it.stack = it.stack[:0]
	it.err = nil
	for i := range it.twilight {
		it.twilight[i] = gpoint{}
	}
	it.glyphZone = make([]gpoint, len(pts))
	for i, p := range pts {
		x, y := f26(p.x), f26(p.y)
		it.glyphZone[i] = gpoint{curX: x, curY: y, origX: x, origY: y, on: p.on}
	}
	if err := it.exec(instructions, 0); err != nil {
		return nil, err
	}
	out := make([]outlinePoint, len(pts))
	for i, g := range it.glyphZone {
		out[i] = outlinePoint{x: float64(g.curX) / 64, y: float64(g.curY) / 64, on: g.on}
	}
	return out, nil
}

// --- point / zone access ----------------------------------------------------

func (it *interp) zone(zp int) []gpoint {
	if zp == 0 {
		return it.twilight
	}
	return it.glyphZone
}

// pt returns the point at idx in the zone selected by zp. On an out-of-range
// index it records an error and returns a throwaway scratch point so callers
// need not nil-check (the surrounding instruction is abandoned via it.err).
func (it *interp) pt(zp, idx int) *gpoint {
	z := it.zone(zp)
	if idx < 0 || idx >= len(z) {
		it.setErr(fmt.Errorf("opentype: hint: point %d out of range in zone %d", idx, zp))
		it.scratch = gpoint{}
		return &it.scratch
	}
	return &z[idx]
}

// --- vectors / projection ---------------------------------------------------

func (it *interp) project(x, y int32) float64 {
	return float64(x)*it.pv.x + float64(y)*it.pv.y
}

func (it *interp) dualProject(x, y int32) float64 {
	return float64(x)*it.dv.x + float64(y)*it.dv.y
}

// moveBy shifts a point along the freedom vector so its projection changes by
// dp (F26Dot6). It also marks the point touched on the axes the freedom vector
// spans. If the freedom and projection vectors are perpendicular the point
// cannot move and the call is a no-op.
func (it *interp) moveBy(p *gpoint, dp float64) {
	f := it.fv.x*it.pv.x + it.fv.y*it.pv.y
	if f == 0 {
		return
	}
	p.curX += int32(math.Round(dp * it.fv.x / f))
	p.curY += int32(math.Round(dp * it.fv.y / f))
	if it.fv.x != 0 {
		p.tx = true
	}
	if it.fv.y != 0 {
		p.ty = true
	}
}

// --- rounding ---------------------------------------------------------------

func (rs roundState) apply(v float64) float64 {
	if rs.mode == rndOff {
		return v
	}
	sign := 1.0
	if v < 0 {
		sign, v = -1, -v
	}
	switch rs.mode {
	case rndUp:
		v = math.Ceil(v/64) * 64
	case rndDown:
		v = math.Floor(v/64) * 64
	default: // rndGrid
		v = math.Floor((v-rs.phase+rs.threshold)/rs.period)*rs.period + rs.phase
		if v < 0 {
			v = 0
		}
	}
	return sign * v
}

// --- instruction length -----------------------------------------------------

// opLen returns the total byte length of the instruction at prog[pc], including
// any inline push operands, or -1 if it runs past the end of prog.
func opLen(prog []byte, pc int) int {
	op := prog[pc]
	switch {
	case op == 0x40: // NPUSHB
		if pc+1 >= len(prog) {
			return -1
		}
		return 2 + int(prog[pc+1])
	case op == 0x41: // NPUSHW
		if pc+1 >= len(prog) {
			return -1
		}
		return 2 + 2*int(prog[pc+1])
	case op >= 0xB0 && op <= 0xB7: // PUSHB[n]
		return 1 + int(op-0xB0) + 1
	case op >= 0xB8 && op <= 0xBF: // PUSHW[n]
		return 1 + 2*(int(op-0xB8)+1)
	default:
		return 1
	}
}

// skipToElseEif advances from the instruction after an IF whose condition was
// false to the matching ELSE (returning the index after it) or EIF (index after
// it), honouring nested IF/EIF. onlyEif skips a taken ELSE straight to EIF.
func skipToElseEif(prog []byte, pc int, onlyEif bool) (int, error) {
	depth := 0
	for pc < len(prog) {
		op := prog[pc]
		l := opLen(prog, pc)
		if l < 0 {
			return 0, errBadProgram
		}
		switch {
		case op == 0x58: // IF
			depth++
		case op == 0x1B && depth == 0 && !onlyEif: // ELSE at our level
			return pc + 1, nil
		case op == 0x59: // EIF
			if depth == 0 {
				return pc + 1, nil
			}
			depth--
		}
		pc += l
	}
	return 0, errBadProgram
}

// scanFDEF returns the body between the instruction after FDEF at fpc and its
// matching ENDF, plus the index just past ENDF.
func scanFDEF(prog []byte, fpc int) ([]byte, int, error) {
	start := fpc
	pc := fpc
	for pc < len(prog) {
		op := prog[pc]
		l := opLen(prog, pc)
		if l < 0 {
			return nil, 0, errBadProgram
		}
		if op == 0x2D { // ENDF
			return prog[start:pc], pc + 1, nil
		}
		pc += l
	}
	return nil, 0, errBadProgram
}

// --- execution --------------------------------------------------------------

// exec runs a program to completion (or the first error), managing the program
// counter, jumps, conditionals and function definitions. depth guards call
// recursion.
func (it *interp) exec(prog []byte, depth int) error {
	if depth > maxCallDepth {
		return errCallDepth
	}
	if depth == 0 {
		it.budget = maxHintBudget
	}
	pc := 0
	for pc < len(prog) {
		it.budget--
		if it.budget < 0 {
			return errHintBudget
		}
		op := prog[pc]
		next := pc + 1
		jumped, err := it.step(prog, &pc, &next, op, depth)
		if err != nil {
			return err
		}
		if jumped {
			continue
		}
		pc = next
	}
	return nil
}

// step executes one opcode. It returns jumped=true when it has already set *pc
// (a jump / conditional / push adjusted the counter and the caller must not use
// next). For ordinary opcodes it leaves *next at pc+1 (adjusted for inline
// operands) and returns jumped=false.
func (it *interp) step(prog []byte, pc, next *int, op uint8, depth int) (bool, error) {
	switch {
	case op == 0x40: // NPUSHB
		return true, it.pushBytes(prog, pc, false, -1)
	case op == 0x41: // NPUSHW
		return true, it.pushWords(prog, pc, false, -1)
	case op >= 0xB0 && op <= 0xB7: // PUSHB[n]
		return true, it.pushBytes(prog, pc, true, int(op-0xB0)+1)
	case op >= 0xB8 && op <= 0xBF: // PUSHW[n]
		return true, it.pushWords(prog, pc, true, int(op-0xB8)+1)
	case op == 0x58: // IF
		return true, it.doIF(prog, pc)
	case op == 0x1B: // ELSE (only reached when executing the true branch)
		np, err := skipToElseEif(prog, *pc+1, true)
		*pc = np
		return true, err
	case op == 0x59: // EIF
		return false, nil // no-op marker
	case op == 0x1C: // JMPR
		return true, it.jump(prog, pc, jmpAlways)
	case op == 0x78: // JROT
		return true, it.jump(prog, pc, jmpTrue)
	case op == 0x79: // JROF
		return true, it.jump(prog, pc, jmpFalse)
	case op == 0x2C: // FDEF
		return true, it.doFDEF(prog, pc)
	case op == 0x89: // IDEF
		return true, it.doIDEF(prog, pc)
	case op == 0x2D: // ENDF
		*pc = len(prog) // terminate this program
		return true, nil
	case op == 0x2B: // CALL
		return false, it.doCALL(depth)
	case op == 0x2A: // LOOPCALL
		return false, it.doLOOPCALL(depth)
	default:
		return false, it.dataOp(op, depth)
	}
}

// pushBytes / pushWords implement the push family. When fixed is true count is
// the operand count; otherwise the count byte follows the opcode.
func (it *interp) pushBytes(prog []byte, pc *int, fixed bool, count int) error {
	p := *pc + 1
	if !fixed {
		if p >= len(prog) {
			return errBadProgram
		}
		count = int(prog[p])
		p++
	}
	if p+count > len(prog) {
		return errBadProgram
	}
	for i := 0; i < count; i++ {
		it.push(int32(prog[p+i]))
	}
	*pc = p + count
	return it.err
}

func (it *interp) pushWords(prog []byte, pc *int, fixed bool, count int) error {
	p := *pc + 1
	if !fixed {
		if p >= len(prog) {
			return errBadProgram
		}
		count = int(prog[p])
		p++
	}
	if p+2*count > len(prog) {
		return errBadProgram
	}
	for i := 0; i < count; i++ {
		it.push(int32(int16(uint16(prog[p+2*i])<<8 | uint16(prog[p+2*i+1]))))
	}
	*pc = p + 2*count
	return it.err
}

func (it *interp) doIF(prog []byte, pc *int) error {
	cond := it.pop()
	if it.err != nil {
		return it.err
	}
	if cond != 0 {
		*pc = *pc + 1 // fall into the true branch
		return nil
	}
	np, err := skipToElseEif(prog, *pc+1, false)
	*pc = np
	return err
}

type jmpKind int

const (
	jmpAlways jmpKind = iota
	jmpTrue
	jmpFalse
)

func (it *interp) jump(prog []byte, pc *int, kind jmpKind) error {
	take := true
	if kind != jmpAlways {
		e := it.pop()
		take = (kind == jmpTrue) == (e != 0)
	}
	offset := it.pop()
	if it.err != nil {
		return it.err
	}
	if !take {
		*pc = *pc + 1
		return nil
	}
	target := *pc + int(offset)
	if target < 0 || target > len(prog) {
		return errBadProgram
	}
	*pc = target
	return nil
}

func (it *interp) doFDEF(prog []byte, pc *int) error {
	id := it.pop()
	if it.err != nil {
		return it.err
	}
	body, after, err := scanFDEF(prog, *pc+1)
	if err != nil {
		return err
	}
	it.funcs[id] = funcDef{body: body}
	*pc = after
	return nil
}

func (it *interp) doIDEF(prog []byte, pc *int) error {
	op := it.pop()
	if it.err != nil {
		return it.err
	}
	body, after, err := scanFDEF(prog, *pc+1)
	if err != nil {
		return err
	}
	it.idefs[uint8(op)] = funcDef{body: body}
	*pc = after
	return nil
}

func (it *interp) doCALL(depth int) error {
	id := it.pop()
	if it.err != nil {
		return it.err
	}
	fd, ok := it.funcs[id]
	if !ok {
		return fmt.Errorf("opentype: hint: call to undefined function %d", id)
	}
	return it.exec(fd.body, depth+1)
}

func (it *interp) doLOOPCALL(depth int) error {
	id := it.pop()
	n := it.pop()
	if it.err != nil {
		return it.err
	}
	fd, ok := it.funcs[id]
	if !ok {
		return fmt.Errorf("opentype: hint: loopcall to undefined function %d", id)
	}
	for i := int32(0); i < n; i++ {
		if err := it.exec(fd.body, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// dataOp handles every opcode that neither branches nor defines a function:
// the stack, arithmetic, state, CVT, storage, point and delta instructions.
func (it *interp) dataOp(op uint8, depth int) error {
	switch op {
	// stack manipulation
	case 0x20: // DUP
		v := it.pop()
		it.push(v)
		it.push(v)
	case 0x21: // POP
		it.pop()
	case 0x22: // CLEAR
		it.stack = it.stack[:0]
	case 0x23: // SWAP
		a := it.pop()
		b := it.pop()
		it.push(a)
		it.push(b)
	case 0x24: // DEPTH
		it.push(int32(len(it.stack)))
	case 0x25: // CINDEX
		it.copyIndex(false)
	case 0x26: // MINDEX
		it.copyIndex(true)
	case 0x8A: // ROLL
		a := it.pop()
		b := it.pop()
		c := it.pop()
		it.push(b)
		it.push(a)
		it.push(c)

	// arithmetic and logic
	case 0x60: // ADD
		b := it.pop()
		it.push(it.pop() + b)
	case 0x61: // SUB
		b := it.pop()
		it.push(it.pop() - b)
	case 0x63: // MUL (F26Dot6)
		b := it.pop()
		a := it.pop()
		it.push(int32((int64(a) * int64(b)) / 64))
	case 0x62: // DIV (F26Dot6)
		b := it.pop()
		a := it.pop()
		if b == 0 {
			return errors.New("opentype: hint: division by zero")
		}
		it.push(int32((int64(a) * 64) / int64(b)))
	case 0x64: // ABS
		v := it.pop()
		if v < 0 {
			v = -v
		}
		it.push(v)
	case 0x65: // NEG
		it.push(-it.pop())
	case 0x66: // FLOOR
		it.push(it.pop() &^ 63)
	case 0x67: // CEILING
		it.push((it.pop() + 63) &^ 63)
	case 0x8B: // MAX
		b := it.pop()
		a := it.pop()
		it.push(maxI32(a, b))
	case 0x8C: // MIN
		b := it.pop()
		a := it.pop()
		it.push(minI32(a, b))
	case 0x5A: // AND
		b := it.pop()
		it.push(boolI(it.pop() != 0 && b != 0))
	case 0x5B: // OR
		b := it.pop()
		it.push(boolI(it.pop() != 0 || b != 0))
	case 0x5C: // NOT
		it.push(boolI(it.pop() == 0))
	case 0x50: // LT
		b := it.pop()
		it.push(boolI(it.pop() < b))
	case 0x51: // LTEQ
		b := it.pop()
		it.push(boolI(it.pop() <= b))
	case 0x52: // GT
		b := it.pop()
		it.push(boolI(it.pop() > b))
	case 0x53: // GTEQ
		b := it.pop()
		it.push(boolI(it.pop() >= b))
	case 0x54: // EQ
		b := it.pop()
		it.push(boolI(it.pop() == b))
	case 0x55: // NEQ
		b := it.pop()
		it.push(boolI(it.pop() != b))

	// storage
	case 0x43: // RS
		i := it.pop()
		if it.err == nil {
			if i < 0 || int(i) >= len(it.storage) {
				return errors.New("opentype: hint: storage read out of range")
			}
			it.push(it.storage[i])
		}
	case 0x42: // WS
		v := it.pop()
		i := it.pop()
		if it.err == nil {
			if i < 0 || int(i) >= len(it.storage) {
				return errors.New("opentype: hint: storage write out of range")
			}
			it.storage[i] = v
		}

	// control value table
	case 0x45: // RCVT
		it.push(it.readCVT(it.pop()))
	case 0x44: // WCVTP (value already in pixels/F26Dot6)
		v := it.pop()
		it.writeCVT(it.pop(), v)
	case 0x70: // WCVTF (value in font units, scale to pixels)
		v := it.pop()
		it.writeCVT(it.pop(), f26(float64(v)*it.scale))

	// graphics-state setters
	case 0x00, 0x01: // SVTCA
		it.setAxis(op&1 != 0, true, true)
	case 0x02, 0x03: // SPVTCA
		it.setAxis(op&1 != 0, true, false)
	case 0x04, 0x05: // SFVTCA
		it.setAxis(op&1 != 0, false, true)
	case 0x10: // SRP0
		it.rp0 = int(it.pop())
	case 0x11: // SRP1
		it.rp1 = int(it.pop())
	case 0x12: // SRP2
		it.rp2 = int(it.pop())
	case 0x13: // SZP0
		return it.setZone(0)
	case 0x14: // SZP1
		return it.setZone(1)
	case 0x15: // SZP2
		return it.setZone(2)
	case 0x16: // SZPS
		return it.setZone(3)
	case 0x17: // SLOOP
		it.loop = int(it.pop())
	case 0x18: // RTG
		it.round = roundState{mode: rndGrid, period: 64, phase: 0, threshold: 32}
	case 0x19: // RTHG
		it.round = roundState{mode: rndGrid, period: 64, phase: 32, threshold: 32}
	case 0x3A: // RTDG
		it.round = roundState{mode: rndGrid, period: 32, phase: 0, threshold: 16}
	case 0x7C: // RUTG
		it.round = roundState{mode: rndUp}
	case 0x7D: // RDTG
		it.round = roundState{mode: rndDown}
	case 0x7A: // ROFF
		it.round = roundState{mode: rndOff}
	case 0x76: // SROUND
		it.setSuperRound(64)
	case 0x77: // S45ROUND
		it.setSuperRound(64 * math.Sqrt2 / 2)
	case 0x1A: // SMD
		it.minDist = it.pop()
	case 0x1D: // SCVTCI
		it.cvtCutIn = it.pop()
	case 0x5E: // SDB
		it.deltaBase = int(it.pop())
	case 0x5F: // SDS
		it.deltaShift = int(it.pop())

	// measurement and movement
	case 0x46, 0x47: // GC
		it.doGC(op&1 != 0)
	case 0x48: // SCFS
		it.doSCFS()
	case 0x2E, 0x2F: // MDAP
		it.doMDAP(op&1 != 0)
	case 0x3E, 0x3F: // MIAP
		it.doMIAP(op&1 != 0)
	case 0x38: // SHPIX
		it.doSHPIX()
	case 0x32, 0x33: // SHP
		it.doSHP(op&1 != 0)
	case 0x39: // IP
		it.doIP()
	case 0x3C: // ALIGNRP
		it.doALIGNRP()
	case 0x30, 0x31: // IUP
		it.doIUP(op&1 != 0)

	// delta exceptions
	case 0x5D: // DELTAP1
		it.doDeltaP(0)
	case 0x71: // DELTAP2
		it.doDeltaP(16)
	case 0x72: // DELTAP3
		it.doDeltaP(32)
	case 0x73: // DELTAC1
		it.doDeltaC(0)
	case 0x74: // DELTAC2
		it.doDeltaC(16)
	case 0x75: // DELTAC3
		it.doDeltaC(32)

	// information
	case 0x4B: // MPPEM
		it.push(int32(it.ppem))
	case 0x4C: // MPS (point size; we report ppem, assuming 72 dpi)
		it.push(int32(it.ppem))
	case 0x88: // GETINFO
		it.doGetInfo()

	default:
		// MDRP 0xC0-0xDF, MIRP 0xE0-0xFF are handled by range; anything else is
		// an IDEF-defined instruction or genuinely unimplemented.
		switch {
		case op >= 0xC0 && op <= 0xDF:
			it.doMDRP(op)
		case op >= 0xE0 && op <= 0xFF:
			it.doMIRP(op)
		default:
			if fd, ok := it.idefs[op]; ok {
				return it.exec(fd.body, depth+1)
			}
			return fmt.Errorf("opentype: hint: unimplemented opcode 0x%02X", op)
		}
	}
	return it.err
}

// copyIndex implements CINDEX (move==false) and MINDEX (move==true).
func (it *interp) copyIndex(move bool) {
	k := int(it.pop())
	if it.err != nil {
		return
	}
	n := len(it.stack)
	if k <= 0 || k > n {
		it.setErr(errStackUnderflow)
		return
	}
	v := it.stack[n-k]
	if move {
		it.stack = append(it.stack[:n-k], it.stack[n-k+1:]...)
	}
	it.push(v)
}

func (it *interp) readCVT(i int32) int32 {
	if i < 0 || int(i) >= len(it.cvt) {
		it.setErr(errors.New("opentype: hint: CVT read out of range"))
		return 0
	}
	return it.cvt[i]
}

func (it *interp) writeCVT(i, v int32) {
	if it.err != nil {
		return
	}
	if i < 0 || int(i) >= len(it.cvt) {
		it.setErr(errors.New("opentype: hint: CVT write out of range"))
		return
	}
	it.cvt[i] = v
}

// setAxis points the projection and/or freedom vectors at the x (xAxis true) or
// y axis.
func (it *interp) setAxis(xAxis, setProj, setFree bool) {
	v := vec{0, 1}
	if xAxis {
		v = vec{1, 0}
	}
	if setProj {
		it.pv = v
		it.dv = v
	}
	if setFree {
		it.fv = v
	}
}

// setZone implements SZP0/1/2 (which=0/1/2) and SZPS (which=3, all three).
func (it *interp) setZone(which int) error {
	z := int(it.pop())
	if it.err != nil {
		return it.err
	}
	if z != 0 && z != 1 {
		return fmt.Errorf("opentype: hint: bad zone pointer %d", z)
	}
	switch which {
	case 0:
		it.zp0 = z
	case 1:
		it.zp1 = z
	case 2:
		it.zp2 = z
	default:
		it.zp0, it.zp1, it.zp2 = z, z, z
	}
	return nil
}

// setSuperRound decodes an SROUND/S45ROUND selector byte into period/phase/
// threshold. base is the grid period (64 for SROUND, 64·√2/2 for S45ROUND).
func (it *interp) setSuperRound(base float64) {
	n := it.pop()
	if it.err != nil {
		return
	}
	rs := roundState{mode: rndGrid}
	switch (n >> 6) & 3 {
	case 0:
		rs.period = base / 2
	case 2:
		rs.period = base * 2
	default: // 1 and the reserved 3
		rs.period = base
	}
	switch (n >> 4) & 3 {
	case 1:
		rs.phase = rs.period / 4
	case 2:
		rs.phase = rs.period / 2
	case 3:
		rs.phase = rs.period * 3 / 4
	default:
		rs.phase = 0
	}
	if thr := n & 0x0F; thr == 0 {
		rs.threshold = rs.period - 1
	} else {
		rs.threshold = (float64(thr) - 4) / 8 * rs.period
	}
	it.round = rs
}

func (it *interp) doGC(useOrig bool) {
	idx := int(it.pop())
	p := it.pt(it.zp2, idx)
	if it.err != nil {
		return
	}
	var v float64
	if useOrig {
		v = it.dualProject(p.origX, p.origY)
	} else {
		v = it.project(p.curX, p.curY)
	}
	it.push(int32(math.Round(v)))
}

func (it *interp) doSCFS() {
	val := it.pop()
	idx := int(it.pop())
	p := it.pt(it.zp2, idx)
	if it.err != nil {
		return
	}
	it.moveBy(p, float64(val)-it.project(p.curX, p.curY))
}

func (it *interp) doMDAP(round bool) {
	idx := int(it.pop())
	p := it.pt(it.zp0, idx)
	if it.err != nil {
		return
	}
	if round {
		cur := it.project(p.curX, p.curY)
		it.moveBy(p, it.round.apply(cur)-cur)
	} else {
		// touch without moving
		if it.fv.x != 0 {
			p.tx = true
		}
		if it.fv.y != 0 {
			p.ty = true
		}
	}
	it.rp0, it.rp1 = idx, idx
}

func (it *interp) doMIAP(round bool) {
	cvtIdx := it.pop()
	idx := int(it.pop())
	d := it.readCVT(cvtIdx)
	p := it.pt(it.zp0, idx)
	if it.err != nil {
		return
	}
	dist := float64(d)
	// On the twilight zone the point starts at the CVT value.
	if it.zp0 == 0 {
		p.origX = int32(math.Round(dist * it.pv.x))
		p.origY = int32(math.Round(dist * it.pv.y))
		p.curX, p.curY = p.origX, p.origY
	}
	cur := it.project(p.curX, p.curY)
	if round {
		if math.Abs(dist-cur) > float64(it.cvtCutIn) {
			dist = cur
		}
		dist = it.round.apply(dist)
	}
	it.moveBy(p, dist-cur)
	it.rp0, it.rp1 = idx, idx
}

// mdrpFlags decodes the shared MDRP/MIRP flag bits.
type mdrpFlags struct {
	setRP0  bool
	minDist bool
	round   bool
}

func decodeMDRP(op uint8) mdrpFlags {
	return mdrpFlags{
		setRP0:  op&0x10 != 0,
		minDist: op&0x08 != 0,
		round:   op&0x04 != 0,
	}
}

func (it *interp) applyDist(d float64, f mdrpFlags) float64 {
	if f.round {
		d = it.round.apply(d)
	}
	if f.minDist {
		md := float64(it.minDist)
		if d >= 0 && d < md {
			d = md
		} else if d < 0 && d > -md {
			d = -md
		}
	}
	return d
}

func (it *interp) doMDRP(op uint8) {
	f := decodeMDRP(op)
	idx := int(it.pop())
	ref := it.pt(it.zp0, it.rp0)
	p := it.pt(it.zp1, idx)
	if it.err != nil {
		return
	}
	orig := it.dualProject(p.origX-ref.origX, p.origY-ref.origY)
	d := it.applyDist(orig, f)
	cur := it.project(p.curX-ref.curX, p.curY-ref.curY)
	it.moveBy(p, d-cur)
	it.rp1, it.rp2 = it.rp0, idx
	if f.setRP0 {
		it.rp0 = idx
	}
}

func (it *interp) doMIRP(op uint8) {
	f := decodeMDRP(op)
	cvtIdx := it.pop()
	idx := int(it.pop())
	cv := it.readCVT(cvtIdx)
	ref := it.pt(it.zp0, it.rp0)
	p := it.pt(it.zp1, idx)
	if it.err != nil {
		return
	}
	orig := it.dualProject(p.origX-ref.origX, p.origY-ref.origY)
	dist := float64(cv)
	// CVT cut-in: fall back to the original distance when the CVT disagrees.
	if math.Abs(dist-orig) > float64(it.cvtCutIn) {
		dist = orig
	}
	d := it.applyDist(dist, f)
	cur := it.project(p.curX-ref.curX, p.curY-ref.curY)
	it.moveBy(p, d-cur)
	it.rp1, it.rp2 = it.rp0, idx
	if f.setRP0 {
		it.rp0 = idx
	}
}

func (it *interp) doSHPIX() {
	amount := float64(it.pop())
	n := it.loop
	it.loop = 1
	for i := 0; i < n; i++ {
		idx := int(it.pop())
		p := it.pt(it.zp2, idx)
		if it.err != nil {
			return
		}
		p.curX += int32(math.Round(amount * it.fv.x))
		p.curY += int32(math.Round(amount * it.fv.y))
		if it.fv.x != 0 {
			p.tx = true
		}
		if it.fv.y != 0 {
			p.ty = true
		}
	}
}

func (it *interp) doSHP(useRP1 bool) {
	zp, rp := it.zp1, it.rp2
	if useRP1 {
		zp, rp = it.zp0, it.rp1
	}
	ref := it.pt(zp, rp)
	if it.err != nil {
		return
	}
	dp := it.project(ref.curX, ref.curY) - it.dualProject(ref.origX, ref.origY)
	n := it.loop
	it.loop = 1
	for i := 0; i < n; i++ {
		idx := int(it.pop())
		p := it.pt(it.zp2, idx)
		if it.err != nil {
			return
		}
		it.moveBy(p, dp)
	}
}

func (it *interp) doALIGNRP() {
	ref := it.pt(it.zp0, it.rp0)
	if it.err != nil {
		return
	}
	rpProj := it.project(ref.curX, ref.curY)
	n := it.loop
	it.loop = 1
	for i := 0; i < n; i++ {
		idx := int(it.pop())
		p := it.pt(it.zp1, idx)
		if it.err != nil {
			return
		}
		it.moveBy(p, rpProj-it.project(p.curX, p.curY))
	}
}

func (it *interp) doIP() {
	rp1 := it.pt(it.zp0, it.rp1)
	rp2 := it.pt(it.zp1, it.rp2)
	if it.err != nil {
		return
	}
	o1 := it.dualProject(rp1.origX, rp1.origY)
	o2 := it.dualProject(rp2.origX, rp2.origY)
	c1 := it.project(rp1.curX, rp1.curY)
	c2 := it.project(rp2.curX, rp2.curY)
	n := it.loop
	it.loop = 1
	for i := 0; i < n; i++ {
		idx := int(it.pop())
		p := it.pt(it.zp2, idx)
		if it.err != nil {
			return
		}
		op := it.dualProject(p.origX, p.origY)
		var newProj float64
		if o2 == o1 {
			newProj = c1 + (op - o1)
		} else {
			newProj = c1 + (op-o1)/(o2-o1)*(c2-c1)
		}
		it.moveBy(p, newProj-it.project(p.curX, p.curY))
	}
}

// doIUP interpolates untouched points of the (single) contour along the x
// (xAxis true) or y axis, following the standard TrueType two-anchor scheme.
func (it *interp) doIUP(xAxis bool) {
	z := it.glyphZone
	n := len(z)
	if n == 0 {
		return
	}
	touched := func(i int) bool {
		if xAxis {
			return z[i].tx
		}
		return z[i].ty
	}
	cur := func(i int) int32 {
		if xAxis {
			return z[i].curX
		}
		return z[i].curY
	}
	orig := func(i int) int32 {
		if xAxis {
			return z[i].origX
		}
		return z[i].origY
	}
	setCur := func(i int, v int32) {
		if xAxis {
			z[i].curX = v
		} else {
			z[i].curY = v
		}
	}

	first := -1
	for i := 0; i < n; i++ {
		if touched(i) {
			first = i
			break
		}
	}
	if first < 0 {
		return // nothing touched: leave the contour untouched
	}
	prev := first
	for k := 1; k <= n; k++ {
		i := (first + k) % n
		if !touched(i) {
			continue
		}
		it.iupSpan(prev, i, n, cur, orig, setCur)
		prev = i
	}
}

// iupSpan interpolates the untouched points strictly between anchors a and b
// (indices in a ring of n points), shifting or interpolating each per its
// original coordinate relative to the two anchors.
func (it *interp) iupSpan(a, b, n int, cur, orig func(int) int32, setCur func(int, int32)) {
	oa, ob := orig(a), orig(b)
	ca, cb := cur(a), cur(b)
	lo, hi := oa, ob
	clo, chi := ca, cb
	if lo > hi {
		lo, hi, clo, chi = hi, lo, chi, clo
	}
	for k := 1; ; k++ {
		i := (a + k) % n
		if i == b {
			break
		}
		o := orig(i)
		switch {
		case o <= lo:
			setCur(i, o+(clo-lo))
		case o >= hi:
			setCur(i, o+(chi-hi))
		default:
			// lo < o < hi here, so hi-lo is strictly positive.
			setCur(i, clo+int32(math.Round(float64(o-lo)/float64(hi-lo)*float64(chi-clo))))
		}
	}
}

// deltaStep converts a delta magnitude nibble into an F26Dot6 movement, using
// the current deltaShift (finer shifts give smaller steps).
func (it *interp) deltaStep(mag int32) float64 {
	step := mag - 8
	if step >= 0 {
		step++ // magnitudes skip zero: -8..-1, +1..+8
	}
	return float64(step) * 64 / float64(int32(1)<<uint(it.deltaShift))
}

func (it *interp) doDeltaP(offset int) {
	count := it.pop()
	if it.err != nil {
		return
	}
	for i := int32(0); i < count; i++ {
		idx := int(it.pop())
		arg := it.pop()
		if it.err != nil {
			return
		}
		ppemEx := it.deltaBase + offset + int((arg>>4)&0x0F)
		if it.ppem != ppemEx {
			continue
		}
		p := it.pt(it.zp0, idx)
		if it.err != nil {
			return
		}
		it.moveBy(p, it.deltaStep(arg&0x0F))
	}
}

func (it *interp) doDeltaC(offset int) {
	count := it.pop()
	if it.err != nil {
		return
	}
	for i := int32(0); i < count; i++ {
		cvtIdx := it.pop()
		arg := it.pop()
		if it.err != nil {
			return
		}
		ppemEx := it.deltaBase + offset + int((arg>>4)&0x0F)
		if it.ppem != ppemEx {
			continue
		}
		cur := it.readCVT(cvtIdx)
		if it.err != nil {
			return
		}
		it.writeCVT(cvtIdx, cur+int32(math.Round(it.deltaStep(arg&0x0F))))
	}
}

// doGetInfo answers the common GETINFO selectors: bit 0 requests the
// rasteriser version (reported as 42, the classic value); bit 5 (0x20) asks
// whether grayscale rendering is active, which this engine always reports true.
func (it *interp) doGetInfo() {
	sel := it.pop()
	if it.err != nil {
		return
	}
	var res int32
	if sel&0x0001 != 0 {
		res |= 42
	}
	if sel&0x0020 != 0 {
		res |= 1 << 12 // grayscale active
	}
	it.push(res)
}

// --- small numeric helpers --------------------------------------------------

func boolI(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func maxI32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func minI32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}
