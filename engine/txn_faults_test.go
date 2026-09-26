package engine_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/blob"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

var errFault = errors.New("injected: the backend fails")

// faultBlobs is a backend that fails on demand: reads of objects (gets),
// reads of the root (roots), and writes of objects and the root (puts).
type faultBlobs struct {
	blob.BlobStore
	gets, roots, puts atomic.Bool
	log               scrubLog // the database's Logger: where a failure's details go
}

func (f *faultBlobs) Get(ctx context.Context, name string, off, n int64) (io.ReadCloser, error) {
	if f.gets.Load() {
		return nil, errFault
	}
	return f.BlobStore.Get(ctx, name, off, n)
}

func (f *faultBlobs) Root(ctx context.Context) (blob.Root, error) {
	if f.roots.Load() {
		return blob.Root{}, errFault
	}
	return f.BlobStore.Root(ctx)
}

func (f *faultBlobs) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	if f.puts.Load() {
		return errFault
	}
	return f.BlobStore.Put(ctx, name, r, size)
}

func (f *faultBlobs) SwapRoot(ctx context.Context, expected blob.Version, next []byte) (blob.Version, error) {
	if f.puts.Load() {
		var v blob.Version
		return v, errFault
	}
	return f.BlobStore.SwapRoot(ctx, expected, next)
}

// heal turns every fault off.
func (f *faultBlobs) heal() { f.gets.Store(false); f.roots.Store(false); f.puts.Store(false) }

// failed says a call failed because the backend did: the caller holds
// ErrInternal and a correlation id and nothing of the fault, and the log
// holds the injected fault itself under that id, so an operator can see
// what went wrong.
func (f *faultBlobs) failed(err error) bool {
	var e *engine.Error
	if !errors.Is(err, engine.ErrInternal) || !errors.As(err, &e) || errors.Is(err, errFault) {
		return false
	}
	f.log.mu.Lock()
	defer f.log.mu.Unlock()
	return strings.Contains(f.log.entries[e.Correlation], errFault.Error())
}

const faultN = 3000

