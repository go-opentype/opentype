// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"sort"
	"testing"
)

func TestTableTagsAndTable(t *testing.T) {
	f := descFont(t, descHead(1000, 0, 0, 0, 0, 0), os2Table(2, 400, 5, 0, 0, 0, 0), nil)

	tags := f.TableTags()
	if !sort.StringsAreSorted(tags) {
		t.Errorf("TableTags not sorted: %v", tags)
	}
	want := map[string]bool{"head": true, "maxp": true, "hhea": true, "hmtx": true, "cmap": true, "loca": true, "glyf": true, "OS/2": true}
	got := map[string]bool{}
	for _, tg := range tags {
		got[tg] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf("TableTags missing %q (got %v)", w, tags)
		}
	}

	head, ok := f.Table("head")
	if !ok || len(head) != 54 {
		t.Fatalf("Table(head) ok=%v len=%d", ok, len(head))
	}
	// The returned slice is an independent copy: mutating it must not affect the
	// font's own view of the table.
	head[18] = 0xFF
	if again, _ := f.Table("head"); again[18] == 0xFF {
		t.Error("Table must return a copy, not an alias")
	}

	if _, ok := f.Table("ZZZZ"); ok {
		t.Error("Table of an absent tag should report ok=false")
	}
}
