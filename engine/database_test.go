package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
	"github.com/SmithOperatingSolutions/snapshot-core/core/blob/mem"
	"github.com/SmithOperatingSolutions/snapshot-core/core/seal"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

var ctx = context.Background()

var alice = engine.Principal{ID: "user:alice"}

// dbOptions is a database on a fresh in-memory backend that allows
// everything.
func dbOptions(t *testing.T) engine.Options {
	t.Helper()
	keys, err := seal.NewKeyring()
	if err != nil {
		t.Fatal(err)
	}
	return engine.Options{Blobs: mem.New(), Keys: keys, Authorizer: auth.AllowAll{}}
}

// A database is created on a backend, opened again from it as a fresh
// process would, and serves a session on main each time; a backend that
// holds one refuses a second Create, and one that holds none refuses Open.
func TestADatabaseIsCreatedAndOpenedAgain(t *testing.T) {
	o := dbOptions(t)
	db, err := engine.Create(ctx, alice, o)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	s, err := db.Session(ctx, alice, "main")
	if err != nil {
		t.Fatalf("a session on main of a new database: %v", err)
	}
	if s.Branch() != "main" {
		t.Errorf("the session is on %q, want main", s.Branch())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Create(ctx, alice, o); !errors.Is(err, engine.ErrExists) {
		t.Errorf("a second Create on the same backend = %v, want ErrExists", err)
	}
	again, err := engine.Open(ctx, o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if _, err := again.Session(ctx, alice, "main"); err != nil {
		t.Errorf("a session on main of the reopened database: %v", err)
	}
	if _, err := engine.Open(ctx, dbOptions(t)); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Open on an empty backend = %v, want ErrNotFound", err)
	}
}

// A session is refused on a branch that does not exist, to a principal the
// authorizer denies, and on a closed database.
func TestASessionIsRefusedWhereItCannotServe(t *testing.T) {
	o := dbOptions(t)
	db, err := engine.Create(ctx, alice, o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Session(ctx, alice, "nowhere"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("a session on a branch that does not exist = %v, want ErrNotFound", err)
	}
	if _, err := db.Session(ctx, engine.Principal{}, "main"); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("a session for no principal = %v, want ErrInvalid", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Session(ctx, alice, "main"); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("a session on a closed database = %v, want ErrClosed", err)
	}
	denied := o
	denied.Authorizer = auth.DenyAll{}
	db, err = engine.Open(ctx, denied)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Session(ctx, alice, "main"); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Errorf("a session for a principal the authorizer denies = %v, want ErrPermissionDenied", err)
	}
}
