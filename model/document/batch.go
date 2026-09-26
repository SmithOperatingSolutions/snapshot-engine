package document

import (
	"bytes"
	"context"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"github.com/SmithOperatingSolutions/snapshot-core/model/mapobject"
)

// Batch applies many sides to one collection with one flush (#22): each side
// is checked against the target and the sides taken before it, as Rebase
// checks it against the target alone, and Flush writes every side taken at
// once. Check, Take and Flush are called from one goroutine.
type Batch struct {
	s     mapobject.Spec
	rw    chunk.ReadWriter
	onto  *prolly.Map
	ed    *prolly.Editor
	taken map[string]bool // records the sides taken so far changed
}

// Pending is one side's changes, checked against the batch and not yet
// taken: taking it makes its records the batch's, refusing later sides that
// change them.
type Pending struct {
	edits []edit
}

type edit struct {
	key, val []byte // val nil: a delete
}

// Batch opens onto for a batch of sides.
func (m Model) Batch(ctx context.Context, rw chunk.ReadWriter, onto model.Root) (*Batch, error) {
	s := spec(m.Config)
	mp, err := s.Open(ctx, rw, onto)
	if err != nil {
		return nil, err
	}
	return &Batch{s: s, rw: rw, onto: mp, ed: mp.Editor(), taken: map[string]bool{}}, nil
}

// Check reads what side changed from base and checks each record against the
// target and the sides taken so far: a record the target holds otherwise than
// base did, or one a side taken changed, is ErrChangedSince, and the side
// is not taken. It reads the side's changes and one record of the target per
// change, as Rebase does.
func (b *Batch) Check(ctx context.Context, base, side model.Root) (*Pending, error) {
	var maps [2]*prolly.Map
	for i, r := range []model.Root{base, side} {
		var err error
		if maps[i], err = b.s.Open(ctx, b.rw, r); err != nil {
			return nil, err
		}
	}
	d, err := prolly.Diff(ctx, maps[0], maps[1])
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
		now, held, err := b.onto.Get(ctx, c.Key)
		if err != nil {
			return nil, err
		}
		if held != (c.Kind != prolly.Added) || !bytes.Equal(now, c.From) {
			return nil, ErrChangedSince
		}
		if c.Kind != prolly.Removed {
			if err := b.s.Check(c.Key, c.To); err != nil {
				return nil, err
			}
		}
		p.edits = append(p.edits, edit{key: c.Key, val: c.To})
	}
}

// Take records a checked side's changes for the flush. A Put or Delete on
// the editor fails only for a key or value the spec refuses, which Check
// has already accepted.
func (b *Batch) Take(p *Pending) {
	for _, e := range p.edits {
		b.taken[string(e.key)] = true
		if e.val == nil {
			_ = b.ed.Delete(e.key)
		} else {
			_ = b.ed.Put(e.key, e.val)
		}
	}
}

// Flush writes every side taken, once, and is the map they made.
func (b *Batch) Flush(ctx context.Context) (model.Root, error) {
	merged, err := b.ed.Flush(ctx)
	if err != nil {
		return model.Root{}, err
	}
	return b.s.Root(merged), nil
}
