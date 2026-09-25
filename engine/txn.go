package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/merge"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/object"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"github.com/SmithOperatingSolutions/snapshot-core/core/vcs"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
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

	beforeSwap func() // a test seam: called before every swap of a publish carrying the transaction
}

// txnObject is an object the transaction touched: opened, created or
// dropped.
type txnObject struct {
	h       handle // the open handle; nil when dropped
	dropped bool
}

// handle is an object open in a transaction: a table, a key-value map or a
// document collection.
type handle interface {
	ref() object.Ref                 // the object as it stands, its pending writes aside
	flush(ctx context.Context) error // write the pending writes, so reads see them
	drop()                           // the object was dropped: refuse every call after
}

// maxCommitAttempts is how many times a batch of commits is rebuilt on a
// root another process moved, and swapped, before its members give up
// with ErrSerialization.
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
		return o.h.ref(), true, nil
	}
	ref, _, ok, err := t.base.Get(ctx, name)
	if err != nil {
		return object.Ref{}, false, translate(err)
	}
	return ref, ok, nil
}

// creatable refuses a name an object cannot be made under: one that is not
// an object path (ErrInvalid) or is taken (ErrExists).
func (t *Txn) creatable(ctx context.Context, name string) error {
	if err := t.check(); err != nil {
		return err
	}
	if err := object.ValidPath(name); err != nil {
		return fmt.Errorf("%w (%w)", ErrInvalid, err)
	}
	if _, ok, err := t.lookup(ctx, name); err != nil {
		return err
	} else if ok {
		return ErrExists
	}
	return nil
}

// opened is the handle this transaction already holds for name, if any; a
// name dropped in this transaction is ErrNotFound.
func (t *Txn) opened(name string) (handle, bool, error) {
	o, ok := t.objs[name]
	if !ok {
		return nil, false, nil
	}
	if o.dropped {
		return nil, false, ErrNotFound
	}
	return o.h, true, nil
}

// stored is the object named name as the transaction began on it, which
// must be of model id.
func (t *Txn) stored(ctx context.Context, name string, id model.ID) (object.Ref, error) {
	ref, ok, err := t.lookup(ctx, name)
	if err != nil {
		return object.Ref{}, err
	}
	if !ok {
		return object.Ref{}, ErrNotFound
	}
	if ref.Model != id {
		return object.Ref{}, ErrWrongKind
	}
	return ref, nil
}

// CreateTable makes a table named name.
func (t *Txn) CreateTable(ctx context.Context, name string, s Schema) (_ *Table, err error) {
	defer t.s.db.scrubInto(ctx, &err)
	if err := t.creatable(ctx, name); err != nil {
		return nil, err
	}
	tb, err := table.Create(ctx, t.s.db.r.Chunks(), t.s.db.models.table.Config, s)
	if err != nil {
		return nil, tableErr(err)
	}
	h := &Table{tx: t, t: tb}
	t.objs[name] = &txnObject{h: h}
	return h, nil
}

// Table opens the table named name.
func (t *Txn) Table(ctx context.Context, name string) (_ *Table, err error) {
	defer t.s.db.scrubInto(ctx, &err)
	if err := t.check(); err != nil {
		return nil, err
	}
	if err := t.s.mayRead(ctx, name); err != nil { // asked on every open, so a revoked grant blocks the next
		return nil, err
	}
	if h, ok, err := t.opened(name); err != nil {
		return nil, err
	} else if ok {
		tb, ok := h.(*Table)
		if !ok {
			return nil, ErrWrongKind
		}
		return tb, nil
	}
	ref, err := t.stored(ctx, name, table.ID)
	if err != nil {
		return nil, err
	}
	tb, err := table.Open(ctx, t.s.db.r.Chunks(), t.s.db.models.table.Config, ref.Root)
	if err != nil {
		return nil, translate(err)
	}
	h := &Table{tx: t, t: tb}
	t.objs[name] = &txnObject{h: h}
	return h, nil
}

