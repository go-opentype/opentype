// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"errors"
	"strings"
	"testing"
)

// TrueType opcodes referenced by the tests, named for readability.
const (
	opNPUSHB   = 0x40
	opNPUSHW   = 0x41
	opPUSHB1   = 0xB0
	opPUSHB2   = 0xB1
	opPUSHW1   = 0xB8
	opDUP      = 0x20
	opPOP      = 0x21
	opCLEAR    = 0x22
	opSWAP     = 0x23
	opDEPTH    = 0x24
	opCINDEX   = 0x25
	opMINDEX   = 0x26
	opROLL     = 0x8A
	opADD      = 0x60
	opSUB      = 0x61
	opDIV      = 0x62
	opMUL      = 0x63
	opABS      = 0x64
	opNEG      = 0x65
	opFLOOR    = 0x66
	opCEILING  = 0x67
	opMAX      = 0x8B
	opMIN      = 0x8C
	opAND      = 0x5A
	opOR       = 0x5B
	opNOT      = 0x5C
	opLT       = 0x50
	opLTEQ     = 0x51
	opGT       = 0x52
	opGTEQ     = 0x53
	opEQ       = 0x54
	opNEQ      = 0x55
	opIF       = 0x58
	opELSE     = 0x1B
	opEIF      = 0x59
	opJMPR     = 0x1C
	opJROT     = 0x78
	opJROF     = 0x79
	opRS       = 0x43
	opWS       = 0x42
	opRCVT     = 0x45
	opWCVTP    = 0x44
	opWCVTF    = 0x70
	opSVTCAx   = 0x01
	opSVTCAy   = 0x00
	opSPVTCAx  = 0x03
	opSFVTCAy  = 0x04
	opSRP0     = 0x10
	opSRP1     = 0x11
	opSRP2     = 0x12
	opSZP0     = 0x13
	opSZP1     = 0x14
	opSZP2     = 0x15
	opSZPS     = 0x16
	opSLOOP    = 0x17
	opRTG      = 0x18
	opRTHG     = 0x19
	opRTDG     = 0x3A
	opRUTG     = 0x7C
	opRDTG     = 0x7D
	opROFF     = 0x7A
	opSROUND   = 0x76
	opS45ROUND = 0x77
	opSMD      = 0x1A
	opSCVTCI   = 0x1D
	opSDB      = 0x5E
	opSDS      = 0x5F
	opFDEF     = 0x2C
	opENDF     = 0x2D
	opCALL     = 0x2B
	opLOOPCALL = 0x2A
	opIDEF     = 0x89
	opGCcur    = 0x46
	opGCorig   = 0x47
	opSCFS     = 0x48
	opMDAPr    = 0x2F
	opMDAP0    = 0x2E
	opMIAPr    = 0x3F
	opMIAP0    = 0x3E
	opSHPIX    = 0x38
	opSHP0     = 0x32
	opSHP1     = 0x33
	opIP       = 0x39
	opALIGNRP  = 0x3C
	opIUPx     = 0x31
	opIUPy     = 0x30
	opDELTAP1  = 0x5D
	opDELTAC1  = 0x73
	opMPPEM    = 0x4B
	opMPS      = 0x4C
	opGETINFO  = 0x88
)

// --- test harness -----------------------------------------------------------

// freshInterp builds a font-less interpreter with generous limits for
// hand-assembled opcode tests.
func freshInterp() *interp {
	it := &interp{
		ppem:     16,
		scale:    16.0 / 1000.0,
		maxStack: 256,
		storage:  make([]int32, 8),
		cvt:      make([]int32, 8),
		funcs:    map[int32]funcDef{},
		idefs:    map[uint8]funcDef{},
		twilight: make([]gpoint, 4),
	}
	it.resetState()
	it.dflt = it.gstate
	return it
}

// pw assembles an NPUSHW pushing the given signed 16-bit values.
func pw(vals ...int) []byte {
	b := []byte{opNPUSHW, byte(len(vals))}
	for _, v := range vals {
		b = append(b, byte(v>>8), byte(v))
	}
	return b
}

// pb assembles an NPUSHB pushing the given bytes.
func pb(vals ...int) []byte {
	b := []byte{opNPUSHB, byte(len(vals))}
	for _, v := range vals {
		b = append(b, byte(v))
	}
	return b
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// runOps executes prog on a fresh stack, returning the resulting stack.
func runOps(t *testing.T, it *interp, prog []byte) []int32 {
	t.Helper()
	it.stack = it.stack[:0]
	it.err = nil
	if err := it.exec(prog, 0); err != nil {
		t.Fatalf("exec(% x): unexpected error %v", prog, err)
	}
	return it.stack
}

func wantStack(t *testing.T, got []int32, want ...int32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("stack=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stack=%v want %v", got, want)
		}
	}
}

// wantErr executes prog expecting an error containing sub.
func wantErr(t *testing.T, it *interp, prog []byte, sub string) {
	t.Helper()
	it.stack = it.stack[:0]
	it.err = nil
	err := it.exec(prog, 0)
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("exec(% x) error=%v want containing %q", prog, err, sub)
	}
}

// --- pushes -----------------------------------------------------------------

func TestPushes(t *testing.T) {
	it := freshInterp()
	// NPUSHB / NPUSHW
	wantStack(t, runOps(t, it, pb(1, 2, 3)), 1, 2, 3)
	wantStack(t, runOps(t, it, pw(-5, 300)), -5, 300)
	// PUSHB[1], PUSHB[2]
	wantStack(t, runOps(t, it, []byte{opPUSHB1, 7}), 7)
	wantStack(t, runOps(t, it, []byte{opPUSHB2, 8, 9}), 8, 9)
	// PUSHW[1] sign-extends
	wantStack(t, runOps(t, it, []byte{opPUSHW1, 0xFF, 0xFE}), -2)
}

func TestPushTruncation(t *testing.T) {
	it := freshInterp()
	wantErr(t, it, []byte{opNPUSHB}, "truncated")       // missing count
	wantErr(t, it, []byte{opNPUSHB, 3, 1}, "truncated") // short data
	wantErr(t, it, []byte{opNPUSHW}, "truncated")       // missing count
	wantErr(t, it, []byte{opNPUSHW, 2, 0}, "truncated") // short data
	wantErr(t, it, []byte{opPUSHB2, 1}, "truncated")    // fixed short
	wantErr(t, it, []byte{opPUSHW1, 1}, "truncated")    // fixed short word
}

