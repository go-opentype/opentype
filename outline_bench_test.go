// Copyright (c) the go-opentype/opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"os"
	"testing"
)

// BenchmarkCFFOutlineAllGlyphs interprets every charstring of a CFF font once
// per iteration — the work behind every outline, mask and measurement of a CFF
// face, and the loop the control box rides in.
func BenchmarkCFFOutlineAllGlyphs(b *testing.B) {
	data, err := os.ReadFile("testdata/SourceSerif4-Regular.otf")
	if err != nil {
		b.Skip(err)
	}
	f, err := Parse(data)
	if err != nil {
		b.Fatal(err)
	}
	n := f.NumGlyphs()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for g := 0; g < n; g++ {
			if _, err := f.cff.outline(g); err != nil {
				b.Fatal(err)
			}
		}
	}
}