// CreateKV makes a key-value map named name.
func (t *Txn) CreateKV(ctx context.Context, name string) (_ *KV, err error) {
	defer t.s.db.scrubInto(ctx, &err)
	if err := t.creatable(ctx, name); err != nil {
		return nil, err
	}
	m, err := kv.Empty(ctx, t.s.db.r.Chunks(), t.s.db.models.kv.Config)
	if err != nil {
		return nil, translate(err)
	}
	h := &KV{tx: t, m: m}
	t.objs[name] = &txnObject{h: h}
	return h, nil
}

// KV opens the key-value map named name.
func (t *Txn) KV(ctx context.Context, name string) (_ *KV, err error) {
	defer t.s.db.scrubInto(ctx, &err)
	if err := t.check(); err != nil {
		return nil, err
	}
	if err := t.s.mayRead(ctx, name); err != nil { // asked on every open, so a revoked grant blocks the next
		return nil, err
	}
	if h, ok, err := t.opened(name); err != nil {
		return nil, err
	} else if ok {
		m, ok := h.(*KV)
		if !ok {
			return nil, ErrWrongKind
		}
		return m, nil
	}
	ref, err := t.stored(ctx, name, kv.ID)
	if err != nil {
		return nil, err
	}
	m, err := kv.Open(ctx, t.s.db.r.Chunks(), t.s.db.models.kv.Config, ref.Root)
	if err != nil {
		return nil, kvErr(err)
	}
	h := &KV{tx: t, m: m}
	t.objs[name] = &txnObject{h: h}
	return h, nil
}

// CreateCollection makes a document collection named name.
func (t *Txn) CreateCollection(ctx context.Context, name string) (_ *Collection, err error) {
	defer t.s.db.scrubInto(ctx, &err)
	if err := t.creatable(ctx, name); err != nil {
		return nil, err
	}
	c, err := document.Empty(ctx, t.s.db.r.Chunks(), t.s.db.models.document.Config)
	if err != nil {
		return nil, translate(err)
	}
	h := &Collection{tx: t, c: c}
	t.objs[name] = &txnObject{h: h}
	return h, nil
}

// Collection opens the document collection named name.
func (t *Txn) Collection(ctx context.Context, name string) (_ *Collection, err error) {
	defer t.s.db.scrubInto(ctx, &err)
	if err := t.check(); err != nil {
		return nil, err
	}
	if err := t.s.mayRead(ctx, name); err != nil { // asked on every open, so a revoked grant blocks the next
		return nil, err
	}
	if h, ok, err := t.opened(name); err != nil {
		return nil, err
	} else if ok {
		c, ok := h.(*Collection)
		if !ok {
			return nil, ErrWrongKind
		}
		return c, nil
	}
	ref, err := t.stored(ctx, name, document.ID)
	if err != nil {
		return nil, err
	}
	c, err := document.Open(ctx, t.s.db.r.Chunks(), t.s.db.models.document.Config, ref.Root)
	if err != nil {
		return nil, docErr(err)
	}
	h := &Collection{tx: t, c: c}
	t.objs[name] = &txnObject{h: h}
	return h, nil
}

// Drop removes the object named name, of whatever kind; a handle to it
// refuses every call after.
func (t *Txn) Drop(ctx context.Context, name string) (err error) {
	defer t.s.db.scrubInto(ctx, &err)
	if err := t.check(); err != nil {
		return err
	}
	if _, ok, err := t.lookup(ctx, name); err != nil {
		return err
	} else if !ok {
		return ErrNotFound
	}
	if o, ok := t.objs[name]; ok && o.h != nil {
		o.h.drop()
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
		if err := o.h.flush(ctx); err != nil {
			return nil, err
		}
		if err := e.Put(name, o.h.ref()); err != nil {
			return nil, err
		}
	}
	return e.Flush(ctx)
}

