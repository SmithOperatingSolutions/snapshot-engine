package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/merge"
	"github.com/SmithOperatingSolutions/snapshot-core/core/object"
	"github.com/SmithOperatingSolutions/snapshot-core/core/vcs"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// Txn is a transaction on a session's branch: snapshot isolation (it reads
// the working set as of Begin, and its own writes), and an optimistic Commit
// that merges with whatever committed meanwhile through the models, and
// fails with ErrSerialization, writing nothing, when the two collide. It is
// not safe for concurrent use.
type Txn struct {
	s      *Session
	ws     vcs.WorkingSet    // the working set as of Begin
	base   *object.Namespace // its working namespace: what the transaction reads
	objs   map[string]*txnObject
	closed bool

	beforeSwap func() // a test seam: called before every swap Commit attempts
}

// txnObject is an object the transaction touched: opened, created or
// dropped.
type txnObject struct {
	table   *Table // the open handle; nil when dropped
	dropped bool
}

// maxCommitAttempts is how many times Commit re-reads, merges and swaps
// before it gives up with ErrSerialization.
const maxCommitAttempts = 5

// begin opens a transaction for s on its branch's working set as of now.
func begin(ctx context.Context, s *Session) (*Txn, error) {
	if s.db.isClosed() {
		return nil, ErrClosed
	}
	ws, err := s.db.r.WorkingSet(ctx, s.p, s.branch)
	if err != nil {
		return nil, translate(err)
	}
	if ws.Merge != nil {
		return nil, ErrMergeInProgress
	}
	base, err := s.db.r.Namespace(ctx, ws.Working)
	if err != nil {
		return nil, translate(err)
	}
	return &Txn{s: s, ws: ws, base: base, objs: map[string]*txnObject{}}, nil
}

// check refuses a call on a finished transaction or a closed database.
func (t *Txn) check() error {
	if t.closed || t.s.db.isClosed() {
		return ErrClosed
	}
	return nil
}

// lookup finds the object named name as the transaction sees it.
func (t *Txn) lookup(ctx context.Context, name string) (object.Ref, bool, error) {
	if o, ok := t.objs[name]; ok {
		if o.dropped {
			return object.Ref{}, false, nil
		}
		return object.Ref{Model: table.ID, Root: o.table.t.Root()}, true, nil
	}
	ref, _, ok, err := t.base.Get(ctx, name)
	if err != nil {
		return object.Ref{}, false, translate(err)
	}
	return ref, ok, nil
}

// CreateTable makes a table named name.
func (t *Txn) CreateTable(ctx context.Context, name string, s Schema) (*Table, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	if err := object.ValidPath(name); err != nil {
		return nil, fmt.Errorf("%w (%w)", ErrInvalid, err)
	}
	if _, ok, err := t.lookup(ctx, name); err != nil {
		return nil, err
	} else if ok {
		return nil, ErrExists
	}
	tb, err := table.Create(ctx, t.s.db.r.Chunks(), t.s.db.models.table.Config, s)
	if err != nil {
		return nil, tableErr(err)
	}
	h := &Table{tx: t, t: tb}
	t.objs[name] = &txnObject{table: h}
	return h, nil
}

