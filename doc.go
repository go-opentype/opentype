// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

// Package opentype is a pure-Go, CGO=0, standard-library-only parser and
// anti-aliased rasteriser for TrueType/OpenType fonts.
//
// It is a functional replacement for the narrow slice of
// golang.org/x/image/font/opentype needed by a glyph-blitting UI: parse a
// font blob, build a Face at a pixel size, then obtain per-rune advances and
// 8-bit alpha coverage masks.
//
// # Scope
//
// Phase 1 covers TrueType 'glyf' outlines: the sfnt container, the head,
// maxp, hhea, hmtx, cmap (formats 4 and 12), loca and glyf tables, simple and
// composite glyphs, implied on-curve points, and a non-zero-winding
// supersampling rasteriser. OpenType/CFF ('OTTO') outlines, GPOS/GSUB
// shaping, kerning and hinting are not implemented; see the README for the
// full support matrix.
//
// # Usage
//
//	f, err := opentype.Parse(ttf)
//	if err != nil { /* handle */ }
//	face := f.NewFace(16)
//	adv := face.Measure("Hello")
//	bounds, mask, maskp, advance, ok := face.GlyphMask('H', penX, baselineY)
//
// A Font is immutable after Parse and safe for concurrent use; a Face caches
// rasterised glyphs and is not safe for concurrent use.
package opentype
