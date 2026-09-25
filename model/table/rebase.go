package table

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
)

// ErrChangedSince is a rebase refused: a row the side changed from the
// base has changed in the target since the base too.
var ErrChangedSince = errors.New("table: a row changed on both sides since the base")

// Rebase applies to onto the rows side changed from base, row by row, and
// is Merge(base, onto, side) whenever the three share one schema and that
// merge finds no row changed on both sides. It reads side's changes and one
// row of onto per change, not what onto changed: its cost is the side's,
// however far onto has moved from base. A row side changed that onto holds
// otherwise than base did (any cell changed, added or removed there too,
// even to the same value) is ErrChangedSince; schemas that are not all one
// are ErrSchema (Merge combines those); either way the result is not
// written.
func (m Model) Rebase(ctx context.Context, base, side, onto model.Root, rw chunk.ReadWriter) (model.Root, error) {
	var ts [3]*Table
	for i, r := range []model.Root{base, side, onto} {
		var err error
		if ts[i], err = Open(ctx, rw, m.Config, r); err != nil {
			return model.Root{}, err
		}
	}
	if !bytes.Equal(ts[0].catalog, ts[1].catalog) || !bytes.Equal(ts[0].catalog, ts[2].catalog) {
		return model.Root{}, fmt.Errorf("%w: a rebase needs one schema on every side", ErrSchema)
	}
	d, err := prolly.Diff(ctx, ts[0].primary, ts[1].primary)
	if err != nil {
		return model.Root{}, err
	}
	ed := ts[2].Edit()
	for {
		c, ok, err := d.Next()
		if err != nil {
			return model.Root{}, err
		}
		if !ok {
			break
		}
		now, held, err := ts[2].primary.Get(ctx, c.Key)
		if err != nil {
			return model.Root{}, err
		}
		if held != (c.Kind != prolly.Added) || !bytes.Equal(now, c.From) {
			return model.Root{}, ErrChangedSince
		}
		if c.Kind == prolly.Removed {
			err = ed.remove(c.Key)
		} else {
			var key Key
			var row Row
			if key, row, err = ts[1].decodeRow(c.Key, c.To); err == nil {
				err = ed.put(c.Key, key, row)
			}
		}
		if err != nil {
			return model.Root{}, err
		}
	}
	merged, err := ed.Flush(ctx)
	if err != nil {
		return model.Root{}, err
	}
	return merged.Root(), nil
}