// Commit makes the transaction's changes part of the branch's working set:
// all of them or, with ErrSerialization, none. It waits its turn in the
// branch's commit queue and is published with the transactions queued
// with it (DESIGN D20), each checked alone: if the working set moved since
// Begin, the transaction's changes are merged with what landed, through
// the models, and a collision anywhere is ErrSerialization. A swap lost to
// another process is tried again, up to maxCommitAttempts times. A context
// that ends while the commit waits takes it out of the queue, changing
// nothing. The transaction is finished whatever happens.
func (t *Txn) Commit(ctx context.Context) (err error) {
	defer t.s.db.scrubInto(ctx, &err)
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
	return t.s.db.enqueue(t.s.branch, &queued{ctx: ctx, tx: t, ours: ours})
}

// onto is the namespace the transaction makes of cur: its own when cur is
// its snapshot, else ours merged with cur through the models (merged).
func (t *Txn) onto(ctx context.Context, ours, cur *object.Namespace) (*object.Namespace, error) {
	if cur.Root() == t.base.Root() {
		return ours, nil
	}
	return t.merged(ctx, ours, cur)
}

// merged is ours merged with cur through the models, refused
// (ErrSerialization) when an item both wrote (D18) or the merge conflicts.
func (t *Txn) merged(ctx context.Context, ours, cur *object.Namespace) (*object.Namespace, error) {
	if err := t.writeWrite(ctx, ours, cur); err != nil {
		return nil, err
	}
	// cur is the merge's first side, so the models rewrite it with what
	// this transaction wrote, a few items, rather than rewrite ours with
	// everything that landed since its snapshot: a batch's later members
	// would otherwise each rewrite all the earlier ones. The item rule has
	// refused every item both wrote, so which side is first changes
	// nothing in the result.
	res, err := merge.Merge(ctx, t.s.db.models.registry, t.base, cur, ours, t.s.db.r.Chunks(), merge.Options{})
	if errors.Is(err, merge.ErrTooManyConflicts) {
		return nil, fmt.Errorf("%w (%w)", ErrSerialization, err)
	}
	if err != nil {
		return nil, translate(err)
	}
	if len(res.Conflicts) > 0 {
		return nil, ErrSerialization
	}
	return res.Merged, nil
}

// writeWrite is ErrSerialization when ours and theirs, each against the
// transaction's snapshot, wrote one item: a table row, a kv key, a document
// record (the objects a transaction writes). A transaction is the
// one writer of an item until it commits (DESIGN D17), so a merge never
// decides between two writes of one item, even two the same: each was a
// write, and an application that read the item for either would lose the
// other. Objects added, deleted or changed to another model on either side
// are the merge's to refuse.
func (t *Txn) writeWrite(ctx context.Context, ours, theirs *object.Namespace) error {
	od, err := object.Diff(ctx, t.base, ours)
	if err != nil {
		return translate(err)
	}
	mine := map[string]object.Change{}
	for {
		c, ok, err := od.Next()
		if err != nil {
			return translate(err)
		}
		if !ok {
			break
		}
		if c.Kind == prolly.Modified && c.From.Model == c.To.Model {
			mine[c.Path] = c
		}
	}
	if len(mine) == 0 {
		return nil
	}
	td, err := object.Diff(ctx, t.base, theirs)
	if err != nil {
		return translate(err)
	}
	for {
		c, ok, err := td.Next()
		if err != nil {
			return translate(err)
		}
		if !ok {
			return nil
		}
		m, both := mine[c.Path]
		if !both || c.Kind != prolly.Modified || c.To.Model != m.To.Model {
			continue
		}
		written, whole, err := items(ctx, od, m)
		if err != nil {
			return err
		}
		if m.To.Model == kv.ID {
			if err := t.counterDeltas(ctx, m, written); err != nil {
				return err
			}
		}
		hit := whole
		if !hit {
			if hit, err = anyItem(ctx, td, c, written); err != nil {
				return err
			}
		}
		if hit {
			return fmt.Errorf("%w: a transaction committed since this one began wrote an item this one writes", ErrSerialization)
		}
	}
}

