// Copyright (c) the go-opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The brotli stream is inflated against the size its directory declares: one
// that inflates past it is refused before it is read to the end of memory, and
// a directory may not declare more than maxWOFF2Stream.
func TestWOFF2InflateIsBounded(t *testing.T) {
	src, err := os.ReadFile(ttfOracle)
	if err != nil {
		t.Skip(err)
	}
	good := sfntBuildTables(t, src)

	// Every table but the first declares its true length; the first declares
	// one byte less, so the stream holds one byte more than the directory sums.
	short := append([]buildTable(nil), good...)
	short[0].origWrong = uint32(len(short[0].data)) - 1
	if _, err := Parse(buildWOFF2(short, 0x00010000, nil)); err == nil ||
		!strings.Contains(err.Error(), "inflates past") {
		t.Errorf("a stream longer than its tables: err = %v, want the inflate bound", err)
	}

	huge := append([]buildTable(nil), good...)
	huge[0].origWrong = maxWOFF2Stream + 1
	if _, err := Parse(buildWOFF2(huge, 0x00010000, nil)); err == nil ||
		!strings.Contains(err.Error(), "more than the") {
		t.Errorf("a directory over the bound: err = %v, want the declared-size bound", err)
	}
}

// TestWOFF2LinksNoNetHTTP is why the decoder is dsnet/compress/brotli: the
// package must not import net/http, whose init (and crypto/tls's) the linker
// keeps in every importer. andybalholm/brotli, used by the tests to compress,
// is the planted positive: its root package does import net/http.
func TestWOFF2LinksNoNetHTTP(t *testing.T) {
	deps := func(pkg string) map[string]bool {
		cmd := exec.Command("go", "list", "-deps", pkg)
		cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			t.Skipf("go list unavailable: %v", err)
		}
		m := map[string]bool{}
		for _, l := range strings.Fields(string(out)) {
			m[l] = true
		}
		return m
	}
	if !deps("github.com/andybalholm/brotli")["net/http"] {
		t.Fatal("control failed: andybalholm/brotli no longer imports net/http, so this check sees nothing")
	}
	if deps(".")["net/http"] {
		t.Error("package opentype imports net/http: every importer would link it")
	}
}
