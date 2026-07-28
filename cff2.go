// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// This file adds CFF2 (variable Compact Font Format) outline support, so
// variable OpenType/CFF fonts (variable .otf, e.g. Source Serif Variable and
// many Adobe families) can be parsed and instanced. CFF2 is a close variant of
// CFF1/Type2 (cff.go): it shares the INDEX/DICT encodings and nearly all of the
// Type2 charstring path operators, so this file reuses cff.go's INDEX/DICT
// parsers, the Type2 operand decoder, and the whole Bézier-flattening pen
// (t2machine's moveTo/lineTo/curveTo and the curve helpers) to emit the same
// []contour model glyf.go produces.
//
// CFF2 differs from CFF1 in these ways, all handled here:
//   - Header carries a 16-bit topDictLength; the Top DICT is a single DICT (no
//     Top DICT INDEX), and every INDEX uses a 32-bit count (parseIndex2).
//   - There is no per-glyph advance width, no endchar, no seac and no charset;
//     charstrings simply end at their last byte.
//   - Outlines vary with the design space: the 'blend' and 'vsindex' charstring
//     operators pull deltas from the shared OpenType ItemVariationStore
//     ('vstore') and interpolate operands in place at a normalized coordinate.
//   - The Private DICT and its Local Subrs live per Font DICT in the FDArray,
//     selected per glyph by FDSelect.
//
// CFF2 hinting is supported: hstem/vstem/hintmask stems and each Font DICT's
// Private-DICT hint parameters are recorded here and grid-fitted by cffhint.go
// exactly as for CFF1, so Face.SetHinting(true) grid-fits CFF2 glyphs too.
// Deferred (not implemented here, matching cff.go's scope): FDSelect format 4
// (for >65535 glyphs) and non-format-1 ItemVariationStores are rejected; metric
// variation (HVAR and friends) is out of scope. The interpreter's operand stack
// limit follows the CFF2 maximum.

// maxCFF2Stack is the CFF2 charstring operand-stack limit (the spec permits 513
// entries; blend expands the stack well past the Type2 limit of 48).
const maxCFF2Stack = 513

// cff2Table is a parsed CFF2 table: the CharStrings INDEX (one charstring per
// glyph), the Global Subr INDEX with its bias, the per-Font-DICT Local Subr
// INDEXes (with biases and default variation-store indices), the per-glyph
// Font-DICT selector, and the shared ItemVariationStore driving blends.
type cff2Table struct {
	charStrings [][]byte
	globalSubrs [][]byte
	gsubrBias   int
	fdSelect    []int         // gid -> Font DICT index; nil means every glyph uses 0
	localSubrs  [][][]byte    // per Font DICT: its Local Subr INDEX
	localBias   []int         // per Font DICT: its Local Subr bias
	privVsindex []int         // per Font DICT: default vsindex from the Private DICT
	privHints   []*cffPrivate // per Font DICT: parsed Private-DICT hint parameters
	vstore      *itemVarStore
}

// regionAxis is one axis of one variation region: the tent function's lower,
// peak and upper normalized coordinates.
type regionAxis struct {
	start, peak, end float64
}

// ivData is one ItemVariationData subtable reduced to what a CFF2 blend needs:
// the ordered list of region indices whose deltas the blend supplies. The delta
// values themselves come from the charstring, not from the store.
type ivData struct {
	regionIndices []int
}

// itemVarStore is the shared OpenType ItemVariationStore (format 1) referenced
// by the Top DICT 'vstore' operator: the region list plus each variation-data
// subtable's region ordering.
type itemVarStore struct {
	axisCount int
	regions   [][]regionAxis
	data      []ivData
}