// items is every item a modified object's change wrote, or whole when it
// wrote the object as one item.
func items(ctx context.Context, d *object.DiffIter, c object.Change) (written map[string]bool, whole bool, err error) {
	written = map[string]bool{}
	whole, err = eachItem(ctx, d, c, func(item string) bool {
		written[item] = true
		return true
	})
	return written, whole, err
}

// counterDeltas takes out of written the keys a kv object's change c
// wrote from a counter to a counter: an INCR or DECR, whose delta the kv
// model adds to whatever landed since (DESIGN D18), not an item the rule
// refuses. A key that was or became another kind, or was added or
// deleted, stays an item; and a counter written here that the other side
// set to another kind or deleted is the merge's conflict.
func (t *Txn) counterDeltas(ctx context.Context, c object.Change, written map[string]bool) error {
	var maps [2]*kv.Map
	for i, root := range []model.Root{c.From.Root, c.To.Root} {
		m, err := kv.Open(ctx, t.s.db.r.Chunks(), t.s.db.models.kv.Config, root)
		if err != nil {
			return kvErr(err)
		}
		maps[i] = m
	}
	for key := range written {
		counter := true
		for _, m := range maps {
			v, ok, err := m.Get(ctx, []byte(key))
			if err != nil {
				return kvErr(err)
			}
			counter = counter && ok && v.Kind == kv.Counter
		}
		if counter {
			delete(written, key)
		}
	}
	return nil
}

// anyItem reports whether a modified object's change wrote any of written,
// or the object as one item.
func anyItem(ctx context.Context, d *object.DiffIter, c object.Change, written map[string]bool) (bool, error) {
	hit := false
	whole, err := eachItem(ctx, d, c, func(item string) bool {
		hit = written[item]
		return !hit
	})
	return hit || whole, err
}

// eachItem calls each for the item of every change the object's model
// reports, until each returns false: a table's row (a changed cell's
// location is the row's, then the column's tag: table.Locate), a kv key or
// a record's id (their locations). It reports whole instead, calling each
// for nothing, for a table whose schema changed: every row of it.
func eachItem(ctx context.Context, d *object.DiffIter, c object.Change, each func(item string) bool) (whole bool, err error) {
	it, err := d.Detail(ctx, c)
	if errors.Is(err, table.ErrSchema) {
		return true, nil
	}
	if err != nil {
		return false, translate(err)
	}
	for {
		mc, ok, err := it.Next(ctx)
		if err != nil {
			return false, translate(err)
		}
		if !ok {
			return false, nil
		}
		item := mc.Location
		if c.To.Model == table.ID && mc.Kind == model.Modified && len(item) >= 2 {
			item = item[:len(item)-2] // a cell's location: the row's, then the column's tag
		}
		if !each(string(item)) {
			return false, nil
		}
	}
}

