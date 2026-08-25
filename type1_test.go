package opentype

import (
	"fmt"
	"strings"
	"testing"
)

func TestATypeOneProgramIsReadAndDrawn(t *testing.T) {
	f, err := ParseType1(simpleType1(t1Options{}))
	if err != nil {
		t.Fatalf("a Type 1 program was refused: %v", err)
	}
	if f.NumGlyphs() != 3 {
		t.Errorf("NumGlyphs() = %d", f.NumGlyphs())
	}
	if f.UnitsPerEm() != 1000 {
		t.Errorf("UnitsPerEm() = %d", f.UnitsPerEm())
	}
	if f.HasCharacterMap() {
		t.Error("a Type 1 program claims a character map")
	}
	for name, want := range map[string]GlyphIndex{".notdef": 0, "A": 1, "B": 2} {
		if gid, ok := f.GlyphIndexByName(name); !ok || gid != want {
			t.Errorf("%q is glyph %d (%v), want %d", name, gid, ok, want)
		}
	}
	if got, ok := f.GlyphName(2); !ok || got != "B" {
		t.Errorf("glyph 2 is called %q (%v)", got, ok)
	}
	if _, ok := f.GlyphName(9); ok {
		t.Error("a glyph past the end has a name")
	}
	face := f.NewFace(f.UnitsPerEm())
	if segs, ok := face.GlyphOutline(1); !ok || len(segs) == 0 {
		t.Error("glyph A has no outline")
	}
	if segs, _ := face.GlyphOutline(0); len(segs) != 0 {
		t.Error(".notdef drew something")
	}
	// The width is the second operand of hsbw, and the side bearing the first.
	if got := face.AdvanceIndex(1); got != 600 {
		t.Errorf("A is %d wide, want 600", got)
	}
	if got := face.AdvanceIndex(2); got != 700 {
		t.Errorf("B is %d wide, want 700", got)
	}
	// With no encoding of its own it is read through the standard one.
	if gid, ok := f.GlyphIndexByCode('A'); !ok || gid != 1 {
		t.Errorf("code 'A' is glyph %d (%v), want 1", gid, ok)
	}
	if _, ok := f.GlyphIndexByCode(0); ok {
		t.Error("a code the standard encoding does not name was found")
	}
	if _, ok := f.GlyphIndexByCode('Z'); ok {
		t.Error("a code naming a glyph this font has not got was found")
	}
}