func TestPushOverflow(t *testing.T) {
	it := freshInterp()
	it.maxStack = 2
	wantErr(t, it, pb(1, 2, 3), "overflow")
}

// --- stack manipulation -----------------------------------------------------

func TestStackOps(t *testing.T) {
	it := freshInterp()
	wantStack(t, runOps(t, it, cat(pb(5), []byte{opDUP})), 5, 5)
	wantStack(t, runOps(t, it, cat(pb(5, 6), []byte{opPOP})), 5)
	wantStack(t, runOps(t, it, cat(pb(5, 6), []byte{opCLEAR})))
	wantStack(t, runOps(t, it, cat(pb(1, 2), []byte{opSWAP})), 2, 1)
	wantStack(t, runOps(t, it, cat(pb(1, 2, 3), []byte{opDEPTH})), 1, 2, 3, 3)
	// CINDEX: copy the 2nd-from-top (index arg=2)
	wantStack(t, runOps(t, it, cat(pb(10, 20, 30), pb(2), []byte{opCINDEX})), 10, 20, 30, 20)
	// MINDEX: move the 3rd-from-top to the top
	wantStack(t, runOps(t, it, cat(pb(10, 20, 30), pb(3), []byte{opMINDEX})), 20, 30, 10)
	// ROLL top three: a,b,c -> b? (c,a,b? per impl push b,a,c)
	wantStack(t, runOps(t, it, cat(pb(1, 2, 3), []byte{opROLL})), 2, 3, 1)
}

func TestStackUnderflow(t *testing.T) {
	it := freshInterp()
	wantErr(t, it, []byte{opPOP}, "underflow")
	wantErr(t, it, []byte{opADD}, "underflow")
	// CINDEX with bad index (pop error, then arg <=0/>n)
	wantErr(t, it, []byte{opCINDEX}, "underflow")
	wantErr(t, it, cat(pb(1), pb(5), []byte{opCINDEX}), "underflow") // k>n
	wantErr(t, it, cat(pb(1), pb(0), []byte{opCINDEX}), "underflow") // k<=0
}

// --- arithmetic and logic ---------------------------------------------------

func TestArithmetic(t *testing.T) {
	it := freshInterp()
	cases := []struct {
		prog []byte
		want int32
	}{
		{cat(pw(3, 5), []byte{opADD}), 8},
		{cat(pw(10, 4), []byte{opSUB}), 6},
		{cat(pw(128, 128), []byte{opMUL}), 256}, // (128*128)/64
		{cat(pw(256, 128), []byte{opDIV}), 128}, // (256*64)/128
		{cat(pw(-7), []byte{opABS}), 7},
		{cat(pw(7), []byte{opNEG}), -7},
		{cat(pw(100), []byte{opFLOOR}), 64},
		{cat(pw(65), []byte{opCEILING}), 128},
		{cat(pw(3, 9), []byte{opMAX}), 9},
		{cat(pw(3, 9), []byte{opMIN}), 3},
	}
	for _, c := range cases {
		wantStack(t, runOps(t, it, c.prog), c.want)
	}
	wantErr(t, it, cat(pw(5, 0), []byte{opDIV}), "division by zero")
}

func TestLogic(t *testing.T) {
	it := freshInterp()
	cases := []struct {
		prog []byte
		want int32
	}{
		{cat(pw(1, 2), []byte{opAND}), 1},
		{cat(pw(0, 2), []byte{opAND}), 0},
		{cat(pw(0, 0), []byte{opOR}), 0},
		{cat(pw(0, 2), []byte{opOR}), 1},
		{cat(pw(0), []byte{opNOT}), 1},
		{cat(pw(5), []byte{opNOT}), 0},
		{cat(pw(1, 2), []byte{opLT}), 1},
		{cat(pw(2, 2), []byte{opLTEQ}), 1},
		{cat(pw(3, 2), []byte{opGT}), 1},
		{cat(pw(2, 2), []byte{opGTEQ}), 1},
		{cat(pw(2, 2), []byte{opEQ}), 1},
		{cat(pw(2, 3), []byte{opNEQ}), 1},
	}
	for _, c := range cases {
		wantStack(t, runOps(t, it, c.prog), c.want)
	}
}

// --- control flow -----------------------------------------------------------

func TestIfElse(t *testing.T) {
	it := freshInterp()
	// IF true -> push 1, ELSE branch skipped.
	prog := cat(pw(1), []byte{opIF}, pb(1), []byte{opELSE}, pb(2, 2), []byte{opEIF})
	wantStack(t, runOps(t, it, prog), 1)
	// IF false -> take ELSE branch (push 2).
	prog = cat(pw(0), []byte{opIF}, pb(1), []byte{opELSE}, pb(2), []byte{opEIF})
	wantStack(t, runOps(t, it, prog), 2)
	// IF false, no ELSE -> skip to EIF.
	prog = cat(pw(0), []byte{opIF}, pb(9), []byte{opEIF}, pb(7))
	wantStack(t, runOps(t, it, prog), 7)
	// Nested IF exercises depth tracking during the skip.
	prog = cat(pw(0), []byte{opIF}, pw(1), []byte{opIF}, pb(1), []byte{opEIF}, []byte{opEIF}, pb(3))
	wantStack(t, runOps(t, it, prog), 3)
	// IF with empty condition underflows.
	wantErr(t, it, []byte{opIF}, "underflow")
	// Unterminated IF (no EIF) is a truncated program.
	wantErr(t, it, cat(pw(0), []byte{opIF}, pb(1)), "truncated")
}

func TestJumps(t *testing.T) {
	it := freshInterp()
	// JMPR: offset is relative to the JMPR opcode's position. The JMPR sits at
	// byte 4 (after the 4-byte PUSHW offset); offset 3 lands on the second
	// PUSHB1 (byte 7), skipping "PUSHB1 9".
	prog := cat(pw(3), []byte{opJMPR, opPUSHB1, 9, opPUSHB1, 7})
	wantStack(t, runOps(t, it, prog), 7)
	// JROT taken: push offset then bool. JMPR sits at byte 8; offset 3 lands on
	// the second PUSHB1.
	prog = cat(pw(3), pw(1), []byte{opJROT, opPUSHB1, 9, opPUSHB1, 7})
	wantStack(t, runOps(t, it, prog), 7)
	// JROT not taken (falls through, pushes 9 then 7).
	prog = cat(pw(3), pw(0), []byte{opJROT, opPUSHB1, 9, opPUSHB1, 7})
	wantStack(t, runOps(t, it, prog), 9, 7)
	// JROF taken.
	prog = cat(pw(3), pw(0), []byte{opJROF, opPUSHB1, 9, opPUSHB1, 7})
	wantStack(t, runOps(t, it, prog), 7)
	// Jump target out of range.
	wantErr(t, it, cat(pw(9999), []byte{opJMPR}), "truncated")
	// Jump with empty stack underflows.
	wantErr(t, it, []byte{opJMPR}, "underflow")
	wantErr(t, it, cat(pw(1), []byte{opJROT}), "underflow") // only bool, no offset
}

