package kv

import (
	"context"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"github.com/SmithOperatingSolutions/snapshot-core/model/mapobject"
)

// Batch applies many sides to one target with one flush (#22). Stub: it
// takes every side and writes nothing.
type Batch struct {
	s    mapobject.Spec
	onto *prolly.Map
}

// Pending is one side's changes, checked against the batch and not yet
// taken.
type Pending struct{}

// Batch opens onto for a batch of sides.
func (m Model) Batch(ctx context.Context, rw chunk.ReadWriter, onto model.Root) (*Batch, error) {
	s := spec(m.Config)
	mp, err := s.Open(ctx, rw, onto)
	if err != nil {
		return nil, err
	}
	return &Batch{s: s, onto: mp}, nil
}

// Check reads what side changed from base and checks each key against the
// target and the sides taken so far.
func (b *Batch) Check(ctx context.Context, base, side model.Root) (*Pending, error) {
	return &Pending{}, nil
}

// Take records a checked side's changes for the flush.
func (b *Batch) Take(p *Pending) {}

// Flush writes every side taken, once.
func (b *Batch) Flush(ctx context.Context) (model.Root, error) {
	return b.s.Root(b.onto), nil
}
