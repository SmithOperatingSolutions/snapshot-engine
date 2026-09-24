package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// txnGate allows everything until denyWrite is set, then refuses writes.
type txnGate struct{ denyWrite atomic.Bool }

func (g *txnGate) Authorize(_ context.Context, _ auth.Principal, a auth.Action, _ string) error {
	if a != auth.Read && g.denyWrite.Load() {
		return auth.ErrDenied
	}
	return nil
}

// txnPeople is a keyed table: id int8, name varchar(8), age int8 nullable,
// with an index on age.
func txnPeople() engine.Schema {
	return engine.Schema{
		Columns: []engine.Column{
			{Tag: 1, Name: "id", Type: table.TypeInt8},
			{Tag: 2, Name: "name", Type: table.TypeVarchar, MaxLen: 8},
			{Tag: 3, Name: "age", Type: table.TypeInt8, Nullable: true},
		},
		PrimaryKey: []engine.Tag{1},
		Indexes:    []engine.Index{{Tag: 10, Columns: []engine.Tag{3}}},
	}
}

func txnPerson(id int64, name string, age int64) engine.Row {
	return engine.Row{1: id, 2: name, 3: age}
}

// txnDB is a database whose main holds a table people with rows 1 ada 36
// and 2 bob 40, and the session that wrote it. The gate lets a test refuse
// writes later.
func txnDB(t *testing.T) (*engine.Database, *engine.Session, *txnGate) {
	t.Helper()
	g := &txnGate{}
	o := dbOptions(t)
	o.Authorizer = g
	db, err := engine.Create(ctx, alice, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := txnSession(t, db, "main")
	tx := txnBegin(t, s)
	people, err := tx.CreateTable(ctx, "people", txnPeople())
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	for _, r := range []engine.Row{txnPerson(1, "ada", 36), txnPerson(2, "bob", 40)} {
		if _, err := people.Insert(ctx, r); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("committing the fixture: %v", err)
	}
	return db, s, g
}

func txnSession(t *testing.T, db *engine.Database, branch string) *engine.Session {
	t.Helper()
	s, err := db.Session(ctx, alice, branch)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func txnBegin(t *testing.T, s *engine.Session) *engine.Txn {
	t.Helper()
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return tx
}

func txnTable(t *testing.T, tx *engine.Txn, name string) *engine.Table {
	t.Helper()
	tb, err := tx.Table(ctx, name)
	if err != nil {
		t.Fatalf("Table(%s): %v", name, err)
	}
	return tb
}

// txnName reads row id's name through a new transaction on s: what the
// working set holds now.
func txnName(t *testing.T, s *engine.Session, id int64) (string, bool) {
	t.Helper()
	tx := txnBegin(t, s)
	defer func() { _ = tx.Rollback(ctx) }()
	row, ok, err := txnTable(t, tx, "people").Get(ctx, engine.Key{id})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return "", false
	}
	return row[2].(string), true
}

func txnWS(t *testing.T, s *engine.Session) engine.Hash {
	t.Helper()
	h, err := engine.WorkingSetHash(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// E4: a transaction reads the working set as of Begin: another's commit
// after it began is invisible to it; its own writes are visible to it,
// before any commit, to Get, Scan and Lookup.
func TestATransactionReadsItsSnapshotAndItsOwnWrites(t *testing.T) {
	db, s, _ := txnDB(t)
	x := txnBegin(t, s)
	other := txnSession(t, db, "main")
	y := txnBegin(t, other)
	if _, err := txnTable(t, y, "people").Insert(ctx, txnPerson(3, "cy", 50)); err != nil {
		t.Fatal(err)
	}
	if err := y.Commit(ctx); err != nil {
		t.Fatalf("the other transaction's commit: %v", err)
	}
	people := txnTable(t, x, "people")
	if _, ok, err := people.Get(ctx, engine.Key{int64(3)}); err != nil || ok {
		t.Errorf("a row committed after this transaction began is visible to it (%v, %v)", ok, err)
	}
	if _, err := people.Insert(ctx, txnPerson(4, "dee", 36)); err != nil { // each read comes straight after a write
		t.Fatal(err)
	}
	if row, ok, err := people.Get(ctx, engine.Key{int64(4)}); err != nil || !ok || row[2] != "dee" {
		t.Errorf("Get does not see the transaction's own insert: %v, %v, %v", row, ok, err)
	}
	if err := people.Update(ctx, engine.Key{int64(1)}, txnPerson(1, "ada2", 36)); err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := people.Scan(ctx, func(_ engine.Key, r engine.Row) (bool, error) {
		names = append(names, r[2].(string))
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "ada2,bob,dee" {
		t.Errorf("Scan = %v, want ada2,bob,dee: the snapshot and its own writes, in key order", names)
	}
	if _, err := people.Insert(ctx, txnPerson(5, "eli", 36)); err != nil {
		t.Fatal(err)
	}
	var at36 []string
	if err := people.Lookup(ctx, 10, []any{int64(36)}, func(_ engine.Key, r engine.Row) (bool, error) {
		at36 = append(at36, r[2].(string))
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(at36, ",") != "ada2,dee,eli" {
		t.Errorf("Lookup(age 36) = %v, want ada2,dee,eli: the index sees the transaction's writes", at36)
	}
}

// E4: two transactions updating different rows both commit, and both
// changes are in the working set.
func TestTwoTransactionsOnDifferentRowsBothCommit(t *testing.T) {
	db, s, _ := txnDB(t)
	x := txnBegin(t, s)
	y := txnBegin(t, txnSession(t, db, "main"))
	if err := txnTable(t, x, "people").Update(ctx, engine.Key{int64(1)}, txnPerson(1, "ann", 36)); err != nil {
		t.Fatal(err)
	}
	if err := txnTable(t, y, "people").Update(ctx, engine.Key{int64(2)}, txnPerson(2, "ben", 40)); err != nil {
		t.Fatal(err)
	}
	if err := x.Commit(ctx); err != nil {
		t.Fatalf("the first commit: %v", err)
	}
	if err := y.Commit(ctx); err != nil {
		t.Fatalf("the second commit, on a different row: %v", err)
	}
	for id, want := range map[int64]string{1: "ann", 2: "ben"} {
		if got, _ := txnName(t, s, id); got != want {
			t.Errorf("row %d is %q, want %q: both transactions' changes land", id, got, want)
		}
	}
}

// E4: two transactions updating the same cell: the second's commit fails
// with a serialization conflict and none of its changes land, not even its
// change to another table; the working set is exactly the first's.
func TestTwoTransactionsOnTheSameCellSerialize(t *testing.T) {
	db, s, _ := txnDB(t)
	setup := txnBegin(t, s)
	if _, err := setup.CreateTable(ctx, "notes", txnPeople()); err != nil {
		t.Fatal(err)
	}
	if err := setup.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	x := txnBegin(t, s)
	y := txnBegin(t, txnSession(t, db, "main"))
	if err := txnTable(t, x, "people").Update(ctx, engine.Key{int64(1)}, txnPerson(1, "xavier", 36)); err != nil {
		t.Fatal(err)
	}
	if err := txnTable(t, y, "people").Update(ctx, engine.Key{int64(1)}, txnPerson(1, "yolanda", 36)); err != nil {
		t.Fatal(err)
	}
	if _, err := txnTable(t, y, "notes").Insert(ctx, txnPerson(9, "note", 1)); err != nil {
		t.Fatal(err)
	}
	if err := x.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	after := txnWS(t, s)
	if err := y.Commit(ctx); !errors.Is(err, engine.ErrSerialization) {
		t.Fatalf("the second commit on the same cell = %v, want ErrSerialization", err)
	}
	if got := txnWS(t, s); got != after {
		t.Errorf("the failed commit changed the working set (%s, want %s)", got.Short(), after.Short())
	}
	if got, _ := txnName(t, s, 1); got != "xavier" {
		t.Errorf("row 1 is %q, want the first transaction's xavier", got)
	}
	tx := txnBegin(t, s)
	if _, ok, err := txnTable(t, tx, "notes").Get(ctx, engine.Key{int64(9)}); err != nil || ok {
		t.Errorf("the failed transaction's insert into another table landed (%v, %v): nothing partial", ok, err)
	}
	if err := y.Rollback(ctx); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("a call on a transaction whose commit failed = %v, want ErrClosed", err)
	}
}

// A commit that lost the swap to another (a commit landed between its read
// and its swap) reads again, merges again and lands; one that loses every
// attempt gives up with ErrSerialization and writes nothing.
func TestACommitThatLosesTheSwapTriesAgain(t *testing.T) {
	db, s, _ := txnDB(t)
	other := txnSession(t, db, "main")
	x := txnBegin(t, s)
	if err := txnTable(t, x, "people").Update(ctx, engine.Key{int64(1)}, txnPerson(1, "xena", 36)); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	engine.SetBeforeSwap(x, func() {
		attempts++
		if attempts > 1 {
			return
		}
		y := txnBegin(t, other) // lands between x's read and x's swap
		if err := txnTable(t, y, "people").Update(ctx, engine.Key{int64(2)}, txnPerson(2, "yuri", 40)); err != nil {
			t.Fatal(err)
		}
		if err := y.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	if err := x.Commit(ctx); err != nil {
		t.Fatalf("a commit that lost one swap: %v", err)
	}
	if attempts != 2 {
		t.Errorf("the commit made %d attempts, want 2: one lost to the racing commit, one that landed", attempts)
	}
	for id, want := range map[int64]string{1: "xena", 2: "yuri"} {
		if got, _ := txnName(t, s, id); got != want {
			t.Errorf("row %d is %q, want %q", id, got, want)
		}
	}

	z := txnBegin(t, s)
	if err := txnTable(t, z, "people").Update(ctx, engine.Key{int64(1)}, txnPerson(1, "zed", 36)); err != nil {
		t.Fatal(err)
	}
	losses, n := 0, int64(100)
	engine.SetBeforeSwap(z, func() { // a racing commit lands before every swap
		losses++
		n++
		y := txnBegin(t, other)
		if _, err := txnTable(t, y, "people").Insert(ctx, txnPerson(n, "racer", 1)); err != nil {
			t.Fatal(err)
		}
		if err := y.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	if err := z.Commit(ctx); !errors.Is(err, engine.ErrSerialization) {
		t.Fatalf("a commit that loses every swap = %v, want ErrSerialization", err)
	}
	if losses != engine.MaxCommitAttempts {
		t.Errorf("the commit gave up after %d attempts, want %d", losses, engine.MaxCommitAttempts)
	}
	if got, _ := txnName(t, s, 1); got != "xena" {
		t.Errorf("row 1 is %q after a commit that gave up, want xena: nothing of it lands", got)
	}
}

// E4: a principal denied write gets ErrPermissionDenied at Commit, and the
// working set is unchanged.
func TestACommitWithoutWriteIsDeniedAndChangesNothing(t *testing.T) {
	_, s, gate := txnDB(t)
	x := txnBegin(t, s)
	if err := txnTable(t, x, "people").Update(ctx, engine.Key{int64(1)}, txnPerson(1, "mallory", 36)); err != nil {
		t.Fatal(err)
	}
	before := txnWS(t, s)
	gate.denyWrite.Store(true)
	if err := x.Commit(ctx); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Fatalf("a commit by a principal denied write = %v, want ErrPermissionDenied", err)
	}
	gate.denyWrite.Store(false)
	if got := txnWS(t, s); got != before {
		t.Errorf("a denied commit changed the working set (%s, want %s)", got.Short(), before.Short())
	}
	if got, _ := txnName(t, s, 1); got != "ada" {
		t.Errorf("row 1 is %q after a denied commit, want ada", got)
	}
}

// Rollback discards the transaction: the working set is untouched, and
// every call after it, as after Commit, is ErrClosed.
func TestRollbackLeavesTheWorkingSetUntouched(t *testing.T) {
	_, s, _ := txnDB(t)
	before := txnWS(t, s)
	x := txnBegin(t, s)
	people := txnTable(t, x, "people")
	if _, err := people.Insert(ctx, txnPerson(5, "eve", 20)); err != nil {
		t.Fatal(err)
	}
	if err := x.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := txnWS(t, s); got != before {
		t.Errorf("a rolled-back transaction changed the working set")
	}
	if _, err := x.Table(ctx, "people"); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("Table after Rollback = %v, want ErrClosed", err)
	}
	if _, err := people.Insert(ctx, txnPerson(6, "fay", 20)); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("a handle's Insert after Rollback = %v, want ErrClosed", err)
	}
	if err := x.Commit(ctx); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("Commit after Rollback = %v, want ErrClosed", err)
	}
	y := txnBegin(t, s)
	if err := y.Commit(ctx); err != nil {
		t.Fatalf("committing an empty transaction: %v", err)
	}
	if got := txnWS(t, s); got != before {
		t.Errorf("an empty transaction's commit changed the working set")
	}
	if _, err := y.Table(ctx, "people"); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("Table after Commit = %v, want ErrClosed", err)
	}
}

// A write the table refuses is ErrInvalid and writes nothing: the row is
// not there, and the rest of the transaction commits. Updating or deleting
// a row that is not there is ErrNotFound; inserting a key that exists,
// ErrExists.
func TestARefusedWriteWritesNothing(t *testing.T) {
	_, s, _ := txnDB(t)
	x := txnBegin(t, s)
	people := txnTable(t, x, "people")
	if _, err := people.Insert(ctx, txnPerson(7, "far too long a name", 1)); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("inserting a name over varchar(8) = %v, want ErrInvalid", err)
	}
	if _, ok, err := people.Get(ctx, engine.Key{int64(7)}); err != nil || ok {
		t.Errorf("the refused row is there (%v, %v)", ok, err)
	}
	if err := people.Update(ctx, engine.Key{int64(8)}, txnPerson(8, "gus", 1)); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("updating a row that is not there = %v, want ErrNotFound", err)
	}
	if err := people.Delete(ctx, engine.Key{int64(8)}); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("deleting a row that is not there = %v, want ErrNotFound", err)
	}
	if _, err := people.Insert(ctx, txnPerson(1, "dup", 1)); !errors.Is(err, engine.ErrExists) {
		t.Errorf("inserting a key that exists = %v, want ErrExists", err)
	}
	if err := people.Delete(ctx, engine.Key{int64(2)}); err != nil {
		t.Fatal(err)
	}
	if err := x.Commit(ctx); err != nil {
		t.Fatalf("committing the rest: %v", err)
	}
	if _, ok := txnName(t, s, 2); ok {
		t.Error("row 2, deleted by the transaction, is still there")
	}
	if _, ok := txnName(t, s, 7); ok {
		t.Error("the refused row landed")
	}
}

// Objects by name: a name that is not an object path is ErrInvalid, a name
// taken is ErrExists, a name not there is ErrNotFound, an object of
// another model asked for as a table is ErrWrongKind; a table dropped is
// gone from the transaction at once (a handle to it refuses writes) and
// from the working set at commit, and its name can be used again; a
// table's schema changes by Alter.
func TestObjectsByName(t *testing.T) {
	_, s, _ := txnDB(t)
	if err := engine.PutKV(ctx, s, "cache"); err != nil {
		t.Fatal(err)
	}
	x := txnBegin(t, s)
	for _, bad := range []string{"", "a//b", "/abs", "a/../b"} {
		if _, err := x.CreateTable(ctx, bad, txnPeople()); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("CreateTable(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	if _, err := x.CreateTable(ctx, "people", txnPeople()); !errors.Is(err, engine.ErrExists) {
		t.Errorf("CreateTable on a name taken = %v, want ErrExists", err)
	}
	if _, err := x.CreateTable(ctx, "cache", txnPeople()); !errors.Is(err, engine.ErrExists) {
		t.Errorf("CreateTable on a name another model's object holds = %v, want ErrExists", err)
	}
	if _, err := x.CreateTable(ctx, "bad", engine.Schema{}); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("CreateTable with an empty schema = %v, want ErrInvalid", err)
	}
	if _, err := x.Table(ctx, "nowhere"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Table on a name not there = %v, want ErrNotFound", err)
	}
	if _, err := x.Table(ctx, "cache"); !errors.Is(err, engine.ErrWrongKind) {
		t.Errorf("Table on a key-value map = %v, want ErrWrongKind", err)
	}
	if err := x.Drop(ctx, "nowhere"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Drop on a name not there = %v, want ErrNotFound", err)
	}
	old := txnTable(t, x, "people")
	if err := x.Drop(ctx, "people"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := old.Insert(ctx, txnPerson(3, "lost", 1)); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("a write through a dropped table's handle = %v, want ErrNotFound: it would be lost", err)
	}
	if _, err := x.Table(ctx, "people"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Table on a table dropped in this transaction = %v, want ErrNotFound", err)
	}
	if err := x.Drop(ctx, "people"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("dropping it twice = %v, want ErrNotFound", err)
	}
	again, err := x.CreateTable(ctx, "people", txnPeople())
	if err != nil {
		t.Fatalf("creating a table under a name dropped in this transaction: %v", err)
	}
	if _, ok, err := again.Get(ctx, engine.Key{int64(1)}); err != nil || ok {
		t.Errorf("the new table holds the dropped table's row (%v, %v)", ok, err)
	}
	if err := x.Drop(ctx, "cache"); err != nil {
		t.Fatalf("dropping the key-value map: %v", err)
	}
	if err := x.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	y := txnBegin(t, s)
	if _, err := y.Table(ctx, "cache"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("the dropped key-value map is still there after commit: %v", err)
	}
	people := txnTable(t, y, "people")
	if _, ok, _ := people.Get(ctx, engine.Key{int64(1)}); ok {
		t.Error("after commit the recreated table holds the dropped table's rows")
	}
	next := txnPeople()
	next.Columns[1].Name = "full_name"
	next.Columns = append(next.Columns, engine.Column{Tag: 4, Name: "email", Type: table.TypeText, Nullable: true})
	if err := people.Alter(ctx, next); err != nil {
		t.Fatalf("Alter: %v", err)
	}
	if got := people.Schema(); len(got.Columns) != 4 || got.Columns[1].Name != "full_name" {
		t.Errorf("after Alter the schema is %+v, want the renamed column and the new one", got.Columns)
	}
	if _, err := people.Insert(ctx, engine.Row{1: int64(1), 2: "ada", 4: "a@x"}); err != nil {
		t.Fatalf("inserting under the new schema: %v", err)
	}
	bad := txnPeople()
	bad.PrimaryKey = []engine.Tag{2}
	if err := people.Alter(ctx, bad); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("an Alter that changes the primary key = %v, want ErrInvalid", err)
	}
	if err := y.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	z := txnBegin(t, s)
	if got := txnTable(t, z, "people").Schema(); len(got.Columns) != 4 || got.Columns[1].Name != "full_name" {
		t.Errorf("the altered schema did not land: %+v", got.Columns)
	}
}

// A transaction cannot begin on a branch mid-merge, and one that began
// before a merge started cannot commit into it: ErrMergeInProgress,
// nothing written.
func TestABranchMidMergeTakesNoTransaction(t *testing.T) {
	db, s, _ := txnDB(t)
	if err := engine.CommitWorkingSet(ctx, s, "base"); err != nil {
		t.Fatal(err)
	}
	if err := engine.CreateBranchHere(ctx, s, "feature"); err != nil {
		t.Fatal(err)
	}
	feature := txnSession(t, db, "feature")
	for sess, name := range map[*engine.Session]string{s: "main-ada", feature: "feat-ada"} {
		tx := txnBegin(t, sess)
		if err := txnTable(t, tx, "people").Update(ctx, engine.Key{int64(1)}, txnPerson(1, name, 36)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := engine.CommitWorkingSet(ctx, sess, name); err != nil {
			t.Fatal(err)
		}
	}
	early := txnBegin(t, s)
	if err := txnTable(t, early, "people").Update(ctx, engine.Key{int64(2)}, txnPerson(2, "late", 40)); err != nil {
		t.Fatal(err)
	}
	n, err := engine.MergeBranch(ctx, s, "feature")
	if err != nil || n == 0 {
		t.Fatalf("fixture: the merge conflicted on %d paths, %v; want a conflict", n, err)
	}
	if _, err := s.Begin(ctx); !errors.Is(err, engine.ErrMergeInProgress) {
		t.Errorf("Begin on a branch mid-merge = %v, want ErrMergeInProgress", err)
	}
	before := txnWS(t, s)
	if err := early.Commit(ctx); !errors.Is(err, engine.ErrMergeInProgress) {
		t.Errorf("committing into a branch that went mid-merge = %v, want ErrMergeInProgress", err)
	}
	if txnWS(t, s) != before {
		t.Error("the refused commit changed the working set")
	}
}

// A finished transaction, and every handle it opened, refuses every call
// with ErrClosed; so does a transaction on a closed database, and a closed
// database begins none.
func TestAFinishedTransactionAndItsHandlesRefuse(t *testing.T) {
	db, s, _ := txnDB(t)
	x := txnBegin(t, s)
	people := txnTable(t, x, "people")
	if err := x.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	each := func(engine.Key, engine.Row) (bool, error) { return true, nil }
	for name, err := range map[string]error{
		"Get":         func() error { _, _, err := people.Get(ctx, engine.Key{int64(1)}); return err }(),
		"Scan":        people.Scan(ctx, each),
		"Lookup":      people.Lookup(ctx, 10, []any{int64(36)}, each),
		"Update":      people.Update(ctx, engine.Key{int64(1)}, txnPerson(1, "z", 1)),
		"Delete":      people.Delete(ctx, engine.Key{int64(1)}),
		"Alter":       people.Alter(ctx, txnPeople()),
		"CreateTable": func() error { _, err := x.CreateTable(ctx, "t2", txnPeople()); return err }(),
		"Drop":        x.Drop(ctx, "people"),
		"Rollback":    x.Rollback(ctx),
	} {
		if !errors.Is(err, engine.ErrClosed) {
			t.Errorf("%s after Rollback = %v, want ErrClosed", name, err)
		}
	}
	y := txnBegin(t, s)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := y.Table(ctx, "people"); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("Table on a closed database = %v, want ErrClosed", err)
	}
	if _, err := s.Begin(ctx); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("Begin on a closed database = %v, want ErrClosed", err)
	}
}

