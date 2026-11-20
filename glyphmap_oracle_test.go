// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "fmt"

// oracleCoverage and oracleClassDef are parseCoverage and parseClassDef as they
// were at e4de2be, before glyphMap: they expand every range into a map entry.
// They are the equality oracle of the glyphMap tests; nothing else may call them,
// since they are the unbounded amplification of issue #75.
func oracleCoverage(b []byte) (map[GlyphIndex]int, error) {
	r := reader{b: b}
	format := r.u16()
	m := map[GlyphIndex]int{}
	switch format {
	case 1:
		n := int(r.u16())
		for i := 0; i < n; i++ {
			m[GlyphIndex(r.u16())] = i
		}
	case 2:
		n := int(r.u16())
		for i := 0; i < n; i++ {
			start := int(r.u16())
			end := int(r.u16())
			startCov := int(r.u16())
			for g := start; g <= end; g++ {
				m[GlyphIndex(g)] = startCov + (g - start)
			}
		}
	default:
		return nil, fmt.Errorf("opentype: coverage format %d", format)
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: coverage: %w", r.err)
	}
	return m, nil
}

func oracleClassDef(b []byte) (map[GlyphIndex]int, error) {
	r := reader{b: b}
	format := r.u16()
	m := map[GlyphIndex]int{}
	switch format {
	case 1:
		start := int(r.u16())
		n := int(r.u16())
		for i := 0; i < n; i++ {
			m[GlyphIndex(start+i)] = int(r.u16())
		}
	case 2:
		n := int(r.u16())
		for i := 0; i < n; i++ {
			s := int(r.u16())
			e := int(r.u16())
			c := int(r.u16())
			for g := s; g <= e; g++ {
				m[GlyphIndex(g)] = c
			}
		}
	default:
		return nil, fmt.Errorf("opentype: classdef format %d", format)
	}
	if r.err != nil {
		return nil, fmt.Errorf("opentype: classdef: %w", r.err)
	}
	return m, nil
}
