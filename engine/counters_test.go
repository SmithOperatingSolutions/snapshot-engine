package engine_test

import (
	"errors"
	"strconv"
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

// #7, DESIGN D18: counters leave the item rule. Two transactions from one
// snapshot that only INCR or DECR one counter both commit, and the
// counter holds the sum of what both added: the kv model sums a
// counter's deltas, so neither write is lost.
func TestConcurrentIncrementsOfOneCounterBothCommit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		by     [2]int64
		expect int64
	}{
		{"the same INCR on both", [2]int64{1, 1}, 1002},
		{"an INCR and a DECR", [2]int64{5, -2}, 1003},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, s := counterDB(t)
			x, y := txnBegin(t, s), txnBegin(t, txnSession(t, db, "main"))
			incr(t, x, "hits", tc.by[0])
			incr(t, y, "hits", tc.by[1])
			for i, tx := range []*engine.Txn{x, y} {
				if err := tx.Commit(ctx); err != nil {
					t.Fatalf("transaction %d, adding %d to hits from one snapshot = %v, want it to commit: a counter's increments sum", i+1, tc.by[i], err)
				}
			}
			if v, _ := kindsGet(t, s, "hits"); v.Counter != tc.expect {
				t.Errorf("hits, 1000 with %d and %d added by two transactions, is %d, want %d: an increment was lost", tc.by[0], tc.by[1], v.Counter, tc.expect)
			}
		})
	}
}

// #7, DESIGN D18: only a counter's write over a counter leaves the item
// rule. A SET of another kind, or a delete, racing an INCR of one counter
// still serializes, whichever commits first, and the counter holds the
// first's write alone; so does a key both transactions turned into the
// same counter, and a counter both replaced with the same bytes.
func TestACounterSetOrDeletedWhileIncrementedStillSerializes(t *testing.T) {
	set := func(key string, v engine.Value) itemWrite {
		return func(t *testing.T, tx *engine.Txn) {
			if err := kindsKV(t, tx, "cache").Set(ctx, []byte(key), v); err != nil {
				t.Fatal(err)
			}
		}
	}
	del := func(t *testing.T, tx *engine.Txn) {
		if err := kindsKV(t, tx, "cache").Delete(ctx, []byte("hits")); err != nil {
			t.Fatal(err)
		}
	}
	hitsIs := func(want string) func(t *testing.T, s *engine.Session) string {
		return func(t *testing.T, s *engine.Session) string {
			v, ok := kindsGet(t, s, "hits")
			got := "deleted"
			switch {
			case ok && v.Kind == engine.ValueCounter:
				got = strconv.FormatInt(v.Counter, 10)
			case ok:
				got = string(v.Bytes)
			}
			if got != want {
				return "hits is " + got + ", want the first's write alone, " + want
			}
			return ""
		}
	}
	for _, tc := range []struct {
		name          string
		first, second itemWrite
		want          func(t *testing.T, s *engine.Session) string
	}{
		{"a SET, then an INCR", set("hits", kindsBytes("reset")), counterBy(1), hitsIs("reset")},
		{"an INCR, then a SET", counterBy(1), set("hits", kindsBytes("reset")), hitsIs("1001")},
		{"a delete, then an INCR", del, counterBy(1), hitsIs("deleted")},
		{"an INCR, then a delete", counterBy(1), del, hitsIs("1001")},
		{"a counter replaced with the same bytes on both", set("hits", kindsBytes("x")), set("hits", kindsBytes("x")), hitsIs("x")},
		{"a key made the same counter on both", set("a", kindsCounter(5)), set("a", kindsCounter(5)), func(t *testing.T, s *engine.Session) string {
			if v, _ := kindsGet(t, s, "a"); v.Kind != engine.ValueCounter || v.Counter != 5 {
				return "a is " + strconv.FormatInt(v.Counter, 10) + ", want the first's counter, 5"
			}
			return ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, s := counterDB(t)
			x, y := txnBegin(t, s), txnBegin(t, txnSession(t, db, "main"))
			tc.first(t, x)
			tc.second(t, y)
			if err := x.Commit(ctx); err != nil {
				t.Fatalf("the first commit: %v", err)
			}
			after := txnWS(t, s)
			if err := y.Commit(ctx); !errors.Is(err, engine.ErrSerialization) {
				t.Errorf("the second transaction's commit = %v, want ErrSerialization: only a counter's increments sum", err)
			}
			if got := txnWS(t, s); got != after {
				t.Errorf("the refused commit changed the working set (%s, want the first's %s)", got.Short(), after.Short())
			}
			if msg := tc.want(t, s); msg != "" {
				t.Error(msg)
			}
		})
	}
}
