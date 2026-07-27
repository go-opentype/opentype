// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"fmt"
	"math"
)

// This file implements the 'fvar' (Font Variations) table: the declaration of
// a variable font's design-variation axes and its named instances. Together
// with 'avar' (avar.go) and 'gvar' (gvar.go) it lets a variable font be
// instantiated at a chosen point in its design space instead of only at its
// default master.
//
// The three tables are parsed as optional add-ons: a font without them is a
// plain static font and is unaffected. Deferred (not implemented here): metric
// variations (HVAR/VVAR/MVAR), style-attribute variations (STAT) and CFF2
// outline variations. Only 'glyf' outline variation is supported.

// Axis is one design-variation axis of a variable font, decoded from the
// 'fvar' table. Min, Default and Max are user-space coordinates (for example
// 100, 400 and 900 for a typical weight axis).
type Axis struct {
	Tag     string  // four-character axis tag, e.g. "wght", "wdth", "slnt"
	Min     float64 // minimum user coordinate
	Default float64 // default user coordinate (the font's default master)
	Max     float64 // maximum user coordinate
	Flags   uint16  // axis flags (bit 0: hidden axis)
	NameID  uint16  // 'name' table entry describing the axis
}

// NamedInstance is a named position in the variation space (for example
// "Bold" or "Condensed"), decoded from the 'fvar' table.
type NamedInstance struct {
	SubfamilyNameID  uint16             // 'name' entry for the instance subfamily
	Flags            uint16             // instance flags (reserved)
	Coordinates      map[string]float64 // axis tag -> user coordinate
	PostScriptNameID uint16             // 'name' entry for the PostScript name, 0 if absent
}

// fvarTable is the parsed 'fvar' table.
type fvarTable struct {
	axes      []Axis
	instances []NamedInstance
}

// parseVariations parses the optional variation tables. Each is independent:
// any subset (or none) may be present. A malformed table is reported as an
// error so a corrupt font fails cleanly rather than silently mis-rendering.
func (f *Font) parseVariations(tables map[string][]byte) error {
	if b, ok := tables["fvar"]; ok {
		if err := f.parseFvar(b); err != nil {
			return err
		}
	}
	if b, ok := tables["avar"]; ok {
		if err := f.parseAvar(b); err != nil {
			return err
		}
	}
	if b, ok := tables["gvar"]; ok {
		if err := f.parseGvar(b); err != nil {
			return err
		}
	}
	return nil
}

// readTag reads a four-byte OpenType tag as a string.
func readTag(r *reader) string {
	if r.pos+4 > len(r.b) {
		r.err = errTruncated
		return ""
	}
	s := string(r.b[r.pos : r.pos+4])
	r.pos += 4
	return s
}

// readFixed reads a 16.16 signed fixed-point value as a float64.
func readFixed(r *reader) float64 {
	return float64(int32(r.u32())) / 65536.0
}

// parseFvar decodes the 'fvar' table: a header, an array of axis records and
// an array of named-instance records. The header carries the offset to the
// axis array and the (forward-compatible) record sizes, which are honoured so
// that unknown trailing fields are skipped.
func (f *Font) parseFvar(b []byte) error {
	r := reader{b: b}
	r.u16() // majorVersion
	r.u16() // minorVersion
	axesOffset := int(r.u16())
	r.u16() // reserved
	axisCount := int(r.u16())
	axisSize := int(r.u16())
	instanceCount := int(r.u16())
	instanceSize := int(r.u16())
	if r.err != nil {
		return fmt.Errorf("opentype: fvar table: %w", r.err)
	}

	axes := make([]Axis, axisCount)
	for i := range axes {
		ar := reader{b: b, pos: axesOffset + i*axisSize}
		tag := readTag(&ar)
		min := readFixed(&ar)
		def := readFixed(&ar)
		max := readFixed(&ar)
		flags := ar.u16()
		nameID := ar.u16()
		if ar.err != nil {
			return fmt.Errorf("opentype: fvar axis %d: %w", i, ar.err)
		}
		axes[i] = Axis{Tag: tag, Min: min, Default: def, Max: max, Flags: flags, NameID: nameID}
	}

	instances := make([]NamedInstance, instanceCount)
	for i := range instances {
		ir := reader{b: b, pos: axesOffset + axisCount*axisSize + i*instanceSize}
		sub := ir.u16()
		flags := ir.u16()
		coords := make(map[string]float64, axisCount)
		for a := 0; a < axisCount; a++ {
			coords[axes[a].Tag] = readFixed(&ir)
		}
		var psID uint16
		if instanceSize >= axisCount*4+6 {
			psID = ir.u16()
		}
		if ir.err != nil {
			return fmt.Errorf("opentype: fvar instance %d: %w", i, ir.err)
		}
		instances[i] = NamedInstance{
			SubfamilyNameID:  sub,
			Flags:            flags,
			Coordinates:      coords,
			PostScriptNameID: psID,
		}
	}

	f.fvar = &fvarTable{axes: axes, instances: instances}
	return nil
}

// Axes returns the font's variation axes, or nil for a non-variable font.
func (f *Font) Axes() []Axis {
	if f.fvar == nil {
		return nil
	}
	return f.fvar.axes
}

// NamedInstances returns the font's named instances, or nil if the font is not
// variable or declares none.
func (f *Font) NamedInstances() []NamedInstance {
	if f.fvar == nil {
		return nil
	}
	return f.fvar.instances
}

// NormalizeCoords converts a set of user-space axis coordinates (keyed by axis
// tag) into per-axis normalized F2Dot14 values in the range [-1, 1], the form
// gvar and avar operate in. Axes not present in user take their default (which
// normalizes to 0). Values outside an axis's [min, max] are clamped. If the
// font declares an 'avar' table its segment maps are applied. The result has
// one entry per fvar axis, in axis order; it is nil for a non-variable font.
func (f *Font) NormalizeCoords(user map[string]float64) []int16 {
	if f.fvar == nil {
		return nil
	}
	axes := f.fvar.axes
	out := make([]int16, len(axes))
	for i, ax := range axes {
		v := ax.Default
		if u, ok := user[ax.Tag]; ok {
			v = u
		}
		if v < ax.Min {
			v = ax.Min
		}
		if v > ax.Max {
			v = ax.Max
		}

		// v is clamped to [min, max]; whenever v < default the axis has
		// default > min, and whenever v > default it has max > default, so the
		// divisors below are always positive. v == default yields n == 0.
		var n float64
		switch {
		case v < ax.Default:
			n = (v - ax.Default) / (ax.Default - ax.Min)
		case v > ax.Default:
			n = (v - ax.Default) / (ax.Max - ax.Default)
		}

		if f.avar != nil {
			n = f.avar.apply(i, n)
		}
		if n < -1 {
			n = -1
		}
		if n > 1 {
			n = 1
		}
		out[i] = int16(math.Round(n * 16384))
	}
	return out
}
