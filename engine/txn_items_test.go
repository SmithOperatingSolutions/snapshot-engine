package engine_test

import (
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// Transactions conflict per item (the user's decision, 2026-09-25): two
// transactions that write one item from one snapshot, a table row, a kv key
// or a document record, cannot both commit, whatever they wrote. Before it
// a three-way merge took the same change made on both sides as one change,
// and a read-modify-write that two sessions ran at once kept one increment
// of two. Branch merges are untouched: they still combine cells and fields.

// itemWrite is one transaction's write to the shared item: what an
// application does, read the item as its snapshot holds it and write it
// back changed.
type itemWrite func(t *testing.T, tx *engine.Txn)

func rowAge(by int64) itemWrite {
	return func(t *testing.T, tx *engine.Txn) {
		tb := txnTable(t, tx, "people")
		row, ok, err := tb.Get(ctx, engine.Key{int64(1)})
		if err != nil || !ok {
			t.Fatalf("row 1: %v, %v", ok, err)
		}
		row[3] = row[3].(int64) + by
		if err := tb.Update(ctx, engine.Key{int64(1)}, row); err != nil {
			t.Fatal(err)
		}
	}
}

func rowName(name string) itemWrite {
	return func(t *testing.T, tx *engine.Txn) {
		tb := txnTable(t, tx, "people")
		row, ok, err := tb.Get(ctx, engine.Key{int64(1)})
		if err != nil || !ok {
			t.Fatalf("row 1: %v, %v", ok, err)
		}
		row[2] = name
		if err := tb.Update(ctx, engine.Key{int64(1)}, row); err != nil {
			t.Fatal(err)
		}
	}
}

func keyBytes(v string) itemWrite {
	return func(t *testing.T, tx *engine.Txn) {
		m := kindsKV(t, tx, "cache")
		if _, _, err := m.Get(ctx, []byte("a")); err != nil {
			t.Fatal(err)
		}
		if err := m.Set(ctx, []byte("a"), kindsBytes(v)); err != nil {
			t.Fatal(err)
		}
	}
}

func counterBy(by int64) itemWrite {
	return func(t *testing.T, tx *engine.Txn) {
		m := kindsKV(t, tx, "cache")
		v, _, err := m.Get(ctx, []byte("hits"))
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Set(ctx, []byte("hits"), kindsCounter(v.Counter+by)); err != nil {
			t.Fatal(err)
		}
	}
}

func recordAge(by int) itemWrite {
	return func(t *testing.T, tx *engine.Txn) {
		c := kindsCollection(t, tx, "users")
		doc, ok, err := c.Get(ctx, []byte("u1"))
		if err != nil || !ok {
			t.Fatalf("record u1: %v, %v", ok, err)
		}
		fields := slices.Clone(doc.Fields)
		for i, f := range fields {
			if f.Name == "age" {
				n, err := strconv.Atoi(f.Value.Number)
				if err != nil {
					t.Fatal(err)
				}
				fields[i].Value.Number = strconv.Itoa(n + by)
			}
		}
		doc.Fields = fields
		if err := c.Put(ctx, []byte("u1"), doc); err != nil {
			t.Fatal(err)
		}
	}
}

// E4: two transactions writing one item from one snapshot, each reading it
// and writing it back: the first commits, the second fails with
// ErrSerialization and writes nothing, and the item holds the first's
// write alone. The same write on both sides is two writes, one of which an
// application would lose: an increment of a row's age, of a kv value, of a
// counter, of a record's field.
func TestTransactionsWritingOneItemSerializeWhateverTheyWrote(t *testing.T) {
	for _, tc := range []struct {
		name         string
		first, again itemWrite
		want         func(t *testing.T, s *engine.Session) string // "" when the item holds the first's write
	}{
		{"a row, the same increment on both sides", rowAge(1), rowAge(1), func(t *testing.T, s *engine.Session) string {
			return ageIs(t, s, 37)
		}},
		{"a row, different cells", rowName("ann"), rowAge(1), func(t *testing.T, s *engine.Session) string {
			if name, _ := txnName(t, s, 1); name != "ann" {
				return "row 1's name is " + name + ", want the first's ann"
			}
			return ageIs(t, s, 36)
		}},
		{"a kv key, the same value on both sides", keyBytes("2"), keyBytes("2"), func(t *testing.T, s *engine.Session) string {
			if v, _ := kindsGet(t, s, "a"); string(v.Bytes) != "2" {
				return "a is " + string(v.Bytes) + ", want 2"
			}
			return ""
		}},
		{"a counter, the same increment on both sides", counterBy(1), counterBy(1), func(t *testing.T, s *engine.Session) string {
			return counterIs(t, s, 11)
		}},
		{"a record, one field changed the same way on both sides", recordAge(1), recordAge(1), func(t *testing.T, s *engine.Session) string {
			if got := kindsRecord(t, s, "u1"); got != `{"age":37,"name":"ada"}` {
				return "u1 is " + got + `, want {"age":37,"name":"ada"}`
			}
			return ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, s := kindsDB(t)
			x, y := txnBegin(t, s), txnBegin(t, txnSession(t, db, "main"))
			tc.first(t, x)
			tc.again(t, y)
			if err := x.Commit(ctx); err != nil {
				t.Fatalf("the first commit: %v", err)
			}
			after := txnWS(t, s)
			if err := y.Commit(ctx); !errors.Is(err, engine.ErrSerialization) {
				t.Errorf("the second transaction's commit, on the item the first wrote = %v, want ErrSerialization: an application would lose one of the two writes", err)
			}
			if got := txnWS(t, s); got != after {
				t.Errorf("the second commit changed the working set (%s, want the first's %s)", got.Short(), after.Short())
			}
			if msg := tc.want(t, s); msg != "" {
				t.Error(msg)
			}
		})
	}
}

// E4, the positive control: transactions from one snapshot writing
// different items, a row each, a kv key each, a record each, all commit and
// all their writes land.
func TestTransactionsWritingDifferentItemsAllCommit(t *testing.T) {
	db, s := kindsDB(t)
	setup := txnBegin(t, s)
	if err := kindsCollection(t, setup, "users").PutJSON(ctx, []byte("u2"), []byte(`{"name": "bob", "age": 40}`)); err != nil {
		t.Fatal(err)
	}
	if err := setup.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	txs := make([]*engine.Txn, 6)
	for i := range txs {
		txs[i] = txnBegin(t, txnSession(t, db, "main"))
	}
	if err := txnTable(t, txs[0], "people").Update(ctx, engine.Key{int64(1)}, txnPerson(1, "ann", 36)); err != nil {
		t.Fatal(err)
	}
	if err := txnTable(t, txs[1], "people").Update(ctx, engine.Key{int64(2)}, txnPerson(2, "ben", 40)); err != nil {
		t.Fatal(err)
	}
	if err := kindsKV(t, txs[2], "cache").Set(ctx, []byte("a"), kindsBytes("A")); err != nil {
		t.Fatal(err)
	}
	if err := kindsKV(t, txs[3], "cache").Set(ctx, []byte("hits"), kindsCounter(11)); err != nil {
		t.Fatal(err)
	}
	if err := kindsCollection(t, txs[4], "users").PutJSON(ctx, []byte("u1"), []byte(`{"name": "ada", "age": 37}`)); err != nil {
		t.Fatal(err)
	}
	if err := kindsCollection(t, txs[5], "users").PutJSON(ctx, []byte("u2"), []byte(`{"name": "bob", "age": 41}`)); err != nil {
		t.Fatal(err)
	}
	for i, tx := range txs {
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("transaction %d, on an item no other transaction wrote = %v, want it to commit", i, err)
		}
	}
	for id, want := range map[int64]string{1: "ann", 2: "ben"} {
		if got, _ := txnName(t, s, id); got != want {
			t.Errorf("row %d is %q, want %q", id, got, want)
		}
	}
	if v, _ := kindsGet(t, s, "a"); string(v.Bytes) != "A" {
		t.Errorf("a is %q, want A", v.Bytes)
	}
	if msg := counterIs(t, s, 11); msg != "" {
		t.Error(msg)
	}
	for id, want := range map[string]string{"u1": `{"age":37,"name":"ada"}`, "u2": `{"age":41,"name":"bob"}`} {
		if got := kindsRecord(t, s, id); got != want {
			t.Errorf("%s is %s, want %s", id, got, want)
		}
	}
}

