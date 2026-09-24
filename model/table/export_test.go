package table

// EncodeCell and DecodeCell expose the cell codec to the package's tests.
func EncodeCell(c Column, v any) ([]byte, error) { return encodeCell(nil, c, v) }

func DecodeCell(b []byte, c Column) (any, []byte, error) { return decodeCell(b, c) }

// EncodeRoot and DecodeRoot expose the root record codec.
func EncodeRoot(catalog []byte, primaryRoot [32]byte, rows uint64, indexes []IndexRoot) []byte {
	r := rootRecord{catalog: catalog, primary: indexRoot{root: primaryRoot, count: rows}}
	for _, ix := range indexes {
		r.indexes = append(r.indexes, indexRoot{tag: ix.Tag, root: ix.Root, count: ix.Count})
	}
	return encodeRoot(r)
}

// IndexRoot is one index map in a root record, for tests.
type IndexRoot struct {
	Tag   Tag
	Root  [32]byte
	Count uint64
}

func DecodeRoot(b []byte) (catalog []byte, primaryRoot [32]byte, rows uint64, indexes []IndexRoot, err error) {
	r, err := decodeRoot(b)
	if err != nil {
		return nil, [32]byte{}, 0, nil, err
	}
	for _, ix := range r.indexes {
		indexes = append(indexes, IndexRoot{Tag: ix.tag, Root: ix.root, Count: ix.count})
	}
	return r.catalog, r.primary.root, r.primary.count, indexes, nil
}