func TestBudget(t *testing.T) {
	it := freshInterp()
	// An unconditional backward jump loops forever; the budget guard stops it.
	// PUSHW offset=0 then JMPR jumps to itself (offset relative to JMPR pos=0).
	// Use offset that returns to the PUSHW: total bytes before JMPR = 4, so
	// offset -4 loops.
	prog := cat(pw(-4), []byte{opJMPR})
	it.stack = it.stack[:0]
	it.err = nil
	if err := it.exec(prog, 0); !errors.Is(err, errHintBudget) {
		t.Fatalf("expected budget error, got %v", err)
	}
}

// --- functions --------------------------------------------------------------

func TestFunctions(t *testing.T) {
	it := freshInterp()
	// Define function 5 that pushes 42, then call it.
	def := cat(pb(5), []byte{opFDEF}, pb(42), []byte{opENDF})
	call := cat(pb(5), []byte{opCALL})
	wantStack(t, runOps(t, it, cat(def, call)), 42)
	// LOOPCALL runs it 3 times.
	loop := cat(pb(3), pb(5), []byte{opLOOPCALL})
	wantStack(t, runOps(t, it, cat(def, loop)), 42, 42, 42)
}

func TestFunctionErrors(t *testing.T) {
	it := freshInterp()
	wantErr(t, it, []byte{opFDEF}, "underflow")                      // no id
	wantErr(t, it, cat(pb(1), []byte{opFDEF}, pb(2)), "truncated")   // no ENDF
	wantErr(t, it, cat(pb(9), []byte{opCALL}), "undefined function") // undefined
	wantErr(t, it, []byte{opCALL}, "underflow")                      // CALL no id
	wantErr(t, it, cat(pb(1), []byte{opLOOPCALL}), "underflow")      // LOOPCALL missing count
	wantErr(t, it, cat(pb(1, 9), []byte{opLOOPCALL}), "undefined")   // LOOPCALL undefined
}

func TestLoopCallPropagatesError(t *testing.T) {
	it := freshInterp()
	// Function 5 pops from an empty stack -> underflow inside the loop body.
	def := cat(pb(5), []byte{opFDEF}, []byte{opPOP}, []byte{opENDF})
	prog := cat(def, pb(1, 5), []byte{opLOOPCALL})
	wantErr(t, it, prog, "underflow")
}

func TestCallDepth(t *testing.T) {
	it := freshInterp()
	// Function 1 calls itself: recursion hits the call-depth guard.
	def := cat(pb(1), []byte{opFDEF}, pb(1), []byte{opCALL}, []byte{opENDF})
	prog := cat(def, pb(1), []byte{opCALL})
	wantErr(t, it, prog, "call nesting too deep")
}

func TestIDEF(t *testing.T) {
	it := freshInterp()
	// Define instruction 0x92 (unused) to push 99, then invoke it as a raw op.
	def := cat(pb(0x92), []byte{opIDEF}, pb(99), []byte{opENDF})
	wantStack(t, runOps(t, it, cat(def, []byte{0x92})), 99)
	// IDEF errors.
	wantErr(t, it, []byte{opIDEF}, "underflow")
	wantErr(t, it, cat(pb(0x93), []byte{opIDEF}, pb(1)), "truncated")
}

func TestUnimplementedOpcode(t *testing.T) {
	it := freshInterp()
	// 0x56 (ODD) is deliberately not implemented and has no IDEF.
	wantErr(t, it, []byte{0x56}, "unimplemented opcode 0x56")
}

// --- storage ----------------------------------------------------------------

func TestStorage(t *testing.T) {
	it := freshInterp()
	// WS[3]=77 then RS[3] -> 77.
	wantStack(t, runOps(t, it, cat(pb(3, 77), []byte{opWS}, pb(3), []byte{opRS})), 77)
	wantErr(t, it, cat(pb(99), []byte{opRS}), "storage read out of range")
	wantErr(t, it, cat(pb(99, 1), []byte{opWS}), "storage write out of range")
	wantErr(t, it, []byte{opRS}, "underflow")
	wantErr(t, it, []byte{opWS}, "underflow")
}

// --- CVT --------------------------------------------------------------------

func TestCVTops(t *testing.T) {
	it := freshInterp()
	// WCVTP[2]=200, RCVT[2] -> 200.
	wantStack(t, runOps(t, it, cat(pw(2, 200), []byte{opWCVTP}, pb(2), []byte{opRCVT})), 200)
	// WCVTF scales font units by scale (16/1000) into F26Dot6.
	// 1000 funits -> 16 px -> 1024 in 26.6.
	wantStack(t, runOps(t, it, cat(pw(2, 1000), []byte{opWCVTF}, pb(2), []byte{opRCVT})), 1024)
	wantErr(t, it, cat(pb(99), []byte{opRCVT}), "CVT read out of range")
	wantErr(t, it, cat(pw(99, 1), []byte{opWCVTP}), "CVT write out of range")
}

// --- graphics-state setters -------------------------------------------------

func TestStateSetters(t *testing.T) {
	it := freshInterp()
	runOps(t, it, cat(pb(2), []byte{opSRP0}))
	if it.rp0 != 2 {
		t.Errorf("SRP0: rp0=%d", it.rp0)
	}
	runOps(t, it, cat(pb(3), []byte{opSRP1}))
	runOps(t, it, cat(pb(4), []byte{opSRP2}))
	if it.rp1 != 3 || it.rp2 != 4 {
		t.Errorf("SRP1/2: %d %d", it.rp1, it.rp2)
	}
	runOps(t, it, cat(pb(5), []byte{opSLOOP}))
	if it.loop != 5 {
		t.Errorf("SLOOP: loop=%d", it.loop)
	}
	runOps(t, it, cat(pw(50), []byte{opSMD}))
	if it.minDist != 50 {
		t.Errorf("SMD: minDist=%d", it.minDist)
	}
	runOps(t, it, cat(pw(80), []byte{opSCVTCI}))
	if it.cvtCutIn != 80 {
		t.Errorf("SCVTCI: cvtCutIn=%d", it.cvtCutIn)
	}
	runOps(t, it, cat(pb(11), []byte{opSDB}))
	runOps(t, it, cat(pb(4), []byte{opSDS}))
	if it.deltaBase != 11 || it.deltaShift != 4 {
		t.Errorf("SDB/SDS: %d %d", it.deltaBase, it.deltaShift)
	}
}