// Rollback discards the transaction.
func (t *Txn) Rollback(ctx context.Context) (err error) {
	defer t.s.db.scrubInto(ctx, &err)
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

func (h *Table) ref() object.Ref { return object.Ref{Model: table.ID, Root: h.t.Root()} }

func (h *Table) drop() { h.gone = true }

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
func (h *Table) Get(ctx context.Context, key Key) (_ Row, _ bool, err error) {
	defer h.tx.s.db.scrubInto(ctx, &err)
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
func (h *Table) Scan(ctx context.Context, each func(Key, Row) (bool, error)) (err error) {
	defer h.tx.s.db.scrubInto(ctx, &err)
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
func (h *Table) Lookup(ctx context.Context, index Tag, values []any, each func(Key, Row) (bool, error)) (err error) {
	defer h.tx.s.db.scrubInto(ctx, &err)
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
			return callersOwn(err)
		}
	}
}

// Insert adds a row and returns its key; a row the table refuses writes
// nothing.
func (h *Table) Insert(ctx context.Context, row Row) (_ Key, err error) {
	defer h.tx.s.db.scrubInto(ctx, &err)
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
func (h *Table) Update(ctx context.Context, key Key, row Row) (err error) {
	defer h.tx.s.db.scrubInto(ctx, &err)
	if err := h.check(); err != nil {
		return err
	}
	return tableErr(h.editor().Update(key, row))
}

// Delete removes the row with key.
func (h *Table) Delete(ctx context.Context, key Key) (err error) {
	defer h.tx.s.db.scrubInto(ctx, &err)
	if err := h.check(); err != nil {
		return err
	}
	return tableErr(h.editor().Delete(key))
}

// Alter gives the table a new schema (columns by tag: renames, additions,
// drops, widenings).
func (h *Table) Alter(ctx context.Context, next Schema) (err error) {
	defer h.tx.s.db.scrubInto(ctx, &err)
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

// KV is a key-value map inside a transaction; reads see the transaction's
// writes.
type KV struct {
	tx   *Txn
	m    *kv.Map
	ed   *kv.MapEditor // writes not yet flushed; nil when none
	gone bool          // dropped in this transaction
}

func (m *KV) ref() object.Ref { return object.Ref{Model: kv.ID, Root: m.m.Root()} }

func (m *KV) drop() { m.gone = true }

// check refuses a call on a finished transaction or a dropped map.
func (m *KV) check() error {
	if err := m.tx.check(); err != nil {
		return err
	}
	if m.gone {
		return ErrNotFound
	}
	return nil
}

// flush writes the pending writes, so reads see them.
func (m *KV) flush(ctx context.Context) error {
	if m.ed == nil {
		return nil
	}
	next, err := m.ed.Flush(ctx)
	if err != nil {
		return kvErr(err)
	}
	m.m, m.ed = next, nil
	return nil
}

// editor is the pending writes, begun on the first.
func (m *KV) editor() *kv.MapEditor {
	if m.ed == nil {
		m.ed = m.m.Edit()
	}
	return m.ed
}

// Get reads key.
func (m *KV) Get(ctx context.Context, key []byte) (_ Value, _ bool, err error) {
	defer m.tx.s.db.scrubInto(ctx, &err)
	if err := m.check(); err != nil {
		return Value{}, false, err
	}
	if err := m.flush(ctx); err != nil {
		return Value{}, false, err
	}
	v, ok, err := m.m.Get(ctx, key)
	if err != nil {
		return Value{}, false, kvErr(err)
	}
	return v, ok, nil
}

// Set writes key; a key or value the kv model refuses is ErrInvalid and
// writes nothing.
func (m *KV) Set(ctx context.Context, key []byte, v Value) (err error) {
	defer m.tx.s.db.scrubInto(ctx, &err)
	if err := m.check(); err != nil {
		return err
	}
	return kvErr(m.editor().Set(key, v))
}

// Delete removes key. A key that is not there is a no-op, as in the kv
// model (Redis's DEL); a table's Delete of a missing row is ErrNotFound.
func (m *KV) Delete(ctx context.Context, key []byte) (err error) {
	defer m.tx.s.db.scrubInto(ctx, &err)
	if err := m.check(); err != nil {
		return err
	}
	return kvErr(m.editor().Delete(key))
}

// Scan calls each for every key at or after from (nil: the first) in key
// order until it returns false or an error.
func (m *KV) Scan(ctx context.Context, from []byte, each func([]byte, Value) (bool, error)) (err error) {
	defer m.tx.s.db.scrubInto(ctx, &err)
	if err := m.check(); err != nil {
		return err
	}
	if err := m.flush(ctx); err != nil {
		return err
	}
	es, err := m.m.Scan(ctx, from)
	if err != nil {
		return kvErr(err)
	}
	for {
		k, v, ok, err := es.Next()
		if err != nil {
			return kvErr(err)
		}
		if !ok {
			return nil
		}
		if more, err := each(k, v); err != nil || !more {
			return callersOwn(err)
		}
	}
}

// kvErr is the engine error for the kv model's.
func kvErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, kv.ErrKey), errors.Is(err, kv.ErrValue):
		return fmt.Errorf("%w (%w)", ErrInvalid, err)
	}
	return translate(err)
}