// ageIs says how row 1's age differs from want ("" when it does not).
func ageIs(t *testing.T, s *engine.Session, want int64) string {
	t.Helper()
	tx := txnBegin(t, s)
	defer func() { _ = tx.Rollback(ctx) }()
	row, ok, err := txnTable(t, tx, "people").Get(ctx, engine.Key{int64(1)})
	if err != nil || !ok {
		t.Fatalf("row 1: %v, %v", ok, err)
	}
	if got := row[3].(int64); got != want {
		return "row 1's age is " + strconv.FormatInt(got, 10) + ", want the first transaction's " + strconv.FormatInt(want, 10)
	}
	return ""
}

// counterIs says how the counter hits differs from want.
func counterIs(t *testing.T, s *engine.Session, want int64) string {
	t.Helper()
	if v, _ := kindsGet(t, s, "hits"); v.Counter != want {
		return "hits is " + strconv.FormatInt(v.Counter, 10) + ", want " + strconv.FormatInt(want, 10)
	}
	return ""
}

// E4: a transaction that changes a table's schema writes every row of it,
// so it cannot commit beside one that wrote a row of the table since its
// snapshot, whichever commits first; the one that commits second fails
// with ErrSerialization and leaves the working set as the first left it.
// A transaction on another table commits beside either (the positive
// control).
func TestATransactionAlteringATableConflictsWithOneWritingItsRows(t *testing.T) {
	alter := func(t *testing.T, tx *engine.Txn) {
		next := txnPeople()
		next.Columns = append(next.Columns, engine.Column{Tag: 4, Name: "email", Type: table.TypeText, Nullable: true})
		if err := txnTable(t, tx, "people").Alter(ctx, next); err != nil {
			t.Fatalf("Alter: %v", err)
		}
	}
	write := func(t *testing.T, tx *engine.Txn) {
		if err := txnTable(t, tx, "people").Update(ctx, engine.Key{int64(2)}, txnPerson(2, "ben", 40)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name          string
		first, second itemWrite
	}{
		{"the alteration first", alter, write},
		{"the row first", write, alter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, s, _ := txnDB(t)
			setup := txnBegin(t, s)
			if _, err := setup.CreateTable(ctx, "notes", txnPeople()); err != nil {
				t.Fatal(err)
			}
			if err := setup.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			x, y := txnBegin(t, s), txnBegin(t, txnSession(t, db, "main"))
			other := txnBegin(t, txnSession(t, db, "main"))
			tc.first(t, x)
			tc.second(t, y)
			if _, err := txnTable(t, other, "notes").Insert(ctx, txnPerson(9, "note", 1)); err != nil {
				t.Fatal(err)
			}
			if err := x.Commit(ctx); err != nil {
				t.Fatalf("the first commit: %v", err)
			}
			after := txnWS(t, s)
			if err := y.Commit(ctx); !errors.Is(err, engine.ErrSerialization) {
				t.Errorf("the second commit, on the table the first %s = %v, want ErrSerialization", map[bool]string{true: "altered", false: "wrote a row of"}[tc.name == "the alteration first"], err)
			}
			if got := txnWS(t, s); got != after {
				t.Errorf("the refused commit changed the working set (%s, want %s)", got.Short(), after.Short())
			}
			if err := other.Commit(ctx); err != nil {
				t.Errorf("a transaction on another table = %v, want it to commit", err)
			}
		})
	}
}
