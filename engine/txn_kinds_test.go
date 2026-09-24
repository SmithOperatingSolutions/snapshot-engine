package engine_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

func kindsBytes(s string) engine.Value { return engine.Value{Kind: kv.Bytes, Bytes: []byte(s)} }

func kindsCounter(n int64) engine.Value { return engine.Value{Kind: kv.Counter, Counter: n} }

func kindsDoc(t *testing.T, text string) engine.Node {
	t.Helper()
	n, err := document.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// kindsDB is txnDB's database (a table people on main) with a key-value
// map cache holding a:1, b:2 and the counter hits at 10, and a collection
// users holding u1 {"name":"ada","age":36}, all committed.
func kindsDB(t *testing.T) (*engine.Database, *engine.Session) {
	t.Helper()
	db, s, _ := txnDB(t)
	tx := txnBegin(t, s)
	cache, err := tx.CreateKV(ctx, "cache")
	if err != nil {
		t.Fatalf("CreateKV: %v", err)
	}
	for k, v := range map[string]engine.Value{"a": kindsBytes("1"), "b": kindsBytes("2"), "hits": kindsCounter(10)} {
		if err := cache.Set(ctx, []byte(k), v); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}
	users, err := tx.CreateCollection(ctx, "users")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if err := users.PutJSON(ctx, []byte("u1"), []byte(`{"name": "ada", "age": 36}`)); err != nil {
		t.Fatalf("PutJSON: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("committing the fixture: %v", err)
	}
	return db, s
}

func kindsKV(t *testing.T, tx *engine.Txn, name string) *engine.KV {
	t.Helper()
	m, err := tx.KV(ctx, name)
	if err != nil {
		t.Fatalf("KV(%q): %v", name, err)
	}
	return m
}

func kindsCollection(t *testing.T, tx *engine.Txn, name string) *engine.Collection {
	t.Helper()
	c, err := tx.Collection(ctx, name)
	if err != nil {
		t.Fatalf("Collection(%q): %v", name, err)
	}
	return c
}

// kindsGet reads key from the committed cache in a fresh transaction.
func kindsGet(t *testing.T, s *engine.Session, key string) (engine.Value, bool) {
	t.Helper()
	tx := txnBegin(t, s)
	defer func() { _ = tx.Rollback(ctx) }()
	v, ok, err := kindsKV(t, tx, "cache").Get(ctx, []byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return v, ok
}

// kindsRecord reads a committed record as canonical text.
func kindsRecord(t *testing.T, s *engine.Session, id string) string {
	t.Helper()
	tx := txnBegin(t, s)
	defer func() { _ = tx.Rollback(ctx) }()
	n, ok, err := kindsCollection(t, tx, "users").Get(ctx, []byte(id))
	if err != nil || !ok {
		t.Fatalf("record %s: %v, %v", id, ok, err)
	}
	return string(document.Encode(n))
}

// Key-value maps and document collections are objects by name as tables
// are: a name must be a valid object path and free; an object of one kind
// asked for as another is ErrWrongKind, in the transaction that made it as
// after commit; each object is one handle per transaction; Drop removes
// any kind, and a handle to a dropped object refuses writes with
// ErrNotFound; a name dropped can be made again, empty.
func TestKVAndCollectionsAreObjectsByName(t *testing.T) {
	_, s := kindsDB(t)
	x := txnBegin(t, s)
	for _, bad := range []string{"", "a//b", "/abs"} {
		if _, err := x.CreateKV(ctx, bad); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("CreateKV(%q) = %v, want ErrInvalid", bad, err)
		}
		if _, err := x.CreateCollection(ctx, bad); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("CreateCollection(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	for _, taken := range []string{"people", "cache", "users"} {
		if _, err := x.CreateKV(ctx, taken); !errors.Is(err, engine.ErrExists) {
			t.Errorf("CreateKV on %s, taken = %v, want ErrExists", taken, err)
		}
		if _, err := x.CreateCollection(ctx, taken); !errors.Is(err, engine.ErrExists) {
			t.Errorf("CreateCollection on %s, taken = %v, want ErrExists", taken, err)
		}
	}
	fresh, err := x.CreateKV(ctx, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := x.CreateCollection(ctx, "docs")
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"KV on a table":                    func() error { _, err := x.KV(ctx, "people"); return err },
		"KV on a collection":               func() error { _, err := x.KV(ctx, "users"); return err },
		"KV on a collection made here":     func() error { _, err := x.KV(ctx, "docs"); return err },
		"Collection on a kv map":           func() error { _, err := x.Collection(ctx, "cache"); return err },
		"Collection on a kv map made here": func() error { _, err := x.Collection(ctx, "fresh"); return err },
		"Table on a kv map made here":      func() error { _, err := x.Table(ctx, "fresh"); return err },
		"Table on a collection":            func() error { _, err := x.Table(ctx, "users"); return err },
	} {
		if err := call(); !errors.Is(err, engine.ErrWrongKind) {
			t.Errorf("%s = %v, want ErrWrongKind", name, err)
		}
	}
	if _, err := x.KV(ctx, "nowhere"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("KV on a name not there = %v, want ErrNotFound", err)
	}
	if _, err := x.Collection(ctx, "nowhere"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Collection on a name not there = %v, want ErrNotFound", err)
	}
	if again := kindsKV(t, x, "fresh"); again != fresh {
		t.Error("a kv map asked for twice in one transaction is two handles: writes through one are lost to the other")
	}
	if again := kindsCollection(t, x, "docs"); again != docs {
		t.Error("a collection asked for twice in one transaction is two handles")
	}
	cache := kindsKV(t, x, "cache")
	if kindsKV(t, x, "cache") != cache {
		t.Error("an existing kv map asked for twice in one transaction is two handles")
	}
	users := kindsCollection(t, x, "users")
	if err := x.Drop(ctx, "cache"); err != nil {
		t.Fatalf("Drop of a kv map: %v", err)
	}
	if err := x.Drop(ctx, "users"); err != nil {
		t.Fatalf("Drop of a collection: %v", err)
	}
	if err := cache.Set(ctx, []byte("z"), kindsBytes("lost")); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("a write through a dropped kv map's handle = %v, want ErrNotFound", err)
	}
	if err := users.PutJSON(ctx, []byte("u9"), []byte(`{}`)); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("a write through a dropped collection's handle = %v, want ErrNotFound", err)
	}
	if _, err := x.KV(ctx, "cache"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("KV on a map dropped here = %v, want ErrNotFound", err)
	}
	if _, err := x.Collection(ctx, "users"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Collection on a collection dropped here = %v, want ErrNotFound", err)
	}
	remade, err := x.CreateKV(ctx, "cache")
	if err != nil {
		t.Fatalf("remaking a dropped name: %v", err)
	}
	if _, ok, err := remade.Get(ctx, []byte("a")); err != nil || ok {
		t.Errorf("the remade map holds the dropped one's key (%v, %v)", ok, err)
	}
	if err := fresh.Set(ctx, []byte("k"), kindsBytes("v")); err != nil {
		t.Fatal(err)
	}
	if err := docs.PutJSON(ctx, []byte("d1"), []byte(`{"x": 1}`)); err != nil {
		t.Fatal(err)
	}
	if err := x.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	y := txnBegin(t, s)
	if _, err := y.Collection(ctx, "users"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("the dropped collection is there after commit: %v", err)
	}
	if v, ok, err := kindsKV(t, y, "fresh").Get(ctx, []byte("k")); err != nil || !ok || string(v.Bytes) != "v" {
		t.Errorf("the new map's key after commit = %q, %v, %v", v.Bytes, ok, err)
	}
	if _, ok, _ := kindsKV(t, y, "cache").Get(ctx, []byte("a")); ok {
		t.Error("after commit the remade map holds the dropped one's key")
	}
	if _, err := y.Table(ctx, "fresh"); !errors.Is(err, engine.ErrWrongKind) {
		t.Errorf("Table on a kv map after commit = %v, want ErrWrongKind", err)
	}
}

// A kv handle reads its transaction's own writes, scans in key order from
// a key, deletes a key that is not there as a no-op (the kv model's rule),
// stops a scan when told and passes on the callback's error, and refuses
// a bad key or value with ErrInvalid, writing nothing.
func TestAKVHandleReadsItsWritesAndRefusesWhatIsNotAValue(t *testing.T) {
	_, s := kindsDB(t)
	tx := txnBegin(t, s)
	cache := kindsKV(t, tx, "cache")
	if err := cache.Set(ctx, []byte("c"), kindsBytes("3")); err != nil {
		t.Fatal(err)
	}
	if err := cache.Delete(ctx, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := cache.Delete(ctx, []byte("never")); err != nil {
		t.Errorf("deleting a key that is not there = %v, want a no-op", err)
	}
	if v, ok, err := cache.Get(ctx, []byte("c")); err != nil || !ok || string(v.Bytes) != "3" {
		t.Errorf("the transaction's own write reads as %q, %v, %v", v.Bytes, ok, err)
	}
	if _, ok, _ := cache.Get(ctx, []byte("a")); ok {
		t.Error("the transaction's own delete is not seen")
	}
	var keys []string
	if err := cache.Scan(ctx, []byte("b"), func(k []byte, _ engine.Value) (bool, error) {
		keys = append(keys, string(k))
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys, ",") != "b,c,hits" {
		t.Errorf("scan from b = %v, want b,c,hits in key order", keys)
	}
	keys = nil
	if err := cache.Scan(ctx, nil, func(k []byte, _ engine.Value) (bool, error) {
		keys = append(keys, string(k))
		return false, nil
	}); err != nil || strings.Join(keys, ",") != "b" {
		t.Errorf("a scan told to stop after one = %v, %v; want [b]", keys, err)
	}
	errStop := errors.New("stop")
	if err := cache.Scan(ctx, nil, func([]byte, engine.Value) (bool, error) { return true, errStop }); !errors.Is(err, errStop) {
		t.Errorf("a scan whose callback fails = %v, want the callback's error", err)
	}
	for name, call := range map[string]func() error{
		"Set of an empty key":    func() error { return cache.Set(ctx, nil, kindsBytes("x")) },
		"Set of a key too long":  func() error { return cache.Set(ctx, bytes.Repeat([]byte("k"), 4097), kindsBytes("x")) },
		"Set of no kind":         func() error { return cache.Set(ctx, []byte("d"), engine.Value{}) },
		"Delete of an empty key": func() error { return cache.Delete(ctx, nil) },
		"Get of an empty key":    func() error { _, _, err := cache.Get(ctx, nil); return err },
	} {
		if err := call(); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s = %v, want ErrInvalid", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := kindsGet(t, s, "d"); ok {
		t.Error("a refused Set wrote its key")
	}
	if v, ok := kindsGet(t, s, "c"); !ok || string(v.Bytes) != "3" {
		t.Errorf("the committed write reads as %q, %v", v.Bytes, ok)
	}
}

// A collection handle reads its own writes (Put and PutJSON), scans in id
// order from an id, deletes a record that is not there as a no-op (the
// document model's rule), and refuses a bad id, malformed JSON and an
// oversized document with ErrInvalid, writing nothing.
func TestACollectionHandleReadsItsWritesAndRefusesWhatIsNotADocument(t *testing.T) {
	_, s := kindsDB(t)
	tx := txnBegin(t, s)
	users := kindsCollection(t, tx, "users")
	if err := users.Put(ctx, []byte("u2"), kindsDoc(t, `{"name": "grace"}`)); err != nil {
		t.Fatal(err)
	}
	if err := users.PutJSON(ctx, []byte("u3"), []byte(`{"name": "linus", "tags": ["a", "b"]}`)); err != nil {
		t.Fatal(err)
	}
	if err := users.Delete(ctx, []byte("never")); err != nil {
		t.Errorf("deleting a record that is not there = %v, want a no-op", err)
	}
	if n, ok, err := users.Get(ctx, []byte("u2")); err != nil || !ok || !n.Equal(kindsDoc(t, `{"name": "grace"}`)) {
		t.Errorf("the transaction's own Put reads as %v, %v, %v", n, ok, err)
	}
	var ids []string
	if err := users.Scan(ctx, []byte("u2"), func(id []byte, _ engine.Node) (bool, error) {
		ids = append(ids, string(id))
		return true, nil
	}); err != nil || strings.Join(ids, ",") != "u2,u3" {
		t.Errorf("scan from u2 = %v, %v; want u2,u3", ids, err)
	}
	errStop := errors.New("stop")
	if err := users.Scan(ctx, nil, func([]byte, engine.Node) (bool, error) { return true, errStop }); !errors.Is(err, errStop) {
		t.Errorf("a scan whose callback fails = %v, want the callback's error", err)
	}
	ids = nil
	if err := users.Scan(ctx, nil, func(id []byte, _ engine.Node) (bool, error) {
		ids = append(ids, string(id))
		return false, nil
	}); err != nil || strings.Join(ids, ",") != "u1" {
		t.Errorf("a scan told to stop after one = %v, %v; want [u1]", ids, err)
	}
	if err := users.Delete(ctx, []byte("u3")); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"PutJSON of malformed JSON": func() error { return users.PutJSON(ctx, []byte("u4"), []byte(`{"name": `)) },
		"PutJSON of an oversized doc": func() error {
			return users.PutJSON(ctx, []byte("u4"), []byte(`"`+strings.Repeat("x", document.MaxDocument)+`"`))
		},
		"Put of an empty id":    func() error { return users.Put(ctx, nil, kindsDoc(t, `{}`)) },
		"Delete of an empty id": func() error { return users.Delete(ctx, nil) },
		"Get of an empty id":    func() error { _, _, err := users.Get(ctx, nil); return err },
	} {
		if err := call(); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s = %v, want ErrInvalid", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	y := txnBegin(t, s)
	c := kindsCollection(t, y, "users")
	if _, ok, _ := c.Get(ctx, []byte("u4")); ok {
		t.Error("a refused document was written")
	}
	if _, ok, _ := c.Get(ctx, []byte("u3")); ok {
		t.Error("the transaction's delete did not land")
	}
	if got := kindsRecord(t, s, "u2"); got != `{"name":"grace"}` {
		t.Errorf("the committed record is %s", got)
	}
}

// Transactions on one kv map merge through the kv model: different keys
// both commit; one key set two ways serializes, the second's write absent;
// a counter incremented in two transactions sums.
func TestTransactionsOnOneKVMapMergeByKey(t *testing.T) {
	_, s := kindsDB(t)
	a, b := txnBegin(t, s), txnBegin(t, s)
	if err := kindsKV(t, a, "cache").Set(ctx, []byte("a"), kindsBytes("A")); err != nil {
		t.Fatal(err)
	}
	if err := kindsKV(t, b, "cache").Set(ctx, []byte("b"), kindsBytes("B")); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx); err != nil {
		t.Fatalf("a transaction on another key of the same map = %v, want it to commit", err)
	}
	if va, _ := kindsGet(t, s, "a"); string(va.Bytes) != "A" {
		t.Errorf("a = %q after both commits, want A", va.Bytes)
	}
	if vb, _ := kindsGet(t, s, "b"); string(vb.Bytes) != "B" {
		t.Errorf("b = %q after both commits, want B", vb.Bytes)
	}

	c, d := txnBegin(t, s), txnBegin(t, s)
	if err := kindsKV(t, c, "cache").Set(ctx, []byte("a"), kindsBytes("first")); err != nil {
		t.Fatal(err)
	}
	if err := kindsKV(t, d, "cache").Set(ctx, []byte("a"), kindsBytes("second")); err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Commit(ctx); !errors.Is(err, engine.ErrSerialization) {
		t.Errorf("a second transaction setting the same key another way = %v, want ErrSerialization", err)
	}
	if v, _ := kindsGet(t, s, "a"); string(v.Bytes) != "first" {
		t.Errorf("after the refused commit a = %q, want the first's", v.Bytes)
	}

	e, f := txnBegin(t, s), txnBegin(t, s)
	for tx, by := range map[*engine.Txn]int64{e: 1, f: 2} {
		m := kindsKV(t, tx, "cache")
		v, _, err := m.Get(ctx, []byte("hits"))
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Set(ctx, []byte("hits"), kindsCounter(v.Counter+by)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Commit(ctx); err != nil {
		t.Fatalf("a second increment of a counter = %v, want it to commit", err)
	}
	if v, _ := kindsGet(t, s, "hits"); v.Counter != 13 {
		t.Errorf("a counter at 10 incremented by 1 and by 2 in two transactions is %d, want 13", v.Counter)
	}
}

// Transactions on one collection merge through the document model: two
// editing different fields of one record both land; one field set two
// ways serializes.
func TestTransactionsOnOneCollectionMergeByField(t *testing.T) {
	_, s := kindsDB(t)
	a, b := txnBegin(t, s), txnBegin(t, s)
	if err := kindsCollection(t, a, "users").PutJSON(ctx, []byte("u1"), []byte(`{"name": "ada", "age": 37}`)); err != nil {
		t.Fatal(err)
	}
	if err := kindsCollection(t, b, "users").PutJSON(ctx, []byte("u1"), []byte(`{"name": "Ada Lovelace", "age": 36}`)); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx); err != nil {
		t.Fatalf("a transaction on another field of the same record = %v, want it to commit", err)
	}
	if got := kindsRecord(t, s, "u1"); got != `{"age":37,"name":"Ada Lovelace"}` {
		t.Errorf("after both commits u1 is %s, want both fields changed", got)
	}

	c, d := txnBegin(t, s), txnBegin(t, s)
	if err := kindsCollection(t, c, "users").Put(ctx, []byte("u1"), setField(t, kindsDoc(t, `{"name": "Ada Lovelace", "age": 37}`), "age", "38")); err != nil {
		t.Fatal(err)
	}
	if err := kindsCollection(t, d, "users").Put(ctx, []byte("u1"), setField(t, kindsDoc(t, `{"name": "Ada Lovelace", "age": 37}`), "age", "39")); err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Commit(ctx); !errors.Is(err, engine.ErrSerialization) {
		t.Errorf("a second transaction setting the same field another way = %v, want ErrSerialization", err)
	}
	if got := kindsRecord(t, s, "u1"); got != `{"age":38,"name":"Ada Lovelace"}` {
		t.Errorf("after the refused commit u1 is %s, want the first's age", got)
	}
}

// setField is n with field set to the number text.
func setField(t *testing.T, n engine.Node, field, number string) engine.Node {
	t.Helper()
	out, err := document.Parse([]byte(strings.Replace(string(document.Encode(n)), fmt.Sprintf(`"%s":37`, field), fmt.Sprintf(`"%s":%s`, field, number), 1)))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A scan right after a write, with no read between to flush it, sees the
// write: a kv scan the key just set, a collection scan the record just put.
func TestAScanSeesWritesNotYetRead(t *testing.T) {
	_, s := kindsDB(t)
	tx := txnBegin(t, s)
	cache := kindsKV(t, tx, "cache")
	if err := cache.Set(ctx, []byte("c"), kindsBytes("3")); err != nil {
		t.Fatal(err)
	}
	var keys []string
	if err := cache.Scan(ctx, nil, func(k []byte, _ engine.Value) (bool, error) {
		keys = append(keys, string(k))
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(keys, ",") != "a,b,c,hits" {
		t.Errorf("a scan right after setting c = %v, want a,b,c,hits", keys)
	}
	users := kindsCollection(t, tx, "users")
	if err := users.PutJSON(ctx, []byte("u2"), []byte(`{"name": "grace"}`)); err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := users.Scan(ctx, nil, func(id []byte, _ engine.Node) (bool, error) {
		ids = append(ids, string(id))
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "u1,u2" {
		t.Errorf("a scan right after putting u2 = %v, want u1,u2", ids)
	}
}
