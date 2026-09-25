package engine_test

import (
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// incr is an INCR as an application runs it on the kv map cache: read the
// counter as the transaction's snapshot holds it and write it back by more.
func incr(t *testing.T, tx *engine.Txn, key string, by int64) {
	t.Helper()
	m := kindsKV(t, tx, "cache")
	v, ok, err := m.Get(ctx, []byte(key))
	if err != nil || !ok {
		t.Fatalf("the counter %s: %v, %v", key, ok, err)
	}
	if err := m.Set(ctx, []byte(key), kindsCounter(v.Counter+by)); err != nil {
		t.Fatal(err)
	}
}

// counterDB is kindsDB with the counter hits at 1000, committed on main.
func counterDB(t *testing.T) (*engine.Database, *engine.Session) {
	t.Helper()
	db, s := kindsDB(t)
	tx := txnBegin(t, s)
	if err := kindsKV(t, tx, "cache").Set(ctx, []byte("hits"), kindsCounter(1000)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(ctx, "hits at 1000"); err != nil {
		t.Fatal(err)
	}
	return db, s
}

// #7: two branches that each INCR a counter from 1000 merge to 1002: a
// counter each side added one to is two more, even when both branches made
// exactly the same changes and hold the same object. A value of another
// kind set identically on both sides is still one change.
func TestACounterIncrementedOnTwoBranchesMergesToTheSum(t *testing.T) {
	db, s := counterDB(t)
	if err := s.CreateBranch(ctx, "feature", ""); err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"main", "feature"} {
		bs := txnSession(t, db, branch)
		tx := txnBegin(t, bs)
		incr(t, tx, "hits", 1)
		if err := kindsKV(t, tx, "cache").Set(ctx, []byte("a"), kindsBytes("same")); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("the INCR on %s: %v", branch, err)
		}
		if _, err := bs.Commit(ctx, "one more hit, and a set to same"); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Merge(ctx, "feature", "merge feature")
	if err != nil || len(r.Conflicts) != 0 {
		t.Fatalf("merging two branches that each added one to hits = %+v, %v; want a clean merge", r.Conflicts, err)
	}
	if v, _ := kindsGet(t, s, "hits"); v.Counter != 1002 {
		t.Errorf("hits, 1000 with one INCR on each branch, merged to %d, want 1002: an increment was lost", v.Counter)
	}
	if v, _ := kindsGet(t, s, "a"); string(v.Bytes) != "same" {
		t.Errorf("a, set to %q on both branches, merged to %q, want it once", "same", v.Bytes)
	}
}
