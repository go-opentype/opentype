// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"encoding/binary"
	"testing"
)

func TestBuildSFNTWithoutHead(t *testing.T) {
	// A table set without a head table exercises the branch that skips the
	// checkSumAdjustment patch, and an odd-length table exercises the checksum's
	// zero-padded tail.
	out := buildSFNT(versionTrueType, map[string][]byte{
		"aaaa": {1, 2, 3},    // odd length -> checksum tail padding
		"bbbb": {4, 5, 6, 7}, // 4-aligned
	})
	if binary.BigEndian.Uint16(out[4:]) != 2 {
		t.Fatalf("numTables = %d, want 2", binary.BigEndian.Uint16(out[4:]))
	}
	// The directory records are present and the bodies follow the header.
	if len(out) < 12+2*16 {
		t.Errorf("output too short: %d bytes", len(out))
	}
}

func TestMinimalCmapParses(t *testing.T) {
	// The minimal cmap must be structurally decodable on its own.
	f := &Font{}
	if err := f.parseCmap(minimalCmap()); err != nil {
		t.Fatalf("minimalCmap did not parse: %v", err)
	}
	if _, ok := f.cmap.lookup('A'); ok {
		t.Error("minimal cmap should map nothing")
	}
}
