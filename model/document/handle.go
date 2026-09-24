package document

import (
	"context"
	"errors"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

var errNotImplemented = errors.New("document: not implemented")

// Collection is a document object opened for reading and editing: point
// reads by id, scans in id order, and an editor whose Flush makes a new
// Collection. Reads see what was flushed. (Stub.)
type Collection struct {
	s   chunk.ReadWriter
	cfg prolly.Config
	m   *prolly.Map
}

// Empty is a new, empty collection. (Stub.)
func Empty(ctx context.Context, s chunk.ReadWriter, c prolly.Config) (*Collection, error) {
	return nil, errNotImplemented
}

// Open opens the object under root, checking the root's claims. (Stub.)
func Open(ctx context.Context, s chunk.ReadWriter, c prolly.Config, root model.Root) (*Collection, error) {
	return nil, errNotImplemented
}

// Root is the collection as an object. (Stub.)
func (c *Collection) Root() model.Root { return model.Root{} }

// Get reads the record with id. (Stub.)
func (c *Collection) Get(ctx context.Context, id []byte) (merge.Node, bool, error) {
	return merge.Node{}, false, errNotImplemented
}

// Scan walks the records from id from, inclusive (nil: the first), in id
// order. (Stub.)
func (c *Collection) Scan(ctx context.Context, from []byte) (*Records, error) {
	return nil, errNotImplemented
}

// Records walks a collection's records in id order.
type Records struct{ it *prolly.Iter }

// Next is the next record; ok is false at the end. (Stub.)
func (r *Records) Next() (id []byte, n merge.Node, ok bool, err error) {
	return nil, merge.Node{}, false, errNotImplemented
}

// Edit starts editing c. (Stub.)
func (c *Collection) Edit() *CollectionEditor { return &CollectionEditor{c: c} }

// CollectionEditor holds edits to a collection until Flush.
type CollectionEditor struct {
	c  *Collection
	ed *prolly.Editor
}

// Put writes the record with id. (Stub.)
func (e *CollectionEditor) Put(id []byte, n merge.Node) error { return errNotImplemented }

// PutJSON parses text with the model's bounded parser and writes it.
// (Stub.)
func (e *CollectionEditor) PutJSON(id []byte, text []byte) error { return errNotImplemented }

// Delete removes the record with id. (Stub.)
func (e *CollectionEditor) Delete(id []byte) error { return errNotImplemented }

// Flush writes the edits and returns the new collection. (Stub.)
func (e *CollectionEditor) Flush(ctx context.Context) (*Collection, error) {
	return nil, errNotImplemented
}
