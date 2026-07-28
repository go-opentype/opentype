// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import "sort"

// This file exposes read-only access to the raw, undecoded sfnt table bytes the
// font was parsed from. A consumer that embeds a font (a PDF or EPUB writer) or
// inspects a table this package does not decode can read the exact table data
// without re-parsing the container itself.

// TableTags returns the four-byte tags of every table in the font, sorted
// lexicographically. The returned slice is freshly allocated and owned by the
// caller. Tags retain their sfnt spelling, including any trailing space (for
// example "cvt " and "OS/2").
func (f *Font) TableTags() []string {
	tags := make([]string, 0, len(f.tables))
	for t := range f.tables {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	return tags
}

// Table returns a copy of the raw bytes of the table named by its four-byte tag,
// and ok reporting whether the font carries that table. The tag must be the exact
// sfnt spelling including any trailing space (for example "cvt " or "OS/2"). The
// returned slice is a fresh copy the caller may retain or mutate; it never aliases
// the font's backing data.
func (f *Font) Table(tag string) (data []byte, ok bool) {
	b, ok := f.tables[tag]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), b...), true
}
