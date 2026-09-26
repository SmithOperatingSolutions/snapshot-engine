package table

import (
	"context"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
)

// Batch applies many sides to one table with one flush (#22). Stub: it
// takes every side and writes nothing.
type Batch struct {
	t *Table
}

// Pending is one side's changes, checked against the batch and not yet
// taken.
type Pending struct{}

// Batch opens onto for a batch of sides.
func (m Model) Batch(ctx context.Context, rw chunk.ReadWriter, onto model.Root) (*Batch, error) {
	t, err := Open(ctx, rw, m.Config, onto)
	if err != nil {
		return nil, err
	}
	return &Batch{t: t}, nil
}

// Check reads what side changed from base and checks each row against the
// target and the sides taken so far.
func (b *Batch) Check(ctx context.Context, base, side model.Root) (*Pending, error) {
	return &Pending{}, nil
}

// Take records a checked side's changes for the flush.
func (b *Batch) Take(p *Pending) {}

// Flush writes every side taken, once.
func (b *Batch) Flush(ctx context.Context) (model.Root, error) {
	return b.t.Root(), nil
}
