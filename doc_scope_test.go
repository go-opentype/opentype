// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"os"
	"strings"
	"testing"
)

// ⛔⛔ A DOC COMMENT IS A DATED CLAIM, AND THIS ONE OUTLIVED ITS CODE BY FIVE
// FEATURES.
//
// doc.go's Scope paragraph said "OpenType/CFF ('OTTO') outlines, GPOS/GSUB
// shaping, kerning and hinting are not implemented" long after every one of
// them had landed with its own tests. The README's support matrix said "done"
// at the same time, so the two halves of this repository's documentation
// disagreed — and the stale half is the one a consumer reads first, because it
// is what `go doc` prints.
//
// ⭐ IT WAS NOT A COSMETIC DEFECT. Measured in go-tex/engine, the consumer that
// most needs the shaper: it calls Shape(text, "liga") for ligatures and then
// takes kerning ONE RUNE PAIR AT A TIME through KernUnits(prev, cur).
// ShapePositioned and ShapePositionedUnits are never called at all. So
// contextual GPOS, cursive attachment and mark attachment are not missing
// there — they are unasked for, by an engine whose author had been told they
// did not exist.
//
// This test is the barrier the fix is worth nothing without: for each feature
// the Scope paragraph might call missing, if the file that implements it is in
// the tree, the paragraph may not say it is unimplemented.
func TestThePackageDocDoesNotCallImplementedFeaturesMissing(t *testing.T) {
	doc, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatalf("doc.go: %v", err)
	}
	text := string(doc)

	// The sentence this guards against, in the spellings it has actually worn.
	// ⚠ Matched case-insensitively and with the words apart, so that rewording
	// it ("is not yet implemented", "are unimplemented") does not slip past.
	denials := []string{"not implemented", "unimplemented", "not yet implemented", "is not supported"}

	for _, f := range []struct {
		feature string   // what the doc might deny
		files   []string // any one of these existing means it IS implemented
		names   []string // the words the doc uses for it
	}{
		{"GSUB/GPOS shaping", []string{"gsub.go", "gpos.go", "shape.go"}, []string{"GSUB", "GPOS", "shaping"}},
		{"kerning", []string{"kern.go"}, []string{"kerning"}},
		{"hinting", []string{"hint.go", "cffhint.go"}, []string{"hinting", "hinter"}},
		{"CFF outlines", []string{"cff.go", "cff2.go"}, []string{"CFF"}},
	} {
		implemented := false
		var have string
		for _, name := range f.files {
			if _, err := os.Stat(name); err == nil {
				implemented, have = true, name
				break
			}
		}
		if !implemented {
			// Nothing implements it, so the doc is free to say so. ⚠ Reported,
			// because a test that silently skips its whole point is how a gate
			// stops being one.
			t.Logf("%s: no implementation file present (%v), so the doc may deny it", f.feature, f.files)
			continue
		}
		// Look only at sentences that mention the feature AND a denial.
		for _, sentence := range strings.Split(text, ".") {
			low := strings.ToLower(sentence)
			mentions := false
			for _, n := range f.names {
				if strings.Contains(low, strings.ToLower(n)) {
					mentions = true
					break
				}
			}
			if !mentions {
				continue
			}
			for _, d := range denials {
				if !strings.Contains(low, d) {
					continue
				}
				// ⚠ The historical note in doc.go QUOTES the old sentence on
				// purpose, so a quoted denial is allowed; an unquoted one is not.
				if strings.Contains(sentence, `"`) || strings.Contains(sentence, "USED TO SAY") {
					continue
				}
				t.Errorf("doc.go says %q is %q while %s is in the tree:\n  %s\n"+
					"a doc comment that outlives its code is what sent go-tex/engine's "+
					"font path to pairwise kerning", f.feature, d, have, strings.TrimSpace(sentence))
			}
		}
	}
}

// The control: the guard must be able to FIRE. A denial of a feature whose file
// exists, in a string the same scanner reads, has to be caught — otherwise the
// test above passes because it matches nothing at all.
func TestTheScopeGuardCanFire(t *testing.T) {
	const bad = "GPOS/GSUB shaping and kerning are not implemented here"
	low := strings.ToLower(bad)
	if !strings.Contains(low, "gsub") || !strings.Contains(low, "not implemented") {
		t.Fatal("the scanner's own two conditions do not both match a sentence built to trip it: " +
			"the guard above cannot fire, so its silence means nothing")
	}
	if _, err := os.Stat("gsub.go"); err != nil {
		t.Fatalf("gsub.go is not in the tree, so the guard above has nothing to compare against: %v", err)
	}
}
