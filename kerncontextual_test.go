package opentype

import "testing"

// ⛔⛔ A PAIR QUERY CANNOT SPEAK FOR A FONT THAT KERNS CONTEXTUALLY, AND IT USED
// TO ANSWER 0 WITHOUT SAYING SO.
//
// kernCovered walks the lookups the `kern` feature selects and takes the
// pair-adjustment subtables, skipping the rest with a `continue`. GPOS lookup
// type 7 (Contextual) and type 8 (Chained Contextual) position pairs too, and the
// shaping path applies them — contextPos1/2/3 each carry an
// `apply(ctx *posContext, i int)`. So the two APIs disagree about the same font,
// and the pair side's disagreement is silent: Kerning returns 0, which is exactly
// what it returns for a pair the font deliberately leaves alone. The comment on
// Kerning is careful about that distinction for pairPos ("a pair listed with no
// adjustment is the font saying nothing here, which is an answer and not a
// silence") and could not make it here.
//
// ⚠ It is NOT fixable by teaching kernCovered those types. A contextual rule is
// matched against the glyphs AROUND a position — backtrack, input and lookahead
// coverage — and a caller holding only (left, right) has none of that. The
// question has no answer for such a font, so the API's job is to say which
// question to ask instead.
//
// The consumer that paid for the silence: go-tex/engine's text path calls
// KernUnits (its font.go:337), and go-tex/engine#716 records a kerning defect of
// 1.12pt per "AV" that was invisible to every number that measurement steers by.
func TestKerningIsContextualIsTrueOnlyWhenAPairQueryCannotAnswer(t *testing.T) {
	// A pair-adjustment kern of -50 between A and V: the ORDINARY font, where a
	// pair query is exactly right.
	pairOnly := func() []byte {
		sub := buildPairPos1(buildCoverage1(1), vfXAdvance, 0, [][]byte{
			buildPairSet(vfXAdvance, 0, []pv{{second: 2, xa1: -50}}),
		})
		return buildLayoutTable(
			[]tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}},
			[]tFeature{{tag: "kern", lookups: []uint16{0}}},
			[][]byte{buildLookup(2, [][]byte{sub})})
	}
	// The same kern expressed CONTEXTUALLY: a format-3 contextual positioning
	// subtable over A then V, delegating position 0 to a single-adjustment
	// lookup. The `kern` feature selects the contextual lookup; the lookup it
	// delegates to is not in the feature.
	contextual := func() []byte {
		st := buildContextPos3(
			[][]byte{buildCoverage1(1), buildCoverage1(2)},
			[]plr{{seq: 0, lookup: 1}})
		return buildLayoutTable(
			[]tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}},
			[]tFeature{{tag: "kern", lookups: []uint16{0}}},
			[][]byte{
				buildLookup(7, [][]byte{st}),                      // the contextual one
				buildLookup(1, [][]byte{singleAdvLookup(1, -50)}), // what it delegates to
			})
	}

	for _, c := range []struct {
		name string
		gpos []byte
		want bool
		why  string
	}{
		{
			name: "pair adjustment only",
			gpos: pairOnly(), want: false,
			why: "the ordinary case: a pair query answers this font exactly, and a " +
				"true here would send every caller down the shaping path for nothing",
		},
		{
			name: "a contextual kern lookup",
			gpos: contextual(), want: true,
			why: "KernUnits returns 0 for this font's A/V and the shaping path does not; " +
				"a caller must be able to learn that before it believes the 0",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := makeFont(t, [][]byte{nil, squareGlyph(), squareGlyph()},
				map[rune]uint16{'A': 1, 'V': 2},
				map[string][]byte{"GPOS": c.gpos})
			if f.gpos == nil {
				t.Fatal("no GPOS parsed: the fixture is wrong, not the answer")
			}
			fc := f.NewFace(1000)
			if got := fc.KerningIsContextual(); got != c.want {
				t.Errorf("KerningIsContextual() = %v, want %v — %s", got, c.want, c.why)
			}
		})
	}
}

