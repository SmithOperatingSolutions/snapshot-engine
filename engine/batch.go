package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/object"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// objectBatch is one object's batch: many members' changes to it, checked
// one member at a time and written with one flush (the models' Batch,
// DESIGN D23).
type objectBatch interface {
	// check reads what side changed from base and checks it against the
	// target and the members taken: refused (the model's ErrChangedSince,
	// table's ErrSchema) or a pending to take.
	check(ctx context.Context, base, side model.Root) (any, error)
	take(pending any)
	flush(ctx context.Context) (model.Root, error)
}

// batcher opens an object's batch for a model that has one.
func (m models) batcher(ctx context.Context, id model.ID, rw chunk.ReadWriter, onto model.Root) (objectBatch, error) {
	switch id {
	case table.ID:
		b, err := m.table.Batch(ctx, rw, onto)
		return tableBatch{b}, err
	case kv.ID:
		b, err := m.kv.Batch(ctx, rw, onto)
		return kvBatch{b}, err
	case document.ID:
		b, err := m.document.Batch(ctx, rw, onto)
		return documentBatch{b}, err
	}
	return nil, nil
}

type tableBatch struct{ b *table.Batch }

func (a tableBatch) check(ctx context.Context, base, side model.Root) (any, error) {
	return a.b.Check(ctx, base, side)
}
func (a tableBatch) take(p any)                                    { a.b.Take(p.(*table.Pending)) }
func (a tableBatch) flush(ctx context.Context) (model.Root, error) { return a.b.Flush(ctx) }

type kvBatch struct{ b *kv.Batch }

func (a kvBatch) check(ctx context.Context, base, side model.Root) (any, error) {
	return a.b.Check(ctx, base, side)
}
func (a kvBatch) take(p any)                                    { a.b.Take(p.(*kv.Pending)) }
func (a kvBatch) flush(ctx context.Context) (model.Root, error) { return a.b.Flush(ctx) }

type documentBatch struct{ b *document.Batch }

func (a documentBatch) check(ctx context.Context, base, side model.Root) (any, error) {
	return a.b.Check(ctx, base, side)
}
func (a documentBatch) take(p any)                                    { a.b.Take(p.(*document.Pending)) }
func (a documentBatch) flush(ctx context.Context) (model.Root, error) { return a.b.Flush(ctx) }

// run is a stretch of a batch's members applied as one to the working set
// it began on: each member's changes are checked, object by object, against
// that working set and the members taken before it, and every object the
// run touched is written once when the run closes (D23).
type run struct {
	d       *Database
	branch  string
	g       *granted
	w       *object.Namespace     // the working set the run began on
	refs    map[string]object.Ref // the current ref of every object a member touched
	batches map[string]objectBatch
	members []int // the batch indexes of the members taken
}

func (d *Database) newRun(branch string, g *granted, w *object.Namespace) *run {
	return &run{d: d, branch: branch, g: g, w: w, refs: map[string]object.Ref{}, batches: map[string]objectBatch{}}
}

// errDeclined says the run does not take a member: it is applied by the
// merge, on its own, once the run has closed.
var errDeclined = errors.New("declined")

// pending is one member's checked changes, ready to take.
type pending struct {
	path string
	ref  object.Ref // set: the member's object as is, nobody else changed it
	p    any        // else: the object's batch has checked the member's changes
}

// take checks member i's changes against the run and takes them when every
// object passes and the member may write them all; it is errDeclined for a
// change the run cannot check (an object added, dropped or changed in kind
// on either side, one of a model without a batch, a table whose schema
// differs, a kv key both wrote: a counter both incremented sums in the
// merge, D18), ErrSerialization for an item both wrote, and the store's or
// the authorizer's error otherwise. A member not taken has nothing of it
// applied.
func (r *run) take(ctx context.Context, i int, c *queued) error {
	t := c.tx
	d, err := object.Diff(ctx, t.base, c.ours)
	if err != nil {
		return translate(err)
	}
	var ps []pending
	for {
		ch, ok, err := d.Next()
		if err != nil {
			return translate(err)
		}
		if !ok {
			break
		}
		if ch.From.Model != ch.To.Model {
			return errDeclined
		}
		now, held := r.refs[ch.Path]
		if !held {
			if now, _, held, err = r.w.Get(ctx, ch.Path); err != nil {
				return translate(err)
			}
			if !held || now.Model != ch.From.Model {
				return errDeclined
			}
		}
		b, batched := r.batches[ch.Path]
		if !batched && now == ch.From {
			ps = append(ps, pending{path: ch.Path, ref: ch.To})
			continue
		}
		if !batched {
			if b, err = r.d.models.batcher(ctx, ch.To.Model, r.d.r.Chunks(), now.Root); err != nil {
				return translate(err)
			}
			if b == nil {
				return errDeclined
			}
			r.refs[ch.Path] = now // the batch's target: its model, until the flush gives its root
		}
		p, err := b.check(ctx, ch.From.Root, ch.To.Root)
		switch {
		case errors.Is(err, table.ErrChangedSince), errors.Is(err, document.ErrChangedSince):
			return fmt.Errorf("%w: a transaction committed since this one began wrote an item this one writes", ErrSerialization)
		case errors.Is(err, kv.ErrChangedSince), errors.Is(err, table.ErrSchema):
			return errDeclined
		case err != nil:
			return translate(err)
		}
		if !batched {
			r.batches[ch.Path] = b
		}
		ps = append(ps, pending{path: ch.Path, p: p})
	}
	paths := make([]string, len(ps))
	for i, p := range ps {
		paths[i] = p.path
	}
	if err := r.d.mayWritePaths(ctx, t.s.p, r.branch, paths, r.g); err != nil {
		return err
	}
	for _, p := range ps {
		if p.p != nil {
			r.batches[p.path].take(p.p)
			continue
		}
		r.refs[p.path] = p.ref
	}
	r.members = append(r.members, i)
	return nil
}

// close writes every object the run touched once and is the working set
// the run made; a store fault is every taken member's.
func (r *run) close(ctx context.Context) (*object.Namespace, error) {
	if len(r.members) == 0 {
		return r.w, nil
	}
	e := r.w.Editor()
	for path, b := range r.batches {
		root, err := b.flush(ctx)
		if err != nil {
			return nil, translate(err)
		}
		r.refs[path] = object.Ref{Model: r.refs[path].Model, Root: root}
	}
	for path, ref := range r.refs {
		if err := e.Put(path, ref); err != nil {
			return nil, translate(err)
		}
	}
	ns, err := e.Flush(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return ns, nil
}
