package engine_test

import (
	"fmt"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// A transaction reads its own writes without flushing them: one that reads
// each row back after writing it publishes about what one that only writes
// does, not a tree rewrite per read (#9). The budget is the bytes the
// backend takes for each commit; rows read from a table, a kv map and a
// collection alike.
func TestATransactionReadsItsWritesWithoutFlushingThem(t *testing.T) {
	const n = 200
	db, blobs, _ := queueDB(t, n)
	writeAll := func(readBack bool) int64 {
		t.Helper()
		tx := txnBegin(t, txnSession(t, db, "main"))
		people := txnTable(t, tx, "people")
		tag, ageOffset := "a", int64(1000) // a name that fits varchar(8), and ages of this variant's own
		if readBack {
			tag, ageOffset = "b", 2000
		}
		cache, err := tx.CreateKV(ctx, fmt.Sprintf("cache-%t", readBack))
		if err != nil {
			t.Fatal(err)
		}
		docs, err := tx.CreateCollection(ctx, fmt.Sprintf("docs-%t", readBack))
		if err != nil {
			t.Fatal(err)
		}
		for i := range n {
			id := int64(i + 1)
			if err := people.Update(ctx, engine.Key{id}, txnPerson(id, tag+fmt.Sprint(i), id+ageOffset)); err != nil { // the age moves too: a lookup must find the row at its new age
				t.Fatal(err)
			}
			if err := cache.Set(ctx, []byte(fmt.Sprintf("k%d", i)), kindsBytes(fmt.Sprintf("v%d", i))); err != nil {
				t.Fatal(err)
			}
			if err := docs.PutJSON(ctx, []byte(fmt.Sprintf("r%d", i)), []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
				t.Fatal(err)
			}
			if !readBack {
				continue
			}
			if row, ok, err := people.Get(ctx, engine.Key{id}); err != nil || !ok || row[2] != tag+fmt.Sprint(i) {
				t.Fatalf("row %d read back = %v, %t, %v", id, row, ok, err)
			}
			if v, ok, err := cache.Get(ctx, []byte(fmt.Sprintf("k%d", i))); err != nil || !ok || string(v.Bytes) != fmt.Sprintf("v%d", i) {
				t.Fatalf("key k%d read back = %v, %t, %v", i, v, ok, err)
			}
			if _, ok, err := docs.Get(ctx, []byte(fmt.Sprintf("r%d", i))); err != nil || !ok {
				t.Fatalf("record r%d read back = %t, %v", i, ok, err)
			}
		}
		if readBack { // the walks see them too, in order, still with nothing flushed
			var names, keys, ids []string
			if err := people.Scan(ctx, func(_ engine.Key, row engine.Row) (bool, error) {
				names = append(names, row[2].(string))
				return true, nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := cache.Scan(ctx, nil, func(k []byte, _ engine.Value) (bool, error) {
				keys = append(keys, string(k))
				return true, nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := docs.Scan(ctx, nil, func(id []byte, _ engine.Node) (bool, error) {
				ids = append(ids, string(id))
				return true, nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(names) != n || names[0] != tag+"0" || len(keys) != n || len(ids) != n {
				t.Fatalf("the walks after %d writes: %d rows (first %q), %d keys, %d records; want %d each, the rows renamed", n, len(names), names[0], len(keys), len(ids), n)
			}
			var found int
			if err := people.Lookup(ctx, 10, []any{7 + ageOffset}, func(_ engine.Key, row engine.Row) (bool, error) {
				found++
				return true, nil
			}); err != nil {
				t.Fatal(err)
			}
			if found != 1 {
				t.Fatalf("a lookup by the age the writes gave row 7 found %d rows, want 1: the lookup walked the stored index", found)
			}
		}
		before := blobs.bytes.Load()
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return blobs.bytes.Load() - before
	}
	writesOnly := writeAll(false)
	readBack := writeAll(true)
	t.Logf("a commit of %d rows, keys and records written blind: %d bytes; each read back after its write: %d bytes", n, writesOnly, readBack)
	if readBack > writesOnly*3/2 {
		t.Errorf("a transaction reading each of %d rows, keys and records back after writing it published %d bytes, one writing them blind %d: every read flushed the pending edits into new chunks", n, readBack, writesOnly)
	}
}
