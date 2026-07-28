// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype_test

import (
	"fmt"
	"os"

	"github.com/go-opentype/opentype"
)

// loadTestFont reads the CFF/OpenType font bundled under testdata for the
// examples below (Adobe Source Serif 4, SIL Open Font License; see
// testdata/OFL.txt).
func loadTestFont() *opentype.Font {
	b, err := os.ReadFile("testdata/SourceSerif4-Regular.otf")
	if err != nil {
		panic(err)
	}
	f, err := opentype.Parse(b)
	if err != nil {
		panic(err)
	}
	return f
}

// ExampleParse parses a font blob and reports its glyph count.
func ExampleParse() {
	f := loadTestFont()
	fmt.Println(f.NumGlyphs())
	// Output: 1464
}

// ExampleFont_NewFace builds a sized Face and measures a string, mirroring
// the narrow slice of golang.org/x/image/font/opentype this package
// replaces.
func ExampleFont_NewFace() {
	f := loadTestFont()
	face := f.NewFace(16) // 16px per em

	m := face.Metrics()
	fmt.Println("ascent", m.Ascent, "descent", m.Descent, "height", m.Height)
	fmt.Println("Measure(Hello) =", face.Measure("Hello"))
	fmt.Println("Advance('H') =", face.Advance('H'))
	// Output:
	// ascent 17 descent 5 height 22
	// Measure(Hello) = 40
	// Advance('H') = 13
}

// ExampleFace_GlyphMask rasterises a single glyph and reports the coverage
// mask placement, exactly the shape golang.org/x/image/font.Face.Glyph
// returns, so swapping this Face in for an x/image face is mechanical.
func ExampleFace_GlyphMask() {
	f := loadTestFont()
	face := f.NewFace(16)

	bounds, mask, maskp, advance, ok := face.GlyphMask('H', 0, 16)
	fmt.Println("ok =", ok)
	fmt.Println("bounds =", bounds)
	fmt.Println("mask != nil:", mask != nil)
	if mask != nil {
		fmt.Println("mask bounds =", mask.Bounds())
	}
	fmt.Println("maskp =", maskp)
	fmt.Println("advance =", advance)
	// Output:
	// ok = true
	// bounds = (0,5)-(12,16)
	// mask != nil: true
	// mask bounds = (0,0)-(12,11)
	// maskp = (0,0)
	// advance = 13
}

// ExampleFace_Kern shows kerning-aware measurement: MeasureKerned folds in
// the pair adjustment that plain Measure does not.
func ExampleFace_Kern() {
	f := loadTestFont()
	face := f.NewFace(16)

	fmt.Println("Kern('A','V') =", face.Kern('A', 'V'))
	fmt.Println("Measure(\"AV\") =", face.Measure("AV"))
	fmt.Println("MeasureKerned(\"AV\") =", face.MeasureKerned("AV"))
	// Output:
	// Kern('A','V') = -2
	// Measure("AV") = 22
	// MeasureKerned("AV") = 20
}

// ExampleFace_Shape runs the font's own GSUB/GPOS tables through Shape and
// ShapePositioned to turn a rune run into glyph indices and pen-relative
// positions, without any external shaping engine.
func ExampleFace_Shape() {
	f := loadTestFont()
	face := f.NewFace(16)

	glyphs := face.Shape("Hi")
	fmt.Println("glyphs =", glyphs)

	for _, g := range face.ShapePositioned("Hi") {
		fmt.Printf("glyph=%d dx=%d dy=%d adv=%d\n", g.Glyph, g.XOffset, g.YOffset, g.XAdvance)
	}
	// Output:
	// glyphs = [9 36]
	// glyph=9 dx=0 dy=0 adv=13
	// glyph=36 dx=0 dy=0 adv=5
}
