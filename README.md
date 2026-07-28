# go-opentype/opentype

[![CI](https://github.com/go-opentype/opentype/actions/workflows/ci.yml/badge.svg)](https://github.com/go-opentype/opentype/actions/workflows/ci.yml)
[![pkg.go.dev](https://img.shields.io/badge/pkg.go.dev-opentype-007d9c?logo=go&logoColor=white)](https://pkg.go.dev/github.com/go-opentype/opentype)
![coverage](https://img.shields.io/badge/coverage-100%25-1a7f37)
![go](https://img.shields.io/badge/Go-1.26.4%2B-00ADD8?logo=go&logoColor=white)
[![license](https://img.shields.io/badge/license-BSD--3--Clause-blue)](./LICENSE)

Pure-Go, **CGO=0, standard-library-only** parser, shaper and anti-aliased
rasteriser for TrueType/OpenType fonts. No `golang.org/x/*`, no third-party
modules — it imports only `image`, `encoding/binary`, `math`, `errors` and
friends, so it builds for every Go target including `GOOS=js GOARCH=wasm`.

It exists to functionally replace the narrow slice of
`golang.org/x/image/font/opentype` that a glyph-blitting UI needs — parse a
font blob, build a face at a pixel size, pull per-rune advances and 8-bit
alpha coverage masks — and then goes further: CFF/CFF2 outlines, variable
fonts, GPOS/GSUB shaping, kerning and TrueType/CFF hinting are all in scope.

## Install

```sh
go get github.com/go-opentype/opentype
```

## Quick start

```go
package main

import (
	"fmt"
	"os"

	"github.com/go-opentype/opentype"
)

func main() {
	ttf, err := os.ReadFile("MyFont.ttf") // or .otf (CFF/CFF2)
	if err != nil {
		panic(err)
	}

	f, err := opentype.Parse(ttf)
	if err != nil {
		panic(err)
	}
	face := f.NewFace(16) // 16px per em

	m := face.Metrics()             // Metrics{Ascent, Descent, Height} in pixels
	w := face.Measure("Hello")      // total advance width in pixels
	adv := face.Advance('H')        // one rune's advance in pixels
	fmt.Println(m, w, adv)

	// Rasterise a glyph with (penX, baselineY) as the pen origin on the baseline.
	bounds, mask, maskp, advance, ok := face.GlyphMask('H', 0, 16)
	if ok && mask != nil {
		// Composite the coverage mask onto your destination:
		// pixel (maskp.X+i, maskp.Y+j) covers destination (bounds.Min.X+i, bounds.Min.Y+j).
		_ = bounds
		_ = maskp
		_ = advance
	}
}
```

The `GlyphMask` shape deliberately mirrors `x/image`'s `font.Face.Glyph`, so
swapping this in for the x/image face is mechanical.

## Features

- **sfnt container** parsing (`0x00010000` TrueType and `OTTO` CFF magics)
- **TrueType `glyf` outlines** — simple and composite glyphs, implied
  on-curve midpoint synthesis, cycle/depth-guarded composites
- **CFF and CFF2 outlines** — Type 2 charstrings, subroutines, `seac`
  accent composition, CFF2 variable-font blend operators
- **Variable fonts** — `fvar` axes and named instances, `avar` axis-value
  mapping, `gvar`/CFF2 glyph-outline interpolation, `MVAR` metric variation,
  via `(*Face).SetVariation`
- **GSUB/GPOS shaping** — ligatures, contextual and positional-form
  substitution (`isol`/`init`/`medi`/`fina`), pair/single/cursive
  positioning, mark-to-base and mark-to-mark attachment
- **Kerning** — GPOS pair positioning with legacy `kern`-table fallback
  through a single `Kerner`
- **Hinting** — TrueType instruction interpreter and a CFF/Type 2 stem
  grid-fitter, toggled per `Face` with `SetHinting`; optional
  `SetStemDarkening`
- **Vertical metrics** — `vhea`/`vmtx`/`VORG`-aware vertical advances and
  origins for vertical writing modes
- **Anti-aliased rasterisation** via 4×4 supersampling under the non-zero
  winding rule

A `Font` is immutable after `Parse` and safe for concurrent use. A `Face`
caches rasterised glyphs and is **not** safe for concurrent use; build one
`Face` per goroutine if needed.

## API tour

| Symbol | Purpose |
| --- | --- |
| `Parse(b []byte) (*Font, error)` | Decode an sfnt (TrueType or CFF/OTTO) font blob. |
| `(*Font).NumGlyphs() int` | Glyph count (from `maxp`). |
| `(*Font).GlyphIndex(r rune) (GlyphIndex, bool)` | Map a rune via the cmap. |
| `(*Font).GlyphIndexVariation(r, vs rune) (GlyphIndex, bool)` | Map a rune + Unicode variation selector. |
| `(*Font).Axes() []Axis` | Variable-font design axes (`fvar`). |
| `(*Font).NamedInstances() []NamedInstance` | Named positions in the variation space. |
| `(*Font).GPOS() *GPOS` / `(*Font).GSUB() *GSUB` | Parsed layout tables, or `nil`. |
| `(*Font).HasVerticalMetrics() bool` | Whether `vhea`/`vmtx` are present. |
| `(*Font).NewFace(sizePx int) *Face` | Build a sized face (scale = `sizePx / unitsPerEm`). |
| `(*Face).Metrics() Metrics` | `Ascent`, `Descent`, `Height` in pixels. |
| `(*Face).Advance(r rune) int` / `(*Face).Measure(s string) int` | Rune / string advance in pixels. |
| `(*Face).Kern(prev, r rune) int` / `(*Face).MeasureKerned(s string) int` | GPOS-or-`kern` pair adjustment. |
| `(*Face).GlyphMask(r, x, y) (image.Rectangle, *image.Alpha, image.Point, int, bool)` | Rasterised glyph + advance. |
| `(*Face).Shape(text string, features ...string) []GlyphIndex` | GSUB-substituted glyph run. |
| `(*Face).ShapePositioned(text string, features ...string) []PositionedGlyph` | GSUB + GPOS glyph run with pen offsets. |
| `(*Face).SetVariation(coords map[string]float64)` | Instance a variable font at the given axis coordinates. |
| `(*Face).SetHinting(on bool)` / `(*Face).SetStemDarkening(on bool)` | Toggle the TrueType/CFF hinter. |
| `(*Face).VerticalAdvance(r rune) int` / `(*Face).VerticalOrigin(r rune) (int, bool)` | Vertical writing-mode metrics. |

See [`example_test.go`](./example_test.go) for runnable examples of each of
these, and `go doc github.com/go-opentype/opentype` for the full reference.

## Support matrix

- sfnt container (`0x00010000` and `true` TrueType magics, `OTTO` CFF magic)
- `head`, `maxp`, `hhea`, `hmtx` (including the trailing-run shared advance)
- `cmap` formats **4** (BMP) and **12** (full Unicode), preferring 12, plus
  format **14** (Unicode variation sequences)
- `loca` (short and long), `glyf` simple and composite TrueType glyphs
- CFF and CFF2 (`OTTO`) Type 2 charstring outlines, including `seac`
- `fvar`/`avar`/`gvar`/`MVAR` and CFF2 blends for variable fonts
- `GSUB`/`GPOS`/`GDEF`, the legacy `kern` table
- the OpenType **`MATH`** table — math-typesetting metrics (constants, per-glyph
  italic correction / top-accent attachment / corner kerning, and stretchy-glyph
  size variants and assemblies), exposed pixel-scaled through a `Face`
  (`HasMath`, `MathConstant`, `ItalicCorrection`, `TopAccentAttachment`,
  `MathKern`, `MathVariants`); math *layout* is left to a higher-level engine
- TrueType instruction hinting and a CFF stem grid-fitter/darkener
- `vhea`/`vmtx`/`VORG` vertical metrics
- anti-aliased rasterisation via 4×4 supersampling under the non-zero
  winding rule

Rasterisation uses uniform supersampling (not the delta-hinted rasterisation
of a native TrueType/CFF hinter's drop-out control), so it favours
correctness and portability over the very last drop of small-size sharpness.

## Testing

Tests never depend on an external font to reach 100% coverage: they
synthesise minimal-but-valid TrueType and CFF fonts in memory to
deterministically exercise every parse, shape and raster branch, including
the error paths. A real-world font, Adobe's Source Serif 4 (SIL Open Font
License, bundled under [`testdata/`](./testdata)), is used in
[`example_test.go`](./example_test.go) and in a handful of sanity-check
tests, so the documented examples double as an end-to-end smoke test against
production CFF charstrings and Private DICTs. CI enforces **100.0% statement
coverage**, `go vet`, and a cross-compile smoke over
`linux/{amd64,arm64,riscv64,loong64,ppc64le,s390x}`, `js/wasm`,
`darwin/arm64` and `windows/amd64`.

## Part of the go-opentype pure-Go text stack

`go-opentype/opentype` is the parsing/shaping/rasterising engine at the base
of a dependency-free text stack:

- **[opentype](https://github.com/go-opentype/opentype)** (this repo) — font
  parsing, GSUB/GPOS shaping, hinting and rasterisation.
- **[bidi](https://github.com/go-opentype/bidi)** — a full Unicode
  Bidirectional Algorithm (UBA) implementation, for ordering mixed
  left-to-right/right-to-left text before it is shaped.
- **[shape](https://github.com/go-opentype/shape)** — a HarfBuzz-lite
  complex-script shaper (Arabic, Indic, Hangul, USE, Egyptian
  hieroglyphs, ...) built on this package's GSUB/GPOS engine.
- **[fonts](https://github.com/go-opentype/fonts)** — 36 bundled OFL/BSD
  font families, per-family lazily `go:embed`-ed, ready to feed to `Parse`.

## License

BSD-3-Clause. See [LICENSE](./LICENSE).
