package kv

import (
	"context"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"github.com/SmithOperatingSolutions/snapshot-core/model/mapobject"
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

// spec is kv as a map-shaped model: a map from key to value frame under a
// configuration, its records checked as keys of ours holding frames of ours.
func spec(c prolly.Config) mapobject.Spec {
	return mapobject.Spec{Name: "kv", Format: Format, Config: c, Check: func(key, frame []byte) error {
		_, err := checked(key, frame)
		return err
	}}
}

// checkKey refuses an empty key and one over MaxKeySize.
func checkKey(k []byte) error {
	if len(k) == 0 || len(k) > MaxKeySize {
		return fmt.Errorf("%w: %d bytes", ErrKey, len(k))
	}
	return nil
}

// Write stores entries as an object: a new map with every entry set.
func Write(ctx context.Context, s chunk.ReadWriter, c prolly.Config, entries map[string]Value) (model.Root, error) {
	m, err := Empty(ctx, s, c)
	if err != nil {
		return model.Root{}, err
	}
	e := m.Edit()
	for k, v := range entries {
		if err := e.Set([]byte(k), v); err != nil {
			return model.Root{}, err
		}
	}
	if m, err = e.Flush(ctx); err != nil {
		return model.Root{}, err
	}
	return m.Root(), nil
}

// checked decodes a stored entry and its key.
func checked(key, frame []byte) (Value, error) {
	if err := checkKey(key); err != nil {
		return Value{}, fmt.Errorf("%w: a stored key: %w", chunk.ErrCorrupt, err)
	}
	return DecodeValue(frame)
}

// Read returns an object's entries.
func Read(ctx context.Context, r chunk.Reader, c prolly.Config, root model.Root) (map[string]Value, error) {
	m, err := Open(ctx, mapobject.ReadOnly(r), c, root)
	if err != nil {
		return nil, err
	}
	it, err := m.Scan(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]Value{} // grown as entries are read, never sized from the root's claim (snapshot-engine#5)
	for {
		k, v, ok, err := it.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		out[string(k)] = v
	}
}

// Validate implements model.Model: the map opens under the root's claims
// and every entry is a key of ours holding a frame of ours.
func (m Model) Validate(ctx context.Context, root model.Root, r chunk.Reader) error {
	_, err := Read(ctx, r, m.Config, root)
	return err
}

// Walk implements model.Walker: the object is its map, every record a key
// of ours holding a frame of ours.
func (m Model) Walk(ctx context.Context, root model.Root, r chunk.Reader, visit func(h hash.Hash, leaf bool) (bool, error)) error {
	return spec(m.Config).Walk(ctx, root, r, visit, func(key, frame []byte) error {
		_, err := checked(key, frame)
		return err
	})
}

// Diff implements model.Model: a change per key, located by the key.
func (m Model) Diff(ctx context.Context, from, to model.Root, r chunk.Reader) (model.DiffIter, error) {
	return spec(m.Config).Diff(ctx, from, to, r)
}

// Merge implements model.Model, per key: what only one side changed lands,
// and a key both changed is decided by the kind of its value (decide);
// conflicts are located with Location.
func (m Model) Merge(ctx context.Context, base, ours, theirs model.Root, rw chunk.ReadWriter) (model.MergeResult, error) {
	return spec(m.Config).MergeWith(ctx, base, ours, theirs, rw, decide)
}
