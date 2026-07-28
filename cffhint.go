// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"math"
	"sort"
)

// This file grid-fits CFF/CFF2 (Type2) outlines the way FreeType's CFF hinter
// does: it aligns stem edges and blue-zone reference points to the pixel grid
// at a target ppem, then interpolates the rest of the outline between them. It
// is the CFF counterpart of the TrueType instruction interpreter (hint.go);
// where TrueType grid-fitting is imperative (a bytecode VM moves points), CFF
// grid-fitting is declarative — the font only states its stems (recorded by the
// Type2 interpreter in cff.go/cff2.go) and its Private-DICT hint parameters
// (blue zones, standard and snap stem widths, parsed here), and the renderer
// decides how to snap them.
//
// Pipeline (per glyph, per axis, independent x and y):
//  1. Scale every stem edge and blue-zone edge to device space (pixels/em).
//  2. Snap each active stem's width toward StdHW/StdVW / StemSnap and round it,
//     then position the stem so one edge lands on an integer pixel boundary,
//     anchoring horizontal (hstem) edges that fall in a blue zone to the rounded
//     zone position instead. This yields, per stem edge, an original device
//     position and its hinted (grid-fitted) position.
//  3. Build a piecewise-linear control map from those (original -> hinted)
//     edge pairs plus a blue-zone flat-edge anchor per zone, and remap every
//     outline point: a point on a control edge snaps with it, a point between
//     two edges interpolates proportionally, and a point outside the outermost
//     edges shifts rigidly with the nearest one. This is the same interpolation
//     TrueType uses for untouched points (IUP), applied here to the whole
//     outline.
//
// What is implemented: Private-DICT hint-parameter parsing (BlueValues,
// OtherBlues, FamilyBlues, FamilyOtherBlues, BlueScale, BlueShift, BlueFuzz,
// StdHW, StdVW, StemSnapH, StemSnapV); top/bottom blue-zone construction with
// overshoot, family-blue substitution and the BlueScale overshoot-suppression
// rule; stem-width snapping and edge grid-fitting; blue-zone alignment of stem
// edges and reference points; and per-subpath hintmask hint selection.
//
// Genuinely deferred sub-nuances (documented, not applied): counter-control
// (cntrmask) hint groups are parsed but their counters are not equalised;
// flex-region hints get no special treatment (flex curves are already flattened
// by the interpreter and follow the ordinary interpolation); a hintmask that
// changes mid-subpath is applied using the mask in effect at the subpath's
// first point; and stem-darkening (weight compensation) is not performed.

// cffStemHint is one resolved stem hint in font units: the two edge positions
// (min < max) and whether it is a horizontal stem (hstem, edges are Y
// positions, controlling horizontal features and blue alignment) or a vertical
// stem (vstem, edges are X positions).
type cffStemHint struct {
	horizontal bool
	min, max   float64
}

// cffHintMask is one recorded hintmask activation: the outline-point index at
// which it takes effect (so per-subpath hint selection can find the mask in
// force for a given point) and, per declared stem, whether that stem is active.
type cffHintMask struct {
	pointIndex int
	active     []bool
}

// cffGlyphHints bundles a glyph's recorded stems and hintmask activations with
// the font's parsed Private-DICT hint parameters, as returned by the CFF/CFF2
// interpreters alongside the outline.
type cffGlyphHints struct {
	stems     []cffStemHint
	hintMasks []cffHintMask
	priv      *cffPrivate
}

// activeMaskAt returns the per-stem activation of the hintmask in force at the
// given outline-point index — the last hintmask whose pointIndex does not
// exceed it. A nil result means "all stems active" (no hintmask applies yet, or
// the glyph declared none), which the grid-fitter treats as every stem on.
func (gh *cffGlyphHints) activeMaskAt(pointIndex int) []bool {
	var active []bool
	for _, hm := range gh.hintMasks {
		if hm.pointIndex <= pointIndex {
			active = hm.active
		}
	}
	return active
}

