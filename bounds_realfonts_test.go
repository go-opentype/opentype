// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// realFontDigest is what a font gives when asked: its glyph count and a digest
// of the glyph its cmap returns for every code point of the first three planes'
// worth of interest. The expected values were taken from the parser before the
// bounds of issue #75 existed (origin/main at v0.21.1+), so a font that parses
// differently now fails here.
func realFontDigest(f *Font) (int, string) {
	h := sha256.New()
	var buf [8]byte
	for r := rune(0); r < 0x30000; r++ {
		g, ok := f.GlyphIndex(r)
		if !ok {
			continue
		}
		binary.BigEndian.PutUint32(buf[:4], uint32(r))
		binary.BigEndian.PutUint32(buf[4:], uint32(g))
		h.Write(buf[:])
	}
	return f.NumGlyphs(), hex.EncodeToString(h.Sum(nil))
}

// TestRealFontsParseUnchanged parses every font under testdata and compares it
// with what the unbounded parser produced.
func TestRealFontsParseUnchanged(t *testing.T) {
	want := map[string]struct {
		glyphs int
		cmap   string
	}{
		"IBMPlexSans-Regular-oracle.ttf":   {971, "6002e49361aef9f896d93c5cca9baca88251ff5133fc5d4e70e1269827fe96a2"},
		"STIXTwoMath-Regular.otf":          {6760, "e85fa7ca9ce42fa5c32cfaf617df68be014fb3f5522d75b2f1730941a2ae0c67"},
		"SourceSerif4-Regular.otf":         {1464, "5d5343225ab15ada88c86be5b18e4350e803dd5e76ead611f353e9539c093a76"},
		"IBMPlexSans-Regular-subset.woff2": {270, "c836ffc49cdac4a365d158ab6006f6fe6576a160dd8d5eb94063eb3d173d297b"},
	}
	paths, err := filepath.Glob(filepath.Join("testdata", "*"))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, p := range paths {
		name := filepath.Base(p)
		w, ok := want[name]
		if !ok {
			continue
		}
		seen++
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := Parse(b)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		glyphs, digest := realFontDigest(f)
		if os.Getenv("OPENTYPE_PRINT_GOLDEN") == "1" {
			t.Logf("GOLDEN %q: {%d, %q},", name, glyphs, digest)
		}
		if glyphs != w.glyphs || digest != w.cmap {
			t.Errorf("%s: %d glyphs, cmap digest %s; before the bounds: %d, %s", name, glyphs, digest, w.glyphs, w.cmap)
		}
	}
	if seen != len(want) {
		t.Errorf("found %d of the %d fonts under testdata", seen, len(want))
	}
}
