package kv

import (
	"bytes"
	"context"
	"sort"

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
	m, err := prolly.Empty(ctx, s, config(c))
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

// Entries walks a map's entries in key order: the stored entries, with an
// editor's pending edits laid over them when it made the scan.
type Entries struct {
	it   *prolly.Iter
	over []overlay // pending edits from the scan's start, in key order
	// the stored entry read ahead, so the two can be merged
	sk, sf []byte
	sok    bool
	primed bool
}

// overlay is one pending edit: a nil value is a delete.
type overlay struct {
	key []byte
	v   *Value
}

// Next is the next entry; ok is false at the end. A stored entry that is
// not ours is an error.
func (e *Entries) Next() (key []byte, v Value, ok bool, err error) {
	for {
		if !e.primed {
			if e.sk, e.sf, e.sok, err = e.it.Next(); err != nil {
				return nil, Value{}, false, err
			}
			e.primed = true
		}
		var o *overlay
		if len(e.over) > 0 {
			o = &e.over[0]
		}
		if o == nil && !e.sok {
			return nil, Value{}, false, nil
		}
		// The stored entry comes first, or the pending one; on the same
		// key the pending edit replaces or deletes the stored entry.
		if o == nil || (e.sok && bytes.Compare(e.sk, o.key) < 0) {
			e.primed = false
			if v, err = checked(e.sk, e.sf); err != nil {
				return nil, Value{}, false, err
			}
			return e.sk, v, true, nil
		}
		if e.sok && bytes.Equal(e.sk, o.key) {
			e.primed = false
		}
		e.over = e.over[1:]
		if o.v != nil {
			return o.key, *o.v, true, nil
		}
	}
}

// Edit starts editing m.
func (m *Map) Edit() *MapEditor {
	return &MapEditor{m: m, ed: m.m.Editor(), pending: map[string]*Value{}}
}

// MapEditor holds edits to a map until Flush, and reads them back over the
// stored map meanwhile (#9).
type MapEditor struct {
	m       *Map
	ed      *prolly.Editor
	pending map[string]*Value // by key; nil: a delete
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
	if err := e.ed.Put(key, f); err != nil {
		return err
	}
	kept := v
	kept.Bytes = bytes.Clone(v.Bytes)
	e.pending[string(key)] = &kept
	return nil
}

// Delete removes key; a key that is not there is a no-op.
func (e *MapEditor) Delete(key []byte) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if err := e.ed.Delete(key); err != nil {
		return err
	}
	e.pending[string(key)] = nil
	return nil
}

// Get reads key as the map the editor would flush holds it: its pending
// edit, else the stored entry. It writes nothing.
func (e *MapEditor) Get(ctx context.Context, key []byte) (Value, bool, error) {
	if err := checkKey(key); err != nil {
		return Value{}, false, err
	}
	if p, ok := e.pending[string(key)]; ok {
		if p == nil {
			return Value{}, false, nil
		}
		return *p, true, nil
	}
	return e.m.Get(ctx, key)
}

// Scan walks the map the editor would flush from key from, inclusive
// (nil: the first), in key order: the stored entries with the pending
// edits laid over them. It writes nothing.
func (e *MapEditor) Scan(ctx context.Context, from []byte) (*Entries, error) {
	it, err := e.m.m.IterRange(ctx, from, nil)
	if err != nil {
		return nil, err
	}
	var over []overlay
	for k, v := range e.pending {
		if bytes.Compare([]byte(k), from) >= 0 {
			over = append(over, overlay{key: []byte(k), v: v})
		}
	}
	sort.Slice(over, func(i, j int) bool { return bytes.Compare(over[i].key, over[j].key) < 0 })
	return &Entries{it: it, over: over}, nil
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
