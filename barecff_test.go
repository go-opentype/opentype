package opentype

import (
	"strings"
	"testing"
)

// boxGlyph is a charstring that draws a box: a width, a move, and four
// lines. The leading operand is the width, since rmoveto takes two.
func boxGlyph(width int) []byte {
	return append(t2nums(width, 50, 50), 21, // width dx dy rmoveto
		byte(139+100), byte(139), 5, // 100 0 rlineto
		byte(139), byte(139+100), 5, // 0 100 rlineto
		byte(139-100), byte(139), 5, // -100 0 rlineto
		14) // endchar
}

// t2nums encodes small integers as Type 2 charstring operands.
func t2nums(vs ...int) []byte {
	var out []byte
	for _, v := range vs {
		switch {
		case v >= -107 && v <= 107:
			out = append(out, byte(v+139))
		default:
			out = append(out, 28, byte(v>>8), byte(v))
		}
	}
	return out
}

func TestTheStandardStringsAgreeWithTheStandardEncoding(t *testing.T) {
	// The first ninety-five standard strings are the printable ASCII
	// characters in order, which is what the Standard Encoding says too. Two
	// tables written from the same specification have to agree, and this is
	// what says the long one was not merely transcribed.
	for code := 32; code <= 126; code++ {
		sid, ok := standardEncodingSID(code)
		if !ok {
			t.Fatalf("the standard encoding has no string for code %d", code)
		}
		if int(sid) != code-31 {
			t.Fatalf("code %d has string id %d, not %d", code, sid, code-31)
		}
		if int(sid) >= nStdStrings {
			t.Fatalf("code %d names string %d, past the %d there are", code, sid, nStdStrings)
		}
	}
	// Every code the encoding names has a name, and the names are distinct:
	// two codes standing for the same glyph would make a lookup ambiguous.
	seen := map[string]int{}
	for code := 0; code < 256; code++ {
		sid, ok := standardEncodingSID(code)
		if !ok {
			continue
		}
		if int(sid) >= nStdStrings {
			t.Errorf("code %d names string %d, past the end", code, sid)
			continue
		}
		name := cffStandardStrings[sid]
		if name == "" || name == ".notdef" {
			t.Errorf("code %d is named %q", code, name)
		}
		if was, dup := seen[name]; dup {
			t.Errorf("codes %d and %d are both %q", was, code, name)
		}
		seen[name] = code
	}
	if nStdStrings != 391 {
		t.Errorf("there are %d standard strings, not the 391 the format has", nStdStrings)
	}
	// The ASCII letters land where their own characters are, which no
	// transcription error could survive.
	for r := 'A'; r <= 'Z'; r++ {
		sid, _ := standardEncodingSID(int(r))
		if got := cffStandardStrings[sid]; got != string(r) {
			t.Errorf("%q is named %q", r, got)
		}
	}
	for r := 'a'; r <= 'z'; r++ {
		sid, _ := standardEncodingSID(int(r))
		if got := cffStandardStrings[sid]; got != string(r) {
			t.Errorf("%q is named %q", r, got)
		}
	}
}

func TestABareCFFProgramIsAddressedByName(t *testing.T) {
	// A PDF carries a CFF program with no sfnt around it and addresses it by
	// glyph name. 'A' is standard string 34 and 'B' is 35.
	data := buildCFF(cffOptions{
		glyphs:      [][]byte{boxGlyph(0), boxGlyph(600), boxGlyph(700)},
		charsetSIDs: []int{34, 35},
		includePriv: true,
	})
	f, err := ParseCFF(data)
	if err != nil {
		t.Fatalf("a bare program was refused: %v", err)
	}
	if f.NumGlyphs() != 3 {
		t.Errorf("NumGlyphs() = %d", f.NumGlyphs())
	}
	if f.UnitsPerEm() != 1000 {
		t.Errorf("UnitsPerEm() = %d", f.UnitsPerEm())
	}
	if f.HasCharacterMap() {
		t.Error("a bare program claims a character map")
	}
	for name, want := range map[string]GlyphIndex{".notdef": 0, "A": 1, "B": 2} {
		gid, ok := f.GlyphIndexByName(name)
		if !ok || gid != want {
			t.Errorf("%q is glyph %d (%v), want %d", name, gid, ok, want)
		}
	}
	if _, ok := f.GlyphIndexByName("Omega"); ok {
		t.Error("a name the font has not got was found")
	}
	if got, ok := f.GlyphName(2); !ok || got != "B" {
		t.Errorf("glyph 2 is called %q (%v)", got, ok)
	}
	if _, ok := f.GlyphName(99); ok {
		t.Error("a glyph past the end has a name")
	}
	// The outlines are there, which is the whole point of the exercise.
	face := f.NewFace(f.UnitsPerEm())
	segs, ok := face.GlyphOutline(1)
	if !ok || len(segs) == 0 {
		t.Error("glyph A has no outline")
	}
}

