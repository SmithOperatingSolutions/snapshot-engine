package kv

import (
	"context"
	"errors"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
)

// ID is the key-value model's id (proposed to the Storage Core Spec's
// registry table; docs/DESIGN.md D3).
const ID model.ID = 6

// Format is the format version this package writes.
const Format = 1

// MaxKeySize is the longest key, the prolly map's own limit; a key is never
// empty.
const MaxKeySize = prolly.MaxKeySize

// ErrKey is a key that is empty or over MaxKeySize.
var ErrKey = errors.New("kv: a key must be 1 to 4096 bytes")

// Model is the key-value model; Config is how it writes the maps it merges.
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

var errNotImplemented = errors.New("kv: not implemented")

// Write stores entries as an object.
func Write(ctx context.Context, s chunk.ReadWriter, c prolly.Config, entries map[string]Value) (model.Root, error) {
	return model.Root{}, errNotImplemented
}

// Read returns an object's entries.
func Read(ctx context.Context, r chunk.Reader, c prolly.Config, root model.Root) (map[string]Value, error) {
	return nil, errNotImplemented
}

// Validate implements model.Model.
func (m Model) Validate(ctx context.Context, root model.Root, r chunk.Reader) error {
	return errNotImplemented
}

// Walk implements model.Walker.
func (m Model) Walk(ctx context.Context, root model.Root, r chunk.Reader, visit func(h hash.Hash, leaf bool) (bool, error)) error {
	return errNotImplemented
}

// Diff implements model.Model.
func (m Model) Diff(ctx context.Context, from, to model.Root, r chunk.Reader) (model.DiffIter, error) {
	return nil, errNotImplemented
}

// Merge implements model.Model.
func (m Model) Merge(ctx context.Context, base, ours, theirs model.Root, rw chunk.ReadWriter) (model.MergeResult, error) {
	return model.MergeResult{}, errNotImplemented
}
