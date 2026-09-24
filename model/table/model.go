package table

import (
	"context"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
)

// Model is the table model; Config is how it writes the tables it merges.
type Model struct {
	Config prolly.Config
}

var (
	_ model.Model  = Model{}
	_ model.Walker = Model{}
)

// ID implements model.Model.
func (Model) ID() model.ID { return ID }

// FormatVersion implements model.Model.
func (Model) FormatVersion() uint16 { return Format }

// Validate implements model.Model: the root record, the catalog, every
// map with the count the record claims, every row under the schema, and
// every index as full as the primary map.
func (m Model) Validate(ctx context.Context, root model.Root, r chunk.Reader) error {
	t, err := Open(ctx, readOnly{r}, m.Config, root)
	if err != nil {
		return err
	}
	rows, err := t.Scan(ctx)
	if err != nil {
		return err
	}
	var n uint64
	for {
		_, _, ok, err := rows.Next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		n++
	}
	if n != root.Size {
		return fmt.Errorf("%w: %d rows scanned, the root says %d", chunk.ErrCorrupt, n, root.Size)
	}
	for _, ix := range t.schema.Indexes {
		rows, err := t.IndexLookup(ctx, ix.Tag)
		if err != nil {
			return err
		}
		var seen uint64
		for {
			_, _, ok, err := rows.Next() // each entry decodes and names a row that exists
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			seen++
		}
		if seen != n {
			return fmt.Errorf("%w: index %d walks %d rows of %d", chunk.ErrCorrupt, ix.Tag, seen, n)
		}
	}
	return nil
}

// Walk implements model.Walker: the root chunk, then every map.
func (m Model) Walk(ctx context.Context, root model.Root, r chunk.Reader, visit func(h hash.Hash, leaf bool) (bool, error)) error {
	return errors.New("table: not implemented")
}

// Diff implements model.Model: a change per row added or removed, located
// by its key, and per cell changed, located by the key then the column tag.
func (m Model) Diff(ctx context.Context, from, to model.Root, r chunk.Reader) (model.DiffIter, error) {
	return nil, errors.New("table: not implemented")
}

// Merge implements model.Model for tables of one schema: rows merge
// independently, cells by mergeCell; two schemas are one conflict.
func (m Model) Merge(ctx context.Context, base, ours, theirs model.Root, rw chunk.ReadWriter) (model.MergeResult, error) {
	return model.MergeResult{}, errors.New("table: not implemented")
}

// Locate is the model's address of a row (column 0) or of one cell of it:
// the encoded key, then for a cell the column's tag, big-endian.
func (t *Table) Locate(key Key, column Tag) ([]byte, error) {
	return nil, errors.New("table: not implemented")
}

// ParseLocation reads what Locate wrote: the key, and the column (0 for the
// row itself).
func (t *Table) ParseLocation(loc []byte) (Key, Tag, error) {
	return nil, 0, errors.New("table: not implemented")
}
