// Copyright (c) the go-opentype authors.
// SPDX-License-Identifier: BSD-3-Clause

package opentype

import (
	"bytes"
	"io"
	"os"
	"testing"

	andy "github.com/andybalholm/brotli"
	dsnet "github.com/dsnet/compress/brotli"
)

// BenchmarkBrotliInflate compares the decoder WOFF2 uses (dsnet) with the one
// it used before (andybalholm) on the same stream: the oracle TrueType,
// compressed at quality 11 the way font services ship WOFF2. Run the two
// interleaved (-count) on an idle machine before reading the ratio.
func BenchmarkBrotliInflate(b *testing.B) {
	src, err := os.ReadFile(ttfOracle)
	if err != nil {
		b.Skip(err)
	}
	var comp bytes.Buffer
	zw := andy.NewWriterLevel(&comp, andy.BestCompression)
	_, _ = zw.Write(src)
	_ = zw.Close()
	stream := comp.Bytes()

	b.Run("dsnet", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		for b.Loop() {
			r, _ := dsnet.NewReader(bytes.NewReader(stream), nil)
			if n, err := io.Copy(io.Discard, r); err != nil || n != int64(len(src)) {
				b.Fatalf("dsnet: %d bytes, %v", n, err)
			}
		}
	})
	b.Run("andybalholm", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		for b.Loop() {
			r := andy.NewReader(bytes.NewReader(stream))
			if n, err := io.Copy(io.Discard, r); err != nil || n != int64(len(src)) {
				b.Fatalf("andybalholm: %d bytes, %v", n, err)
			}
		}
	})
}
