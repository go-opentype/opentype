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
// The sfnt container, head, maxp, hhea, hmtx, cmap (formats 4 and 12), loca
// and glyf tables, simple and composite glyphs, implied on-curve points, and a
// non-zero-winding supersampling rasteriser. Beyond that: CFF and CFF2
// ('OTTO') Type 2 charstrings, variable fonts ('fvar'/'avar'/'gvar' and the
// CFF2 blends), GSUB/GPOS/GDEF shaping with the legacy 'kern' table as a
// fallback, and both hinters. See the README for the full support matrix, and
// [Face.ShapePositionedUnits] for the shaped-run entry point.
//
// ⛔⛔ THIS PARAGRAPH USED TO SAY THE OPPOSITE, AND A CONSUMER BELIEVED IT. It
// read "Phase 1 covers TrueType 'glyf' outlines … OpenType/CFF ('OTTO')
// outlines, GPOS/GSUB shaping, kerning and hinting are not implemented" long
// after each of those landed with its own tests — gsub.go, gpos.go, kern.go,
// shape.go, hint.go, cff.go, cff2.go, cffhint.go. The README's matrix said
// "done" at the same time, so the two halves of this repository's own
// documentation disagreed, and the stale half is the one a consumer reads
// first.
//
// What that cost, measured in go-tex/engine at its origin/main: the engine
// calls Shape(text, "liga") for ligatures and then obtains kerning ONE RUNE
// PAIR AT A TIME through KernUnits(prev, cur) — ShapePositioned and
// ShapePositionedUnits are never called, so contextual GPOS, cursive
// attachment and mark attachment are not missing there but UNASKED FOR. The
// doc comment on ShapePositionedUnits describes that engine's predicament in
// its own words; this paragraph is why it had no reason to look.
//
// doc_scope_test.go now fails if this paragraph calls a feature unimplemented
// while the file implementing it is in the tree.
//
// The OpenType MATH table (math-typesetting metrics: constants, per-glyph
// italic correction and kerning, and stretchy-glyph size variants and
// assemblies) is decoded and exposed pixel-scaled through a Face; see
// [Font.HasMath] and math.go. Math layout itself (box building, the TeX math
// rules) is a higher-level engine that consumes these values.
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
//
// # Embedding and subsetting
//
// For a consumer that embeds a font (a PDF or EPUB writer), the package exposes
// the primitives such an embedder otherwise re-implements by re-parsing the sfnt:
// the PDF FontDescriptor scalars ([Font.UnitsPerEm], [Font.FontBBox],
// [Font.CapHeight], [Font.XHeight], [Font.ItalicAngle], [Font.StemV],
// [Font.Flags], ...); raw table access ([Font.TableTags], [Font.Table]);
// by-glyph-index advances that honour the current variation
// ([Face.AdvanceIndex], [Face.AdvanceIndexUnits], [Face.VerticalAdvanceIndex]);
// TrueType glyph subsetting ([Font.SubsetTrueType]) and CFF charstring subsetting
// ([Font.SubsetCFF]); and baking a variable font at a chosen axis position into a
// static instance ([Font.Instance], [Font.InstanceBytes]).
package opentype