// cffPrivate holds the Private-DICT hint parameters that drive grid-fitting.
// Blue arrays are stored already accumulated (the DICT delta encoding undone)
// and in font units; BlueScale/BlueShift/BlueFuzz carry their CFF defaults when
// the DICT omits them.
type cffPrivate struct {
	blueValues       []float64 // paired: [0:2] baseline (bottom) zone, rest top zones
	otherBlues       []float64 // paired: additional bottom zones
	familyBlues      []float64 // paired: family baseline+top zones (consistency)
	familyOtherBlues []float64 // paired: family bottom zones
	blueScale        float64   // overshoot-suppression threshold (default 0.039625)
	blueShift        float64   // minimum overshoot before it is shown (default 7)
	blueFuzz         float64   // blue-zone capture extension (default 1)
	stdHW            float64   // standard horizontal-stem width (hstem)
	stdVW            float64   // standard vertical-stem width (vstem)
	stemSnapH        []float64 // horizontal-stem snap widths
	stemSnapV        []float64 // vertical-stem snap widths
}

// CFF Private-DICT defaults for the overshoot-control parameters (CFF spec).
const (
	defaultBlueScale = 0.039625
	defaultBlueShift = 7
	defaultBlueFuzz  = 1
)

// snapTolerancePx is how close (in device pixels) a stem's scaled width must be
// to a standard/snap width for the width to be rendered as that standard width
// (unifying near-equal stems) before rounding to whole pixels.
const snapTolerancePx = 0.5

// parsePrivateHints extracts the hint parameters from an already-decoded
// Private DICT (CFF or CFF2 share the operator codes). Absent overshoot-control
// operators take their CFF defaults; the delta-encoded blue and stem-snap
// arrays are accumulated into absolute font-unit values.
func parsePrivateHints(d map[int][]float64) *cffPrivate {
	p := &cffPrivate{
		blueScale: defaultBlueScale,
		blueShift: defaultBlueShift,
		blueFuzz:  defaultBlueFuzz,
	}
	p.blueValues = accumDelta(d[6])
	p.otherBlues = accumDelta(d[7])
	p.familyBlues = accumDelta(d[8])
	p.familyOtherBlues = accumDelta(d[9])
	if v, ok := dictFloat(d, 10); ok {
		p.stdHW = v
	}
	if v, ok := dictFloat(d, 11); ok {
		p.stdVW = v
	}
	if v, ok := dictFloat(d, 1209); ok {
		p.blueScale = v
	}
	if v, ok := dictFloat(d, 1210); ok {
		p.blueShift = v
	}
	if v, ok := dictFloat(d, 1211); ok {
		p.blueFuzz = v
	}
	p.stemSnapH = accumDelta(d[1212])
	p.stemSnapV = accumDelta(d[1213])
	return p
}

// dictFloat returns the first operand of operator op in d as a float, or
// ok=false when the operator is absent or carries no operand.
func dictFloat(d map[int][]float64, op int) (float64, bool) {
	v, ok := d[op]
	if !ok || len(v) == 0 {
		return 0, false
	}
	return v[0], true
}

// accumDelta undoes the CFF DICT "delta" encoding: the stored operands are
// successive differences, so the absolute values are their running sum. A nil
// or empty input yields nil.
func accumDelta(v []float64) []float64 {
	if len(v) == 0 {
		return nil
	}
	out := make([]float64, len(v))
	sum := 0.0
	for i, d := range v {
		sum += d
		out[i] = sum
	}
	return out
}

// blueZone is one alignment zone in font units: flat is the reference edge that
// snaps to the grid (the baseline, x-height, cap height, …); over is the
// overshoot extent (below flat for a bottom zone, above for a top zone) where
// round glyph features spill past the flat edge.
type blueZone struct {
	flat float64
	over float64
}

// makeZone builds a zone from a (a, b) blue pair. A top zone's flat edge is the
// lower value with overshoot above; a bottom zone's flat edge is the upper
// value with overshoot below.
func makeZone(a, b float64, isTop bool) blueZone {
	lo, hi := math.Min(a, b), math.Max(a, b)
	if isTop {
		return blueZone{flat: lo, over: hi}
	}
	return blueZone{flat: hi, over: lo}
}

// applyFamily substitutes the corresponding FamilyBlues flat edge for a zone's
// flat edge when the two are within one device pixel, so members of a type
// family align identically at small sizes (the CFF FamilyBlues rule). fam is
// the family array paired the same way as the primary array; k is the pair
// index.
func applyFamily(z *blueZone, fam []float64, k int, isTop bool, scale float64) {
	if k+1 >= len(fam) {
		return
	}
	fz := makeZone(fam[k], fam[k+1], isTop)
	if math.Abs((fz.flat-z.flat)*scale) < 1 {
		z.flat = fz.flat
	}
}

