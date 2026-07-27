# go-opentype/opentype

[![CI](https://github.com/go-opentype/opentype/actions/workflows/ci.yml/badge.svg)](https://github.com/go-opentype/opentype/actions/workflows/ci.yml)
[![pkg.go.dev](https://img.shields.io/badge/pkg.go.dev-opentype-007d9c?logo=go&logoColor=white)](https://pkg.go.dev/github.com/go-opentype/opentype)
![coverage](https://img.shields.io/badge/coverage-100%25-1a7f37)
![go](https://img.shields.io/badge/Go-1.26.4%2B-00ADD8?logo=go&logoColor=white)
[![license](https://img.shields.io/badge/license-BSD--3--Clause-blue)](./LICENSE)

Pure-Go, **CGO=0, standard-library-only** parser and anti-aliased rasteriser
for TrueType/OpenType fonts. No `golang.org/x/*`, no third-party modules — it
imports only `image`, `encoding/binary`, `math`, `errors` and friends, so it
builds for every Go target including `GOOS=js GOARCH=wasm`.

It exists to functionally replace the narrow slice of
`golang.org/x/image/font/opentype` that a glyph-blitting UI needs: parse a font
blob, build a face at a pixel size, then pull per-rune advances and 8-bit alpha
coverage masks.

## Usage

```go
f, err := opentype.Parse(ttf) // ttf is a []byte TrueType/OpenType blob
if err != nil {
    log.Fatal(err)
}
face := f.NewFace(16) // 16px per em

m := face.Metrics()             // Metrics{Ascent, Descent, Height} in pixels
w := face.Measure("Hello")      // total advance width in pixels
adv := face.Advance('H')        // one rune's advance in pixels

// Rasterise a glyph with (penX, baselineY) as the pen origin on the baseline.
bounds, mask, maskp, advance, ok := face.GlyphMask('H', penX, baselineY)
if ok && mask != nil {
    // Composite mask (an *image.Alpha coverage mask) onto your destination:
    // pixel (maskp.X+i, maskp.Y+j) covers destination (bounds.Min.X+i, bounds.Min.Y+j).
}
```

The `GlyphMask` shape deliberately mirrors `x/image`'s `font.Face.Glyph`, so
swapping this in for the x/image face is mechanical.

## API

| Symbol | Purpose |
| --- | --- |
| `Parse(b []byte) (*Font, error)` | Decode an sfnt font blob. |
| `(*Font).NumGlyphs() int` | Glyph count (from `maxp`). |
| `(*Font).GlyphIndex(r rune) (GlyphIndex, bool)` | Map a rune via the cmap. |
| `(*Font).NewFace(sizePx int) *Face` | Build a sized face (scale = `sizePx / unitsPerEm`). |
| `(*Face).Metrics() Metrics` | `Ascent`, `Descent`, `Height` in pixels. |
| `(*Face).Advance(r rune) int` | Rune advance in pixels. |
| `(*Face).Measure(s string) int` | Sum of advances in pixels. |
| `(*Face).GlyphMask(r, x, y) (image.Rectangle, *image.Alpha, image.Point, int, bool)` | Rasterised glyph + advance. |

A `Font` is immutable after `Parse` and safe for concurrent use. A `Face`
caches rasterised glyphs and is **not** safe for concurrent use.

## Support matrix

Supported (phase 1 — TrueType `glyf` outlines):

- sfnt container (`0x00010000` and `true` magic), table directory
- `head`, `maxp`, `hhea`, `hmtx` (including the trailing-run shared advance)
- `cmap` formats **4** (BMP) and **12** (full Unicode), preferring 12
- `loca` (short and long), `glyf` simple glyphs (repeat flags, short/long
  deltas) and composite glyphs (`ARGS_ARE_XY_VALUES`, scale / x&y-scale / 2×2,
  `MORE_COMPONENTS`, with cycle and depth guards)
- implied on-curve midpoint synthesis for quadratic contours
- anti-aliased rasterisation via 4×4 supersampling under the non-zero winding
  rule

Not yet implemented (deferred to later phases):

- **CFF / OpenType (`OTTO`) outlines** — `Parse` returns a clear error
- **GPOS / GSUB shaping**, ligatures, contextual substitution
- **kerning** (`kern` / GPOS pairs)
- **hinting** (TrueType instructions are skipped)
- cmap formats other than 4 and 12; vertical metrics (`vhea`/`vmtx`)

Rasterisation is unhinted and uses uniform supersampling, so it favours
correctness and portability over the last drop of small-size sharpness.

## Testing

Tests never depend on an external font: they synthesise minimal-but-valid
TrueType fonts in memory (table directory + `head`/`maxp`/`hhea`/`hmtx`/`cmap`/
`loca`/`glyf`) to deterministically exercise every parse and raster branch,
including the error paths. CI enforces **100.0% statement coverage**, `go vet`,
and a cross-compile smoke over `linux/{amd64,arm64,riscv64,loong64,ppc64le,
s390x}`, `js/wasm`, `darwin/arm64` and `windows/amd64`.

## License

BSD-3-Clause. See [LICENSE](./LICENSE).
