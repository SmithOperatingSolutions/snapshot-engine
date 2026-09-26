package table

import (
	"bytes"
	"context"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
)

// Batch applies many sides to one table with one flush (#22): each side is
// checked against the target and the sides taken before it, as Rebase
// checks it against the target alone, and Flush writes every side taken at
// once, the indexes with the primary map. Check, Take and Flush are called
// from one goroutine.
type Batch struct {
	m     Model
	rw    chunk.ReadWriter
	t     *Table // the target
	ed    *Editor
	taken map[string]bool // encoded row keys the sides taken so far changed
}

// Pending is one side's changes, checked against the batch and not yet
// taken: taking it makes its rows the batch's, refusing later sides that
// change them.
type Pending struct {
	edits []rowEdit
}

type rowEdit struct {
	kb  []byte
	key Key
	row Row // nil: a delete
}

// Batch opens onto for a batch of sides.
func (m Model) Batch(ctx context.Context, rw chunk.ReadWriter, onto model.Root) (*Batch, error) {
	t, err := Open(ctx, rw, m.Config, onto)
	if err != nil {
		return nil, err
	}
	return &Batch{m: m, rw: rw, t: t, ed: t.Edit(), taken: map[string]bool{}}, nil
}

// Check reads the rows side changed from base and checks each against the
// target and the sides taken so far: a row the target holds otherwise than
// base did (any cell), or one a side taken changed, is ErrChangedSince, and
// the side is not taken; a schema that is not the target's on either is
// ErrSchema. It reads the side's changes and one row of the target per
// change, as Rebase does.
func (b *Batch) Check(ctx context.Context, base, side model.Root) (*Pending, error) {
	var ts [2]*Table
	for i, r := range []model.Root{base, side} {
		var err error
		if ts[i], err = Open(ctx, b.rw, b.m.Config, r); err != nil {
			return nil, err
		}
	}
	if !bytes.Equal(ts[0].catalog, b.t.catalog) || !bytes.Equal(ts[1].catalog, b.t.catalog) {
		return nil, fmt.Errorf("%w: a batch needs one schema on every side", ErrSchema)
	}
	d, err := prolly.Diff(ctx, ts[0].primary, ts[1].primary)
	if err != nil {
		return nil, err
	}
	p := &Pending{}
	for {
		c, ok, err := d.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return p, nil
		}
		if b.taken[string(c.Key)] {
			return nil, ErrChangedSince
		}
		now, held, err := b.t.primary.Get(ctx, c.Key)
		if err != nil {
			return nil, err
		}
		if held != (c.Kind != prolly.Added) || !bytes.Equal(now, c.From) {
			return nil, ErrChangedSince
		}
		e := rowEdit{kb: c.Key}
		if c.Kind == prolly.Removed {
			if e.key, err = b.t.decodeKey(c.Key); err != nil {
				return nil, err
			}
		} else if e.key, e.row, err = ts[1].decodeRow(c.Key, c.To); err != nil {
			return nil, err
		}
		p.edits = append(p.edits, e)
	}
}

// Take records a checked side's changes for the flush. put fails only for
// a row its schema refuses, and Check decoded every row under the same
// schema.
func (b *Batch) Take(p *Pending) {
	for _, e := range p.edits {
		b.taken[string(e.kb)] = true
		if e.row == nil {
			b.ed.set(e.kb, &pendingRow{key: e.key})
		} else {
			_ = b.ed.put(e.kb, e.key, e.row)
		}
	}
}

// Flush writes every side taken, once, and is the table they made.
func (b *Batch) Flush(ctx context.Context) (model.Root, error) {
	if len(b.taken) == 0 {
		return b.t.Root(), nil
	}
	t, err := b.ed.Flush(ctx)
	if err != nil {
		return model.Root{}, err
	}
	return t.Root(), nil
}