// parseIndex2 decodes a CFF2 INDEX (identical to a CFF1 INDEX but with a 32-bit
// count) at r's current position, returning each stored object as a sub-slice of
// r.b and leaving r positioned just past the INDEX.
func parseIndex2(r *reader) ([][]byte, error) {
	count := int(r.u32())
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cff2 index count: %w", r.err)
	}
	if count == 0 {
		return nil, nil
	}
	offSize := int(r.u8())
	if offSize < 1 || offSize > 4 {
		return nil, fmt.Errorf("opentype: cff2 index: bad offSize %d", offSize)
	}
	offsets := make([]int, count+1)
	for k := range offsets {
		offsets[k] = readOffset(r, offSize)
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cff2 index offsets: %w", r.err)
	}
	base := r.pos - offsets[0]
	items := make([][]byte, count)
	for k := 0; k < count; k++ {
		start, end := base+offsets[k], base+offsets[k+1]
		if start < 0 || end > len(r.b) || start > end {
			return nil, fmt.Errorf("opentype: cff2 index: item %d out of range", k)
		}
		items[k] = r.b[start:end]
	}
	r.pos = base + offsets[count]
	return items, nil
}

// parseDict2 decodes a CFF2 DICT into a map from operator to its operand list.
// It differs from cff.go's parseDict only in the operator range: CFF2 adds the
// single-byte operators vsindex (22) and blend (23) to the Private DICT and
// vstore (24) to the Top DICT, all of which are reserved in CFF1. Operand bytes
// (28, 29, 30 and 32..255) share cff.go's parseDictOperand decoder.
func parseDict2(b []byte) (map[int][]float64, error) {
	d := map[int][]float64{}
	var operands []float64
	i := 0
	for i < len(b) {
		b0 := b[i]
		if b0 < 28 { // operator (12 introduces a two-byte operator)
			op := int(b0)
			i++
			if b0 == 12 {
				if i >= len(b) {
					return nil, fmt.Errorf("opentype: cff2 dict: truncated operator")
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

// parseCFF2 decodes a CFF2 table: the 5-byte header, the Top DICT, the Global
// Subr INDEX, the CharStrings INDEX, the optional ItemVariationStore, and the
// FDArray/FDSelect that assign each glyph its Private DICT and Local Subrs.
func parseCFF2(data []byte) (*cff2Table, error) {
	if len(data) < 5 {
		return nil, fmt.Errorf("opentype: cff2 header: %w", errTruncated)
	}
	if data[0] != 2 {
		return nil, fmt.Errorf("opentype: cff2: unsupported major version %d", data[0])
	}
	hdrSize := int(data[2])
	hr := reader{b: data, pos: 3}
	topDictLen := int(hr.u16())
	if hdrSize < 5 || hdrSize > len(data) {
		return nil, fmt.Errorf("opentype: cff2: bad headerSize %d", hdrSize)
	}
	if hdrSize+topDictLen > len(data) {
		return nil, fmt.Errorf("opentype: cff2: Top DICT out of range")
	}

	top, err := parseDict2(data[hdrSize : hdrSize+topDictLen])
	if err != nil {
		return nil, err
	}

	r := &reader{b: data, pos: hdrSize + topDictLen}
	gsubrs, err := parseIndex2(r) // Global Subr INDEX
	if err != nil {
		return nil, err
	}

	csOff, ok := dictInt(top, 17, 0)
	if !ok {
		return nil, fmt.Errorf("opentype: cff2: missing CharStrings offset")
	}
	if csOff < 0 || csOff > len(data) {
		return nil, fmt.Errorf("opentype: cff2: CharStrings offset out of range")
	}
	charStrings, err := parseIndex2(&reader{b: data, pos: csOff})
	if err != nil {
		return nil, err
	}

	var vstore *itemVarStore
	if vsOff, ok := dictInt(top, 24, 0); ok {
		if vstore, err = parseItemVarStore(data, vsOff); err != nil {
			return nil, err
		}
	}

	localSubrs, localBias, privVsindex, privHints, err := parseCFF2FDArray(data, top)
	if err != nil {
		return nil, err
	}
	fdSelect, err := parseCFF2FDSelect(data, top, len(charStrings), len(localSubrs))
	if err != nil {
		return nil, err
	}

	return &cff2Table{
		charStrings: charStrings,
		globalSubrs: gsubrs,
		gsubrBias:   subrBias(len(gsubrs)),
		fdSelect:    fdSelect,
		localSubrs:  localSubrs,
		localBias:   localBias,
		privVsindex: privVsindex,
		privHints:   privHints,
		vstore:      vstore,
	}, nil
}

// parseItemVarStore decodes the ItemVariationStore at offset off in data, whose
// data is prefixed with a 16-bit length. Only format 1 is supported. For CFF2
// only the region list and each subtable's region ordering are needed; the
// stored delta sets are not read.
func parseItemVarStore(data []byte, off int) (*itemVarStore, error) {
	lr := reader{b: data, pos: off}
	length := int(lr.u16())
	if lr.err != nil {
		return nil, fmt.Errorf("opentype: cff2 vstore length: %w", lr.err)
	}
	start := lr.pos
	if start+length > len(data) {
		return nil, fmt.Errorf("opentype: cff2 vstore out of range")
	}
	ivs := data[start : start+length]

	r := reader{b: ivs}
	if format := r.u16(); format != 1 {
		return nil, fmt.Errorf("opentype: cff2 vstore: unsupported format %d", format)
	}
	regionListOff := int(r.u32())
	ivdCount := int(r.u16())
	ivdOffsets := make([]int, ivdCount)
	for i := range ivdOffsets {
		ivdOffsets[i] = int(r.u32())
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: cff2 vstore header: %w", r.err)
	}

	rr := reader{b: ivs, pos: regionListOff}
	axisCount := int(rr.u16())
	regionCount := int(rr.u16())
	regions := make([][]regionAxis, regionCount)
	for i := range regions {
		axes := make([]regionAxis, axisCount)
		for a := range axes {
			axes[a].start = rr.f2dot14()
			axes[a].peak = rr.f2dot14()
			axes[a].end = rr.f2dot14()
		}
		regions[i] = axes
	}
	if rr.err != nil {
		return nil, fmt.Errorf("opentype: cff2 vstore regions: %w", rr.err)
	}

	datas := make([]ivData, ivdCount)
	for i, o := range ivdOffsets {
		dr := reader{b: ivs, pos: o}
		dr.u16() // itemCount (delta sets are not consumed by blend)
		dr.u16() // wordDeltaCount
		regionIndexCount := int(dr.u16())
		idx := make([]int, regionIndexCount)
		for k := range idx {
			ri := int(dr.u16())
			if ri >= regionCount {
				return nil, fmt.Errorf("opentype: cff2 vstore: region index %d out of range", ri)
			}
			idx[k] = ri
		}
		if dr.err != nil {
			return nil, fmt.Errorf("opentype: cff2 vstore data %d: %w", i, dr.err)
		}
		datas[i] = ivData{regionIndices: idx}
	}
	return &itemVarStore{axisCount: axisCount, regions: regions, data: datas}, nil
}

// regionScalar returns how strongly region ri applies at the normalized
// coordinate coords, reusing the shared tent-function evaluator (gvar.go).
func (vs *itemVarStore) regionScalar(ri int, coords []float64) float64 {
	reg := vs.regions[ri]
	lower := make([]float64, len(reg))
	peak := make([]float64, len(reg))
	upper := make([]float64, len(reg))
	for a, ax := range reg {
		lower[a], peak[a], upper[a] = ax.start, ax.peak, ax.end
	}
	return supportScalar(coords, lower, peak, upper)
}

// parseCFF2FDArray decodes the FDArray (an INDEX of Font DICTs) and each Font
// DICT's Private DICT, returning the per-Font-DICT Local Subr INDEXes, their
// biases and their default vsindex values. A CFF2 table without an FDArray is
// treated as having a single Font DICT with no Local Subrs.
func parseCFF2FDArray(data []byte, top map[int][]float64) (subrs [][][]byte, bias, vsindex []int, hints []*cffPrivate, err error) {
	off, ok := dictInt(top, 1236, 0)
	if !ok {
		return [][][]byte{nil}, []int{subrBias(0)}, []int{0}, []*cffPrivate{nil}, nil
	}
	if off < 0 || off > len(data) {
		return nil, nil, nil, nil, fmt.Errorf("opentype: cff2: FDArray offset out of range")
	}
	fontDicts, err := parseIndex2(&reader{b: data, pos: off})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(fontDicts) == 0 {
		return nil, nil, nil, nil, fmt.Errorf("opentype: cff2: empty FDArray")
	}
	subrs = make([][][]byte, len(fontDicts))
	bias = make([]int, len(fontDicts))
	vsindex = make([]int, len(fontDicts))
	hints = make([]*cffPrivate, len(fontDicts))
	for i, fd := range fontDicts {
		d, err := parseDict2(fd)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		ls, vs, h, err := parseCFF2Private(data, d)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		subrs[i] = ls
		bias[i] = subrBias(len(ls))
		vsindex[i] = vs
		hints[i] = h
	}
	return subrs, bias, vsindex, hints, nil
}

// parseCFF2Private resolves a Font DICT's Private DICT and, if present, its
// Local Subr INDEX and default vsindex, and parses its hint parameters (blue
// zones, standard/snap stem widths) for the grid-fitter (cffhint.go).
func parseCFF2Private(data []byte, fd map[int][]float64) ([][]byte, int, *cffPrivate, error) {
	pv, ok := fd[18]
	if !ok {
		return nil, 0, nil, nil
	}
	if len(pv) < 2 {
		return nil, 0, nil, fmt.Errorf("opentype: cff2: bad Private DICT entry")
	}
	psize, poff := int(pv[0]), int(pv[1])
	if psize < 0 || poff < 0 || poff+psize > len(data) {
		return nil, 0, nil, fmt.Errorf("opentype: cff2: Private DICT out of range")
	}
	priv, err := parseDict2(data[poff : poff+psize])
	if err != nil {
		return nil, 0, nil, err
	}
	hints := parsePrivateHints(priv)
	vsindex := 0
	if v, ok := dictInt(priv, 22, 0); ok {
		vsindex = v
	}
	soff, ok := dictInt(priv, 19, 0)
	if !ok {
		return nil, vsindex, hints, nil
	}
	soff += poff
	if soff < 0 || soff > len(data) {
		return nil, 0, nil, fmt.Errorf("opentype: cff2: Local Subrs offset out of range")
	}
	ls, err := parseIndex2(&reader{b: data, pos: soff})
	if err != nil {
		return nil, 0, nil, err
	}
	return ls, vsindex, hints, nil
}

// parseCFF2FDSelect decodes the FDSelect (glyph -> Font DICT index) in format 0
// or 3. A table without an FDSelect returns nil (every glyph uses Font DICT 0).
func parseCFF2FDSelect(data []byte, top map[int][]float64, numGlyphs, numFDs int) ([]int, error) {
	off, ok := dictInt(top, 1237, 0)
	if !ok {
		return nil, nil
	}
	if off < 0 || off >= len(data) {
		return nil, fmt.Errorf("opentype: cff2: FDSelect offset out of range")
	}
	r := reader{b: data, pos: off}
	format := r.u8()
	sel := make([]int, numGlyphs)
	switch format {
	case 0:
		for g := 0; g < numGlyphs; g++ {
			fd := int(r.u8())
			if fd >= numFDs {
				return nil, fmt.Errorf("opentype: cff2: FDSelect fd %d out of range", fd)
			}
			sel[g] = fd
		}
		if r.err != nil {
			return nil, fmt.Errorf("opentype: cff2 FDSelect: %w", r.err)
		}
	case 3:
		nRanges := int(r.u16())
		firsts := make([]int, nRanges)
		fds := make([]int, nRanges)
		for i := 0; i < nRanges; i++ {
			firsts[i] = int(r.u16())
			fds[i] = int(r.u8())
		}
		sentinel := int(r.u16())
		if r.err != nil {
			return nil, fmt.Errorf("opentype: cff2 FDSelect: %w", r.err)
		}
		for i := 0; i < nRanges; i++ {
			if fds[i] >= numFDs {
				return nil, fmt.Errorf("opentype: cff2: FDSelect fd %d out of range", fds[i])
			}
			next := sentinel
			if i+1 < nRanges {
				next = firsts[i+1]
			}
			for g := firsts[i]; g < next && g < numGlyphs; g++ {
				sel[g] = fds[i]
			}
		}
	default:
		return nil, fmt.Errorf("opentype: cff2: unsupported FDSelect format %d", format)
	}
	return sel, nil
}

// cff2machine interprets one glyph's CFF2 charstring. It borrows a t2machine as
// its pen (pen position, contour under construction, finished contours and the
// Bézier-flattening path operators) and adds the CFF2-only variation state: the
// normalized coordinate, the active variation-store index and the blend vector
// (the per-region scalars, with a leading 1 for the default master).
type cff2machine struct {
	c          *cff2Table
	tm         *t2machine
	localSubrs [][]byte
	localBias  int
	coords     []float64
	vsindex    int
	bv         []float64
}

// outline interprets glyph gid's CFF2 charstring at the normalized variation
// coordinate coords (F2Dot14 int16 per axis, as produced by NormalizeCoords);
// a nil or short coords vector leaves the remaining axes at their default. The
// result is the instanced outline as contours in font units, all points
// on-curve with cubics pre-flattened.
func (c *cff2Table) outline(gid int, coords []int16) ([]contour, error) {
	cs, _, err := c.outlineHints(gid, coords)
	return cs, err
}

// outlineHints is outline plus the hinting data (recorded stems, per-hintmask
// activation and the glyph's Font-DICT Private-DICT hint parameters) that
// cffhint.go's grid-fitter consumes. The outline is still instanced at coords.
func (c *cff2Table) outlineHints(gid int, coords []int16) ([]contour, *cffGlyphHints, error) {
	if gid < 0 || gid >= len(c.charStrings) {
		return nil, nil, fmt.Errorf("opentype: cff2 glyph %d out of range", gid)
	}
	fd := 0
	if c.fdSelect != nil {
		fd = c.fdSelect[gid]
	}
	axisCount := 0
	if c.vstore != nil {
		axisCount = c.vstore.axisCount
	}
	m := &cff2machine{
		c:          c,
		tm:         &t2machine{},
		localSubrs: c.localSubrs[fd],
		localBias:  c.localBias[fd],
		coords:     make([]float64, axisCount),
		vsindex:    c.privVsindex[fd],
	}
	for a := 0; a < axisCount && a < len(coords); a++ {
		m.coords[a] = float64(coords[a]) / 16384.0
	}
	if err := m.computeBV(); err != nil {
		return nil, nil, err
	}
	if err := m.run(c.charStrings[gid], 0); err != nil {
		return nil, nil, err
	}
	m.tm.finishContour()
	gh := &cffGlyphHints{
		stems:      m.tm.stemHints,
		hintMasks:  m.tm.hintMasks,
		cntrGroups: m.tm.cntrGroups,
		flexRanges: m.tm.flexRanges,
		priv:       c.privHints[fd],
	}
	return m.tm.contours, gh, nil
}

// computeBV rebuilds the blend vector for the active vsindex: a leading 1 (the
// default master's scalar) followed by one scalar per region of the selected
// ItemVariationData. With no variation store the vector is just [1], so blend
// becomes a no-op that leaves its default operands unchanged.
func (m *cff2machine) computeBV() error {
	m.bv = append(m.bv[:0], 1)
	vs := m.c.vstore
	if vs == nil {
		return nil
	}
	if m.vsindex < 0 || m.vsindex >= len(vs.data) {
		return fmt.Errorf("opentype: cff2: vsindex %d out of range", m.vsindex)
	}
	for _, ri := range vs.data[m.vsindex].regionIndices {
		m.bv = append(m.bv, vs.regionScalar(ri, m.coords))
	}
	return nil
}

// push appends v to the operand stack, guarding the CFF2 stack-depth limit.
func (m *cff2machine) push(v float64) error {
	if len(m.tm.stack) >= maxCFF2Stack {
		return fmt.Errorf("opentype: cff2: charstring stack overflow")
	}
	m.tm.stack = append(m.tm.stack, v)
	return nil
}

// run executes a charstring (or subroutine) body; depth guards recursion. It
// returns when the code is exhausted or a 'return' operator ends a subroutine.
func (m *cff2machine) run(code []byte, depth int) error {
	if depth > maxT2Depth {
		return fmt.Errorf("opentype: cff2: subroutine recursion too deep")
	}
	i := 0
	for i < len(code) {
		b0 := code[i]
		i++
		if b0 >= 32 || b0 == 28 {
			v, n, err := m.tm.operand(code, i-1)
			if err != nil {
				return err
			}
			if err := m.push(v); err != nil {
				return err
			}
			i += n - 1
			continue
		}
		ni, ret, err := m.operator(b0, code, i, depth)
		if err != nil {
			return err
		}
		i = ni
		if ret {
			return nil
		}
	}
	return nil
}

// operator executes one CFF2 operator byte b0. code/i give the stream position
// just past b0 (for hintmask bytes and the escape second byte); it returns the
// updated stream index, whether the current subroutine should stop ('return'),
// and any error. Unlike Type2 there is no leading-width handling.
func (m *cff2machine) operator(b0 byte, code []byte, i, depth int) (int, bool, error) {
	tm := m.tm
	switch b0 {
	case 1, 3, 18, 23: // hstem, vstem, hstemhm, vstemhm
		tm.recordStems(b0 == 1 || b0 == 18)
	case 19, 20: // hintmask, cntrmask
		tm.recordStems(false) // any operands here are implicit vstem hints
		ni, herr := tm.readHintMask(code, i, b0 == 19)
		if herr != nil {
			return i, false, herr
		}
		i = ni
	case 21: // rmoveto
		if len(tm.stack) < 2 {
			return i, false, fmt.Errorf("opentype: cff2: rmoveto needs 2 args")
		}
		n := len(tm.stack)
		tm.moveTo(tm.stack[n-2], tm.stack[n-1])
		tm.stack = tm.stack[:0]
	case 22: // hmoveto
		if len(tm.stack) < 1 {
			return i, false, fmt.Errorf("opentype: cff2: hmoveto needs 1 arg")
		}
		tm.moveTo(tm.stack[len(tm.stack)-1], 0)
		tm.stack = tm.stack[:0]
	case 4: // vmoveto
		if len(tm.stack) < 1 {
			return i, false, fmt.Errorf("opentype: cff2: vmoveto needs 1 arg")
		}
		tm.moveTo(0, tm.stack[len(tm.stack)-1])
		tm.stack = tm.stack[:0]
	case 5: // rlineto
		s := tm.stack
		for j := 0; j+2 <= len(s); j += 2 {
			tm.lineTo(s[j], s[j+1])
		}
		tm.stack = tm.stack[:0]
	case 6: // hlineto
		tm.altLine(true)
	case 7: // vlineto
		tm.altLine(false)
	case 8: // rrcurveto
		tm.rrcurveto(tm.stack)
		tm.stack = tm.stack[:0]
	case 24: // rcurveline
		tm.rcurveline(tm.stack)
		tm.stack = tm.stack[:0]
	case 25: // rlinecurve
		tm.rlinecurve(tm.stack)
		tm.stack = tm.stack[:0]
	case 26: // vvcurveto
		tm.vvcurveto(tm.stack)
		tm.stack = tm.stack[:0]
	case 27: // hhcurveto
		tm.hhcurveto(tm.stack)
		tm.stack = tm.stack[:0]
	case 30: // vhcurveto
		tm.altCurve(tm.stack, false)
		tm.stack = tm.stack[:0]
	case 31: // hvcurveto
		tm.altCurve(tm.stack, true)
		tm.stack = tm.stack[:0]
	case 10: // callsubr
		if err := m.callSubr(m.localSubrs, m.localBias, depth); err != nil {
			return i, false, err
		}
	case 29: // callgsubr
		if err := m.callSubr(m.c.globalSubrs, m.c.gsubrBias, depth); err != nil {
			return i, false, err
		}
	case 11: // return
		return i, true, nil
	case 15: // vsindex
		if err := m.vsindexOp(); err != nil {
			return i, false, err
		}
	case 16: // blend
		if err := m.blendOp(); err != nil {
			return i, false, err
		}
	case 12: // escape: two-byte operator (flex family)
		if i >= len(code) {
			return i, false, fmt.Errorf("opentype: cff2 charstring: truncated escape")
		}
		b1 := code[i]
		i++
		if err := tm.escape(b1); err != nil {
			return i, false, err
		}
	default:
		return i, false, fmt.Errorf("opentype: cff2: unknown operator %d", b0)
	}
	return i, false, nil
}

// callSubr executes the subroutine selected by the top-of-stack index (plus the
// bias) from subrs. A 'return' inside the subroutine ends only that subroutine.
func (m *cff2machine) callSubr(subrs [][]byte, bias, depth int) error {
	st := m.tm.stack
	if len(st) < 1 {
		return fmt.Errorf("opentype: cff2: callsubr needs an index")
	}
	idx := int(st[len(st)-1]) + bias
	m.tm.stack = st[:len(st)-1]
	if idx < 0 || idx >= len(subrs) {
		return fmt.Errorf("opentype: cff2: subr index %d out of range", idx)
	}
	return m.run(subrs[idx], depth+1)
}

// vsindexOp handles the vsindex operator: it selects the ItemVariationData that
// subsequent blends draw from and recomputes the blend vector.
func (m *cff2machine) vsindexOp() error {
	st := m.tm.stack
	if len(st) < 1 {
		return fmt.Errorf("opentype: cff2: vsindex needs 1 arg")
	}
	m.vsindex = int(st[len(st)-1])
	m.tm.stack = st[:0]
	return m.computeBV()
}

// blendOp handles the blend operator: the top operand n gives the number of
// resulting values; below it are n default values followed by n groups of k
// region deltas (k = regions of the active vsindex). Each default value is
// replaced in place by default + Σ bv[region]·delta, and the deltas and n are
// popped, leaving the n blended values as operands for the following operator.
func (m *cff2machine) blendOp() error {
	st := m.tm.stack
	if len(st) < 1 {
		return fmt.Errorf("opentype: cff2: blend needs a count")
	}
	numBlends := int(st[len(st)-1])
	st = st[:len(st)-1]
	if numBlends < 0 {
		return fmt.Errorf("opentype: cff2: blend count %d invalid", numBlends)
	}
	k := len(m.bv) - 1
	need := numBlends * (k + 1)
	if need > len(st) {
		return fmt.Errorf("opentype: cff2: blend needs %d operands, have %d", need, len(st))
	}
	base := len(st) - need
	di := base + numBlends
	for vi := 0; vi < numBlends; vi++ {
		sum := st[base+vi]
		for j := 1; j <= k; j++ {
			sum += m.bv[j] * st[di]
			di++
		}
		st[base+vi] = sum
	}
	m.tm.stack = st[:base+numBlends]
	return nil
}