func TestAxisAndZoneSetters(t *testing.T) {
	it := freshInterp()
	runOps(t, it, []byte{opSVTCAy})
	if it.pv != (vec{0, 1}) || it.fv != (vec{0, 1}) {
		t.Errorf("SVTCA[y]: pv=%v fv=%v", it.pv, it.fv)
	}
	runOps(t, it, []byte{opSVTCAx})
	if it.pv != (vec{1, 0}) {
		t.Errorf("SVTCA[x]: pv=%v", it.pv)
	}
	runOps(t, it, []byte{opSPVTCAx})
	runOps(t, it, []byte{opSFVTCAy})
	if it.pv != (vec{1, 0}) || it.fv != (vec{0, 1}) {
		t.Errorf("SPVTCA/SFVTCA: pv=%v fv=%v", it.pv, it.fv)
	}
	// Zones.
	runOps(t, it, cat(pb(0), []byte{opSZP0}))
	runOps(t, it, cat(pb(1), []byte{opSZP1}))
	runOps(t, it, cat(pb(0), []byte{opSZP2}))
	if it.zp0 != 0 || it.zp1 != 1 || it.zp2 != 0 {
		t.Errorf("SZP0/1/2: %d %d %d", it.zp0, it.zp1, it.zp2)
	}
	runOps(t, it, cat(pb(1), []byte{opSZPS}))
	if it.zp0 != 1 || it.zp1 != 1 || it.zp2 != 1 {
		t.Errorf("SZPS: %d %d %d", it.zp0, it.zp1, it.zp2)
	}
	wantErr(t, it, cat(pb(2), []byte{opSZP0}), "bad zone pointer")
	wantErr(t, it, []byte{opSZP0}, "underflow")
}

func TestRoundModes(t *testing.T) {
	it := freshInterp()
	// GC then rounding is tested via MDAP below; here check each setter's effect
	// through roundState.apply.
	cases := []struct {
		op   byte
		in   float64
		want float64
	}{
		{opRTG, 96, 128},   // 1.5px -> 2px
		{opRTHG, 96, 96},   // half grid: 1.5 stays at 1.5 (phase .5)
		{opRTDG, 80, 96},   // double grid (0.5px steps)
		{opRUTG, 65, 128},  // ceil to 2px
		{opRDTG, 127, 64},  // floor to 1px
		{opROFF, 100, 100}, // no rounding
	}
	for _, c := range cases {
		runOps(t, it, []byte{c.op})
		if got := it.round.apply(c.in); got != c.want {
			t.Errorf("op %#x apply(%v)=%v want %v", c.op, c.in, got, c.want)
		}
	}
	// Negative value path and the clamp-to-zero grid branch.
	runOps(t, it, []byte{opRTG})
	if got := it.round.apply(-96); got != -128 {
		t.Errorf("RTG apply(-96)=%v want -128", got)
	}
}

func TestSuperRound(t *testing.T) {
	it := freshInterp()
	// SROUND selector: period=2*grid (bits 10), phase=1/4, threshold nibble 5.
	runOps(t, it, cat(pb(0b10_01_0101), []byte{opSROUND}))
	if it.round.mode != rndGrid || it.round.period != 128 {
		t.Errorf("SROUND period=%v", it.round.period)
	}
	// period=1/2 grid (bits 00), phase 1/2 (10), threshold nibble 0 -> period-1.
	runOps(t, it, cat(pb(0b00_10_0000), []byte{opSROUND}))
	if it.round.period != 32 || it.round.threshold != 31 {
		t.Errorf("SROUND period=%v thr=%v", it.round.period, it.round.threshold)
	}
	// phase 3/4 (bits 11) branch.
	runOps(t, it, cat(pb(0b01_11_0001), []byte{opSROUND}))
	if it.round.phase != it.round.period*3/4 {
		t.Errorf("SROUND phase=%v", it.round.phase)
	}
	// S45ROUND uses the sqrt2/2 grid.
	runOps(t, it, cat(pb(0b01_00_0011), []byte{opS45ROUND}))
	if it.round.mode != rndGrid {
		t.Errorf("S45ROUND mode=%v", it.round.mode)
	}
	wantErr(t, it, []byte{opSROUND}, "underflow")
}

// --- point measurement and movement -----------------------------------------

// setZone1 installs a glyph-zone contour of the given points.
func setZone1(it *interp, pts ...gpoint) {
	it.glyphZone = pts
}

func TestGC_SCFS(t *testing.T) {
	it := freshInterp()
	setZone1(it, gpoint{curX: 96, curY: 0, origX: 64, origY: 0})
	// GC[current] on x-axis -> 96.
	wantStack(t, runOps(t, it, cat(pb(0), []byte{opGCcur})), 96)
	// GC[original] -> 64.
	wantStack(t, runOps(t, it, cat(pb(0), []byte{opGCorig})), 64)
	// SCFS sets the coordinate to 128.
	runOps(t, it, cat(pw(0, 128), []byte{opSCFS}))
	if it.glyphZone[0].curX != 128 {
		t.Errorf("SCFS: curX=%d want 128", it.glyphZone[0].curX)
	}
	wantErr(t, it, cat(pb(9), []byte{opGCcur}), "out of range")
}

