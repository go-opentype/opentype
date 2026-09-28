package opentype_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-opentype/opentype"
)

// seedDir holds font programs pulled out of a thousand real PDFs: subset CFFs,
// Type 1 programs, TrueType fragments. A generator does not invent a subset
// font; a typesetter does, badly, and there are nine thousand of them here.
var seedDir = os.Getenv("FONT_SEEDS")

func addFontSeeds(f *testing.F, max, cap int) {
	// Something to chew on even with no corpus about, so that the crashers
	// committed under testdata still run: the corpus makes the search better,
	// it is not what makes the test valid.
	f.Add([]byte("OTTO\x00\x00\x00\x00"))
	f.Add([]byte("\x00\x01\x00\x00\x00\x00\x00\x00"))
	f.Add([]byte("%!PS-AdobeFont-1.0\n"))
	if seedDir == "" {
		return
	}
	ents, err := os.ReadDir(seedDir)
	if err != nil {
		return
	}
	n := 0
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || info.Size() > int64(cap) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(seedDir, e.Name()))
		if err != nil {
			continue
		}
		f.Add(b)
		if n++; n >= max {
			return
		}
	}
}

// exercise parses and then reads the font back. A parser that only checks
// headers would otherwise pass everything: the tables are walked lazily, so
// the bounds a malformed table implies are not tested until a glyph is asked
// for.
func exercise(t *testing.T, fn func([]byte) (*opentype.Font, error), b []byte) {
	start := time.Now()
	f, err := fn(b)
	if err != nil || f == nil {
		return
	}
	face := f.NewFace(16)
	n := f.NumGlyphs()
	if n > 200 {
		n = 200
	}
	for g := 0; g < n; g++ {
		gid := opentype.GlyphIndex(g)
		f.GlyphName(gid)
		f.GlyphAdvance(gid)
		if face != nil {
			_, _ = face.GlyphOutline(gid)
		}
	}
	for r := rune(0); r < 0x200; r++ {
		f.GlyphIndex(r)
	}
	f.TableTags()
	f.Flags()
	f.StemV()
	// A small font that takes a long time is the defect this campaign is
	// hunting, and it does not raise anything on its own.
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("%d bytes took %s", len(b), d)
	}
}

func FuzzParse(f *testing.F) {
	addFontSeeds(f, 1200, 60*1024)
	f.Fuzz(func(t *testing.T, b []byte) { exercise(t, opentype.Parse, b) })
}

func FuzzParseCFF(f *testing.F) {
	addFontSeeds(f, 1200, 60*1024)
	f.Fuzz(func(t *testing.T, b []byte) { exercise(t, opentype.ParseCFF, b) })
}

func FuzzParseType1(f *testing.F) {
	addFontSeeds(f, 1200, 60*1024)
	f.Fuzz(func(t *testing.T, b []byte) { exercise(t, opentype.ParseType1, b) })
}
