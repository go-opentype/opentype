// Copyright (c) the go-opentype authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package opentype

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

// decodeWOFF1 unwraps a WOFF (version 1) file. Unlike WOFF2 it really is a
// compressed SFNT: each table is stored on its own, deflated when that is
// smaller than the original, and no table is transformed. So this is a
// directory walk and a per-table inflate.
func decodeWOFF1(b []byte) ([]byte, error) {
	const headerSize = 44
	if len(b) < headerSize {
		return nil, fmt.Errorf("opentype: woff: short header: %w", errTruncated)
	}
	flavor := be32(b[4:])
	numTables := int(be16(b[12:]))
	if numTables == 0 {
		return nil, fmt.Errorf("opentype: woff: no tables")
	}
	type entry struct {
		tag                string
		offset, comp, orig uint32
	}
	entries := make([]entry, numTables)
	for i := 0; i < numTables; i++ {
		e := headerSize + 20*i
		if e+20 > len(b) {
			return nil, fmt.Errorf("opentype: woff: table directory: %w", errTruncated)
		}
		entries[i] = entry{
			tag:    string(b[e : e+4]),
			offset: be32(b[e+4:]),
			comp:   be32(b[e+8:]),
			orig:   be32(b[e+12:]),
		}
	}
	tables := make([]woff2Table, numTables)
	for i, e := range entries {
		if uint64(e.offset)+uint64(e.comp) > uint64(len(b)) {
			return nil, fmt.Errorf("opentype: woff: %q runs past the file: %w", e.tag, errTruncated)
		}
		raw := b[e.offset : e.offset+e.comp]
		var data []byte
		if e.comp == e.orig {
			data = raw // stored, not deflated
		} else {
			zr, err := zlib.NewReader(bytes.NewReader(raw))
			if err != nil {
				return nil, fmt.Errorf("opentype: woff: %q: %w", e.tag, err)
			}
			data, err = io.ReadAll(io.LimitReader(zr, int64(e.orig)+1))
			zr.Close()
			if err != nil {
				return nil, fmt.Errorf("opentype: woff: %q: %w", e.tag, err)
			}
			if uint32(len(data)) != e.orig {
				return nil, fmt.Errorf("opentype: woff: %q inflated to %d bytes, the directory says %d", e.tag, len(data), e.orig)
			}
		}
		tables[i] = woff2Table{tag: e.tag, origLength: e.orig, out: data, transform: 3}
	}
	return assembleSFNT(flavor, tables)
}