// buildBlueZones assembles the blue zones from the Private-DICT arrays: the
// first BlueValues pair is the baseline (a bottom zone), the remaining
// BlueValues pairs are top zones, and every OtherBlues pair is a bottom zone.
// Family blues substitute for consistency (applyFamily).
func buildBlueZones(p *cffPrivate, scale float64) []blueZone {
	var zones []blueZone
	bv := p.blueValues
	for k := 0; k+1 < len(bv); k += 2 {
		isTop := k >= 2
		z := makeZone(bv[k], bv[k+1], isTop)
		applyFamily(&z, p.familyBlues, k, isTop, scale)
		zones = append(zones, z)
	}
	ob := p.otherBlues
	for k := 0; k+1 < len(ob); k += 2 {
		z := makeZone(ob[k], ob[k+1], false)
		applyFamily(&z, p.familyOtherBlues, k, false, scale)
		zones = append(zones, z)
	}
	return zones
}

// capture reports whether edgeDev (a device-space stem edge) falls in this zone
// and, if so, the device position it should be aligned to. Within BlueFuzz of
// the zone the edge is captured; it snaps to the rounded flat edge unless
// overshoot is enabled (not suppressed) and the edge lies at least BlueShift
// beyond the flat edge, in which case a rounded overshoot of at least one pixel
// is preserved.
func (z blueZone) capture(edgeDev, scale, blueShift, blueFuzz float64, suppress bool) (float64, bool) {
	flatDev := z.flat * scale
	overDev := z.over * scale
	lo, hi := math.Min(flatDev, overDev), math.Max(flatDev, overDev)
	fuzz := blueFuzz * scale
	if edgeDev < lo-fuzz || edgeDev > hi+fuzz {
		return 0, false
	}
	base := math.Round(flatDev)
	if suppress {
		return base, true
	}
	over := edgeDev - flatDev
	if math.Abs(over) < blueShift*scale {
		return base, true
	}
	o := math.Round(math.Abs(over))
	if o < 1 {
		o = 1
	}
	if over < 0 {
		o = -o
	}
	return base + o, true
}

// hintedStem is one stem after grid-fitting: the original device-space edge
// positions (oLo < oHi) and their hinted (snapped) positions.
type hintedStem struct {
	oLo, oHi float64
	hLo, hHi float64
}

// hintStem grid-fits one stem: its width is snapped toward the standard/snap
// widths and rounded; for a horizontal stem an edge that lands in a blue zone
// is anchored to the rounded zone position (and the opposite edge placed one
// snapped width away), otherwise the lower edge is rounded to the pixel grid.
func hintStem(s cffStemHint, scale float64, p *cffPrivate, blues []blueZone, suppress bool) hintedStem {
	lo := math.Min(s.min, s.max) * scale
	hi := math.Max(s.min, s.max) * scale
	std, snaps := p.stdVW, p.stemSnapV
	if s.horizontal {
		std, snaps = p.stdHW, p.stemSnapH
	}
	w := snapWidth(hi-lo, std*scale, snaps, scale)
	if s.horizontal {
		if t, ok := captureEdge(hi, blues, p, scale, suppress); ok {
			return hintedStem{oLo: lo, oHi: hi, hLo: t - w, hHi: t}
		}
		if t, ok := captureEdge(lo, blues, p, scale, suppress); ok {
			return hintedStem{oLo: lo, oHi: hi, hLo: t, hHi: t + w}
		}
	}
	hLo := math.Round(lo)
	return hintedStem{oLo: lo, oHi: hi, hLo: hLo, hHi: hLo + w}
}

// captureEdge returns the aligned device position for a horizontal-stem edge
// that falls in one of the blue zones, testing each zone in turn.
func captureEdge(edgeDev float64, blues []blueZone, p *cffPrivate, scale float64, suppress bool) (float64, bool) {
	for _, z := range blues {
		if t, ok := z.capture(edgeDev, scale, p.blueShift, p.blueFuzz, suppress); ok {
			return t, true
		}
	}
	return 0, false
}

// snapWidth rounds a scaled stem width to whole pixels, first snapping it to the
// nearest standard or snap width (std and snaps are in font units, scaled here)
// when within snapTolerancePx so near-equal stems render at one consistent
// width. The result is at least one pixel.
func snapWidth(w, stdDev float64, snaps []float64, scale float64) float64 {
	best, bestD := w, math.Inf(1)
	consider := func(v float64) {
		if v <= 0 {
			return
		}
		if d := math.Abs(w - v); d < bestD {
			best, bestD = v, d
		}
	}
	consider(stdDev)
	for _, s := range snaps {
		consider(s * scale)
	}
	if bestD > snapTolerancePx {
		best = w
	}
	r := math.Round(best)
	if r < 1 {
		r = 1
	}
	return r
}