func TestABareCFFProgramIsAddressedByCode(t *testing.T) {
	// With no encoding of its own, the program is read through the Standard
	// Encoding: code 65 is 'A', which the charset places at glyph 1.
	std := buildCFF(cffOptions{
		glyphs:      [][]byte{boxGlyph(0), boxGlyph(600)},
		charsetSIDs: []int{34},
		includePriv: true,
	})
	f, err := ParseCFF(std)
	if err != nil {
		t.Fatal(err)
	}
	if gid, ok := f.GlyphIndexByCode('A'); !ok || gid != 1 {
		t.Errorf("code 'A' is glyph %d (%v), want 1", gid, ok)
	}
	if _, ok := f.GlyphIndexByCode(0); ok {
		t.Error("a code the standard encoding does not name was found")
	}
	if _, ok := f.GlyphIndexByCode('Z'); ok {
		t.Error("a code naming a glyph this font has not got was found")
	}

	// A format-0 encoding of its own wins: code 200 is glyph 1.
	own := buildCFF(cffOptions{
		glyphs:      [][]byte{boxGlyph(0), boxGlyph(600)},
		charsetSIDs: []int{34},
		includePriv: true,
		encoding:    []byte{0, 1, 200},
	})
	f, err = ParseCFF(own)
	if err != nil {
		t.Fatal(err)
	}
	if gid, ok := f.GlyphIndexByCode(200); !ok || gid != 1 {
		t.Errorf("code 200 is glyph %d (%v), want 1", gid, ok)
	}
	if _, ok := f.GlyphIndexByCode('A'); ok {
		t.Error("the standard encoding was consulted despite the font having its own")
	}

	// A format-1 encoding names ranges, and a supplement adds a code by the
	// name of the glyph rather than by its number.
	ranged := buildCFF(cffOptions{
		glyphs:      [][]byte{boxGlyph(0), boxGlyph(600), boxGlyph(700)},
		charsetSIDs: []int{34, 35},
		includePriv: true,
		encoding:    []byte{0x81, 1, 100, 1, 1, 250, 0, 35},
	})
	f, err = ParseCFF(ranged)
	if err != nil {
		t.Fatal(err)
	}
	for code, want := range map[byte]GlyphIndex{100: 1, 101: 2, 250: 2} {
		if gid, ok := f.GlyphIndexByCode(code); !ok || gid != want {
			t.Errorf("code %d is glyph %d (%v), want %d", code, gid, ok, want)
		}
	}
}

func TestABareCFFProgramWithNamesOfItsOwn(t *testing.T) {
	// A subset names glyphs the standard list has never heard of; their
	// string ids count on from the end of it.
	data := buildCFF(cffOptions{
		glyphs:      [][]byte{boxGlyph(0), boxGlyph(600), boxGlyph(700)},
		charsetSIDs: []int{nStdStrings, nStdStrings + 1},
		includePriv: true,
		strings:     []string{"uni0141", "g123"},
	})
	f, err := ParseCFF(data)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]GlyphIndex{"uni0141": 1, "g123": 2} {
		if gid, ok := f.GlyphIndexByName(name); !ok || gid != want {
			t.Errorf("%q is glyph %d (%v), want %d", name, gid, ok, want)
		}
	}
	// A string id past the end of what the font carries names nothing.
	beyond := buildCFF(cffOptions{
		glyphs:      [][]byte{boxGlyph(0), boxGlyph(600)},
		charsetSIDs: []int{nStdStrings + 9},
		includePriv: true,
		strings:     []string{"only"},
	})
	f, err = ParseCFF(beyond)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := f.GlyphName(1); ok {
		t.Errorf("a glyph named past the end came back as %q", name)
	}
}