// faultDB is a database on a failing backend holding a table people, a kv
// map cache and a collection users of faultN entries each, written and
// committed by one process and opened again by another, whose cache is
// empty: its first read of a chunk reaches the backend.
func faultDB(t *testing.T) (engine.Options, *engine.Database, *engine.Session, *faultBlobs) {
	t.Helper()
	o := dbOptions(t)
	fb := &faultBlobs{BlobStore: o.Blobs}
	o.Blobs = fb
	o.Logger = &fb.log
	db, err := engine.Create(ctx, alice, o)
	if err != nil {
		t.Fatal(err)
	}
	s := txnSession(t, db, "main")
	tx := txnBegin(t, s)
	people, err := tx.CreateTable(ctx, "people", txnPeople())
	if err != nil {
		t.Fatal(err)
	}
	cache, err := tx.CreateKV(ctx, "cache")
	if err != nil {
		t.Fatal(err)
	}
	users, err := tx.CreateCollection(ctx, "users")
	if err != nil {
		t.Fatal(err)
	}
	for i := range faultN {
		if _, err := people.Insert(ctx, txnPerson(int64(i), fmt.Sprintf("p%d", i), int64(i%90))); err != nil {
			t.Fatal(err)
		}
		if err := cache.Set(ctx, []byte(fmt.Sprintf("k%05d", i)), kindsBytes(fmt.Sprintf("value %d", i))); err != nil {
			t.Fatal(err)
		}
		if err := users.PutJSON(ctx, []byte(fmt.Sprintf("u%05d", i)), []byte(fmt.Sprintf(`{"n": %d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := engine.Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fb.heal(); _ = again.Close() })
	return o, again, txnSession(t, again, "main"), fb
}

// A finished transaction's kv and collection handles refuse every call with
// ErrClosed, and so does the transaction asked for one; a dropped map's or
// collection's handle refuses reads and writes with ErrNotFound.
func TestFinishedAndDroppedKVAndCollectionsRefuse(t *testing.T) {
	_, s := kindsDB(t)
	tx := txnBegin(t, s)
	cache, users := kindsKV(t, tx, "cache"), kindsCollection(t, tx, "users")
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"KV":                 func() error { _, err := tx.KV(ctx, "cache"); return err },
		"Collection":         func() error { _, err := tx.Collection(ctx, "users"); return err },
		"CreateKV":           func() error { _, err := tx.CreateKV(ctx, "x"); return err },
		"CreateCollection":   func() error { _, err := tx.CreateCollection(ctx, "y"); return err },
		"KV.Get":             func() error { _, _, err := cache.Get(ctx, []byte("a")); return err },
		"KV.Set":             func() error { return cache.Set(ctx, []byte("a"), kindsBytes("x")) },
		"KV.Delete":          func() error { return cache.Delete(ctx, []byte("a")) },
		"KV.Scan":            func() error { return cache.Scan(ctx, nil, nil) },
		"Collection.Get":     func() error { _, _, err := users.Get(ctx, []byte("u1")); return err },
		"Collection.Put":     func() error { return users.Put(ctx, []byte("u1"), kindsDoc(t, `{}`)) },
		"Collection.PutJSON": func() error { return users.PutJSON(ctx, []byte("u1"), []byte(`{}`)) },
		"Collection.Delete":  func() error { return users.Delete(ctx, []byte("u1")) },
		"Collection.Scan":    func() error { return users.Scan(ctx, nil, nil) },
	} {
		if err := call(); !errors.Is(err, engine.ErrClosed) {
			t.Errorf("%s on a finished transaction = %v, want ErrClosed", name, err)
		}
	}
	y := txnBegin(t, s)
	cache, users = kindsKV(t, y, "cache"), kindsCollection(t, y, "users")
	if err := y.Drop(ctx, "cache"); err != nil {
		t.Fatal(err)
	}
	if err := y.Drop(ctx, "users"); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"KV.Get":            func() error { _, _, err := cache.Get(ctx, []byte("a")); return err },
		"KV.Delete":         func() error { return cache.Delete(ctx, []byte("a")) },
		"KV.Scan":           func() error { return cache.Scan(ctx, nil, nil) },
		"Collection.Get":    func() error { _, _, err := users.Get(ctx, []byte("u1")); return err },
		"Collection.Put":    func() error { return users.Put(ctx, []byte("u1"), kindsDoc(t, `{}`)) },
		"Collection.Delete": func() error { return users.Delete(ctx, []byte("u1")) },
		"Collection.Scan":   func() error { return users.Scan(ctx, nil, nil) },
	} {
		if err := call(); !errors.Is(err, engine.ErrNotFound) {
			t.Errorf("%s on a dropped object = %v, want ErrNotFound", name, err)
		}
	}
}

// A backend that fails a read reaches the caller as a failure, whatever the
// call was doing when it met it: beginning (the working set, the namespace),
// opening an object, reading it (a point read, a scan, an index lookup), or
// flushing a transaction's pending writes before a read.
func TestABackendFailingAReadFailsTheCall(t *testing.T) {
	_, _, s, fb := faultDB(t)
	fb.roots.Store(true)
	if _, err := s.Begin(ctx); !fb.failed(err) {
		t.Errorf("Begin with the root unreadable = %v, want the backend's failure", err)
	}
	fb.heal()
	fb.gets.Store(true)
	if _, err := s.Begin(ctx); !fb.failed(err) {
		t.Errorf("Begin with objects unreadable (a fresh process, nothing cached) = %v, want the backend's failure", err)
	}
	fb.heal()

	tx := txnBegin(t, s)
	fb.gets.Store(true)
	if _, err := tx.Table(ctx, "people"); !fb.failed(err) {
		t.Errorf("opening a table the backend cannot read = %v, want its failure", err)
	}
	if _, err := tx.KV(ctx, "cache"); !fb.failed(err) {
		t.Errorf("opening a kv map the backend cannot read = %v, want its failure", err)
	}
	if _, err := tx.Collection(ctx, "users"); !fb.failed(err) {
		t.Errorf("opening a collection the backend cannot read = %v, want its failure", err)
	}
	fb.heal()

	people := txnTable(t, tx, "people")
	cache := kindsKV(t, tx, "cache")
	users := kindsCollection(t, tx, "users")
	fb.gets.Store(true)
	far := fmt.Sprintf("%05d", faultN-1)
	for name, call := range map[string]func() error{
		"Table.Get":  func() error { _, _, err := people.Get(ctx, engine.Key{int64(faultN - 1)}); return err },
		"Table.Scan": func() error { return people.Scan(ctx, func(engine.Key, engine.Row) (bool, error) { return true, nil }) },
		"Table.Lookup": func() error {
			return people.Lookup(ctx, 10, []any{int64(89)}, func(engine.Key, engine.Row) (bool, error) { return true, nil })
		},
		"KV.Get": func() error { _, _, err := cache.Get(ctx, []byte("k"+far)); return err },
		"KV.Scan": func() error {
			return cache.Scan(ctx, []byte("k"+far), func([]byte, engine.Value) (bool, error) { return true, nil })
		},
		"Collection.Get": func() error { _, _, err := users.Get(ctx, []byte("u"+far)); return err },
		"Collection.Scan": func() error {
			return users.Scan(ctx, []byte("u"+far), func([]byte, engine.Node) (bool, error) { return true, nil })
		},
	} {
		if err := call(); !fb.failed(err) {
			t.Errorf("%s with the backend failing reads = %v, want its failure", name, err)
		}
	}
	fb.heal()

	// A write into a leaf never read, then a read: the flush reads the leaf.
	tx2 := txnBegin(t, s)
	cache2, users2 := kindsKV(t, tx2, "cache"), kindsCollection(t, tx2, "users")
	if err := cache2.Set(ctx, []byte("k"+fmt.Sprintf("%05d", faultN/2)+"x"), kindsBytes("new")); err != nil {
		t.Fatal(err)
	}
	if err := users2.PutJSON(ctx, []byte("u"+fmt.Sprintf("%05d", faultN/2)+"x"), []byte(`{"new": true}`)); err != nil {
		t.Fatal(err)
	}
	fb.gets.Store(true)
	if _, _, err := cache2.Get(ctx, []byte("k00000")); !fb.failed(err) {
		t.Errorf("a kv read flushing a write the backend cannot merge = %v, want its failure", err)
	}
	if _, _, err := users2.Get(ctx, []byte("u00000")); !fb.failed(err) {
		t.Errorf("a collection read flushing a write the backend cannot merge = %v, want its failure", err)
	}
}

// A backend that fails a commit leaves the working set exactly as it was:
// reading the working set to commit into, reading what another process
// committed meanwhile, flushing the transaction's writes, or publishing.
func TestABackendFailingACommitWritesNothing(t *testing.T) {
	o, _, s, fb := faultDB(t)
	before := txnWS(t, s)
	write := func() *engine.Txn {
		t.Helper()
		tx := txnBegin(t, s)
		if err := kindsKV(t, tx, "cache").Set(ctx, []byte("k00000"), kindsBytes("changed")); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	check := func(what string, err error) {
		t.Helper()
		if !fb.failed(err) {
			t.Errorf("a commit with %s = %v, want the backend's failure", what, err)
		}
		fb.heal()
		if got := txnWS(t, s); got != before {
			t.Errorf("a commit that failed with %s changed the working set", what)
		}
	}

	tx := write()
	fb.roots.Store(true)
	check("the root unreadable", tx.Commit(ctx))

	tx = write()
	fb.puts.Store(true)
	check("the backend refusing writes", tx.Commit(ctx))

	tx = txnBegin(t, s)
	if err := kindsKV(t, tx, "cache").Set(ctx, []byte(fmt.Sprintf("k%05dx", faultN/3)), kindsBytes("new")); err != nil {
		t.Fatal(err)
	}
	fb.gets.Store(true)
	check("the transaction's write unmergeable (its leaf unreadable)", tx.Commit(ctx))

	tx = txnBegin(t, s)
	if err := kindsCollection(t, tx, "users").PutJSON(ctx, []byte(fmt.Sprintf("u%05dx", faultN/3)), []byte(`{"n": 1}`)); err != nil {
		t.Fatal(err)
	}
	fb.gets.Store(true)
	check("the transaction's record unmergeable (its leaf unreadable)", tx.Commit(ctx))

	// Another process commits meanwhile; this one cannot read what it wrote.
	tx = write()
	other, err := engine.Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	os := txnSession(t, other, "main")
	ox := txnBegin(t, os)
	if err := kindsKV(t, ox, "cache").Set(ctx, []byte("k00001"), kindsBytes("theirs")); err != nil {
		t.Fatal(err)
	}
	if err := ox.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before = txnWS(t, s)
	fb.gets.Store(true)
	check("what another process committed unreadable", tx.Commit(ctx))
}
