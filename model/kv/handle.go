package kv

import (
	"context"
	"errors"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
)

var errNotImplemented = errors.New("kv: not implemented")

// Map is a key-value object opened for reading and editing: point reads,
// scans in key order, and an editor whose Flush makes a new Map. Reads see
// what was flushed. (Stub.)
type Map struct {
	s   chunk.ReadWriter
	cfg prolly.Config
	m   *prolly.Map
}

// Empty is a new, empty map. (Stub.)
func Empty(ctx context.Context, s chunk.ReadWriter, c prolly.Config) (*Map, error) {
	return nil, errNotImplemented
}

// Open opens the object under root, checking the root's claims. (Stub.)
func Open(ctx context.Context, s chunk.ReadWriter, c prolly.Config, root model.Root) (*Map, error) {
	return nil, errNotImplemented
}

// Root is the map as an object. (Stub.)
func (m *Map) Root() model.Root { return model.Root{} }

// Get reads key. (Stub.)
func (m *Map) Get(ctx context.Context, key []byte) (Value, bool, error) {
	return Value{}, false, errNotImplemented
}

// Scan walks the entries from key from, inclusive (nil: the first), in key
// order. (Stub.)
func (m *Map) Scan(ctx context.Context, from []byte) (*Entries, error) {
	return nil, errNotImplemented
}

// Entries walks a map's entries in key order.
type Entries struct{ it *prolly.Iter }

// Next is the next entry; ok is false at the end. (Stub.)
func (e *Entries) Next() (key []byte, v Value, ok bool, err error) {
	return nil, Value{}, false, errNotImplemented
}

// Edit starts editing m. (Stub.)
func (m *Map) Edit() *MapEditor { return &MapEditor{m: m} }

// MapEditor holds edits to a map until Flush.
type MapEditor struct {
	m  *Map
	ed *prolly.Editor
}

// Set writes key. (Stub.)
func (e *MapEditor) Set(key []byte, v Value) error { return errNotImplemented }

// Delete removes key. (Stub.)
func (e *MapEditor) Delete(key []byte) error { return errNotImplemented }

// Flush writes the edits and returns the new map. (Stub.)
func (e *MapEditor) Flush(ctx context.Context) (*Map, error) { return nil, errNotImplemented }