func TestMDAP(t *testing.T) {
	it := freshInterp()
	setZone1(it, gpoint{curX: 96, curY: 0, origX: 96, origY: 0})
	// MDAP[round] snaps 1.5px to 2px and touches x.
	runOps(t, it, cat(pb(0), []byte{opMDAPr}))
	if it.glyphZone[0].curX != 128 || !it.glyphZone[0].tx {
		t.Errorf("MDAP[r]: curX=%d tx=%v", it.glyphZone[0].curX, it.glyphZone[0].tx)
	}
	if it.rp0 != 0 || it.rp1 != 0 {
		t.Errorf("MDAP: rp0/1=%d/%d", it.rp0, it.rp1)
	}
	// MDAP[no round] only touches, no move.
	setZone1(it, gpoint{curX: 96})
	runOps(t, it, cat(pb(0), []byte{opMDAP0}))
	if it.glyphZone[0].curX != 96 || !it.glyphZone[0].tx {
		t.Errorf("MDAP[0]: curX=%d tx=%v", it.glyphZone[0].curX, it.glyphZone[0].tx)
	}
	wantErr(t, it, cat(pb(9), []byte{opMDAPr}), "out of range")
}

func TestMIAP(t *testing.T) {
	it := freshInterp()
	it.cvt[1] = 128
	// Cut-in triggered: |cvt(128) - cur(40)| = 88 > default cut-in (68), so the
	// CVT value is dropped and the current position is rounded (40 -> 64).
	setZone1(it, gpoint{curX: 40, curY: 0, origX: 40, origY: 0})
	runOps(t, it, cat(pb(0, 1), []byte{opMIAPr}))
	if it.glyphZone[0].curX != 64 {
		t.Errorf("MIAP[r] cut-in: curX=%d want 64", it.glyphZone[0].curX)
	}
	// Cut-in not triggered: a generous cut-in keeps the CVT value (128).
	it.cvtCutIn = 128
	setZone1(it, gpoint{curX: 100, curY: 0, origX: 100, origY: 0})
	runOps(t, it, cat(pb(0, 1), []byte{opMIAPr}))
	if it.glyphZone[0].curX != 128 {
		t.Errorf("MIAP[r]: curX=%d want 128", it.glyphZone[0].curX)
	}
	// MIAP[no round] on the twilight zone sets the point from the CVT.
	it.zp0 = 0
	it.cvt[2] = 200
	runOps(t, it, cat(pb(0, 2), []byte{opMIAP0}))
	if it.twilight[0].curX != 200 {
		t.Errorf("MIAP twilight: curX=%d want 200", it.twilight[0].curX)
	}
	it.zp0 = 1
	wantErr(t, it, cat(pb(9, 1), []byte{opMIAPr}), "out of range")
}

func TestMDRP(t *testing.T) {
	it := freshInterp()
	// rp0 at origin, point 1 orig 1px, cur 70/64 -> snapped to 1px grid.
	setZone1(it,
		gpoint{curX: 0, origX: 0},
		gpoint{curX: 70, origX: 64},
	)
	runOps(t, it, cat(pb(1), []byte{0xC4})) // MDRP round, no min, no setRP0
	if it.glyphZone[1].curX != 64 {
		t.Errorf("MDRP: curX=%d want 64", it.glyphZone[1].curX)
	}
	if it.rp2 != 1 {
		t.Errorf("MDRP: rp2=%d want 1", it.rp2)
	}
	// setRP0 variant (0x10) updates rp0; min-distance variant enforces >=minDist.
	setZone1(it,
		gpoint{curX: 0, origX: 0},
		gpoint{curX: 10, origX: 10}, // tiny orig distance, min-dist should push it out
	)
	it.minDist = 64
	runOps(t, it, cat(pb(1), []byte{0xD8})) // MDRP min-dist + setRP0
	if it.glyphZone[1].curX != 64 {
		t.Errorf("MDRP min: curX=%d want 64", it.glyphZone[1].curX)
	}
	if it.rp0 != 1 {
		t.Errorf("MDRP setRP0: rp0=%d want 1", it.rp0)
	}
	// negative min-distance branch.
	it.rp0 = 0 // undo the setRP0 from the previous case
	setZone1(it,
		gpoint{curX: 0, origX: 0},
		gpoint{curX: -10, origX: -10},
	)
	runOps(t, it, cat(pb(1), []byte{0xC8})) // min-dist only
	if it.glyphZone[1].curX != -64 {
		t.Errorf("MDRP negmin: curX=%d want -64", it.glyphZone[1].curX)
	}
	wantErr(t, it, cat(pb(9), []byte{0xC0}), "out of range")
}

func TestMIRP(t *testing.T) {
	it := freshInterp()
	it.cvt[0] = 128
	setZone1(it,
		gpoint{curX: 0, origX: 0},
		gpoint{curX: 64, origX: 64},
	)
	// MIRP round: cvt=128 within cut-in of orig(64)? |128-64|=64 < cutIn? Set a
	// generous cut-in so the CVT value wins.
	it.cvtCutIn = 128
	runOps(t, it, cat(pb(1, 0), []byte{0xE4})) // MIRP round
	if it.glyphZone[1].curX != 128 {
		t.Errorf("MIRP: curX=%d want 128", it.glyphZone[1].curX)
	}
	// Cut-in triggered: large CVT disagreement falls back to the original dist.
	it.cvt[0] = 640
	it.cvtCutIn = 64
	setZone1(it,
		gpoint{curX: 0, origX: 0},
		gpoint{curX: 64, origX: 64},
	)
	runOps(t, it, cat(pb(1, 0), []byte{0xF0})) // MIRP setRP0, no round
	if it.glyphZone[1].curX != 64 {
		t.Errorf("MIRP cutin: curX=%d want 64", it.glyphZone[1].curX)
	}
	if it.rp0 != 1 {
		t.Errorf("MIRP setRP0: rp0=%d", it.rp0)
	}
	wantErr(t, it, cat(pb(9, 0), []byte{0xE0}), "out of range")
}

func TestSHPIX(t *testing.T) {
	it := freshInterp()
	setZone1(it, gpoint{curX: 10}, gpoint{curX: 20})
	it.loop = 2
	// SHPIX shifts both looped points by 32 along the x freedom vector.
	runOps(t, it, cat(pb(1, 0), pw(32), []byte{opSHPIX}))
	if it.glyphZone[0].curX != 42 || it.glyphZone[1].curX != 52 {
		t.Errorf("SHPIX: %d %d", it.glyphZone[0].curX, it.glyphZone[1].curX)
	}
	if it.loop != 1 {
		t.Errorf("SHPIX should reset loop, got %d", it.loop)
	}
	it.loop = 1
	wantErr(t, it, cat(pb(9), pw(1), []byte{opSHPIX}), "out of range")
}