// Collection is a document collection inside a transaction; reads see the
// transaction's writes.
type Collection struct {
	tx   *Txn
	c    *document.Collection
	ed   *document.CollectionEditor // writes not yet flushed; nil when none
	gone bool                       // dropped in this transaction
}

func (c *Collection) ref() object.Ref { return object.Ref{Model: document.ID, Root: c.c.Root()} }

func (c *Collection) drop() { c.gone = true }

// check refuses a call on a finished transaction or a dropped collection.
func (c *Collection) check() error {
	if err := c.tx.check(); err != nil {
		return err
	}
	if c.gone {
		return ErrNotFound
	}
	return nil
}

// flush writes the pending writes, so reads see them.
func (c *Collection) flush(ctx context.Context) error {
	if c.ed == nil {
		return nil
	}
	next, err := c.ed.Flush(ctx)
	if err != nil {
		return docErr(err)
	}
	c.c, c.ed = next, nil
	return nil
}

// editor is the pending writes, begun on the first.
func (c *Collection) editor() *document.CollectionEditor {
	if c.ed == nil {
		c.ed = c.c.Edit()
	}
	return c.ed
}

// Get reads the record with id.
func (c *Collection) Get(ctx context.Context, id []byte) (_ Node, _ bool, err error) {
	defer c.tx.s.db.scrubInto(ctx, &err)
	if err := c.check(); err != nil {
		return Node{}, false, err
	}
	if err := c.flush(ctx); err != nil {
		return Node{}, false, err
	}
	n, ok, err := c.c.Get(ctx, id)
	if err != nil {
		return Node{}, false, docErr(err)
	}
	return n, ok, nil
}

// Put writes the record with id; an id or document the model refuses is
// ErrInvalid and writes nothing.
func (c *Collection) Put(ctx context.Context, id []byte, doc Node) (err error) {
	defer c.tx.s.db.scrubInto(ctx, &err)
	if err := c.check(); err != nil {
		return err
	}
	return docErr(c.editor().Put(id, doc))
}

// PutJSON parses text with the document model's bounded parser and writes
// it; malformed or oversized text is ErrInvalid and writes nothing.
func (c *Collection) PutJSON(ctx context.Context, id []byte, text []byte) (err error) {
	defer c.tx.s.db.scrubInto(ctx, &err)
	if err := c.check(); err != nil {
		return err
	}
	return docErr(c.editor().PutJSON(id, text))
}

// Delete removes the record with id. A record that is not there is a
// no-op, as in the document model (Mongo's deleteOne); a table's Delete of
// a missing row is ErrNotFound.
func (c *Collection) Delete(ctx context.Context, id []byte) (err error) {
	defer c.tx.s.db.scrubInto(ctx, &err)
	if err := c.check(); err != nil {
		return err
	}
	return docErr(c.editor().Delete(id))
}

// Scan calls each for every record at or after from (nil: the first) in id
// order until it returns false or an error.
func (c *Collection) Scan(ctx context.Context, from []byte, each func([]byte, Node) (bool, error)) (err error) {
	defer c.tx.s.db.scrubInto(ctx, &err)
	if err := c.check(); err != nil {
		return err
	}
	if err := c.flush(ctx); err != nil {
		return err
	}
	rs, err := c.c.Scan(ctx, from)
	if err != nil {
		return docErr(err)
	}
	for {
		id, n, ok, err := rs.Next()
		if err != nil {
			return docErr(err)
		}
		if !ok {
			return nil
		}
		if more, err := each(id, n); err != nil || !more {
			return callersOwn(err)
		}
	}
}

// docErr is the engine error for the document model's.
func docErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, document.ErrID), errors.Is(err, document.ErrDocument):
		return fmt.Errorf("%w (%w)", ErrInvalid, err)
	}
	return translate(err)
}