func TestATypeOneProgramWithAnEncodingOfItsOwn(t *testing.T) {
	f, err := ParseType1(simpleType1(t1Options{
		encoding: map[byte]string{1: "A", 200: "B"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for code, want := range map[byte]GlyphIndex{1: 1, 200: 2} {
		if gid, ok := f.GlyphIndexByCode(code); !ok || gid != want {
			t.Errorf("code %d is glyph %d (%v), want %d", code, gid, ok, want)
		}
	}
	if _, ok := f.GlyphIndexByCode('A'); ok {
		t.Error("the standard encoding was consulted despite the program having its own")
	}
}

func TestATypeOneProgramInEveryContainer(t *testing.T) {
	// The same program arrives three ways: as a PDF stream runs the two
	// halves together, as a .pfb wraps each in a segment header, and as a
	// .pfa writes the second in hexadecimal.
	for _, container := range []string{"", "pfb", "pfa"} {
		f, err := ParseType1(simpleType1(t1Options{container: container}))
		if err != nil {
			t.Errorf("%q: %v", container, err)
			continue
		}
		if f.NumGlyphs() != 3 {
			t.Errorf("%q: %d glyphs", container, f.NumGlyphs())
		}
		if segs, ok := f.NewFace(1000).GlyphOutline(1); !ok || len(segs) == 0 {
			t.Errorf("%q: glyph A has no outline", container)
		}
	}
}

func TestATypeOneProgramInOtherUnitsAndWithOtherPadding(t *testing.T) {
	f, err := ParseType1(simpleType1(t1Options{
		matrix: "/FontMatrix [0.0005 0 0 0.0005 0 0] readonly def",
		lenIV:  0,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if f.UnitsPerEm() != 2000 {
		t.Errorf("UnitsPerEm() = %d, want 2000", f.UnitsPerEm())
	}
	if segs, ok := f.NewFace(2000).GlyphOutline(1); !ok || len(segs) == 0 {
		t.Error("a program with no charstring padding drew nothing")
	}
	// A matrix that says something impossible, and one that is not there.
	for _, m := range []string{
		"/FontMatrix [0 0 0 0 0 0] readonly def",
		"/FontMatrix [0.001 0 0] readonly def",
		"/FontMatrix [x y z 0 0 0] readonly def",
		"/FontMatrix readonly def",
		"% no matrix at all",
	} {
		f, err = ParseType1(simpleType1(t1Options{matrix: m}))
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		if f.UnitsPerEm() != 1000 {
			t.Errorf("%s gave %d units to the em", m, f.UnitsPerEm())
		}
	}
}

func TestATypeOneProgramThatIsNotOne(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"nothing at all", nil, "no eexec"},
		{"no eexec section", simpleType1(t1Options{noEexec: true}), "no eexec"},
		{"no charstrings", simpleType1(t1Options{noCharStrs: true}), "no CharStrings"},
		{"a pfb segment header cut short", []byte{0x80, 1, 5, 0}, "cut short"},
		{"a pfb segment past the end", []byte{0x80, 1, 0xff, 0xff, 0, 0, 'a'}, "past the end"},
		{"a pfb segment of an unknown kind", []byte{0x80, 9, 0, 0, 0, 0, 'a'}, "unknown pfb segment"},
	}
	for _, c := range cases {
		_, err := ParseType1(c.data)
		if err == nil {
			t.Errorf("%s was accepted", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// A program whose CharStrings dictionary is empty has no glyphs.
	empty := buildType1(t1Options{glyphs: map[string][]byte{}, order: nil})
	if _, err := ParseType1(empty); err == nil || !strings.Contains(err.Error(), "no glyphs") {
		t.Errorf("an empty program gave %v", err)
	}
}

func TestATypeOneProgramCallingItsOwnSubroutines(t *testing.T) {
	// The body of the box is put in a subroutine and called, which is what
	// nearly every real program does.
	body := append(t1num(50, 50), t1RMoveTo)
	body = append(body, t1num(100)...)
	body = append(body, t1HLineTo)
	body = append(body, t1num(100)...)
	body = append(body, t1VLineTo)
	body = append(body, t1num(-100)...)
	body = append(body, t1HLineTo, t1ClosePath, t1Return)

	glyph := append(t1num(0, 600), t1HSbw)
	glyph = append(glyph, t1num(0)...)
	glyph = append(glyph, t1CallSubr, t1EndChar)

	f, err := ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": glyph},
		order:  []string{".notdef", "A"},
		subrs:  [][]byte{body},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if segs, ok := f.NewFace(1000).GlyphOutline(1); !ok || len(segs) == 0 {
		t.Error("a glyph drawn from a subroutine came out empty")
	}
	// A subroutine number nothing answers to is ignored rather than fatal.
	bad := append(t1num(0, 600), t1HSbw)
	bad = append(bad, t1num(99)...)
	bad = append(bad, t1CallSubr, t1EndChar)
	f, err = ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": bad},
		order:  []string{".notdef", "A"},
		subrs:  [][]byte{body},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.NewFace(1000).GlyphOutline(1); !ok {
		t.Error("a call to a subroutine that is not there was fatal")
	}
}

func TestHintReplacementCallsTheSubroutineItAsksFor(t *testing.T) {
	// The idiom is "n 1 3 callothersubr pop callsubr": the program pushes the
	// number of the subroutine holding the hints it wants, and reads it
	// straight back. Handing anything else back calls the wrong subroutine —
	// and on a real corpus that came out as an empty letter i.
	body := append(t1num(50, 50), t1RMoveTo)
	body = append(body, t1num(100)...)
	body = append(body, t1HLineTo)
	body = append(body, t1num(100)...)
	body = append(body, t1VLineTo)
	body = append(body, t1num(-100)...)
	body = append(body, t1HLineTo, t1ClosePath, t1Return)

	// Subroutine 0 is a decoy that draws nothing; subroutine 1 draws the box.
	glyph := append(t1num(0, 600), t1HSbw)
	glyph = append(glyph, t1num(1, 1, 3)...)
	glyph = append(glyph, t1Escape, t1CallOtherSubr, t1Escape, t1Pop, t1CallSubr, t1EndChar)

	f, err := ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": glyph},
		order:  []string{".notdef", "A"},
		subrs:  [][]byte{{t1Return}, body},
	}))
	if err != nil {
		t.Fatal(err)
	}
	segs, ok := f.NewFace(1000).GlyphOutline(1)
	if !ok || len(segs) == 0 {
		t.Error("hint replacement called the wrong subroutine and the glyph came out empty")
	}
}

// drawn builds a one-glyph program from a charstring and gives back what it
// drew, so an operator can be checked on its own.
func drawn(t *testing.T, cs []byte, subrs [][]byte) []contour {
	t.Helper()
	f, err := ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": cs},
		order:  []string{".notdef", "A"},
		subrs:  subrs,
	}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.t1.outline(1)
	if err != nil {
		t.Fatalf("the charstring would not run: %v", err)
	}
	return out
}

// points counts every point of every contour.
func points(cs []contour) int {
	n := 0
	for _, c := range cs {
		n += len(c)
	}
	return n
}

func TestEveryTypeOneDrawingOperator(t *testing.T) {
	start := append(t1num(0, 600), t1HSbw)
	start = append(start, t1num(0, 0)...)
	start = append(start, t1RMoveTo)
	cases := []struct {
		name string
		body []byte
		// A curve is flattened into cffCurveSteps points; a line is one.
		want int
	}{
		{"rlineto", append(t1num(10, 20), t1RLineTo), 1},
		{"hlineto", append(t1num(10), t1HLineTo), 1},
		{"vlineto", append(t1num(10), t1VLineTo), 1},
		{"rrcurveto", append(t1num(10, 0, 10, 10, 0, 10), t1RRCurveTo), cffCurveSteps},
		{"hvcurveto", append(t1num(10, 10, 10, 10), t1HVCurveTo), cffCurveSteps},
		{"vhcurveto", append(t1num(10, 10, 10, 10), t1VHCurveTo), cffCurveSteps},
		{"hmoveto", append(t1num(10), t1HMoveTo), 1},
		{"vmoveto", append(t1num(10), t1VMoveTo), 1},
		{"hstem", append(t1num(0, 10), t1HStem), 0},
		{"vstem", append(t1num(0, 10), t1VStem), 0},
		{"closepath", []byte{t1ClosePath}, 0},
	}
	for _, c := range cases {
		cs := append(append([]byte{}, start...), c.body...)
		cs = append(cs, t1EndChar)
		got := points(drawn(t, cs, nil))
		// The opening move is one point, and a second move starts a contour
		// of its own with one more.
		if got != 1+c.want {
			t.Errorf("%s drew %d points, want %d", c.name, got, 1+c.want)
		}
	}
}

func TestTypeOneOperatorsThatAreNotDrawing(t *testing.T) {
	// div, pop, setcurrentpoint, sbw, the three-stem hints and dotsection all
	// have to be understood, even though none of them puts ink anywhere.
	start := append(t1num(0, 600), t1HSbw)
	cases := [][]byte{
		// 40 divided by 2 is the x of a move: twenty across.
		append(append(t1num(40, 2), t1Escape, t1Div), append(t1num(0), t1RMoveTo)...),
		// A division by nothing leaves the stack as it was rather than
		// dividing.
		append(append(t1num(40, 0), t1Escape, t1Div), append(t1num(0, 0), t1RMoveTo)...),
		{t1Escape, t1DotSection},
		append(t1num(0, 10, 0, 10, 0, 10), t1Escape, t1VStem3),
		append(t1num(0, 10, 0, 10, 0, 10), t1Escape, t1HStem3),
		append(t1num(0, 0, 600, 0), t1Escape, t1Sbw),
		append(t1num(10, 10), t1Escape, t1SetCurrentPoint),
		{t1Escape, t1Pop}, // with nothing to pop
		{t1Escape, 99},    // an escaped operator this format has not got
		{26},              // and a plain one, since anything above 31 is a number
	}
	for i, body := range cases {
		cs := append(append([]byte{}, start...), body...)
		cs = append(cs, t1EndChar)
		if _, err := ParseType1(buildType1(t1Options{
			glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": cs},
			order:  []string{".notdef", "A"},
		})); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		drawn(t, cs, nil) // it must run without error
	}
}

func TestTypeOneOperatorsGivenTooFewOperands(t *testing.T) {
	// A charstring that gives an operator less than it needs is malformed;
	// what is drawn so far is kept and the operator is passed over, rather
	// than the glyph being lost.
	start := append(t1num(0, 600), t1HSbw)
	bodies := [][]byte{
		{t1RMoveTo}, {t1HMoveTo}, {t1VMoveTo}, {t1RLineTo}, {t1HLineTo},
		{t1VLineTo}, {t1RRCurveTo}, {t1HVCurveTo}, {t1VHCurveTo},
		{t1CallSubr}, {t1Escape, t1Div}, {t1Escape, t1Sbw},
		{t1Escape, t1SetCurrentPoint}, {t1Escape, t1Seac},
		{t1Escape, t1CallOtherSubr},
	}
	for i, body := range bodies {
		cs := append(append([]byte{}, start...), body...)
		cs = append(cs, t1EndChar)
		drawn(t, cs, nil)
		_ = i
	}
	// And one that gives hsbw too few operands never sets a width.
	f, err := ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": {t1HSbw, t1EndChar}},
		order:  []string{".notdef", "A"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.NewFace(1000).AdvanceIndex(1); got != 0 {
		t.Errorf("a glyph with no width is %d wide", got)
	}
}

func TestATypeOneAccentedGlyph(t *testing.T) {
	// seac draws one glyph on top of another, both named by their Standard
	// Encoding codes: 'A' is 65 and the acute accent is 194.
	acute := append(t1num(0, 300), t1HSbw)
	acute = append(acute, t1num(0, 700)...)
	acute = append(acute, t1RMoveTo)
	acute = append(acute, t1num(50)...)
	acute = append(acute, t1HLineTo)
	acute = append(acute, t1num(50)...)
	acute = append(acute, t1VLineTo, t1ClosePath, t1EndChar)

	composed := append(t1num(0, 600), t1HSbw)
	composed = append(composed, t1num(0, 0, 100, 65, 194)...)
	composed = append(composed, t1Escape, t1Seac)

	f, err := ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{
			".notdef": {t1EndChar}, "A": t1Box(600), "acute": acute, "Aacute": composed,
		},
		order: []string{".notdef", "A", "acute", "Aacute"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	gid, ok := f.GlyphIndexByName("Aacute")
	if !ok {
		t.Fatal("the composed glyph is not there")
	}
	whole, err := f.t1.outline(int(gid))
	if err != nil {
		t.Fatal(err)
	}
	base, _ := f.t1.outlineByName("A", 0)
	mark, _ := f.t1.outlineByName("acute", 0)
	if len(whole) != len(base)+len(mark) {
		t.Errorf("the composed glyph has %d contours, want %d", len(whole), len(base)+len(mark))
	}

	// A code the standard encoding does not name cannot be composed with.
	bad := append(t1num(0, 600), t1HSbw)
	bad = append(bad, t1num(0, 0, 100, 65, 0)...)
	bad = append(bad, t1Escape, t1Seac)
	f, err = ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": t1Box(600), "X": bad},
		order:  []string{".notdef", "A", "X"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	gid, _ = f.GlyphIndexByName("X")
	if _, err := f.t1.outline(int(gid)); err == nil {
		t.Error("composing with a code that names nothing was accepted")
	}

	// A glyph composed of a glyph the font has not got.
	missing := append(t1num(0, 600), t1HSbw)
	missing = append(missing, t1num(0, 0, 100, 66, 194)...) // 'B'
	missing = append(missing, t1Escape, t1Seac)
	f, err = ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": t1Box(600), "X": missing},
		order:  []string{".notdef", "A", "X"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	gid, _ = f.GlyphIndexByName("X")
	if _, err := f.t1.outline(int(gid)); err == nil {
		t.Error("composing with a glyph the font has not got was accepted")
	}
}

func TestATypeOneFlex(t *testing.T) {
	// A flex is a curve so shallow the format draws it as seven collected
	// points rather than as two curves, through othersubrs 1, 2 and 0.
	cs := append(t1num(0, 600), t1HSbw)
	cs = append(cs, t1num(0, 0)...)
	cs = append(cs, t1RMoveTo)
	// 1 callothersubr begins it.
	cs = append(cs, t1num(0, 1)...)
	cs = append(cs, t1Escape, t1CallOtherSubr)
	for i := 0; i < 7; i++ {
		cs = append(cs, t1num(10, 5)...)
		cs = append(cs, t1RMoveTo)
		cs = append(cs, t1num(0, 2)...)
		cs = append(cs, t1Escape, t1CallOtherSubr)
	}
	// 0 callothersubr ends it, taking the flex height and the point it ends
	// at — three arguments, then the count and the number.
	cs = append(cs, t1num(50, 70, 35, 3, 0)...)
	cs = append(cs, t1Escape, t1CallOtherSubr)
	cs = append(cs, t1Escape, t1Pop, t1Escape, t1Pop)
	cs = append(cs, t1ClosePath, t1EndChar)

	got := points(drawn(t, cs, nil))
	// The opening move, then two flattened curves.
	if want := 1 + 2*cffCurveSteps; got != want {
		t.Errorf("a flex drew %d points, want %d", got, want)
	}

	// A flex that never collected its seven points draws nothing rather than
	// reaching past the end of the list.
	short := append(t1num(0, 600), t1HSbw)
	short = append(short, t1num(0, 0)...)
	short = append(short, t1RMoveTo)
	short = append(short, t1num(0, 1)...)
	short = append(short, t1Escape, t1CallOtherSubr)
	short = append(short, t1num(0, 0)...)
	short = append(short, t1Escape, t1CallOtherSubr)
	short = append(short, t1EndChar)
	drawn(t, short, nil)
}

func TestAnOthersubrThisFormatHasNotGot(t *testing.T) {
	// A program calling an othersubr of its own gets its arguments handed
	// straight back, so that it carries on rather than stops.
	cs := append(t1num(0, 600), t1HSbw)
	cs = append(cs, t1num(7, 9, 2, 14)...) // two arguments to othersubr 14
	cs = append(cs, t1Escape, t1CallOtherSubr)
	cs = append(cs, t1Escape, t1Pop) // reads back 7
	cs = append(cs, t1Escape, t1Pop) // reads back 9
	cs = append(cs, t1RMoveTo)
	cs = append(cs, t1num(10)...)
	cs = append(cs, t1HLineTo, t1EndChar)
	if got := points(drawn(t, cs, nil)); got != 2 {
		t.Errorf("drew %d points, want 2", got)
	}
	// One that says it takes more arguments than are there is passed over.
	bad := append(t1num(0, 600), t1HSbw)
	bad = append(bad, t1num(9, 14)...)
	bad = append(bad, t1Escape, t1CallOtherSubr, t1EndChar)
	drawn(t, bad, nil)
}

// rewrap puts a private half back into a program.
func rewrap(private []byte) []byte {
	clear := []byte("%!PS-AdobeFont-1.0\n/Encoding StandardEncoding def\ncurrentfile eexec\n")
	return append(clear, eexecEncrypt(private, eexecKey, 4)...)
}

func TestTypeOneProgramsThatAreWrongInEveryPlaceTheyCanBe(t *testing.T) {
	cases := []struct {
		name    string
		private string
		glyphs  int
		refuses bool
	}{
		{"a Subrs count that is not a number", "/lenIV 4 def\n/Subrs many array\n" + oneGlyph(), 1, false},
		{"a Subrs count past anything sane", "/lenIV 4 def\n/Subrs 99999999 array\n" + oneGlyph(), 1, false},
		{"a Subrs header and nothing after it", "/lenIV 4 def\n/Subrs\n", 0, true},
		{"a Subrs entry that never arrives", "/lenIV 4 def\n/Subrs 2 array\n" + oneGlyph(), 1, false},
		{"a lenIV that is not a number", "/lenIV wrong def\n" + oneGlyph(), 1, false},
		{"a lenIV nobody uses", "/lenIV 99 def\n" + oneGlyph(), 1, false},
		{"a charstring length that is not a number", "/CharStrings 1 dict dup begin\n/A xx RD zzz ND\nend\n", 0, true},
		{"a charstring longer than what is left", "/CharStrings 1 dict dup begin\n/A 9999 RD ab ND\nend\n", 0, true},
		{"a charstring with nothing after its name", "/CharStrings 1 dict dup begin\n/A\n", 0, true},
		{"the same glyph twice", "/lenIV 4 def\n" + twoOfTheSameGlyph(), 1, false},
	}
	for _, c := range cases {
		f, err := ParseType1(rewrap([]byte("XXXX dup /Private 8 dict dup begin\n" + c.private)))
		if c.refuses {
			if err == nil {
				t.Errorf("%s was accepted", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if f.NumGlyphs() != c.glyphs {
			t.Errorf("%s: %d glyphs, want %d", c.name, f.NumGlyphs(), c.glyphs)
		}
	}
}

// oneGlyph is a CharStrings dictionary holding a single drawable A.
func oneGlyph() string {
	enc := eexecEncrypt(t1Box(600), charstringR, 4)
	return fmt.Sprintf("/CharStrings 1 dict dup begin\n/A %d RD %s ND\nend\n", len(enc), string(enc))
}

// oneEntry is a single CharStrings entry, without the dictionary around it.
func oneEntry() string {
	enc := eexecEncrypt(t1Box(600), charstringR, 4)
	return fmt.Sprintf("/A %d RD %s ND\nend\n", len(enc), string(enc))
}

// twoOfTheSameGlyph is a dictionary that names the same glyph twice, which the
// first one wins.
func twoOfTheSameGlyph() string {
	enc := eexecEncrypt(t1Box(600), charstringR, 4)
	entry := fmt.Sprintf("/A %d RD %s ND\n", len(enc), string(enc))
	return "/CharStrings 2 dict dup begin\n" + entry + entry + "end\n"
}

func TestTypeOneWidthsOfEveryShape(t *testing.T) {
	// The advance is the operand of hsbw or sbw, however the numbers in front
	// of it were written; a charstring that begins with anything else has no
	// width to give.
	cases := []struct {
		name string
		cs   []byte
		want int
		ok   bool
	}{
		{"a small width", append(t1num(0, 100), t1HSbw), 100, true},
		{"a middling width", append(t1num(0, 600), t1HSbw), 600, true},
		{"a width as four bytes", append(t1num(0, 100000), t1HSbw), 100000, true},
		{"a negative side bearing", append(t1num(-50, 600), t1HSbw), 600, true},
		{"sbw", append(t1num(0, 0, 600, 0), t1Escape, t1Sbw), 600, true},
		{"hsbw with one operand", append(t1num(0), t1HSbw), 0, false},
		{"sbw with three operands", append(t1num(0, 0, 600), t1Escape, t1Sbw), 0, false},
		{"an escape that is not sbw", []byte{t1Escape, t1Div}, 0, false},
		{"an operator that is not a width", []byte{t1EndChar}, 0, false},
		{"nothing at all", nil, 0, false},
		{"an operand cut short", []byte{247}, 0, false},
		{"a negative operand cut short", []byte{251}, 0, false},
		{"a four-byte operand cut short", []byte{255, 0, 0}, 0, false},
		{"an escape at the very end", append(t1num(0, 600), t1Escape), 0, false},
	}
	for _, c := range cases {
		_, w, ok := type1Width(c.cs)
		if ok != c.ok {
			t.Errorf("%s: found = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if ok && int(w) != c.want {
			t.Errorf("%s: width %v, want %d", c.name, w, c.want)
		}
	}
}

func TestTypeOneEncodingsWrittenEveryWay(t *testing.T) {
	// A program's /Encoding array is built a name at a time, and a line that
	// is not one of those is passed over rather than taken for one.
	f, err := ParseType1(rewrapWithClear(
		"/Encoding 256 array\ndup 1 /A put\ndup notanumber /B put\ndup 2 B put\ndup 3 /B\ndup 4 /B put\n",
		"/lenIV 4 def\n"+oneGlyph()))
	if err != nil {
		t.Fatal(err)
	}
	if gid, ok := f.GlyphIndexByCode(1); !ok || gid != 0 {
		t.Errorf("code 1 is glyph %d (%v)", gid, ok)
	}
	if _, ok := f.GlyphIndexByCode(4); ok {
		t.Error("code 4 names a glyph the font has not got and was found anyway")
	}
	// An /Encoding whose entries are all unusable is as good as none, and the
	// standard one is used instead.
	f, err = ParseType1(rewrapWithClear("/Encoding 256 array\ndup x /A put\n", "/lenIV 4 def\n"+oneGlyph()))
	if err != nil {
		t.Fatal(err)
	}
	if gid, ok := f.GlyphIndexByCode('A'); !ok || gid != 0 {
		t.Errorf("the standard encoding was not used: 'A' is glyph %d (%v)", gid, ok)
	}
	// A program with no /Encoding at all is read the same way.
	f, err = ParseType1(rewrapWithClear("", "/lenIV 4 def\n"+oneGlyph()))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.GlyphIndexByCode('A'); !ok {
		t.Error("a program with no encoding could not be read by code")
	}
	if _, ok := f.GlyphIndexByCode(200); ok {
		t.Error("a code outside the standard encoding was found")
	}
}

// rewrapWithClear builds a program with the given readable and private halves.
func rewrapWithClear(clear, private string) []byte {
	head := []byte("%!PS-AdobeFont-1.0\n" + clear + "currentfile eexec\n")
	return append(head, eexecEncrypt([]byte("XXXX dup /Private 8 dict dup begin\n"+private), eexecKey, 4)...)
}

func TestATypeOneProgramWrittenInHexadecimal(t *testing.T) {
	// A .pfa writes its private half as hexadecimal, and the specification
	// says to look at the first four bytes to tell.
	prog := simpleType1(t1Options{container: "pfa"})
	if _, err := ParseType1(prog); err != nil {
		t.Fatalf("a hexadecimal program was refused: %v", err)
	}
	// One whose first four bytes are not all hexadecimal is binary.
	if isHexSection([]byte("  zz11223344")) {
		t.Error("a binary section was read as hexadecimal")
	}
	if isHexSection([]byte("   ")) {
		t.Error("a section of nothing but spaces was read as hexadecimal")
	}
	if !isHexSection([]byte(" \n abCD1234ef567890")) {
		t.Error("a hexadecimal section was read as binary")
	}
	// Four hexadecimal digits are not enough to tell: a binary section
	// begins with four random bytes, and four random bytes are all
	// hexadecimal about once in twenty thousand times.
	if isHexSection([]byte("abcd\xd9\xd6\x6f\x63")) {
		t.Error("four digits were taken for a hexadecimal section")
	}
	// The odd digit at the end of a hexadecimal run is dropped rather than
	// half-read.
	if got := unhex([]byte("41 42 4")); string(got) != "AB" {
		t.Errorf("unhex gave %q", got)
	}
}

func TestNotdefIsPutFirstWhereverItWasWritten(t *testing.T) {
	// Glyph zero means the same thing in every format, so a program that
	// writes .notdef somewhere in the middle still has it at zero.
	f, err := ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{"A": t1Box(600), ".notdef": {t1EndChar}, "B": t1Box(700)},
		order:  []string{"A", ".notdef", "B"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := f.GlyphName(0); name != ".notdef" {
		t.Errorf("glyph 0 is %q", name)
	}
	if gid, _ := f.GlyphIndexByName("A"); gid != 1 {
		t.Errorf("A is glyph %d", gid)
	}
	if gid, _ := f.GlyphIndexByName("B"); gid != 2 {
		t.Errorf("B is glyph %d", gid)
	}
	// A program with no .notdef leaves the order it was written in.
	f, err = ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{"A": t1Box(600), "B": t1Box(700)},
		order:  []string{"A", "B"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := f.GlyphName(0); name != "A" {
		t.Errorf("glyph 0 is %q, want A", name)
	}
}

func TestATypeOneOutlineOfAGlyphThatIsNotThere(t *testing.T) {
	f, err := ParseType1(simpleType1(t1Options{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.t1.outline(99); err == nil {
		t.Error("a glyph past the end was drawn")
	}
	if _, err := f.t1.outlineByName("Omega", 0); err == nil {
		t.Error("a glyph the font has not got was drawn")
	}
	// A charstring that calls itself for ever is stopped.
	deep := append(t1num(0, 600), t1HSbw)
	deep = append(deep, t1num(0)...)
	deep = append(deep, t1CallSubr, t1EndChar)
	loop := append(t1num(0), t1CallSubr, t1Return)
	f, err = ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": deep},
		order:  []string{".notdef", "A"},
		subrs:  [][]byte{loop},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.t1.outline(1); err == nil {
		t.Error("a subroutine calling itself for ever was not stopped")
	}
	// So is an accent composed of itself.
	self := append(t1num(0, 600), t1HSbw)
	self = append(self, t1num(0, 0, 0, 65, 65)...)
	self = append(self, t1Escape, t1Seac)
	f, err = ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1EndChar}, "A": self},
		order:  []string{".notdef", "A"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.t1.outline(1); err == nil {
		t.Error("an accent composed of itself was not stopped")
	}
	// A charstring whose operand runs off the end is refused.
	if _, err := f.t1.outlineByName(".notdef", 0); err != nil {
		t.Fatal(err)
	}
	f, err = ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {247}},
		order:  []string{".notdef"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.t1.outline(0); err == nil {
		t.Error("a charstring cut off mid-operand was accepted")
	}
	// And one that pushes more than the stack holds.
	var many []byte
	for i := 0; i < maxT1Stack+2; i++ {
		many = append(many, t1num(1)...)
	}
	f, err = ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": append(many, t1EndChar)},
		order:  []string{".notdef"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.t1.outline(0); err == nil {
		t.Error("a charstring with too many operands was accepted")
	}
	// A charstring ending in an escape has nothing to escape to.
	f, err = ParseType1(buildType1(t1Options{
		glyphs: map[string][]byte{".notdef": {t1Escape}},
		order:  []string{".notdef"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.t1.outline(0); err == nil {
		t.Error("a charstring ending in an escape was accepted")
	}
}

func TestTheLastFewWaysAProgramCanBeWrong(t *testing.T) {
	// An eexec section of nothing decrypts to nothing.
	if _, err := ParseType1([]byte("%!PS\ncurrentfile eexec\n")); err == nil {
		t.Error("a program with an empty private half was accepted")
	}
	if got := eexecDecrypt([]byte{1, 2}, eexecKey, 4); got != nil {
		t.Errorf("decrypting two bytes with four to skip gave %v", got)
	}
	// A pfb file that ends without its end-of-file segment.
	prog := simpleType1(t1Options{container: "pfb"})
	if _, err := ParseType1(prog[:len(prog)-2]); err != nil {
		t.Errorf("a pfb with no end marker was refused: %v", err)
	}
	// A hexadecimal digit in every case.
	for c, want := range map[byte]byte{'7': 7, 'B': 11, 'e': 14} {
		if got := hexVal(c); got != want {
			t.Errorf("hexVal(%q) = %d, want %d", c, got, want)
		}
	}
	// A FontMatrix that opens and never closes.
	f, err := ParseType1(rewrapWithClear("/FontMatrix [0.001 0 0 0.001 0 0\n", "/lenIV 4 def\n"+oneGlyph()))
	if err != nil {
		t.Fatal(err)
	}
	if f.UnitsPerEm() != 1000 {
		t.Errorf("UnitsPerEm() = %d", f.UnitsPerEm())
	}
	// A Subrs array that says two and gives one.
	enc := eexecEncrypt([]byte{t1Return}, charstringR, 4)
	priv := fmt.Sprintf("/lenIV 4 def\n/Subrs 2 array\ndup 0 %d RD %s NP\n", len(enc), string(enc)) + oneGlyph()
	if _, err := ParseType1(rewrap([]byte("XXXX dup begin\n" + priv))); err != nil {
		t.Errorf("a short Subrs array was refused: %v", err)
	}
	// A Subrs array whose entries never arrive at all.
	if _, err := ParseType1(rewrap([]byte("XXXX dup begin\n/lenIV 4 def\n/Subrs 2 array\n/CharStrings 1 dict begin\n" + oneEntry()))); err != nil {
		t.Errorf("a Subrs array with no entries was refused: %v", err)
	}
	// A name with nothing but space after it.
	if _, err := ParseType1(rewrap([]byte("XXXX dup begin\n/CharStrings 1 dict dup begin\n/   "))); err == nil {
		t.Error("a name followed by nothing was accepted")
	}
	// A length that fits but whose bytes do not follow.
	if _, err := ParseType1(rewrap([]byte("XXXX dup begin\n/CharStrings 1 dict dup begin\n/A 2 RD"))); err == nil {
		t.Error("a charstring whose bytes never arrive was accepted")
	}
	// A charstring entry with a name and nothing at all after it.
	if _, err := ParseType1(rewrap([]byte("XXXX dup begin\n/CharStrings 1 dict dup begin\n/A"))); err == nil {
		t.Error("a name with nothing after it was accepted")
	}
	// A charstring whose declared length is longer than what is left.
	if _, err := ParseType1(rewrap([]byte("XXXX dup begin\n/CharStrings 1 dict dup begin\n/A 40 RD ab"))); err == nil {
		t.Error("a charstring longer than the file was accepted")
	}
	// A negative operand in the two-byte form.
	if v, _, isNum, ok := t1Operand([]byte{252, 100}, 0); !ok || !isNum || v != -(1*256+100+108) {
		t.Errorf("a negative operand read as %v", v)
	}
	// Hint replacement with no argument at all falls back rather than
	// reaching past the end of the stack.
	cs := append(t1num(0, 600), t1HSbw)
	cs = append(cs, t1num(0, 3)...)
	cs = append(cs, t1Escape, t1CallOtherSubr, t1Escape, t1Pop, t1EndChar)
	drawn(t, cs, nil)
	// An othersubr handed more arguments than the interpreter will keep.
	cs = append(t1num(0, 600), t1HSbw)
	for i := 0; i < maxT1PostScriptSize+4; i++ {
		cs = append(cs, t1num(1)...)
	}
	cs = append(cs, t1num(maxT1PostScriptSize+4, 14)...)
	cs = append(cs, t1Escape, t1CallOtherSubr, t1EndChar)
	drawn(t, cs, nil)
}

func TestASubroutineThatRunsOffItsEnd(t *testing.T) {
	// A subroutine with no return runs to its end and comes back anyway.
	body := append(t1num(50, 50), t1RMoveTo)
	glyph := append(t1num(0, 600), t1HSbw)
	glyph = append(glyph, t1num(0)...)
	glyph = append(glyph, t1CallSubr)
	glyph = append(glyph, t1num(10)...)
	glyph = append(glyph, t1HLineTo, t1EndChar)
	if got := points(drawn(t, glyph, [][]byte{body})); got != 2 {
		t.Errorf("drew %d points, want 2", got)
	}
}

func TestDotSectionIsPassedOverRatherThanRefused(t *testing.T) {
	// dotsection is a Type 1 hint operator that fonts converted to CFF still
	// carry. Refusing it took the dot off every i, j, colon and semicolon in
	// such a font — 30 letters across the corpus, silently blank.
	cs := append(t2nums(600, 50, 50), 21) // width, then rmoveto
	cs = append(cs, 12, 0)                // dotsection
	cs = append(cs, byte(139+100), byte(139), 5)
	cs = append(cs, byte(139), byte(139+100), 5)
	cs = append(cs, 14)
	data := buildCFF(cffOptions{
		glyphs:      [][]byte{boxGlyph(0), cs},
		charsetSIDs: []int{74}, // "i"
		includePriv: true,
	})
	f, err := ParseCFF(data)
	if err != nil {
		t.Fatal(err)
	}
	segs, ok := f.NewFace(1000).GlyphOutline(1)
	if !ok || len(segs) == 0 {
		t.Error("a glyph carrying dotsection came out empty")
	}
}
