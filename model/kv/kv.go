package kv

import (
	"bytes"
	"context"
	"errors"
	"fmt"

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

// readOnly opens a map over a Reader: reading one never writes.
type readOnly struct{ chunk.Reader }

func (readOnly) Put(context.Context, []byte) (hash.Hash, error) {
	return hash.Hash{}, errors.New("kv: this store is read-only")
}

func rootOf(m *prolly.Map) model.Root {
	return model.Root{Hash: m.Root(), Size: m.Count(), Format: Format}
}

// checkKey refuses an empty key and one over MaxKeySize.
func checkKey(k []byte) error {
	if len(k) == 0 || len(k) > MaxKeySize {
		return fmt.Errorf("%w: %d bytes", ErrKey, len(k))
	}
	return nil
}

// open opens an object's map and checks the root's claims against it.
func open(ctx context.Context, s chunk.ReadWriter, c prolly.Config, root model.Root) (*prolly.Map, error) {
	if root.Format != Format {
		return nil, fmt.Errorf("%w: kv format %d", model.ErrUnknownModel, root.Format)
	}
	if root.Depth != 0 {
		return nil, fmt.Errorf("%w: a kv root claims stream depth %d", chunk.ErrCorrupt, root.Depth)
	}
	m, err := prolly.Open(ctx, s, c, root.Hash)
	if err != nil {
		return nil, err
	}
	if m.Count() != root.Size {
		return nil, fmt.Errorf("%w: a kv object of %d entries whose root says %d", chunk.ErrCorrupt, m.Count(), root.Size)
	}
	return m, nil
}

// Write stores entries as an object.
func Write(ctx context.Context, s chunk.ReadWriter, c prolly.Config, entries map[string]Value) (model.Root, error) {
	m, err := prolly.Empty(ctx, s, c)
	if err != nil {
		return model.Root{}, err
	}
	e := m.Editor()
	for k, v := range entries {
		if err := checkKey([]byte(k)); err != nil {
			return model.Root{}, err
		}
		f, err := EncodeValue(v)
		if err != nil {
			return model.Root{}, err
		}
		if err := e.Put([]byte(k), f); err != nil {
			return model.Root{}, err
		}
	}
	if m, err = e.Flush(ctx); err != nil {
		return model.Root{}, err
	}
	return rootOf(m), nil
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
	m, err := open(ctx, readOnly{r}, c, root)
	if err != nil {
		return nil, err
	}
	it, err := m.IterRange(ctx, nil, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Value, m.Count())
	for {
		k, f, ok, err := it.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		v, err := checked(k, f)
		if err != nil {
			return nil, err
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

// Walk implements model.Walker: the object is its map; a value is inline
// or a stream the map itself reaches.
func (m Model) Walk(ctx context.Context, root model.Root, r chunk.Reader, visit func(h hash.Hash, leaf bool) (bool, error)) error {
	if root.Format != Format {
		return fmt.Errorf("%w: kv format %d", model.ErrUnknownModel, root.Format)
	}
	if root.Depth != 0 {
		return fmt.Errorf("%w: a kv root claims stream depth %d", chunk.ErrCorrupt, root.Depth)
	}
	return prolly.Walk(ctx, r, m.Config, root.Hash, visit, func(key, frame []byte) error {
		_, err := checked(key, frame)
		return err
	})
}

var kinds = map[prolly.ChangeKind]model.ChangeKind{prolly.Added: model.Added, prolly.Removed: model.Removed, prolly.Modified: model.Modified}

type diffIter struct{ d *prolly.DiffIter }

func (d diffIter) Next(context.Context) (model.Change, bool, error) {
	c, ok, err := d.d.Next()
	if err != nil || !ok {
		return model.Change{}, false, err
	}
	return model.Change{Kind: kinds[c.Kind], Location: c.Key}, true, nil
}

// Diff implements model.Model: a change per key, located by the key.
func (m Model) Diff(ctx context.Context, from, to model.Root, r chunk.Reader) (model.DiffIter, error) {
	fm, err := open(ctx, readOnly{r}, m.Config, from)
	if err != nil {
		return nil, err
	}
	tm, err := open(ctx, readOnly{r}, m.Config, to)
	if err != nil {
		return nil, err
	}
	d, err := prolly.Diff(ctx, fm, tm)
	if err != nil {
		return nil, err
	}
	return diffIter{d}, nil
}

// Merge implements model.Model, per key: it zips the two sides' diffs from
// base and applies to ours what only theirs changed; a key both changed is
// clean when they agree and a conflict at that key when they do not.
func (m Model) Merge(ctx context.Context, base, ours, theirs model.Root, rw chunk.ReadWriter) (model.MergeResult, error) {
	var maps [3]*prolly.Map
	for i, r := range []model.Root{base, ours, theirs} {
		var err error
		if maps[i], err = open(ctx, rw, m.Config, r); err != nil {
			return model.MergeResult{}, err
		}
	}
	dOurs, err := prolly.Diff(ctx, maps[0], maps[1])
	if err != nil {
		return model.MergeResult{}, err
	}
	dTheirs, err := prolly.Diff(ctx, maps[0], maps[2])
	if err != nil {
		return model.MergeResult{}, err
	}
	ed := maps[1].Editor()
	var conflicts []model.Conflict
	co, okO, err := dOurs.Next()
	if err != nil {
		return model.MergeResult{}, err
	}
	ct, okT, err := dTheirs.Next()
	if err != nil {
		return model.MergeResult{}, err
	}
	for (okO || okT) && err == nil {
		switch {
		case !okT || (okO && bytes.Compare(co.Key, ct.Key) < 0): // only ours changed it
			co, okO, err = dOurs.Next()
		case !okO || bytes.Compare(ct.Key, co.Key) < 0: // only theirs changed it
			if err = apply(ed, ct); err == nil {
				ct, okT, err = dTheirs.Next()
			}
		default: // both changed it
			if reason := disagreement(co, ct); reason != "" {
				conflicts = append(conflicts, model.Conflict{Location: bytes.Clone(co.Key), Reason: reason})
			}
			if co, okO, err = dOurs.Next(); err == nil {
				ct, okT, err = dTheirs.Next()
			}
		}
	}
	if err != nil {
		return model.MergeResult{}, err
	}
	if len(conflicts) > 0 {
		return model.MergeResult{Root: ours, Conflicts: conflicts}, nil
	}
	merged, err := ed.Flush(ctx)
	if err != nil {
		return model.MergeResult{}, err
	}
	return model.MergeResult{Root: rootOf(merged)}, nil
}

// apply makes theirs' change to a key in the merged object.
func apply(ed *prolly.Editor, c prolly.Change) error {
	if c.Kind == prolly.Removed {
		return ed.Delete(c.Key)
	}
	if _, err := checked(c.Key, c.To); err != nil {
		return err
	}
	return ed.Put(c.Key, c.To)
}

// disagreement is why both sides' changes to one key conflict, or "" if
// they left it the same.
func disagreement(ours, theirs prolly.Change) string {
	switch {
	case ours.Kind == prolly.Removed && theirs.Kind == prolly.Removed:
		return ""
	case ours.Kind == prolly.Removed || theirs.Kind == prolly.Removed:
		return "deleted on one side and changed on the other"
	case bytes.Equal(ours.To, theirs.To):
		return ""
	case ours.Kind == prolly.Added:
		return "added with different values on both sides"
	}
	return "set to different values on both sides"
}
