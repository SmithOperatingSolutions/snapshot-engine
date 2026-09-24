package engine

import "context"

// Txn is a transaction on a session's branch: snapshot isolation (it reads
// the working set as of Begin, and its own writes), and an optimistic Commit
// that merges with whatever committed meanwhile through the models, and
// fails with ErrSerialization, writing nothing, when the two collide. It is
// not safe for concurrent use.
type Txn struct {
	s *Session
}

// begin opens a transaction for s. (Stub.)
func begin(ctx context.Context, s *Session) (*Txn, error) { return nil, errNotImplemented }

// CreateTable makes a table named name. (Stub.)
func (t *Txn) CreateTable(ctx context.Context, name string, s Schema) (*Table, error) {
	return nil, errNotImplemented
}

// Table opens the table named name. (Stub.)
func (t *Txn) Table(ctx context.Context, name string) (*Table, error) {
	return nil, errNotImplemented
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

// Drop removes the object named name. (Stub.)
func (t *Txn) Drop(ctx context.Context, name string) error { return errNotImplemented }

// Commit makes the transaction's changes part of the branch's working set:
// all of them or, with ErrSerialization, none. (Stub.)
func (t *Txn) Commit(ctx context.Context) error { return errNotImplemented }

// Rollback discards the transaction. (Stub.)
func (t *Txn) Rollback(ctx context.Context) error { return errNotImplemented }

// Table is a table inside a transaction; reads see the transaction's writes.
type Table struct{}

// Schema is the table's schema. (Stub.)
func (t *Table) Schema() Schema { return Schema{} }

// Get reads the row with key. (Stub.)
func (t *Table) Get(ctx context.Context, key Key) (Row, bool, error) {
	return nil, false, errNotImplemented
}

// Scan calls each for every row in key order until it returns false or an
// error. (Stub.)
func (t *Table) Scan(ctx context.Context, each func(Key, Row) (bool, error)) error {
	return errNotImplemented
}

// Lookup calls each for every row whose index columns equal values. (Stub.)
func (t *Table) Lookup(ctx context.Context, index Tag, values []any, each func(Key, Row) (bool, error)) error {
	return errNotImplemented
}

// Insert adds a row and returns its key. (Stub.)
func (t *Table) Insert(ctx context.Context, row Row) (Key, error) { return nil, errNotImplemented }

// Update replaces the row with key. (Stub.)
func (t *Table) Update(ctx context.Context, key Key, row Row) error { return errNotImplemented }

// Delete removes the row with key. (Stub.)
func (t *Table) Delete(ctx context.Context, key Key) error { return errNotImplemented }

// Alter gives the table a new schema (columns by tag: renames, additions,
// drops, widenings). (Stub.)
func (t *Table) Alter(ctx context.Context, next Schema) error { return errNotImplemented }

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
