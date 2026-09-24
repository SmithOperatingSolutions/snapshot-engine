package table

import (
	"context"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
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
	cfg     prolly.Config
	store   chunk.ReadWriter
	primary *prolly.Map
	indexes map[Tag]*prolly.Map
	root    model.Root
}

// Create stores an empty table with schema.
func Create(ctx context.Context, s chunk.ReadWriter, cfg prolly.Config, schema Schema) (*Table, error) {
	return nil, errors.New("table: not implemented")
}

// Open opens the table at root and checks the root's claims against it.
func Open(ctx context.Context, s chunk.ReadWriter, cfg prolly.Config, root model.Root) (*Table, error) {
	return nil, errors.New("table: not implemented")
}

// Schema is the table's schema.
func (t *Table) Schema() Schema { return t.schema }

// Root is the table as an object.
func (t *Table) Root() model.Root { return t.root }

// Get returns the row with key.
func (t *Table) Get(ctx context.Context, key Key) (Row, bool, error) {
	return nil, false, errors.New("table: not implemented")
}

// Rows walks rows in key order.
type Rows struct{ err error }

// Next returns the next row and its key; ok is false at the end.
func (r *Rows) Next() (Key, Row, bool, error) { return nil, nil, false, r.err }

// Scan walks every row in key order.
func (t *Table) Scan(ctx context.Context) (*Rows, error) {
	return nil, errors.New("table: not implemented")
}

// IndexLookup walks, in index order, the rows whose leading index columns
// equal values.
func (t *Table) IndexLookup(ctx context.Context, index Tag, values ...any) (*Rows, error) {
	return nil, errors.New("table: not implemented")
}

// Editor collects inserts, updates and deletes against a table; Flush
// writes them all, the indexes with the primary map, or nothing.
type Editor struct{ t *Table }

// Edit starts editing t.
func (t *Table) Edit() *Editor { return &Editor{t: t} }

// Insert adds a row, refusing a value that does not fit its column
// (ErrValue) and a key that exists (ErrDuplicate); it returns the row's key,
// a fresh RowID for a table without a declared key.
func (e *Editor) Insert(row Row) (Key, error) {
	return nil, fmt.Errorf("%w: not implemented", ErrValue)
}

// Update replaces the row with key by row's cells (ErrNotFound if absent).
func (e *Editor) Update(key Key, row Row) error { return ErrNotFound }

// Delete removes the row with key (ErrNotFound if absent).
func (e *Editor) Delete(key Key) error { return ErrNotFound }

// Flush writes the edits and returns the new table.
func (e *Editor) Flush(ctx context.Context) (*Table, error) {
	return nil, errors.New("table: not implemented")
}

// rootMagic starts every table root record.
const rootMagic = "VDTR"

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

// EncodeRoot returns the record: magic "VDTR" · version u16 · catalog
// (uvarint length, bytes) · primary root [32] · row count u64 · index
// count uvarint · per index (tag u16 · root [32] · count u64).
func encodeRoot(r rootRecord) []byte { return nil }

// decodeRoot parses a record (chunk.ErrCorrupt for anything else).
func decodeRoot(b []byte) (rootRecord, error) {
	return rootRecord{}, fmt.Errorf("%w: not implemented", chunk.ErrCorrupt)
}
