package document

import (
	"context"
	"errors"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
)

// ErrChangedSince is a rebase refused: a record the side changed from the
// base has changed in the target since the base too.
var ErrChangedSince = errors.New("document: a record changed on both sides since the base")

// Rebase is a stub.
func (m Model) Rebase(ctx context.Context, base, side, onto model.Root, rw chunk.ReadWriter) (model.Root, error) {
	return onto, nil
}
