package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
	"github.com/SmithOperatingSolutions/snapshot-core/core/blob/mem"
	"github.com/SmithOperatingSolutions/snapshot-core/core/seal"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

var rebaseWho = auth.Principal{ID: "user:rebase"}

func rebasePeople() Schema {
	return Schema{
		Columns: []Column{
			{Tag: 1, Name: "id", Type: table.TypeInt8},
			{Tag: 2, Name: "name", Type: table.TypeText},
			{Tag: 3, Name: "age", Type: table.TypeInt8, Nullable: true},
		},
		PrimaryKey: []Tag{1},
	}
}

// rebaseFixture is a database whose main holds a table people (rows 1 to
// 300), a kv map cache (keys k0 to k299) and a collection docs (records r0
// to r299), and a session on main.
func rebaseFixture(t *testing.T) (*Database, *Session) {
	t.Helper()
	ctx := context.Background()
	keys, err := seal.NewKeyring()
	if err != nil {
		t.Fatal(err)
	}
	db, err := Create(ctx, rebaseWho, Options{Blobs: mem.New(), Keys: keys, Authorizer: auth.AllowAll{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := db.Session(ctx, rebaseWho, "main")
	if err != nil {
		t.Fatal(err)
	}
	rebaseDo(t, s, func(tx *Txn) error {
		people, err := tx.CreateTable(ctx, "people", rebasePeople())
		if err != nil {
			return err
		}
		cache, err := tx.CreateKV(ctx, "cache")
		if err != nil {
			return err
		}
		docs, err := tx.CreateCollection(ctx, "docs")
		if err != nil {
			return err
		}
		if err := cache.Set(ctx, []byte("hits"), Value{Kind: ValueCounter, Counter: 1000}); err != nil {
			return err
		}
		for i := range 300 {
			if _, err := people.Insert(ctx, Row{1: int64(i), 2: "p"}); err != nil {
				return err
			}
			if err := cache.Set(ctx, []byte{'k', byte(i >> 8), byte(i)}, Value{Kind: ValueBytes, Bytes: []byte("v")}); err != nil {
				return err
			}
			if err := docs.PutJSON(ctx, []byte{'r', byte(i >> 8), byte(i)}, []byte(`{"n":1}`)); err != nil {
				return err
			}
		}
		return nil
	})
	return db, s
}

// rebaseDo runs f in a transaction on s and commits it.
func rebaseDo(t *testing.T, s *Session, f func(tx *Txn) error) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

type rebaseEdit func(ctx context.Context, tx *Txn) error

func setRow(id int64, name string) rebaseEdit {
	return func(ctx context.Context, tx *Txn) error {
		tb, err := tx.Table(ctx, "people")
		if err != nil {
			return err
		}
		return tb.Update(ctx, Key{id}, Row{1: id, 2: name})
	}
}

func setAge(id int64, age int64) rebaseEdit {
	return func(ctx context.Context, tx *Txn) error {
		tb, err := tx.Table(ctx, "people")
		if err != nil {
			return err
		}
		return tb.Update(ctx, Key{id}, Row{1: id, 2: "p", 3: age})
	}
}

func addRow(id int64) rebaseEdit {
	return func(ctx context.Context, tx *Txn) error {
		tb, err := tx.Table(ctx, "people")
		if err != nil {
			return err
		}
		_, err = tb.Insert(ctx, Row{1: id, 2: "new"})
		return err
	}
}

func setKey(i int, v string) rebaseEdit {
	return func(ctx context.Context, tx *Txn) error {
		m, err := tx.KV(ctx, "cache")
		if err != nil {
			return err
		}
		return m.Set(ctx, []byte{'k', byte(i >> 8), byte(i)}, Value{Kind: ValueBytes, Bytes: []byte(v)})
	}
}

// incrHits reads the counter hits and writes it back by more, as INCR is run.
func incrHits(by int64) rebaseEdit {
	return func(ctx context.Context, tx *Txn) error {
		m, err := tx.KV(ctx, "cache")
		if err != nil {
			return err
		}
		v, _, err := m.Get(ctx, []byte("hits"))
		if err != nil {
			return err
		}
		return m.Set(ctx, []byte("hits"), Value{Kind: ValueCounter, Counter: v.Counter + by})
	}
}

func setRecord(i int, text string) rebaseEdit {
	return func(ctx context.Context, tx *Txn) error {
		c, err := tx.Collection(ctx, "docs")
		if err != nil {
			return err
		}
		return c.PutJSON(ctx, []byte{'r', byte(i >> 8), byte(i)}, []byte(text))
	}
}

func alterPeople(ctx context.Context, tx *Txn) error {
	tb, err := tx.Table(ctx, "people")
	if err != nil {
		return err
	}
	next := rebasePeople()
	next.Columns = append(next.Columns, Column{Tag: 4, Name: "note", Type: table.TypeText, Nullable: true})
	return tb.Alter(ctx, next)
}

func dropPeople(ctx context.Context, tx *Txn) error {
	return tx.Drop(ctx, "people")
}

// peopleAsKV drops the table people and makes a kv map of the name.
func peopleAsKV(ctx context.Context, tx *Txn) error {
	if err := tx.Drop(ctx, "people"); err != nil {
		return err
	}
	_, err := tx.CreateKV(ctx, "people")
	return err
}

func createNotes(ctx context.Context, tx *Txn) error {
	_, err := tx.CreateTable(ctx, "notes", rebasePeople())
	return err
}

func all(edits ...rebaseEdit) rebaseEdit {
	return func(ctx context.Context, tx *Txn) error {
		for _, e := range edits {
			if err := e(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	}
}

// A transaction's changes rebased onto a working set that moved are what
// the merge they replace makes, root for root, in every case the rebase
// takes: rows, keys and records changed on either side, objects only one
// side touched; an item both changed is ErrSerialization from either. A
// change the rebase does not take (a schema changed on either side, an
// object created or dropped, a counter both incremented, which the merge
// sums, D18) is left to the merge, which still decides it.
func TestARebaseMakesWhatTheMergeMakes(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name       string
		ours, cur  rebaseEdit
		takes      bool
		serializes bool
	}{
		{"different rows", setRow(1, "ours"), setRow(2, "cur"), true, false},
		{"rows, keys and records, each side", all(setRow(1, "o"), addRow(900), setKey(3, "o"), setRecord(4, `{"n":2}`)), all(setRow(250, "c"), addRow(901), setKey(200, "c"), setRecord(5, `{"n":3}`)), true, false},
		{"an object cur did not touch", setKey(1, "o"), setRow(7, "c"), true, false},
		{"one row, the same value", setRow(1, "same"), setRow(1, "same"), true, true},
		{"one row, different cells", setRow(1, "o"), setAge(1, 40), true, true},
		{"one row added on both", addRow(900), addRow(900), true, true},
		{"one key", setKey(9, "o"), setKey(9, "c"), false, true},
		{"one counter both incremented", incrHits(1), incrHits(1), false, false},
		{"one record, different fields", setRecord(9, `{"n":1,"a":1}`), setRecord(9, `{"n":1,"b":1}`), true, true},
		{"a schema changed by cur", setRow(1, "o"), alterPeople, false, true},
		{"a schema changed by ours", alterPeople, setRow(1, "c"), false, true},
		{"an object created", createNotes, setRow(1, "c"), false, false},
		{"an object dropped by cur", setRow(1, "o"), dropPeople, false, true},
		{"an object dropped by ours", dropPeople, setRow(1, "c"), false, true},
		{"an object changed in kind by ours", peopleAsKV, setRow(1, "c"), false, true},
		{"an object changed in kind by cur", setRow(1, "o"), peopleAsKV, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, s := rebaseFixture(t)
			tx, err := s.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.ours(ctx, tx); err != nil {
				t.Fatal(err)
			}
			other, err := db.Session(ctx, rebaseWho, "main")
			if err != nil {
				t.Fatal(err)
			}
			rebaseDo(t, other, func(y *Txn) error { return c.cur(ctx, y) })
			ours, err := tx.namespace(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ws, err := db.r.WorkingSet(ctx, rebaseWho, "main")
			if err != nil {
				t.Fatal(err)
			}
			cur, err := db.r.Namespace(ctx, ws.Working)
			if err != nil {
				t.Fatal(err)
			}
			merged, mergeErr := tx.merged(ctx, ours, cur)
			rebased, took, rebaseErr := tx.rebase(ctx, ours, cur)
			if took != c.takes {
				t.Fatalf("the rebase took the change: %v, want %v", took, c.takes)
			}
			if c.serializes != errors.Is(mergeErr, ErrSerialization) {
				t.Fatalf("the merge = %v, want serialization: %v (the fixture is not the case it names)", mergeErr, c.serializes)
			}
			if !took {
				if rebaseErr != nil {
					t.Fatalf("the rebase declined the change with %v: a change it does not take is the merge's to decide, not a failed commit", rebaseErr)
				}
				return
			}
			switch {
			case c.serializes && !errors.Is(rebaseErr, ErrSerialization):
				t.Errorf("the rebase of an item both changed = %v, want ErrSerialization, as the merge has it: two read-modify-writes of one item would both land", rebaseErr)
			case !c.serializes && (rebaseErr != nil || mergeErr != nil):
				t.Errorf("rebase = %v, merge = %v, want both to succeed", rebaseErr, mergeErr)
			case !c.serializes && rebased.Root() != merged.Root():
				t.Errorf("the rebase made %s, the merge %s: a batch member would land another working set than a commit alone", rebased.Root().Short(), merged.Root().Short())
			}
		})
	}
}