// A scan stops when its callback says so and hands back its callback's
// error; a key of the wrong type, and an index the table does not have,
// are ErrInvalid; an update that changes a key column is ErrInvalid.
func TestHandleReadsStopAndRefuse(t *testing.T) {
	_, s, _ := txnDB(t)
	x := txnBegin(t, s)
	people := txnTable(t, x, "people")
	seen := 0
	if err := people.Scan(ctx, func(engine.Key, engine.Row) (bool, error) { seen++; return false, nil }); err != nil || seen != 1 {
		t.Errorf("a scan told to stop at the first row saw %d rows, %v; want 1", seen, err)
	}
	errStop := errors.New("the callback's own error")
	if err := people.Scan(ctx, func(engine.Key, engine.Row) (bool, error) { return true, errStop }); !errors.Is(err, errStop) {
		t.Errorf("a scan whose callback failed = %v, want the callback's error", err)
	}
	seen = 0
	if err := people.Lookup(ctx, 10, []any{int64(36)}, func(engine.Key, engine.Row) (bool, error) { seen++; return false, nil }); err != nil || seen != 1 {
		t.Errorf("a lookup told to stop saw %d rows, %v; want 1", seen, err)
	}
	if err := people.Lookup(ctx, 99, []any{int64(36)}, func(engine.Key, engine.Row) (bool, error) { return true, nil }); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("a lookup on an index the table does not have = %v, want ErrInvalid", err)
	}
	if _, _, err := people.Get(ctx, engine.Key{"not an int"}); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("Get with a key of the wrong type = %v, want ErrInvalid", err)
	}
	if err := people.Update(ctx, engine.Key{int64(1)}, txnPerson(9, "ada", 36)); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("an update that changes the key = %v, want ErrInvalid", err)
	}
}