// Table opens the table named name.
func (t *Txn) Table(ctx context.Context, name string) (*Table, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	if o, ok := t.objs[name]; ok {
		if o.dropped {
			return nil, ErrNotFound
		}
		return o.table, nil
	}
	ref, ok, err := t.lookup(ctx, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	if ref.Model != table.ID {
		return nil, ErrWrongKind
	}
	tb, err := table.Open(ctx, t.s.db.r.Chunks(), t.s.db.models.table.Config, ref.Root)
	if err != nil {
		return nil, translate(err)
	}
	h := &Table{tx: t, t: tb}
	t.objs[name] = &txnObject{table: h}
	return h, nil
}

// CreateKV makes a key-value map named name. (Stub.)
func (t *Txn) CreateKV(ctx context.Context, name string) (*KV, error) {
	return nil, errNotImplemented
}

// KV opens the key-value map named name. (Stub.)
func (t *Txn) KV(ctx context.Context, name string) (*KV, error) { return nil, errNotImplemented }

// CreateCollection makes a document collection named name. (Stub.)
func (t *Txn) CreateCollection(ctx context.Context, name string) (*Collection, error) {
	return nil, errNotImplemented
}

// Collection opens the document collection named name. (Stub.)
func (t *Txn) Collection(ctx context.Context, name string) (*Collection, error) {
	return nil, errNotImplemented
}

// Drop removes the object named name, of whatever kind; a handle to it
// refuses every call after.
func (t *Txn) Drop(ctx context.Context, name string) error {
	if err := t.check(); err != nil {
		return err
	}
	if _, ok, err := t.lookup(ctx, name); err != nil {
		return err
	} else if !ok {
		return ErrNotFound
	}
	if o, ok := t.objs[name]; ok && o.table != nil {
		o.table.gone = true
	}
	t.objs[name] = &txnObject{dropped: true}
	return nil
}

// namespace is the transaction's namespace: the one it began on with its
// changes applied.
func (t *Txn) namespace(ctx context.Context) (*object.Namespace, error) {
	e := t.base.Editor()
	for name, o := range t.objs {
		if o.dropped {
			if _, _, ok, err := t.base.Get(ctx, name); err != nil {
				return nil, translate(err)
			} else if ok {
				if err := e.Delete(name); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := o.table.flush(ctx); err != nil {
			return nil, err
		}
		if err := e.Put(name, object.Ref{Model: table.ID, Root: o.table.t.Root()}); err != nil {
			return nil, err
		}
	}
	return e.Flush(ctx)
}

// Commit makes the transaction's changes part of the branch's working set:
// all of them or, with ErrSerialization, none. If the working set moved
// since Begin, the transaction's changes are merged with what landed,
// through the models; a collision anywhere is ErrSerialization. A swap lost
// to a commit landing between the read and the swap is tried again, up to
// maxCommitAttempts times. The transaction is finished whatever happens.
func (t *Txn) Commit(ctx context.Context) error {
	if err := t.check(); err != nil {
		return err
	}
	t.closed = true
	ours, err := t.namespace(ctx)
	if err != nil {
		return err
	}
	if ours.Root() == t.base.Root() {
		return nil // nothing changed
	}
	r := t.s.db.r
	for range maxCommitAttempts {
		cur, err := r.WorkingSet(ctx, t.s.p, t.s.branch)
		if err != nil {
			return translate(err)
		}
		if cur.Merge != nil {
			return ErrMergeInProgress
		}
		next := cur
		if cur.Working == t.ws.Working {
			next.Working = ours.Root()
		} else {
			theirs, err := r.Namespace(ctx, cur.Working)
			if err != nil {
				return translate(err)
			}
			res, err := merge.Merge(ctx, t.s.db.models.registry, t.base, ours, theirs, r.Chunks(), merge.Options{})
			if errors.Is(err, merge.ErrTooManyConflicts) {
				return fmt.Errorf("%w (%w)", ErrSerialization, err)
			}
			if err != nil {
				return translate(err)
			}
			if len(res.Conflicts) > 0 {
				return ErrSerialization
			}
			next.Working = res.Merged.Root()
		}
		next.Staged = next.Working // the engine keeps no staging area
		if t.beforeSwap != nil {
			t.beforeSwap()
		}
		_, err = r.UpdateWorkingSet(ctx, t.s.p, t.s.branch, cur, next)
		if !errors.Is(err, vcs.ErrConflict) {
			return translate(err)
		}
	}
	return fmt.Errorf("%w: the working set moved under %d attempts in a row", ErrSerialization, maxCommitAttempts)
}

// Rollback discards the transaction.
func (t *Txn) Rollback(ctx context.Context) error {
	if err := t.check(); err != nil {
		return err
	}
	t.closed = true
	return nil
}

// Table is a table inside a transaction; reads see the transaction's writes.
type Table struct {
	tx   *Txn
	t    *table.Table
	ed   *table.Editor // writes not yet flushed; nil when none
	gone bool          // dropped in this transaction
}

// check refuses a call on a finished transaction or a dropped table.
func (h *Table) check() error {
	if err := h.tx.check(); err != nil {
		return err
	}
	if h.gone {
		return ErrNotFound
	}
	return nil
}

// flush writes the pending edits, so reads see them.
func (h *Table) flush(ctx context.Context) error {
	if h.ed == nil {
		return nil
	}
	t, err := h.ed.Flush(ctx)
	if err != nil {
		return err
	}
	h.t, h.ed = t, nil
	return nil
}

// editor is the pending edits, begun on first write.
func (h *Table) editor() *table.Editor {
	if h.ed == nil {
		h.ed = h.t.Edit()
	}
	return h.ed
}

// Schema is the table's schema.
func (h *Table) Schema() Schema { return h.t.Schema() }

// Get reads the row with key.
func (h *Table) Get(ctx context.Context, key Key) (Row, bool, error) {
	if err := h.check(); err != nil {
		return nil, false, err
	}
	if err := h.flush(ctx); err != nil {
		return nil, false, err
	}
	row, ok, err := h.t.Get(ctx, key)
	if err != nil {
		return nil, false, tableErr(err)
	}
	return row, ok, nil
}

// Scan calls each for every row in key order until it returns false or an
// error.
func (h *Table) Scan(ctx context.Context, each func(Key, Row) (bool, error)) error {
	if err := h.check(); err != nil {
		return err
	}
	if err := h.flush(ctx); err != nil {
		return err
	}
	rows, err := h.t.Scan(ctx)
	if err != nil {
		return tableErr(err)
	}
	return drain(rows, each)
}

// Lookup calls each for every row whose index columns equal values.
func (h *Table) Lookup(ctx context.Context, index Tag, values []any, each func(Key, Row) (bool, error)) error {
	if err := h.check(); err != nil {
		return err
	}
	if err := h.flush(ctx); err != nil {
		return err
	}
	rows, err := h.t.IndexLookup(ctx, index, values...)
	if err != nil {
		return tableErr(err)
	}
	return drain(rows, each)
}

// drain hands rows to each until it says stop.
func drain(rows *table.Rows, each func(Key, Row) (bool, error)) error {
	for {
		k, r, ok, err := rows.Next()
		if err != nil {
			return tableErr(err)
		}
		if !ok {
			return nil
		}
		if more, err := each(k, r); err != nil || !more {
			return err
		}
	}
}

// Insert adds a row and returns its key; a row the table refuses writes
// nothing.
func (h *Table) Insert(ctx context.Context, row Row) (Key, error) {
	if err := h.check(); err != nil {
		return nil, err
	}
	key, err := h.editor().Insert(row)
	if err != nil {
		return nil, tableErr(err)
	}
	return key, nil
}

// Update replaces the row with key.
func (h *Table) Update(ctx context.Context, key Key, row Row) error {
	if err := h.check(); err != nil {
		return err
	}
	return tableErr(h.editor().Update(key, row))
}

// Delete removes the row with key.
func (h *Table) Delete(ctx context.Context, key Key) error {
	if err := h.check(); err != nil {
		return err
	}
	return tableErr(h.editor().Delete(key))
}

// Alter gives the table a new schema (columns by tag: renames, additions,
// drops, widenings).
func (h *Table) Alter(ctx context.Context, next Schema) error {
	if err := h.check(); err != nil {
		return err
	}
	if err := h.flush(ctx); err != nil {
		return err
	}
	t, err := h.t.WithSchema(ctx, next)
	if err != nil {
		return tableErr(err)
	}
	h.t = t
	return nil
}

// tableErr is the engine error for the table model's.
func tableErr(err error) error {
	var to error
	switch {
	case err == nil:
		return nil
	case errors.Is(err, table.ErrValue), errors.Is(err, table.ErrSchema):
		to = ErrInvalid
	case errors.Is(err, table.ErrDuplicate):
		to = ErrExists
	case errors.Is(err, table.ErrNotFound):
		to = ErrNotFound
	default:
		return translate(err)
	}
	return fmt.Errorf("%w (%w)", to, err)
}

// KV is a key-value map inside a transaction.
type KV struct{}

// Get reads key. (Stub.)
func (m *KV) Get(ctx context.Context, key []byte) (Value, bool, error) {
	return Value{}, false, errNotImplemented
}

// Set writes key. (Stub.)
func (m *KV) Set(ctx context.Context, key []byte, v Value) error { return errNotImplemented }

// Delete removes key. (Stub.)
func (m *KV) Delete(ctx context.Context, key []byte) error { return errNotImplemented }

// Scan calls each for every key at or after from in key order until it
// returns false or an error. (Stub.)
func (m *KV) Scan(ctx context.Context, from []byte, each func([]byte, Value) (bool, error)) error {
	return errNotImplemented
}

// Collection is a document collection inside a transaction.
type Collection struct{}

// Get reads the record with id. (Stub.)
func (c *Collection) Get(ctx context.Context, id []byte) (Node, bool, error) {
	return Node{}, false, errNotImplemented
}

// Put writes the record with id. (Stub.)
func (c *Collection) Put(ctx context.Context, id []byte, doc Node) error { return errNotImplemented }

// PutJSON parses text with the document model's bounded parser and writes
// it. (Stub.)
func (c *Collection) PutJSON(ctx context.Context, id []byte, text []byte) error {
	return errNotImplemented
}

// Delete removes the record with id. (Stub.)
func (c *Collection) Delete(ctx context.Context, id []byte) error { return errNotImplemented }

// Scan calls each for every record at or after from in id order until it
// returns false or an error. (Stub.)
func (c *Collection) Scan(ctx context.Context, from []byte, each func([]byte, Node) (bool, error)) error {
	return errNotImplemented
}
