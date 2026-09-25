package kv

import (
	"bytes"
	"context"
	"errors"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
)

// ErrChangedSince is a rebase refused: a key the side changed from the
// base has changed in the target since the base too.
var ErrChangedSince = errors.New("kv: a key changed on both sides since the base")

// Rebase applies to onto what side changed from base, key by key, and
// is Merge(base, onto, side) whenever that merge finds no key changed on
// both sides. It reads side's changes and one key of onto per change, not
// what onto changed: its cost is the side's, however far onto has moved
// from base. A key side changed that onto holds otherwise than base did
// (changed, added or removed there too, even to the same value) is
// ErrChangedSince, and the result is not written.
func (m Model) Rebase(ctx context.Context, base, side, onto model.Root, rw chunk.ReadWriter) (model.Root, error) {
	s := spec(m.Config)
	var maps [3]*prolly.Map
	for i, r := range []model.Root{base, side, onto} {
		var err error
		if maps[i], err = s.Open(ctx, rw, r); err != nil {
			return model.Root{}, err
		}
	}
	d, err := prolly.Diff(ctx, maps[0], maps[1])
	if err != nil {
		return model.Root{}, err
	}
	ed := maps[2].Editor()
	for {
		c, ok, err := d.Next()
		if err != nil {
			return model.Root{}, err
		}
		if !ok {
			break
		}
		now, held, err := maps[2].Get(ctx, c.Key)
		if err != nil {
			return model.Root{}, err
		}
		if held != (c.Kind != prolly.Added) || !bytes.Equal(now, c.From) {
			return model.Root{}, ErrChangedSince
		}
		if c.Kind == prolly.Removed {
			err = ed.Delete(c.Key)
		} else if err = s.Check(c.Key, c.To); err == nil {
			err = ed.Put(c.Key, c.To)
		}
		if err != nil {
			return model.Root{}, err
		}
	}
	merged, err := ed.Flush(ctx)
	if err != nil {
		return model.Root{}, err
	}
	return s.Root(merged), nil
}
