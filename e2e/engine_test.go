package e2e_test

import (
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// E4: a program drives a database through engine/ alone, importing nothing
// of the core or the models (a lint rule holds this file to it): it makes a
// database on disk with a table, a key-value map and a document
// collection, commits them, branches, changes each kind on both branches
// without colliding, merges, reads every change back, closes, and opens the
// database again from disk as a fresh process would, finding it all there;
// and a change both branches make to one cell stops the merge, locating it.
func TestAProgramDrivesADatabaseThroughTheEngineAlone(t *testing.T) {
	keys, err := engine.NewKeyring()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	blobs, err := engine.DiskBlobs(dir)
	if err != nil {
		t.Fatal(err)
	}
	o := engine.Options{Blobs: blobs, Keys: keys, Authorizer: engine.AllowAll()}
	me := engine.Principal{ID: "user:program"}
	db, err := engine.Create(ctx, me, o)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s, err := db.Session(ctx, me, "main")
	if err != nil {
		t.Fatal(err)
	}
	people := engine.Schema{
		Columns: []engine.Column{
			{Tag: 1, Name: "id", Type: engine.TypeInt8},
			{Tag: 2, Name: "name", Type: engine.TypeText},
			{Tag: 3, Name: "city", Type: engine.TypeText, Nullable: true},
		},
		PrimaryKey: []engine.Tag{1},
	}
	write := func(s *engine.Session, f func(tx *engine.Txn) error) {
		t.Helper()
		tx, err := s.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := f(tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Txn.Commit: %v", err)
		}
	}
	write(s, func(tx *engine.Txn) error {
		tb, err := tx.CreateTable(ctx, "people", people)
		if err != nil {
			return err
		}
		for _, r := range []engine.Row{{1: int64(1), 2: "ada", 3: "London"}, {1: int64(2), 2: "grace", 3: "Arlington"}} {
			if _, err := tb.Insert(ctx, r); err != nil {
				return err
			}
		}
		m, err := tx.CreateKV(ctx, "settings")
		if err != nil {
			return err
		}
		if err := m.Set(ctx, []byte("theme"), engine.Value{Kind: engine.ValueBytes, Bytes: []byte("dark")}); err != nil {
			return err
		}
		docs, err := tx.CreateCollection(ctx, "orders")
		if err != nil {
			return err
		}
		return docs.PutJSON(ctx, []byte("o1"), []byte(`{"item": "tea", "qty": 1}`))
	})
	if _, err := s.Commit(ctx, "the first data"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranch(ctx, "feature", ""); err != nil {
		t.Fatal(err)
	}
	feature, err := db.Session(ctx, me, "feature")
	if err != nil {
		t.Fatal(err)
	}
	write(s, func(tx *engine.Txn) error { // main: ada moves; a setting; the order's qty
		tb, err := tx.Table(ctx, "people")
		if err != nil {
			return err
		}
		if err := tb.Update(ctx, engine.Key{int64(1)}, engine.Row{1: int64(1), 2: "ada", 3: "Paris"}); err != nil {
			return err
		}
		m, err := tx.KV(ctx, "settings")
		if err != nil {
			return err
		}
		if err := m.Set(ctx, []byte("lang"), engine.Value{Kind: engine.ValueBytes, Bytes: []byte("en")}); err != nil {
			return err
		}
		docs, err := tx.Collection(ctx, "orders")
		if err != nil {
			return err
		}
		return docs.PutJSON(ctx, []byte("o1"), []byte(`{"item": "tea", "qty": 2}`))
	})
	if _, err := s.Commit(ctx, "main's changes"); err != nil {
		t.Fatal(err)
	}
	write(feature, func(tx *engine.Txn) error { // feature: a new person; another setting; the order's note
		tb, err := tx.Table(ctx, "people")
		if err != nil {
			return err
		}
		if _, err := tb.Insert(ctx, engine.Row{1: int64(3), 2: "linus"}); err != nil {
			return err
		}
		m, err := tx.KV(ctx, "settings")
		if err != nil {
			return err
		}
		if err := m.Set(ctx, []byte("tz"), engine.Value{Kind: engine.ValueBytes, Bytes: []byte("UTC")}); err != nil {
			return err
		}
		docs, err := tx.Collection(ctx, "orders")
		if err != nil {
			return err
		}
		return docs.PutJSON(ctx, []byte("o1"), []byte(`{"item": "tea", "qty": 1, "note": "green"}`))
	})
	if _, err := feature.Commit(ctx, "feature's changes"); err != nil {
		t.Fatal(err)
	}
	res, err := s.Merge(ctx, "feature", "merge feature")
	if err != nil || len(res.Conflicts) != 0 {
		t.Fatalf("merging non-colliding changes of every kind = %+v, %v", res, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if o.Blobs, err = engine.DiskBlobs(dir); err != nil { // a fresh process opens the same directory
		t.Fatal(err)
	}
	db, err = engine.Open(ctx, o)
	if err != nil {
		t.Fatalf("Open again from disk: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err = db.Session(ctx, me, "main")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tb, err := tx.Table(ctx, "people")
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]string{1: "Paris", 3: ""} {
		row, ok, err := tb.Get(ctx, engine.Key{id})
		if err != nil || !ok {
			t.Fatalf("person %d after the merge: %v, %v", id, ok, err)
		}
		if city, _ := row[3].(string); city != want {
			t.Errorf("person %d's city = %q, want %q", id, city, want)
		}
	}
	m, err := tx.KV(ctx, "settings")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"theme", "lang", "tz"} {
		if _, ok, err := m.Get(ctx, []byte(k)); err != nil || !ok {
			t.Errorf("setting %s after the merge: %v, %v", k, ok, err)
		}
	}
	docs, err := tx.Collection(ctx, "orders")
	if err != nil {
		t.Fatal(err)
	}
	o1, ok, err := docs.Get(ctx, []byte("o1"))
	if err != nil || !ok {
		t.Fatalf("order o1 after the merge: %v, %v", ok, err)
	}
	want, err := engine.ParseDocument([]byte(`{"item": "tea", "qty": 2, "note": "green"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !o1.Equal(want) {
		t.Errorf("order o1 after the merge = %s, want both sides' fields %s", o1.Canonical(), want.Canonical())
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// Both branches set one cell: the merge stops there and says where.
	if err := s.CreateBranch(ctx, "other", ""); err != nil {
		t.Fatal(err)
	}
	other, err := db.Session(ctx, me, "other")
	if err != nil {
		t.Fatal(err)
	}
	for _, br := range []struct {
		s    *engine.Session
		city string
	}{{s, "Rome"}, {other, "Oslo"}} {
		write(br.s, func(tx *engine.Txn) error {
			tb, err := tx.Table(ctx, "people")
			if err != nil {
				return err
			}
			return tb.Update(ctx, engine.Key{int64(2)}, engine.Row{1: int64(2), 2: "grace", 3: br.city})
		})
		if _, err := br.s.Commit(ctx, "grace moves to "+br.city); err != nil {
			t.Fatal(err)
		}
	}
	res, err = s.Merge(ctx, "other", "merge other")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].Path != "people" || len(res.Conflicts[0].Parts) != 1 {
		t.Fatalf("merging one cell set two ways = %+v, want one conflict in people at one cell", res.Conflicts)
	}
	if _, err := s.Commit(ctx, "cannot"); !errors.Is(err, engine.ErrMergeInProgress) {
		t.Errorf("a commit mid-merge = %v, want ErrMergeInProgress", err)
	}
	if err := s.AbortMerge(ctx); err != nil {
		t.Errorf("AbortMerge: %v", err)
	}
}