// ⭐ AND THE DISAGREEMENT ITSELF, because the flag above is only worth having if
// the thing it warns about is real. Same font, same pair, two APIs:
//
//	KernUnits    — the pair path, skips the contextual lookup, answers 0
//	the flag     — says so
//
// If this test ever shows KernUnits returning -50 for the contextual fixture, the
// pair path has learned contextual lookups and the flag has become a lie: delete
// it rather than adjusting the number.
func TestThePairPathAnswersZeroWhereTheFlagSaysItCannotAnswer(t *testing.T) {
	st := buildContextPos3(
		[][]byte{buildCoverage1(1), buildCoverage1(2)},
		[]plr{{seq: 0, lookup: 1}})
	gpos := buildLayoutTable(
		[]tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}},
		[]tFeature{{tag: "kern", lookups: []uint16{0}}},
		[][]byte{
			buildLookup(7, [][]byte{st}),
			buildLookup(1, [][]byte{singleAdvLookup(1, -50)}),
		})
	f := makeFont(t, [][]byte{nil, squareGlyph(), squareGlyph()},
		map[rune]uint16{'A': 1, 'V': 2},
		map[string][]byte{"GPOS": gpos})
	fc := f.NewFace(1000)
	if got := fc.KernUnits('A', 'V'); got != 0 {
		t.Errorf("KernUnits('A','V') = %v on a contextual font; if the pair path now "+
			"reads contextual lookups, KerningIsContextual is a lie and goes, not this "+
			"assertion", got)
	}
	if !fc.KerningIsContextual() {
		t.Error("the pair path answered 0 and the face said a pair query was authoritative: " +
			"that is the silence this flag exists to break")
	}
}

// ⛔ A MALFORMED FONT MUST NOT CRASH THE QUESTION. A `kern` feature may name a
// lookup index past the end of the lookup list — fonts in the wild do, and this
// package tolerates such tails on purpose (#47, #48 are the same policy for hmtx
// and loca). kernCovered right above carries the same `li >= len(p.lookups)`
// guard for the same reason.
//
// ⚠ And this guard is NOT the dead clamp I removed from go-typeset/linebreak this
// afternoon, which looked identical. The difference is where the number comes
// from: there it was an internal caller that could not produce an out-of-range
// value, here it is a byte in a file nobody controls. A guard over untrusted input
// is reachable by construction; one over a computed internal bound is not.
func TestAKernFeatureNamingAMissingLookupIsNotContextual(t *testing.T) {
	sub := buildPairPos1(buildCoverage1(1), vfXAdvance, 0, [][]byte{
		buildPairSet(vfXAdvance, 0, []pv{{second: 2, xa1: -50}}),
	})
	// The feature names lookups 0 AND 9; only lookup 0 exists.
	gpos := buildLayoutTable(
		[]tScript{{tag: "DFLT", def: &tLangSys{required: 0xFFFF, features: []uint16{0}}}},
		[]tFeature{{tag: "kern", lookups: []uint16{0, 9}}},
		[][]byte{buildLookup(2, [][]byte{sub})})
	f := makeFont(t, [][]byte{nil, squareGlyph(), squareGlyph()},
		map[rune]uint16{'A': 1, 'V': 2},
		map[string][]byte{"GPOS": gpos})
	if f.gpos == nil {
		t.Fatal("no GPOS parsed: the fixture is wrong, not the answer")
	}
	fc := f.NewFace(1000)
	if fc.KerningIsContextual() {
		t.Error("a lookup index past the end of the list was read as a contextual lookup: " +
			"an unreadable index is not evidence of anything, least of all of a type this " +
			"function cannot see")
	}
	// The control: the lookup that DOES exist still answers, so the false above is
	// about the missing index and not about a fixture that parsed to nothing.
	if got := fc.KernUnits('A', 'V'); got != -50 {
		t.Errorf("KernUnits('A','V') = %v, want -50", got)
	}
}

// ⛔ A font with no GPOS at all must not claim its kerning is contextual: the
// legacy `kern` table is pair adjustment by construction, and a true here would
// push every such caller onto a shaping path that has nothing more to offer.
func TestAFontWithoutGPOSIsNotContextual(t *testing.T) {
	legacy := buildKernTable([][]byte{buildKernFormat0([]kernPair{
		{left: 1, right: 2, value: -50},
	})})
	f := makeFont(t, [][]byte{nil, squareGlyph(), squareGlyph()},
		map[rune]uint16{'A': 1, 'V': 2},
		map[string][]byte{"kern": legacy})
	if f.gpos != nil {
		t.Fatal("the fixture grew a GPOS table; this test is about not having one")
	}
	fc := f.NewFace(1000)
	if fc.KerningIsContextual() {
		t.Error("a font with only the legacy kern table reported contextual kerning")
	}
	// The control: the legacy pair still answers, so the false above is about the
	// absence of contextual lookups and not about an absent font.
	if got := fc.KernUnits('A', 'V'); got != -50 {
		t.Errorf("KernUnits('A','V') = %v, want -50", got)
	}
}