func TestACIDKeyedProgramNamesNothing(t *testing.T) {
	// A font addressed by character identifier has no glyph names at all: its
	// charset holds identifiers, not string ids.
	data := buildCFF(cffOptions{
		glyphs:      [][]byte{boxGlyph(0), boxGlyph(600)},
		charsetSIDs: []int{34},
		includePriv: true,
		strings:     []string{"Adobe", "Identity"},
		ros:         true,
	})
	f, err := ParseCFF(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.GlyphName(1); ok {
		t.Error("a font addressed by identifier named a glyph")
	}
	if _, ok := f.GlyphIndexByName("A"); ok {
		t.Error("a font addressed by identifier was found by name")
	}
	if _, ok := f.GlyphIndexByCode('A'); ok {
		t.Error("a font addressed by identifier was found by code")
	}
	// Its outlines are still there.
	if segs, ok := f.NewFace(1000).GlyphOutline(1); !ok || len(segs) == 0 {
		t.Error("a font addressed by identifier has no outlines")
	}
}

func TestABareCFFProgramInOtherUnits(t *testing.T) {
	// A font matrix of a two-thousandth means two thousand units to the em.
	data := buildCFF(cffOptions{
		glyphs:      [][]byte{boxGlyph(0), boxGlyph(600)},
		charsetSIDs: []int{34},
		includePriv: true,
		fontMatrix:  []float64{0.0005, 0, 0, 0.0005, 0, 0},
	})
	f, err := ParseCFF(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.UnitsPerEm() != 2000 {
		t.Errorf("UnitsPerEm() = %d, want 2000", f.UnitsPerEm())
	}
	// A matrix that says something impossible falls back on the usual.
	for _, m := range [][]float64{{0, 0, 0, 0, 0, 0}, {1, 0, 0, 1, 0, 0}, {1e-9, 0, 0, 1e-9, 0, 0}} {
		data = buildCFF(cffOptions{
			glyphs:      [][]byte{boxGlyph(0), boxGlyph(600)},
			charsetSIDs: []int{34},
			includePriv: true,
			fontMatrix:  m,
		})
		f, err = ParseCFF(data)
		if err != nil {
			t.Fatal(err)
		}
		if f.UnitsPerEm() != 1000 {
			t.Errorf("a matrix of %v gave %d units to the em", m, f.UnitsPerEm())
		}
	}
}

func TestABareCFFProgramIsRefusedWhenItIsNotOne(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"not a font at all", []byte("hello"), "bad hdrSize"},
		{"no glyphs", buildCFF(cffOptions{glyphs: nil, includePriv: true}), "no glyphs"},
		{"an encoding past the end", buildCFF(cffOptions{
			glyphs: [][]byte{boxGlyph(0)}, includePriv: true,
			encoding: []byte{9, 9, 9},
		}), ""},
	}
	for _, c := range cases {
		_, err := ParseCFF(c.data)
		if err == nil {
			t.Errorf("%s was accepted", c.name)
			continue
		}
		if c.want != "" && !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestWhatABareCFFProgramSaysAGlyphIsWide(t *testing.T) {
	// A charstring carries its own advance in front of its first operator,
	// measured from the font's nominal width; one that carries none gets the
	// default.
	priv := append(append([]byte{}, dictLong(400)...), dictOperator(20)...)
	priv = append(priv, dictLong(500)...)
	priv = append(priv, dictOperator(21)...)
	data := buildCFF(cffOptions{
		// glyph 1 says 600-500 = 100 more than nominal; glyph 2 says nothing.
		glyphs:      [][]byte{boxGlyph(0), boxGlyph(100), noWidthGlyph()},
		charsetSIDs: []int{34, 35},
		includePriv: true,
		privExtra:   priv,
	})
	f, err := ParseCFF(data)
	if err != nil {
		t.Fatal(err)
	}
	face := f.NewFace(f.UnitsPerEm())
	if got := face.AdvanceIndex(1); got != 600 {
		t.Errorf("glyph 1 is %v wide, want 600", got)
	}
	if got := face.AdvanceIndex(2); got != 400 {
		t.Errorf("glyph 2 is %v wide, want the default of 400", got)
	}
}

// noWidthGlyph draws the same box with no width in front of it.
func noWidthGlyph() []byte {
	return append(t2nums(50, 50), 21,
		byte(139+100), byte(139), 5,
		byte(139), byte(139+100), 5,
		byte(139-100), byte(139), 5,
		14)
}

func TestReadingTheWidthOffTheFrontOfACharstring(t *testing.T) {
	// The width is whatever operand is left over in front of the first
	// operator that takes a fixed number of them. Every shape one can take is
	// exercised here, because the alternative is to read it wrong and lay a
	// line of text out wrong with nothing to say anything went awry.
	big := []byte{28, 0x02, 0x58}    // 600
	fixed := []byte{255, 0, 1, 0, 0} // 1.0 in sixteen-sixteen fixed point
	join := func(parts ...[]byte) []byte {
		var out []byte
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	const hstem, vmoveto, rmoveto, hmoveto, endchar, hintmask, callsubr = 1, 4, 21, 22, 14, 19, 10
	cases := []struct {
		name  string
		cs    []byte
		want  float64
		found bool
	}{
		{"a width then hstem", join(big, t2nums(10, 20), []byte{hstem}), 600, true},
		{"hstem with no width", join(t2nums(10, 20), []byte{hstem}), 0, false},
		{"a width then hintmask", join(big, t2nums(10, 20), []byte{hintmask, 0}), 600, true},
		{"a width then rmoveto", join(big, t2nums(5, 5), []byte{rmoveto}), 600, true},
		{"rmoveto with no width", join(t2nums(5, 5), []byte{rmoveto}), 0, false},
		{"a width then hmoveto", join(big, t2nums(5), []byte{hmoveto}), 600, true},
		{"hmoveto with no width", join(t2nums(5), []byte{hmoveto}), 0, false},
		{"a width then vmoveto", join(big, t2nums(5), []byte{vmoveto}), 600, true},
		{"a width then endchar", join(big, []byte{endchar}), 600, true},
		{"endchar with no width", []byte{endchar}, 0, false},
		{"a width then a seac endchar", join(big, t2nums(1, 2, 3, 4), []byte{endchar}), 600, true},
		{"a small negative width", join(t2nums(-50), t2nums(5, 5), []byte{rmoveto}), -50, true},
		{"a width as a fixed-point number", join(fixed, t2nums(5, 5), []byte{rmoveto}), 1, true},
		{"a large positive operand", join([]byte{248, 100}, t2nums(5, 5), []byte{rmoveto}), (248-247)*256 + 100 + 108, true},
		{"a large negative operand", join([]byte{252, 100}, t2nums(5, 5), []byte{rmoveto}), -((252-251)*256 + 100 + 108), true},
		{"a width behind a subroutine", join(big, []byte{callsubr}), 0, false},
		{"an operand cut short", []byte{28, 0x02}, 0, false},
		{"a two-byte operand cut short", []byte{247}, 0, false},
		{"a negative two-byte operand cut short", []byte{251}, 0, false},
		{"a fixed-point operand cut short", []byte{255, 0, 0}, 0, false},
		{"nothing but operands", t2nums(1, 2, 3), 0, false},
		{"nothing at all", nil, 0, false},
	}
	for _, c := range cases {
		got, ok := charstringWidth(c.cs)
		if ok != c.found {
			t.Errorf("%s: found = %v, want %v", c.name, ok, c.found)
			continue
		}
		if ok && got != c.want {
			t.Errorf("%s: width %v, want %v", c.name, got, c.want)
		}
	}
}

func TestABuiltInEncodingThatIsNotOne(t *testing.T) {
	cases := []struct {
		name string
		enc  []byte
	}{
		{"a format nobody has defined", []byte{7, 0}},
		{"a format-0 encoding cut short", []byte{0, 4, 1}},
		{"a format-1 encoding cut short", []byte{1, 3, 1, 1}},
		{"a supplement cut short", []byte{0x80, 1, 200, 2, 5}},
	}
	for _, c := range cases {
		data := buildCFF(cffOptions{
			glyphs:      [][]byte{boxGlyph(0), boxGlyph(600)},
			charsetSIDs: []int{34},
			includePriv: true,
			encoding:    c.enc,
		})
		if _, err := ParseCFF(data); err == nil {
			t.Errorf("%s was accepted", c.name)
		}
	}
	// An encoding that says it is past the end of the font is not one.
	beyond := buildCFF(cffOptions{
		glyphs:                 [][]byte{boxGlyph(0), boxGlyph(600)},
		charsetSIDs:            []int{34},
		includePriv:            true,
		encodingOff:            1 << 20,
		namePredefinedEncoding: true,
	})
	if _, err := ParseCFF(beyond); err == nil {
		t.Error("an encoding past the end of the font was accepted")
	}

	// The two predefined encodings are named by number rather than laid out,
	// and the expert one is read as the standard rather than refused.
	for _, off := range []int{0, 1} {
		data := buildCFF(cffOptions{
			glyphs:                 [][]byte{boxGlyph(0), boxGlyph(600)},
			charsetSIDs:            []int{34},
			includePriv:            true,
			encodingOff:            off,
			namePredefinedEncoding: true,
		})
		f, err := ParseCFF(data)
		if err != nil {
			t.Fatalf("the predefined encoding %d was refused: %v", off, err)
		}
		if gid, ok := f.GlyphIndexByCode('A'); !ok || gid != 1 {
			t.Errorf("predefined encoding %d: 'A' is glyph %d (%v)", off, gid, ok)
		}
	}
}

func TestEveryStandardStringIsWhatTheEncodingSaysItIs(t *testing.T) {
	// A second, independent statement of the first ninety-five names: the
	// Standard Encoding puts each printable ASCII character at its own code,
	// so the name of the string that code points at has to be the name of
	// that character. Two codes are the known exception — the Standard
	// Encoding calls 39 and 96 the typographic quotes, where Unicode calls
	// them the straight ones — and naming them here is what keeps the
	// exception from hiding a mistake.
	exceptions := map[int]string{39: "quoteright", 96: "quoteleft"}
	ascii := map[int]string{
		32: "space", 33: "exclam", 34: "quotedbl", 35: "numbersign", 36: "dollar",
		37: "percent", 38: "ampersand", 40: "parenleft", 41: "parenright",
		42: "asterisk", 43: "plus", 44: "comma", 45: "hyphen", 46: "period",
		47: "slash", 48: "zero", 49: "one", 50: "two", 51: "three", 52: "four",
		53: "five", 54: "six", 55: "seven", 56: "eight", 57: "nine", 58: "colon",
		59: "semicolon", 60: "less", 61: "equal", 62: "greater", 63: "question",
		64: "at", 91: "bracketleft", 92: "backslash", 93: "bracketright",
		94: "asciicircum", 95: "underscore", 123: "braceleft", 124: "bar",
		125: "braceright", 126: "asciitilde",
	}
	for code := 32; code <= 126; code++ {
		sid, ok := standardEncodingSID(code)
		if !ok {
			t.Fatalf("code %d names nothing", code)
		}
		got := cffStandardStrings[sid]
		want, named := exceptions[code]
		if !named {
			if want, named = ascii[code]; !named {
				// Letters and digits are named by themselves.
				want = string(rune(code))
			}
		}
		if got != want {
			t.Errorf("code %d is named %q, want %q", code, got, want)
		}
	}
}
