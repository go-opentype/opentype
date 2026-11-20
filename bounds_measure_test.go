// Copyright (c) 2026 the go-opentype/opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMeasureAllocBudget is the measurement behind allocBudgetPerInputByte. It
// is skipped unless OPENTYPE_MEASURE_DIRS names directories (separated like
// PATH) to walk: it parses every sfnt it finds there and reports, over the
// fonts that parse, the largest ratio of
//
//   - bytes charged to the allocation budget to input bytes (the quantity the
//     budget bounds), and
//   - bytes the whole Parse allocated (runtime TotalAlloc) to input bytes,
//     which also counts what no decoder charges, as a cross-check.
//
// and fails if a real font would meet less than a 2x margin under the limit.
func TestMeasureAllocBudget(t *testing.T) {
	dirs := os.Getenv("OPENTYPE_MEASURE_DIRS")
	if dirs == "" {
		t.Skip("set OPENTYPE_MEASURE_DIRS to measure real fonts")
	}
	var parsed, failed int
	var maxCharged, maxTotal float64
	var maxChargedName, maxTotalName string
	var ms0, ms1 runtime.MemStats
	for _, dir := range filepath.SplitList(dirs) {
		filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".ttf", ".otf", ".woff", ".woff2":
			default:
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil || len(b) == 0 {
				return nil
			}
			runtime.ReadMemStats(&ms0)
			f, err := Parse(b)
			runtime.ReadMemStats(&ms1)
			if err != nil {
				failed++
				return nil
			}
			parsed++
			charged := float64(f.bud.spent) / float64(len(b))
			total := float64(ms1.TotalAlloc-ms0.TotalAlloc) / float64(len(b))
			if charged > maxCharged {
				maxCharged, maxChargedName = charged, p
			}
			if total > maxTotal {
				maxTotal, maxTotalName = total, p
			}
			return nil
		})
	}
	t.Logf("parsed %d fonts (%d not parsed: collections or unsupported)", parsed, failed)
	t.Logf("max charged/input = %.2f (%s)", maxCharged, maxChargedName)
	t.Logf("max TotalAlloc/input = %.2f (%s)", maxTotal, maxTotalName)
	if maxCharged*2 > allocBudgetPerInputByte {
		t.Errorf("a real font charges %.2f per input byte: the budget %d has less than a 2x margin", maxCharged, allocBudgetPerInputByte)
	}
}
