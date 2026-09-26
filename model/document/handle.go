package document

import (
	"bytes"
	"context"
	"sort"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

// Collection is a document object opened for reading and editing: point
// reads by id, scans in id order, and an editor whose Flush makes a new
// Collection. Reads see what was flushed.
type Collection struct {
	s   chunk.ReadWriter
	cfg prolly.Config
	m   *prolly.Map
}

// Empty is a new, empty collection.
func Empty(ctx context.Context, s chunk.ReadWriter, c prolly.Config) (*Collection, error) {
	m, err := prolly.Empty(ctx, s, config(c))
	if err != nil {
		return nil, err
	}
	return &Collection{s: s, cfg: c, m: m}, nil
}

// Open opens the object under root, checking the root's claims (its format,
// depth 0, a count equal to its size); a record is checked when a read
// meets it.
func Open(ctx context.Context, s chunk.ReadWriter, c prolly.Config, root model.Root) (*Collection, error) {
	m, err := spec(c).Open(ctx, s, root)
	if err != nil {
		return nil, err
	}
	return &Collection{s: s, cfg: c, m: m}, nil
}

// Root is the collection as an object.
func (c *Collection) Root() model.Root { return spec(c.cfg).Root(c.m) }

// Get reads the record with id, refusing an id that cannot exist (ErrID)
// and a stored frame that is not a record.
func (c *Collection) Get(ctx context.Context, id []byte) (merge.Node, bool, error) {
	if err := checkID(id); err != nil {
		return merge.Node{}, false, err
	}
	f, ok, err := c.m.Get(ctx, id)
	if err != nil || !ok {
		return merge.Node{}, false, err
	}
	n, err := checked(id, f)
	if err != nil {
		return merge.Node{}, false, err
	}
	return n, true, nil
}

// Scan walks the records from id from, inclusive (nil: the first), in id
// order.
func (c *Collection) Scan(ctx context.Context, from []byte) (*Records, error) {
	it, err := c.m.IterRange(ctx, from, nil)
	if err != nil {
		return nil, err
	}
	return &Records{it: it}, nil
}

// Records walks a collection's records in id order: the stored records,
// with an editor's pending edits laid over them when it made the scan.
type Records struct {
	it   *prolly.Iter
	over []overlay // pending edits from the scan's start, in id order
	// the stored record read ahead, so the two can be merged
	sk, sf []byte
	sok    bool
	primed bool
}

// overlay is one pending edit: a nil node is a delete.
type overlay struct {
	id []byte
	n  *merge.Node
}

// Next is the next record; ok is false at the end. A stored record that is
// not ours is an error.
func (r *Records) Next() (id []byte, n merge.Node, ok bool, err error) {
	for {
		if !r.primed {
			if r.sk, r.sf, r.sok, err = r.it.Next(); err != nil {
				return nil, merge.Node{}, false, err
			}
			r.primed = true
		}
		var o *overlay
		if len(r.over) > 0 {
			o = &r.over[0]
		}
		if o == nil && !r.sok {
			return nil, merge.Node{}, false, nil
		}
		// The stored record comes first, or the pending one; on the same
		// id the pending edit replaces or deletes the stored record.
		if o == nil || (r.sok && bytes.Compare(r.sk, o.id) < 0) {
			r.primed = false
			if n, err = checked(r.sk, r.sf); err != nil {
				return nil, merge.Node{}, false, err
			}
			return r.sk, n, true, nil
		}
		if r.sok && bytes.Equal(r.sk, o.id) {
			r.primed = false
		}
		r.over = r.over[1:]
		if o.n != nil {
			return o.id, *o.n, true, nil
		}
	}
}

// Edit starts editing c.
func (c *Collection) Edit() *CollectionEditor {
	return &CollectionEditor{c: c, ed: c.m.Editor(), pending: map[string]*merge.Node{}}
}

// CollectionEditor holds edits to a collection until Flush, and reads them
// back over the stored records meanwhile (#9).
type CollectionEditor struct {
	c       *Collection
	ed      *prolly.Editor
	pending map[string]*merge.Node // by id; nil: a delete
}

// Put writes the record with id, refusing an id that cannot exist (ErrID)
// and a tree that is not a document (ErrDocument); a refused Put leaves
// the edits as they were.
func (e *CollectionEditor) Put(id []byte, n merge.Node) error {
	if err := checkID(id); err != nil {
		return err
	}
	f, err := EncodeRecord(n)
	if err != nil {
		return err
	}
	if err := e.ed.Put(id, f); err != nil {
		return err
	}
	kept := n
	e.pending[string(id)] = &kept
	return nil
}

// PutJSON parses text with the model's bounded parser and writes it; text
// that is not a document is refused (ErrDocument).
func (e *CollectionEditor) PutJSON(id []byte, text []byte) error {
	n, err := Parse(text)
	if err != nil {
		return err
	}
	return e.Put(id, n)
}

// Delete removes the record with id; an id that is not there is a no-op.
func (e *CollectionEditor) Delete(id []byte) error {
	if err := checkID(id); err != nil {
		return err
	}
	if err := e.ed.Delete(id); err != nil {
		return err
	}
	e.pending[string(id)] = nil
	return nil
}

// Get reads the record with id as the collection the editor would flush
// holds it: its pending edit, else the stored record. It writes nothing.
func (e *CollectionEditor) Get(ctx context.Context, id []byte) (merge.Node, bool, error) {
	if err := checkID(id); err != nil {
		return merge.Node{}, false, err
	}
	if p, ok := e.pending[string(id)]; ok {
		if p == nil {
			return merge.Node{}, false, nil
		}
		return *p, true, nil
	}
	return e.c.Get(ctx, id)
}

// Scan walks the collection the editor would flush from id from, inclusive
// (nil: the first), in id order: the stored records with the pending edits
// laid over them. It writes nothing.
func (e *CollectionEditor) Scan(ctx context.Context, from []byte) (*Records, error) {
	it, err := e.c.m.IterRange(ctx, from, nil)
	if err != nil {
		return nil, err
	}
	var over []overlay
	for id, n := range e.pending {
		if bytes.Compare([]byte(id), from) >= 0 {
			over = append(over, overlay{id: []byte(id), n: n})
		}
	}
	sort.Slice(over, func(i, j int) bool { return bytes.Compare(over[i].id, over[j].id) < 0 })
	return &Records{it: it, over: over}, nil
}

// Flush writes the edits and returns the new collection; the editor goes
// on editing it (the map's editor moves to the new map as it flushes).
func (e *CollectionEditor) Flush(ctx context.Context) (*Collection, error) {
	pm, err := e.ed.Flush(ctx)
	if err != nil {
		return nil, err
	}
	return &Collection{s: e.c.s, cfg: e.c.cfg, m: pm}, nil
}