func TestSHP(t *testing.T) {
	it := freshInterp()
	// Reference point rp2 moved +20 (cur 84 vs orig 64); SHP[0] shifts by that.
	setZone1(it,
		gpoint{curX: 84, origX: 64}, // index 0 = rp2 reference
		gpoint{curX: 100, origX: 100},
	)
	it.rp2 = 0
	runOps(t, it, cat(pb(1), []byte{opSHP0}))
	if it.glyphZone[1].curX != 120 {
		t.Errorf("SHP[0]: curX=%d want 120", it.glyphZone[1].curX)
	}
	// SHP[1] uses rp1 in zp0.
	setZone1(it,
		gpoint{curX: 84, origX: 64},
		gpoint{curX: 100, origX: 100},
	)
	it.rp1 = 0
	runOps(t, it, cat(pb(1), []byte{opSHP1}))
	if it.glyphZone[1].curX != 120 {
		t.Errorf("SHP[1]: curX=%d want 120", it.glyphZone[1].curX)
	}
	// Bad reference point.
	it.rp1 = 99
	wantErr(t, it, cat(pb(1), []byte{opSHP1}), "out of range")
	// Bad looped point.
	it.rp1 = 0
	wantErr(t, it, cat(pb(99), []byte{opSHP1}), "out of range")
}

func TestALIGNRP(t *testing.T) {
	it := freshInterp()
	setZone1(it,
		gpoint{curX: 64}, // rp0
		gpoint{curX: 200},
	)
	it.rp0 = 0
	runOps(t, it, cat(pb(1), []byte{opALIGNRP}))
	if it.glyphZone[1].curX != 64 {
		t.Errorf("ALIGNRP: curX=%d want 64", it.glyphZone[1].curX)
	}
	it.rp0 = 99
	wantErr(t, it, cat(pb(1), []byte{opALIGNRP}), "out of range")
	it.rp0 = 0
	wantErr(t, it, cat(pb(99), []byte{opALIGNRP}), "out of range")
}

func TestIP(t *testing.T) {
	it := freshInterp()
	// rp1 orig 0 cur 0, rp2 orig 100 cur 200; interpolate a mid point.
	setZone1(it,
		gpoint{curX: 0, origX: 0},     // rp1 = 0
		gpoint{curX: 200, origX: 100}, // rp2 = 1
		gpoint{curX: 50, origX: 50},   // point to interpolate
	)
	it.rp1, it.rp2 = 0, 1
	runOps(t, it, cat(pb(2), []byte{opIP}))
	if it.glyphZone[2].curX != 100 { // halfway -> 0 + 0.5*(200-0)=100
		t.Errorf("IP: curX=%d want 100", it.glyphZone[2].curX)
	}
	// Degenerate reference (rp1==rp2 orig): shift branch.
	setZone1(it,
		gpoint{curX: 30, origX: 10},
		gpoint{curX: 30, origX: 10},
		gpoint{curX: 5, origX: 25},
	)
	it.rp1, it.rp2 = 0, 1
	runOps(t, it, cat(pb(2), []byte{opIP}))
	if it.glyphZone[2].curX != 45 { // 30 + (25-10) = 45
		t.Errorf("IP shift: curX=%d want 45", it.glyphZone[2].curX)
	}
	it.rp1 = 99
	wantErr(t, it, cat(pb(0), []byte{opIP}), "out of range")
	it.rp1 = 0
	wantErr(t, it, cat(pb(99), []byte{opIP}), "out of range")
}

func TestIUP(t *testing.T) {
	it := freshInterp()
	// Contour: p0 touched (moved +10), p1 untouched (interpolated), p2 touched.
	setZone1(it,
		gpoint{curX: 10, origX: 0, tx: true},
		gpoint{curX: 100, origX: 100},
		gpoint{curX: 330, origX: 300, tx: true},
	)
	runOps(t, it, []byte{opIUPx})
	if it.glyphZone[1].curX != 117 {
		t.Errorf("IUP[x]: curX=%d want 117", it.glyphZone[1].curX)
	}
	// IUP[y] with nothing touched leaves the contour alone.
	setZone1(it, gpoint{curY: 5}, gpoint{curY: 9})
	runOps(t, it, []byte{opIUPy})
	if it.glyphZone[0].curY != 5 {
		t.Errorf("IUP[y] untouched changed points")
	}
	// Empty zone is a no-op.
	setZone1(it)
	runOps(t, it, []byte{opIUPx})
}

func TestIUPSpanBranches(t *testing.T) {
	// Exercise iupSpan directly: swap (lo>hi), o<=lo, o>=hi and interpolation.
	it := freshInterp()
	x := []int32{210, 999, 999, 999, 5} // cur; indices 1..3 will be set
	o := []int32{200, 300, 100, -50, 0} // orig
	cur := func(i int) int32 { return x[i] }
	orig := func(i int) int32 { return o[i] }
	setCur := func(i int, v int32) { x[i] = v }
	it.iupSpan(0, 4, 5, cur, orig, setCur)
	// anchors a=0 (orig200,cur210) b=4 (orig0,cur5) -> lo=0,hi=200,clo=5,chi=210.
	if x[1] != 310 { // o=300>=hi -> 300+(210-200)
		t.Errorf("iupSpan o>=hi: %d want 310", x[1])
	}
	if x[2] != 108 { // o=100 interp -> 5 + round(100/200*205)=5+103
		t.Errorf("iupSpan interp: %d want 108", x[2])
	}
	if x[3] != -45 { // o=-50<=lo -> -50+(5-0)
		t.Errorf("iupSpan o<=lo: %d want -45", x[3])
	}
}

// --- delta ------------------------------------------------------------------

func TestDeltaP(t *testing.T) {
	it := freshInterp()
	setZone1(it, gpoint{curX: 0})
	it.deltaBase = 9
	it.deltaShift = 3
	it.ppem = 16
	// Each exception is pushed as (arg, pointIndex) with the pair count on top.
	// arg high nibble selects ppem (base9 + 7 = 16 = current ppem); low nibble 8
	// -> deltaStep(8): step=0->+1 -> 1*64/8 = 8.
	runOps(t, it, cat(pb(0x78, 0, 1), []byte{opDELTAP1}))
	if it.glyphZone[0].curX != 8 {
		t.Errorf("DELTAP1: curX=%d want 8", it.glyphZone[0].curX)
	}
	// Non-matching ppem is skipped.
	setZone1(it, gpoint{curX: 0})
	runOps(t, it, cat(pb(0x08, 0, 1), []byte{opDELTAP1})) // selects ppem 9
	if it.glyphZone[0].curX != 0 {
		t.Errorf("DELTAP1 skip: curX=%d want 0", it.glyphZone[0].curX)
	}
	// Errors: bad count / bad operands / bad point.
	wantErr(t, it, []byte{opDELTAP1}, "underflow")
	wantErr(t, it, cat(pb(1), []byte{opDELTAP1}), "underflow")
	wantErr(t, it, cat(pb(0x78, 9, 1), []byte{opDELTAP1}), "out of range")
}

