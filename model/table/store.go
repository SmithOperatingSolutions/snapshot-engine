package table

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"github.com/SmithOperatingSolutions/snapshot-core/core/wire"
)

// Row is a row's cells by column tag; a column left out is NULL.
type Row map[Tag]any

// Key is a row's primary key: the key columns' values in the schema's
// order, or, for a table without a declared key, its one RowID.
type Key []any

// RowID is the hidden 16-byte random id of a row in a table without a
// declared primary key.
type RowID [16]byte

// Errors of the row API.
var (
	ErrDuplicate = errors.New("table: a row with that key exists")
	ErrNotFound  = errors.New("table: no row with that key")
)

// Table is an immutable table over a chunk store: its schema, the primary
// map from encoded key to the row's other columns, and one map per index.
type Table struct {
	schema  Schema
	catalog []byte
	cfg     prolly.Config
	store   chunk.ReadWriter
	primary *prolly.Map
	indexes map[Tag]*prolly.Map // by index tag, in the schema's order in indexOrder
	root    model.Root
}

// readOnly opens a map over a Reader: reading one never writes.
type readOnly struct{ chunk.Reader }

func (readOnly) Put(context.Context, []byte) (hash.Hash, error) {
	return hash.Hash{}, errors.New("table: this store is read-only")
}

// Create stores an empty table with schema.
func Create(ctx context.Context, s chunk.ReadWriter, cfg prolly.Config, schema Schema) (*Table, error) {
	t, err := empty(ctx, s, cfg, schema)
	if err != nil {
		return nil, err
	}
	if err := t.writeRoot(ctx); err != nil {
		return nil, err
	}
	return t, nil
}

