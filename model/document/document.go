package document

import (
	"context"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

// ID is the document model's id: the Storage Core Spec's JSON document
// (docs/DESIGN.md D3).
const ID model.ID = 4

// MaxIDSize is the longest record id, the prolly map's own key limit; an id
// is never empty.
const MaxIDSize = prolly.MaxKeySize

// ErrID is a record id that is empty or over MaxIDSize.
var ErrID = errors.New("document: a record id must be 1 to 4096 bytes")

var errNotImplemented = fmt.Errorf("%w: not implemented", ErrDocument)

// Model is the document model; Config is how it writes the maps it merges.
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

// Write stores records, by id, as an object. (Stub.)
func Write(ctx context.Context, s chunk.ReadWriter, c prolly.Config, records map[string]merge.Node) (model.Root, error) {
	return model.Root{}, errNotImplemented
}

// Read returns an object's records by id. (Stub.)
func Read(ctx context.Context, r chunk.Reader, c prolly.Config, root model.Root) (map[string]merge.Node, error) {
	return nil, errNotImplemented
}

// Validate implements model.Model. (Stub.)
func (m Model) Validate(ctx context.Context, root model.Root, r chunk.Reader) error {
	return errNotImplemented
}

// Walk implements model.Walker. (Stub.)
func (m Model) Walk(ctx context.Context, root model.Root, r chunk.Reader, visit func(h hash.Hash, leaf bool) (bool, error)) error {
	return errNotImplemented
}

// Diff implements model.Model. (Stub.)
func (m Model) Diff(ctx context.Context, from, to model.Root, r chunk.Reader) (model.DiffIter, error) {
	return nil, errNotImplemented
}

// Merge implements model.Model. (Stub.)
func (m Model) Merge(ctx context.Context, base, ours, theirs model.Root, rw chunk.ReadWriter) (model.MergeResult, error) {
	return model.MergeResult{}, errNotImplemented
}