// controlMap is a piecewise-linear 1-D remap built from (original -> hinted)
// device positions, sorted and deduplicated by original position.
type controlMap struct {
	orig []float64
	hint []float64
}

// add records one control point (an original device position and where it is
// hinted to).
func (cm *controlMap) add(o, h float64) {
	cm.orig = append(cm.orig, o)
	cm.hint = append(cm.hint, h)
}

// finalize sorts the control points by original position and merges points that
// share an original position (averaging their hinted targets), leaving a
// strictly increasing orig sequence for interpolation.
func (cm *controlMap) finalize() {
	n := len(cm.orig)
	if n == 0 {
		return
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return cm.orig[idx[a]] < cm.orig[idx[b]] })
	var os, hs []float64
	for _, i := range idx {
		o, h := cm.orig[i], cm.hint[i]
		if len(os) > 0 && o == os[len(os)-1] {
			// Same original edge from two hints: average the targets and keep one.
			hs[len(hs)-1] = (hs[len(hs)-1] + h) / 2
			continue
		}
		os = append(os, o)
		hs = append(hs, h)
	}
	cm.orig, cm.hint = os, hs
}

// remap maps a device coordinate through the control map: below the first
// control point or above the last it shifts by that endpoint's delta; between
// two it interpolates linearly. An empty map is the identity.
func (cm *controlMap) remap(v float64) float64 {
	n := len(cm.orig)
	switch {
	case n == 0:
		return v
	case v <= cm.orig[0]:
		return v + (cm.hint[0] - cm.orig[0])
	case v >= cm.orig[n-1]:
		return v + (cm.hint[n-1] - cm.orig[n-1])
	}
	i := sort.Search(n, func(k int) bool { return cm.orig[k] >= v })
	a, b := cm.orig[i-1], cm.orig[i]
	ha, hb := cm.hint[i-1], cm.hint[i]
	t := (v - a) / (b - a)
	return ha + t*(hb-ha)
}

// cffGridFit grid-fits contours (in font units) using the recorded stems and
// Private-DICT hint parameters, returning the hinted contours in font units.
// scale is font-units-to-pixels and ppem is pixels/em (the BlueScale rule). It
// is a no-op returning equivalent geometry when there are no stems and no blue
// zones.
func cffGridFit(contours []contour, gh *cffGlyphHints, scale, ppem float64) []contour {
	priv := gh.priv
	if priv == nil {
		priv = &cffPrivate{blueScale: defaultBlueScale, blueShift: defaultBlueShift, blueFuzz: defaultBlueFuzz}
	}
	suppress := ppem*priv.blueScale < 1
	blues := buildBlueZones(priv, scale)

	// Grid-fit every stem once (its hinted position is independent of subpath).
	hs := make([]hintedStem, len(gh.stems))
	for i, s := range gh.stems {
		hs[i] = hintStem(s, scale, priv, blues, suppress)
	}

	out := make([]contour, len(contours))
	start := 0
	for ci, c := range contours {
		active := gh.activeMaskAt(start)
		xm, ym := buildMaps(hs, gh.stems, active, blues, scale)
		nc := make(contour, len(c))
		for j, p := range c {
			hx := xm.remap(p.x * scale)
			hy := ym.remap(p.y * scale)
			nc[j] = outlinePoint{x: hx / scale, y: hy / scale, on: p.on}
		}
		out[ci] = nc
		start += len(c)
	}
	return out
}

// buildMaps assembles the x- and y-axis control maps for one subpath: each
// active vertical stem contributes its two edges to the x map and each active
// horizontal stem to the y map, and every blue zone contributes a flat-edge
// anchor to the y map (blue zones are always active). A nil active mask means
// every stem is active.
func buildMaps(hs []hintedStem, stems []cffStemHint, active []bool, blues []blueZone, scale float64) (x, y controlMap) {
	for i, st := range hs {
		if active != nil && !active[i] {
			continue
		}
		if stems[i].horizontal {
			y.add(st.oLo, st.hLo)
			y.add(st.oHi, st.hHi)
		} else {
			x.add(st.oLo, st.hLo)
			x.add(st.oHi, st.hHi)
		}
	}
	for _, z := range blues {
		f := z.flat * scale
		y.add(f, math.Round(f))
	}
	x.finalize()
	y.finalize()
	return x, y
}