// empty is a table with schema and no rows, its root not yet written.
func empty(ctx context.Context, s chunk.ReadWriter, cfg prolly.Config, schema Schema) (*Table, error) {
	catalog, err := EncodeCatalog(schema)
	if err != nil {
		return nil, err
	}
	t := &Table{schema: schema, catalog: catalog, cfg: cfg, store: s, indexes: map[Tag]*prolly.Map{}}
	if t.primary, err = prolly.Empty(ctx, s, rowConfig(cfg, schema)); err != nil {
		return nil, err
	}
	for _, ix := range schema.Indexes {
		if t.indexes[ix.Tag], err = prolly.Empty(ctx, s, indexConfig(cfg)); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// writeRoot stores the root record for t's maps and sets t.root.
func (t *Table) writeRoot(ctx context.Context) error {
	r := rootRecord{catalog: t.catalog, primary: indexRoot{root: t.primary.Root(), count: t.primary.Count()}}
	for _, ix := range t.schema.Indexes {
		m := t.indexes[ix.Tag]
		r.indexes = append(r.indexes, indexRoot{tag: ix.Tag, root: m.Root(), count: m.Count()})
	}
	h, err := t.store.Put(ctx, encodeRoot(r))
	if err != nil {
		return err
	}
	t.root = model.Root{Hash: h, Size: t.primary.Count(), Format: Format}
	return nil
}

// Open opens the table at root and checks the root's claims against it.
func Open(ctx context.Context, s chunk.ReadWriter, cfg prolly.Config, root model.Root) (*Table, error) {
	if root.Format != Format {
		return nil, fmt.Errorf("%w: table format %d", model.ErrUnknownModel, root.Format)
	}
	if root.Depth != 0 {
		return nil, fmt.Errorf("%w: a table root claims stream depth %d", chunk.ErrCorrupt, root.Depth)
	}
	b, err := s.Get(ctx, root.Hash)
	if err != nil {
		return nil, err
	}
	r, err := decodeRoot(b)
	if err != nil {
		return nil, err
	}
	schema, err := DecodeCatalog(r.catalog)
	if err != nil {
		return nil, err
	}
	if r.primary.count != root.Size {
		return nil, fmt.Errorf("%w: a table of %d rows whose root says %d", chunk.ErrCorrupt, r.primary.count, root.Size)
	}
	t := &Table{schema: schema, catalog: r.catalog, cfg: cfg, store: s, indexes: map[Tag]*prolly.Map{}, root: root}
	if t.primary, err = openMap(ctx, s, rowConfig(cfg, schema), r.primary); err != nil {
		return nil, err
	}
	for i, ix := range schema.Indexes { // decodeRoot holds the record's indexes to the catalog's, in order, each as full as the primary map
		if t.indexes[ix.Tag], err = openMap(ctx, s, indexConfig(cfg), r.indexes[i]); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// rowConfig is cfg for schema's primary map: no row record is longer
// than its non-key cells at their longest (maxCell), so no longer value is
// read, a stream's claimed length being able to pass what it stores many
// times over (snapshot-core#23). A row of no non-key cells is empty, and
// its limit is one byte, 0 being the core's default.
func rowConfig(cfg prolly.Config, schema Schema) prolly.Config {
	n := 0
	for _, c := range schema.Columns {
		if !schema.isKey(c.Tag) {
			n += maxCell(c)
		}
	}
	cfg.MaxValue = max(n, 1)
	return cfg
}

// indexConfig is cfg for an index map, whose values are empty: its limit is
// one byte, 0 being the core's default.
func indexConfig(cfg prolly.Config) prolly.Config {
	cfg.MaxValue = 1
	return cfg
}

func openMap(ctx context.Context, s chunk.ReadWriter, cfg prolly.Config, ir indexRoot) (*prolly.Map, error) {
	m, err := prolly.Open(ctx, s, cfg, hash.Hash(ir.root))
	if err != nil {
		return nil, err
	}
	if m.Count() != ir.count {
		return nil, fmt.Errorf("%w: a map of %d entries whose root record says %d", chunk.ErrCorrupt, m.Count(), ir.count)
	}
	return m, nil
}

// Schema is the table's schema.
func (t *Table) Schema() Schema { return t.schema }

// Root is the table as an object.
func (t *Table) Root() model.Root { return t.root }

// keyed says whether the table has a declared primary key.
func (t *Table) keyed() bool { return len(t.schema.PrimaryKey) > 0 }

// encodeKey encodes a row's key: the key columns' cells, or the RowID.
func (t *Table) encodeKey(key Key) ([]byte, error) {
	if !t.keyed() {
		if len(key) != 1 {
			return nil, fmt.Errorf("%w: a key of %d values for a table without a declared key, want its row id", ErrValue, len(key))
		}
		id, ok := key[0].(RowID)
		if !ok {
			return nil, fmt.Errorf("%w: a key of %T for a table without a declared key, want a RowID", ErrValue, key[0])
		}
		return id[:], nil
	}
	if len(key) != len(t.schema.PrimaryKey) {
		return nil, fmt.Errorf("%w: a key of %d values, the primary key has %d columns", ErrValue, len(key), len(t.schema.PrimaryKey))
	}
	var b []byte
	for i, tag := range t.schema.PrimaryKey {
		c, _, _ := t.schema.column(tag)
		var err error
		if b, err = encodeCell(b, c, key[i]); err != nil {
			return nil, err
		}
	}
	if len(b) > prolly.MaxKeySize {
		return nil, fmt.Errorf("%w: a key of %d bytes, over %d", ErrValue, len(b), prolly.MaxKeySize)
	}
	return b, nil
}

// decodeKey reads what encodeKey wrote.
func (t *Table) decodeKey(b []byte) (Key, error) {
	if !t.keyed() {
		if len(b) != len(RowID{}) {
			return nil, fmt.Errorf("%w: a row id of %d bytes", chunk.ErrCorrupt, len(b))
		}
		var id RowID
		copy(id[:], b)
		return Key{id}, nil
	}
	key := make(Key, 0, len(t.schema.PrimaryKey))
	for _, tag := range t.schema.PrimaryKey {
		c, _, _ := t.schema.column(tag)
		v, rest, err := decodeCell(b, c)
		if err != nil {
			return nil, err
		}
		key = append(key, v)
		b = rest
	}
	if len(b) != 0 {
		return nil, fmt.Errorf("%w: %d bytes after a key", chunk.ErrCorrupt, len(b))
	}
	return key, nil
}

// isKeyColumn says whether tag is a primary key column.
func (t *Table) isKeyColumn(tag Tag) bool { return t.schema.isKey(tag) }

// isKey says whether tag is a primary key column.
func (s Schema) isKey(tag Tag) bool {
	for _, k := range s.PrimaryKey {
		if k == tag {
			return true
		}
	}
	return false
}

// encodeValue encodes the row's non-key columns in schema order, checking
// every cell against its column and refusing a column the schema lacks.
func (t *Table) encodeValue(row Row) ([]byte, error) {
	for tag := range row {
		if _, _, ok := t.schema.column(tag); !ok {
			return nil, fmt.Errorf("%w: the schema has no column %d", ErrValue, tag)
		}
	}
	var b []byte
	for _, c := range t.schema.Columns {
		if t.isKeyColumn(c.Tag) {
			continue
		}
		var err error
		if b, err = encodeCell(b, c, row[c.Tag]); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// decodeValue reads what encodeValue wrote, into row (the key columns
// already set).
func (t *Table) decodeValue(b []byte, row Row) error {
	for _, c := range t.schema.Columns {
		if t.isKeyColumn(c.Tag) {
			continue
		}
		v, rest, err := decodeCell(b, c)
		if err != nil {
			return err
		}
		if v != nil {
			row[c.Tag] = v
		}
		b = rest
	}
	if len(b) != 0 {
		return fmt.Errorf("%w: %d bytes after a row", chunk.ErrCorrupt, len(b))
	}
	return nil
}

// decodeRow rebuilds a row from its key and value.
func (t *Table) decodeRow(kb, vb []byte) (Key, Row, error) {
	key, err := t.decodeKey(kb)
	if err != nil {
		return nil, nil, err
	}
	row := Row{}
	for i, tag := range t.schema.PrimaryKey {
		row[tag] = key[i]
	}
	if err := t.decodeValue(vb, row); err != nil {
		return nil, nil, err
	}
	return key, row, nil
}

// keyOf takes a row's key from its key columns.
func (t *Table) keyOf(row Row) (Key, error) {
	key := make(Key, 0, len(t.schema.PrimaryKey))
	for _, tag := range t.schema.PrimaryKey {
		v, ok := row[tag]
		if !ok {
			c, _, _ := t.schema.column(tag)
			return nil, fmt.Errorf("%w: key column %q is missing", ErrValue, c.Name)
		}
		key = append(key, v)
	}
	return key, nil
}

// indexKey encodes an index entry for a row: the index columns' cells then
// the encoded primary key.
func (t *Table) indexKey(ix Index, row Row, kb []byte) ([]byte, error) {
	var b []byte
	for _, tag := range ix.Columns {
		c, _, _ := t.schema.column(tag)
		var err error
		if b, err = encodeCell(b, c, row[tag]); err != nil {
			return nil, err
		}
	}
	b = append(b, kb...)
	if len(b) > prolly.MaxKeySize {
		return nil, fmt.Errorf("%w: an index entry of %d bytes, over %d", ErrValue, len(b), prolly.MaxKeySize)
	}
	return b, nil
}

// Get returns the row with key.
func (t *Table) Get(ctx context.Context, key Key) (Row, bool, error) {
	kb, err := t.encodeKey(key)
	if err != nil {
		return nil, false, err
	}
	vb, ok, err := t.primary.Get(ctx, kb)
	if err != nil || !ok {
		return nil, false, err
	}
	_, row, err := t.decodeRow(kb, vb)
	if err != nil {
		return nil, false, err
	}
	return row, true, nil
}

// Rows walks rows in key order, or in an index's order: the stored rows,
// with an editor's pending edits laid over them when it made the walk.
type Rows struct {
	t       *Table
	ctx     context.Context
	it      *prolly.Iter
	indexed bool   // it walks an index: each entry names a row to read
	index   *Index // that index
	err     error
	// an editor's pending rows in the walk's order (by row key, or by
	// index entry), and the keys of every row it changed, whose stored
	// entries the walk skips
	over []rowOverlay
	skip map[string]bool
	// the stored entry read ahead, so the two can be merged
	sk, sv []byte
	sok    bool
	primed bool
}

// rowOverlay is one pending row at its place in the walk: at is its row
// key, or its index entry.
type rowOverlay struct {
	at []byte
	p  *pendingRow
}

// Next returns the next row and its key; ok is false at the end.
func (r *Rows) Next() (Key, Row, bool, error) {
	if r.err != nil {
		return nil, nil, false, r.err
	}
	if r.skip == nil {
		kb, vb, ok, err := r.it.Next()
		if err != nil || !ok {
			return nil, nil, false, err
		}
		return r.row(kb, vb)
	}
	for {
		if !r.primed {
			var err error
			if r.sk, r.sv, r.sok, err = r.it.Next(); err != nil {
				return nil, nil, false, err
			}
			r.primed = true
		}
		var o *rowOverlay
		if len(r.over) > 0 {
			o = &r.over[0]
		}
		if o == nil && !r.sok {
			return nil, nil, false, nil
		}
		if o == nil || (r.sok && bytes.Compare(r.sk, o.at) < 0) {
			r.primed = false
			if r.indexed { // a row the editor changed is walked from its pending entry, if it still matches
				rk, err := r.t.rowKeyOfIndexEntry(*r.index, r.sk)
				if err != nil {
					return nil, nil, false, err
				}
				if r.skip[string(rk)] {
					continue
				}
			} else if r.skip[string(r.sk)] {
				continue
			}
			return r.row(r.sk, r.sv)
		}
		r.over = r.over[1:]
		return o.p.key, cloneRow(o.p.row, o.p.key, r.t), true, nil
	}
}

// row is the row a stored entry names: the entry itself, or, on an index
// walk, the row it points to.
func (r *Rows) row(kb, vb []byte) (Key, Row, bool, error) {
	var err error
	if r.indexed {
		if len(vb) != 0 {
			return nil, nil, false, fmt.Errorf("%w: an index entry with a value", chunk.ErrCorrupt)
		}
		kb, err = r.t.rowKeyOfIndexEntry(*r.index, kb)
		if err != nil {
			return nil, nil, false, err
		}
		var ok bool
		if vb, ok, err = r.t.primary.Get(r.ctx, kb); err != nil {
			return nil, nil, false, err
		} else if !ok {
			return nil, nil, false, fmt.Errorf("%w: an index entry names a row that is not in the table", chunk.ErrCorrupt)
		}
	}
	key, row, err := r.t.decodeRow(kb, vb)
	if err != nil {
		return nil, nil, false, err
	}
	return key, row, true, nil
}

// rowKeyOfIndexEntry is the primary key at the end of an index entry: the
// index columns' cells are skipped by decoding them.
func (t *Table) rowKeyOfIndexEntry(ix Index, b []byte) ([]byte, error) {
	for _, tag := range ix.Columns {
		c, _, _ := t.schema.column(tag)
		_, rest, err := decodeCell(b, c)
		if err != nil {
			return nil, err
		}
		b = rest
	}
	return b, nil
}

// Scan walks every row in key order.
func (t *Table) Scan(ctx context.Context) (*Rows, error) {
	it, err := t.primary.IterRange(ctx, nil, nil)
	if err != nil {
		return nil, err
	}
	return &Rows{t: t, ctx: ctx, it: it}, nil
}

// IndexLookup walks, in index order, the rows whose leading index columns
// equal values.
func (t *Table) IndexLookup(ctx context.Context, index Tag, values ...any) (*Rows, error) {
	var ix *Index
	for i := range t.schema.Indexes {
		if t.schema.Indexes[i].Tag == index {
			ix = &t.schema.Indexes[i]
		}
	}
	if ix == nil {
		return nil, fmt.Errorf("%w: the schema has no index %d", ErrValue, index)
	}
	if len(values) > len(ix.Columns) {
		return nil, fmt.Errorf("%w: %d values for an index of %d columns", ErrValue, len(values), len(ix.Columns))
	}
	var lo []byte
	for i, v := range values {
		c, _, _ := t.schema.column(ix.Columns[i])
		var err error
		if lo, err = encodeCell(lo, c, v); err != nil {
			return nil, err
		}
	}
	hi := successor(lo) // every entry with prefix lo, and none other
	it, err := t.indexes[index].IterRange(ctx, lo, hi)
	if err != nil {
		return nil, err
	}
	return &Rows{t: t, ctx: ctx, it: it, indexed: true, index: ix}, nil
}

// successor is the least byte string greater than every string with prefix
// p; nil if there is none (p is empty or all 0xFF).
func successor(p []byte) []byte {
	s := bytes.Clone(p)
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != 0xFF {
			s[i]++
			return s[:i+1]
		}
	}
	return nil
}

// Editor collects inserts, updates and deletes against a table; Flush
// writes them all, the indexes with the primary map, or nothing.
type Editor struct {
	t       *Table
	pending map[string]*pendingRow // by encoded key
	order   []string
}

// pendingRow is a row's state after the editor's changes: nil row is a delete.
type pendingRow struct {
	key Key
	row Row
	vb  []byte
}

// Edit starts editing t.
func (t *Table) Edit() *Editor { return &Editor{t: t, pending: map[string]*pendingRow{}} }

// exists says whether a row with encoded key kb exists after the pending
// edits, and returns its value.
func (e *Editor) exists(ctx context.Context, kb []byte) ([]byte, bool, error) {
	if p, ok := e.pending[string(kb)]; ok {
		return p.vb, p.row != nil, nil
	}
	return e.t.primary.Get(ctx, kb)
}

func (e *Editor) set(kb []byte, p *pendingRow) {
	if _, ok := e.pending[string(kb)]; !ok {
		e.order = append(e.order, string(kb))
	}
	e.pending[string(kb)] = p
}

// Get reads the row with key as the table the editor would flush holds
// it: its pending edit, else the stored row. It writes nothing.
func (e *Editor) Get(ctx context.Context, key Key) (Row, bool, error) {
	kb, err := e.t.encodeKey(key)
	if err != nil {
		return nil, false, err
	}
	if p, ok := e.pending[string(kb)]; ok {
		if p.row == nil {
			return nil, false, nil
		}
		return cloneRow(p.row, p.key, e.t), true, nil
	}
	return e.t.Get(ctx, key)
}

// Scan walks the table the editor would flush in key order: the stored
// rows with the pending edits laid over them. It writes nothing.
func (e *Editor) Scan(ctx context.Context) (*Rows, error) {
	rows, err := e.t.Scan(ctx)
	if err != nil {
		return nil, err
	}
	rows.skip = map[string]bool{}
	for kb, p := range e.pending {
		rows.skip[kb] = true
		if p.row != nil {
			rows.over = append(rows.over, rowOverlay{at: []byte(kb), p: p})
		}
	}
	sort.Slice(rows.over, func(i, j int) bool { return bytes.Compare(rows.over[i].at, rows.over[j].at) < 0 })
	return rows, nil
}

// IndexLookup walks the rows of the table the editor would flush whose
// index columns equal values, in the index's order: the stored rows, less
// the ones the editor changed, with the pending rows that match laid over
// them. It writes nothing.
func (e *Editor) IndexLookup(ctx context.Context, index Tag, values ...any) (*Rows, error) {
	rows, err := e.t.IndexLookup(ctx, index, values...)
	if err != nil {
		return nil, err
	}
	var lo []byte
	for i, v := range values {
		c, _, _ := e.t.schema.column(rows.index.Columns[i])
		if lo, err = encodeCell(lo, c, v); err != nil {
			return nil, err
		}
	}
	rows.skip = map[string]bool{}
	for kb, p := range e.pending {
		rows.skip[kb] = true
		if p.row == nil {
			continue
		}
		ik, err := e.t.indexKey(*rows.index, p.row, []byte(kb))
		if err != nil {
			return nil, err
		}
		if bytes.HasPrefix(ik, lo) {
			rows.over = append(rows.over, rowOverlay{at: ik, p: p})
		}
	}
	sort.Slice(rows.over, func(i, j int) bool { return bytes.Compare(rows.over[i].at, rows.over[j].at) < 0 })
	return rows, nil
}

// Insert adds a row, refusing a value that does not fit its column
// (ErrValue) and a key that exists (ErrDuplicate); it returns the row's key,
// a fresh RowID for a table without a declared key.
func (e *Editor) Insert(row Row) (Key, error) {
	var key Key
	if e.t.keyed() {
		var err error
		if key, err = e.t.keyOf(row); err != nil {
			return nil, err
		}
	} else {
		var id RowID
		if _, err := rand.Read(id[:]); err != nil {
			return nil, err
		}
		key = Key{id}
	}
	kb, err := e.t.encodeKey(key)
	if err != nil {
		return nil, err
	}
	vb, err := e.t.encodeValue(row)
	if err != nil {
		return nil, err
	}
	if _, ok, err := e.exists(context.Background(), kb); err != nil {
		return nil, err
	} else if ok {
		return nil, fmt.Errorf("%w: %v", ErrDuplicate, key)
	}
	e.set(kb, &pendingRow{key: key, row: cloneRow(row, key, e.t), vb: vb})
	return key, nil
}

// cloneRow copies row with the key columns set from key.
func cloneRow(row Row, key Key, t *Table) Row {
	out := make(Row, len(row))
	for k, v := range row {
		out[k] = v
	}
	for i, tag := range t.schema.PrimaryKey {
		out[tag] = key[i]
	}
	return out
}

// Update replaces the row with key by row's cells (ErrNotFound if absent).
func (e *Editor) Update(key Key, row Row) error {
	kb, err := e.t.encodeKey(key)
	if err != nil {
		return err
	}
	if e.t.keyed() {
		for i, tag := range e.t.schema.PrimaryKey {
			if v, ok := row[tag]; ok && !sameCell(v, key[i]) {
				c, _, _ := e.t.schema.column(tag)
				return fmt.Errorf("%w: an update changes key column %q", ErrValue, c.Name)
			}
		}
	}
	vb, err := e.t.encodeValue(row)
	if err != nil {
		return err
	}
	if _, ok, err := e.exists(context.Background(), kb); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %v", ErrNotFound, key)
	}
	e.set(kb, &pendingRow{key: key, row: cloneRow(row, key, e.t), vb: vb})
	return nil
}

// sameCell compares two cell values as the encoding would.
func sameCell(a, b any) bool {
	switch x := a.(type) {
	case []byte:
		y, ok := b.([]byte)
		return ok && bytes.Equal(x, y)
	case JSONB:
		y, ok := b.(JSONB)
		return ok && bytes.Equal(x, y)
	}
	return a == b
}

// put sets the row with encoded key kb, whatever was there.
func (e *Editor) put(kb []byte, key Key, row Row) error {
	vb, err := e.t.encodeValue(row)
	if err != nil {
		return err
	}
	e.set(kb, &pendingRow{key: key, row: cloneRow(row, key, e.t), vb: vb})
	return nil
}

// remove deletes the row with encoded key kb, if any.
func (e *Editor) remove(kb []byte) error {
	key, err := e.t.decodeKey(kb)
	if err != nil {
		return err
	}
	e.set(kb, &pendingRow{key: key})
	return nil
}

// Delete removes the row with key (ErrNotFound if absent).
func (e *Editor) Delete(key Key) error {
	kb, err := e.t.encodeKey(key)
	if err != nil {
		return err
	}
	if _, ok, err := e.exists(context.Background(), kb); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %v", ErrNotFound, key)
	}
	e.set(kb, &pendingRow{key: key})
	return nil
}

// Flush writes the edits and returns the new table.
func (e *Editor) Flush(ctx context.Context) (*Table, error) {
	t := e.t
	pe := t.primary.Editor()
	ies := make(map[Tag]*prolly.Editor, len(t.schema.Indexes))
	for _, ix := range t.schema.Indexes {
		ies[ix.Tag] = t.indexes[ix.Tag].Editor()
	}
	keys := append([]string(nil), e.order...)
	sort.Strings(keys)
	for _, k := range keys {
		kb := []byte(k)
		p := e.pending[k]
		oldVB, had, err := t.primary.Get(ctx, kb)
		if err != nil {
			return nil, err
		}
		if had {
			_, old, err := t.decodeRow(kb, oldVB)
			if err != nil {
				return nil, err
			}
			for _, ix := range t.schema.Indexes {
				ik, err := t.indexKey(ix, old, kb)
				if err != nil {
					return nil, err
				}
				if err := ies[ix.Tag].Delete(ik); err != nil {
					return nil, err
				}
			}
		}
		if p.row == nil {
			if err := pe.Delete(kb); err != nil {
				return nil, err
			}
			continue
		}
		if err := pe.Put(kb, p.vb); err != nil {
			return nil, err
		}
		for _, ix := range t.schema.Indexes {
			ik, err := t.indexKey(ix, p.row, kb)
			if err != nil {
				return nil, err
			}
			if err := ies[ix.Tag].Put(ik, nil); err != nil {
				return nil, err
			}
		}
	}
	out := &Table{schema: t.schema, catalog: t.catalog, cfg: t.cfg, store: t.store, indexes: map[Tag]*prolly.Map{}}
	var err error
	if out.primary, err = pe.Flush(ctx); err != nil {
		return nil, err
	}
	for tag, ie := range ies {
		if out.indexes[tag], err = ie.Flush(ctx); err != nil {
			return nil, err
		}
	}
	if err := out.writeRoot(ctx); err != nil {
		return nil, err
	}
	e.t = out
	e.pending = map[string]*pendingRow{}
	e.order = nil
	return out, nil
}

// rootMagic starts every table root record.
const rootMagic = "VDTR"

// MaxCatalogLen bounds a catalog record in a root record.
const MaxCatalogLen = 1 << 20

// rootRecord is what a table's root chunk holds.
type rootRecord struct {
	catalog []byte
	primary indexRoot
	indexes []indexRoot // in the schema's index order
}

type indexRoot struct {
	tag   Tag // 0 for the primary map
	root  [32]byte
	count uint64
}

// encodeRoot returns the record: magic "VDTR" · version u16 · catalog
// (uvarint length, bytes) · primary root [32] · row count u64 · index
// count uvarint · per index (tag u16 · root [32] · count u64).
func encodeRoot(r rootRecord) []byte {
	var w wire.Writer
	w.Raw([]byte(rootMagic))
	w.U16(Format)
	w.LenBytes(r.catalog)
	w.Raw(r.primary.root[:])
	w.U64(r.primary.count)
	w.Uvarint(uint64(len(r.indexes)))
	for _, ix := range r.indexes {
		w.U16(uint16(ix.tag))
		w.Raw(ix.root[:])
		w.U64(ix.count)
	}
	return w.Bytes()
}

// decodeRoot parses a record (chunk.ErrCorrupt for anything else): the
// catalog must decode, and the indexes must be the catalog's, in order,
// each as full as the primary map.
func decodeRoot(b []byte) (rootRecord, error) {
	r := wire.NewReader(b)
	if magic := r.Fixed(len(rootMagic)); r.Err() == nil && string(magic) != rootMagic {
		return rootRecord{}, corrupt("not a table root record")
	}
	if v := r.U16(); r.Err() == nil && v != Format {
		return rootRecord{}, corrupt("table format %d, this package reads %d", v, Format)
	}
	var rec rootRecord
	rec.catalog = bytes.Clone(r.LenBytes(MaxCatalogLen))
	copy(rec.primary.root[:], r.Fixed(32))
	rec.primary.count = r.U64()
	n, err := bounded(r, MaxIndexes)
	if err != nil {
		return rootRecord{}, err
	}
	for i := uint64(0); i < n && r.Err() == nil; i++ {
		var ix indexRoot
		ix.tag = Tag(r.U16())
		copy(ix.root[:], r.Fixed(32))
		ix.count = r.U64()
		rec.indexes = append(rec.indexes, ix)
	}
	if err := r.Done(); err != nil {
		return rootRecord{}, fmt.Errorf("%w: %w", chunk.ErrCorrupt, err)
	}
	schema, err := DecodeCatalog(rec.catalog)
	if err != nil {
		return rootRecord{}, err
	}
	if len(rec.indexes) != len(schema.Indexes) {
		return rootRecord{}, fmt.Errorf("%w: a root record of %d indexes for a catalog of %d", chunk.ErrCorrupt, len(rec.indexes), len(schema.Indexes))
	}
	for i, ix := range rec.indexes {
		if ix.tag != schema.Indexes[i].Tag {
			return rootRecord{}, fmt.Errorf("%w: the root record's index %d is tag %d, the catalog's is %d", chunk.ErrCorrupt, i, ix.tag, schema.Indexes[i].Tag)
		}
		if ix.count != rec.primary.count {
			return rootRecord{}, fmt.Errorf("%w: index %d holds %d entries for %d rows", chunk.ErrCorrupt, ix.tag, ix.count, rec.primary.count)
		}
	}
	return rec, nil
}