func TestDeltaC(t *testing.T) {
	it := freshInterp()
	it.cvt[0] = 100
	it.deltaBase = 9
	it.deltaShift = 3
	it.ppem = 16
	runOps(t, it, cat(pb(0x78, 0, 1), []byte{opDELTAC1}))
	if it.cvt[0] != 108 {
		t.Errorf("DELTAC1: cvt0=%d want 108", it.cvt[0])
	}
	// Non-matching ppem skipped.
	runOps(t, it, cat(pb(0x08, 0, 1), []byte{opDELTAC1}))
	if it.cvt[0] != 108 {
		t.Errorf("DELTAC1 skip changed cvt: %d", it.cvt[0])
	}
	wantErr(t, it, []byte{opDELTAC1}, "underflow")
	wantErr(t, it, cat(pb(1), []byte{opDELTAC1}), "underflow")
	wantErr(t, it, cat(pb(0x78, 99, 1), []byte{opDELTAC1}), "CVT read out of range")
}

func TestDeltaStepNegative(t *testing.T) {
	it := freshInterp()
	setZone1(it, gpoint{curX: 0})
	it.ppem = 16
	// low nibble 0 -> step = 0-8 = -8 -> -8*64/8 = -64.
	runOps(t, it, cat(pb(0x70, 0, 1), []byte{opDELTAP1}))
	if it.glyphZone[0].curX != -64 {
		t.Errorf("DELTAP1 neg: curX=%d want -64", it.glyphZone[0].curX)
	}
}

// --- info -------------------------------------------------------------------

func TestInfoOps(t *testing.T) {
	it := freshInterp()
	it.ppem = 24
	wantStack(t, runOps(t, it, []byte{opMPPEM}), 24)
	wantStack(t, runOps(t, it, []byte{opMPS}), 24)
	// GETINFO version bit -> 42.
	wantStack(t, runOps(t, it, cat(pb(1), []byte{opGETINFO})), 42)
	// GETINFO grayscale bit -> 0x1000.
	wantStack(t, runOps(t, it, cat(pb(0x20), []byte{opGETINFO})), 1<<12)
	wantErr(t, it, []byte{opGETINFO}, "underflow")
}

// --- helpers ----------------------------------------------------------------

func TestNumericHelpers(t *testing.T) {
	if boolI(true) != 1 || boolI(false) != 0 {
		t.Error("boolI")
	}
	if maxI32(1, 2) != 2 || maxI32(3, 2) != 3 {
		t.Error("maxI32")
	}
	if minI32(1, 2) != 1 || minI32(3, 2) != 2 {
		t.Error("minI32")
	}
}

func TestMoveByPerpendicular(t *testing.T) {
	it := freshInterp()
	// Projection on x, freedom on y: perpendicular -> point cannot move.
	it.pv = vec{1, 0}
	it.fv = vec{0, 1}
	setZone1(it, gpoint{curX: 50, curY: 50})
	// MDAP[round] tries to move but the perpendicular freedom vector blocks it.
	runOps(t, it, cat(pb(0), []byte{opMDAPr}))
	if it.glyphZone[0].curX != 50 || it.glyphZone[0].curY != 50 {
		t.Errorf("perpendicular move should be a no-op: %v", it.glyphZone[0])
	}
}

// --- font-level wiring: newInterp / run -------------------------------------

// hintFontBytes builds a synthesized font carrying fpgm, prep, cvt and a full
// version-1.0 maxp so newInterp exercises real table wiring.
func hintFontBytes(fpgm, prep []byte, cvt []int16) []byte {
	tb := stdTables(false, false)
	tb["fpgm"] = fpgm
	tb["prep"] = prep
	// cvt table.
	cw := &bw{}
	for _, v := range cvt {
		cw.i16(v)
	}
	tb["cvt "] = cw.bytes()
	// Full maxp v1.0 (32 bytes): set numGlyphs and the interpreter limits.
	m := make([]byte, 32)
	m[0], m[1], m[2], m[3] = 0x00, 0x01, 0x00, 0x00 // version 1.0
	putU16(m[4:], 11)                               // numGlyphs
	putU16(m[16:], 8)                               // maxTwilightPoints
	putU16(m[18:], 8)                               // maxStorage
	putU16(m[20:], 16)                              // maxFunctionDefs
	putU16(m[24:], 64)                              // maxStackElements
	tb["maxp"] = m
	return assemble(versionTrueType, tb)
}

func putU16(b []byte, v uint16) { b[0], b[1] = byte(v>>8), byte(v) }

func TestNewInterpAndRun(t *testing.T) {
	// fpgm defines function 0 (pushes nothing, harmless). prep writes cvt[0].
	fpgm := cat(pb(0), []byte{opFDEF}, []byte{opENDF})
	prep := cat(pw(0, 256), []byte{opWCVTP}) // set cvt[0]=256 (4px)
	f, err := Parse(hintFontBytes(fpgm, prep, []int16{100, 200}))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.maxStorage != 8 || f.maxFunctionDefs != 16 || f.maxStackElements != 64 || f.maxTwilightPts != 8 {
		t.Fatalf("maxp limits not parsed: %+v", []int{f.maxStorage, f.maxFunctionDefs, f.maxStackElements, f.maxTwilightPts})
	}
	it, err := newInterp(f, 32) // 32px em -> scale 32/1000
	if err != nil {
		t.Fatalf("newInterp: %v", err)
	}
	if it.cvt[0] != 256 {
		t.Errorf("prep did not set cvt[0]: %d", it.cvt[0])
	}
	// run: a single point instruction stream (MDAP[round]) snaps the point.
	pts := []outlinePoint{{x: 1.5, y: 0, on: true}}
	instr := cat(pb(0), []byte{opMDAPr})
	out, err := it.run(pts, instr)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out[0].x != 2 {
		t.Errorf("run MDAP: x=%v want 2", out[0].x)
	}
}

