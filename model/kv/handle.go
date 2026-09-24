package kv

import (
	"context"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
)

// Map is a key-value object opened for reading and editing: point reads,
// scans in key order, and an editor whose Flush makes a new Map. Reads see
// what was flushed.
type Map struct {
	s   chunk.ReadWriter
	cfg prolly.Config
	m   *prolly.Map
}

// Empty is a new, empty map.
func Empty(ctx context.Context, s chunk.ReadWriter, c prolly.Config) (*Map, error) {
	m, err := prolly.Empty(ctx, s, c)
	if err != nil {
		return nil, err
	}
	return &Map{s: s, cfg: c, m: m}, nil
}

// Open opens the object under root, checking the root's claims (its format,
// depth 0, a count equal to its size); a record is checked when a read
// meets it.
func Open(ctx context.Context, s chunk.ReadWriter, c prolly.Config, root model.Root) (*Map, error) {
	m, err := spec(c).Open(ctx, s, root)
	if err != nil {
		return nil, err
	}
	return &Map{s: s, cfg: c, m: m}, nil
}

// Root is the map as an object.
func (m *Map) Root() model.Root { return spec(m.cfg).Root(m.m) }

// Get reads key, refusing a key that cannot exist (ErrKey) and a stored
// frame that is not a value.
func (m *Map) Get(ctx context.Context, key []byte) (Value, bool, error) {
	if err := checkKey(key); err != nil {
		return Value{}, false, err
	}
	f, ok, err := m.m.Get(ctx, key)
	if err != nil || !ok {
		return Value{}, false, err
	}
	v, err := checked(key, f)
	if err != nil {
		return Value{}, false, err
	}
	return v, true, nil
}

// Scan walks the entries from key from, inclusive (nil: the first), in key
// order.
func (m *Map) Scan(ctx context.Context, from []byte) (*Entries, error) {
	it, err := m.m.IterRange(ctx, from, nil)
	if err != nil {
		return nil, err
	}
	return &Entries{it: it}, nil
}

// Entries walks a map's entries in key order.
type Entries struct{ it *prolly.Iter }

// Next is the next entry; ok is false at the end. A stored entry that is
// not ours is an error.
func (e *Entries) Next() (key []byte, v Value, ok bool, err error) {
	k, f, ok, err := e.it.Next()
	if err != nil || !ok {
		return nil, Value{}, false, err
	}
	if v, err = checked(k, f); err != nil {
		return nil, Value{}, false, err
	}
	return k, v, true, nil
}

// Edit starts editing m.
func (m *Map) Edit() *MapEditor { return &MapEditor{m: m, ed: m.m.Editor()} }

// MapEditor holds edits to a map until Flush.
type MapEditor struct {
	m  *Map
	ed *prolly.Editor
}

// Set writes key, refusing a key that cannot exist (ErrKey) and a value
// that does not encode (ErrValue); a refused Set leaves the edits as they
// were.
func (e *MapEditor) Set(key []byte, v Value) error {
	if err := checkKey(key); err != nil {
		return err
	}
	f, err := EncodeValue(v)
	if err != nil {
		return err
	}
	return e.ed.Put(key, f)
}

// Delete removes key; a key that is not there is a no-op.
func (e *MapEditor) Delete(key []byte) error {
	if err := checkKey(key); err != nil {
		return err
	}
	return e.ed.Delete(key)
}

// Flush writes the edits and returns the new map; the editor goes on
// editing it (the map's editor moves to the new map as it flushes).
func (e *MapEditor) Flush(ctx context.Context) (*Map, error) {
	pm, err := e.ed.Flush(ctx)
	if err != nil {
		return nil, err
	}
	return &Map{s: e.m.s, cfg: e.m.cfg, m: pm}, nil
}