func TestNewInterpErrors(t *testing.T) {
	f, err := Parse(hintFontBytes(nil, nil, nil))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, err := newInterp(f, 0); err == nil || !strings.Contains(err.Error(), "non-positive ppem") {
		t.Fatalf("newInterp(ppem=0) err=%v", err)
	}
	// fpgm error: an unimplemented opcode aborts newInterp.
	ff, _ := Parse(hintFontBytes([]byte{0x56}, nil, nil))
	if _, err := newInterp(ff, 16); err == nil || !strings.Contains(err.Error(), "unimplemented") {
		t.Fatalf("newInterp fpgm err=%v", err)
	}
	// prep error.
	pf, _ := Parse(hintFontBytes(nil, []byte{0x56}, nil))
	if _, err := newInterp(pf, 16); err == nil || !strings.Contains(err.Error(), "unimplemented") {
		t.Fatalf("newInterp prep err=%v", err)
	}
}

func TestRunError(t *testing.T) {
	f, _ := Parse(hintFontBytes(nil, nil, nil))
	it, err := newInterp(f, 16)
	if err != nil {
		t.Fatalf("newInterp: %v", err)
	}
	if _, err := it.run([]outlinePoint{{}}, []byte{0x56}); err == nil {
		t.Fatalf("run should surface the unimplemented-opcode error")
	}
}

// TestYAxisMovement exercises the freedom-vector y-axis branches of moveBy,
// MDAP, SHPIX and IUP.
func TestYAxisMovement(t *testing.T) {
	it := freshInterp()
	runOps(t, it, []byte{opSVTCAy}) // projection and freedom on y
	setZone1(it, gpoint{curX: 0, curY: 96, origX: 0, origY: 96})
	runOps(t, it, cat(pb(0), []byte{opMDAPr})) // moves along y, sets ty
	if it.glyphZone[0].curY != 128 || !it.glyphZone[0].ty {
		t.Errorf("MDAP[r] y: curY=%d ty=%v", it.glyphZone[0].curY, it.glyphZone[0].ty)
	}
	// MDAP[no round] on y only touches ty.
	setZone1(it, gpoint{curY: 96})
	runOps(t, it, cat(pb(0), []byte{opMDAP0}))
	if it.glyphZone[0].curY != 96 || !it.glyphZone[0].ty {
		t.Errorf("MDAP[0] y: curY=%d ty=%v", it.glyphZone[0].curY, it.glyphZone[0].ty)
	}
	// SHPIX along y.
	setZone1(it, gpoint{curY: 10})
	it.loop = 1
	runOps(t, it, cat(pb(0), pw(32), []byte{opSHPIX}))
	if it.glyphZone[0].curY != 42 || !it.glyphZone[0].ty {
		t.Errorf("SHPIX y: curY=%d ty=%v", it.glyphZone[0].curY, it.glyphZone[0].ty)
	}
}

func TestIUPy(t *testing.T) {
	it := freshInterp()
	setZone1(it,
		gpoint{curY: 10, origY: 0, ty: true},
		gpoint{curY: 100, origY: 100},
		gpoint{curY: 330, origY: 300, ty: true},
	)
	runOps(t, it, []byte{opIUPy})
	if it.glyphZone[1].curY != 117 {
		t.Errorf("IUP[y]: curY=%d want 117", it.glyphZone[1].curY)
	}
}

func TestDeltaHigherBands(t *testing.T) {
	it := freshInterp()
	// DELTAP2/3 and DELTAC2/3 dispatch: run with an empty exception list (count
	// zero) so only the opcode dispatch is exercised.
	for _, op := range []byte{0x71, 0x72, 0x73, 0x74, 0x75} {
		runOps(t, it, cat(pb(0), []byte{op}))
	}
}

func TestBareENDF(t *testing.T) {
	it := freshInterp()
	// A bare ENDF terminates the running program (the trailing push is skipped).
	wantStack(t, runOps(t, it, cat(pb(1), []byte{opENDF}, pb(2))), 1)
}

func TestOpLenInSkip(t *testing.T) {
	it := freshInterp()
	// A skipped IF branch containing fixed PUSHB[1]/PUSHW[1] exercises opLen's
	// fixed-push length cases.
	prog := cat(pw(0), []byte{opIF, opPUSHB1, 9, opPUSHW1, 0x00, 0x05, opEIF}, pb(7))
	wantStack(t, runOps(t, it, prog), 7)
	// Truncated NPUSHB / NPUSHW inside the skipped branch -> opLen returns -1.
	wantErr(t, it, cat(pw(0), []byte{opIF, opNPUSHB}), "truncated")
	wantErr(t, it, cat(pw(0), []byte{opIF, opNPUSHW}), "truncated")
}

func TestScanFDEFTruncatedPush(t *testing.T) {
	it := freshInterp()
	// A function body with a truncated NPUSHB before ENDF -> scanFDEF errors.
	wantErr(t, it, cat(pb(1), []byte{opFDEF, opNPUSHB}), "truncated")
}

func TestWriteCVTUnderflow(t *testing.T) {
	it := freshInterp()
	// WCVTP with an empty stack underflows before the CVT write.
	wantErr(t, it, []byte{opWCVTP}, "underflow")
}

func TestSCFSBadPoint(t *testing.T) {
	it := freshInterp()
	setZone1(it, gpoint{})
	wantErr(t, it, cat(pw(9, 0), []byte{opSCFS}), "out of range")
}

func TestGridRoundClampToZero(t *testing.T) {
	// A grid rounding whose phase exceeds the shifted value clamps to zero.
	rs := roundState{mode: rndGrid, period: 64, phase: 48, threshold: 0}
	if got := rs.apply(10); got != 0 {
		t.Errorf("grid clamp: apply(10)=%v want 0", got)
	}
}

func TestSmallMaxStackFloor(t *testing.T) {
	// A font declaring a tiny maxStackElements is floored to 32.
	tb := stdTables(false, false)
	m := make([]byte, 32)
	m[0], m[2] = 0x00, 0x00
	m[1] = 0x01
	putU16(m[4:], 11)
	putU16(m[24:], 4) // maxStackElements = 4 (below the floor)
	tb["maxp"] = m
	f, err := Parse(assemble(versionTrueType, tb))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	it, err := newInterp(f, 16)
	if err != nil {
		t.Fatalf("newInterp: %v", err)
	}
	if it.maxStack != 32 {
		t.Errorf("maxStack=%d want floored to 32", it.maxStack)
	}
}
