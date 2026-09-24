package engine_test

import (
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// Engine Spec, L4 authorization: read is granted per table as well as per
// branch. A reader granted one table on main opens a session on main and a
// transaction there, reads that table, and is refused the other (opening
// it, diffing it), which Objects does not list; a reader granted main sees
// both; revoking the table grant mid-session blocks the next call.
func TestReadIsGrantedPerTable(t *testing.T) {
	db, g := grantDB(t)
	reader := engine.Principal{ID: "user:reader"}
	if err := g.Grant(reader.ID, engine.ObjectScope("main", "people"), engine.PermRead); err != nil {
		t.Fatalf("granting read on one table: %v", err)
	}
	s, err := db.Session(ctx, reader, "main")
	if err != nil {
		t.Fatalf("a session on main for a reader of one table on it: %v", err)
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatalf("a transaction for a reader of one table: %v", err)
	}
	people, err := tx.Table(ctx, "people")
	if err != nil {
		t.Fatalf("opening the table the reader may read: %v", err)
	}
	if row, ok, err := people.Get(ctx, engine.Key{int64(1)}); err != nil || !ok || row[2] != "ada" {
		t.Errorf("reading the granted table = %v, %v, %v", row, ok, err)
	}
	if _, err := tx.Table(ctx, "pets"); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Errorf("opening a table the reader may not read = %v, want ErrPermissionDenied", err)
	}
	if _, err := s.Diff(ctx, "main", "main", "pets"); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Errorf("diffing a table the reader may not read = %v, want ErrPermissionDenied", err)
	}
	objs, err := s.Objects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := objs["pets"]; ok || len(objs) != 1 {
		t.Errorf("Objects for a reader of people alone = %v, want people only: a name is data", objs)
	}

	all := engine.Principal{ID: "user:all"}
	if err := g.Grant(all.ID, engine.BranchScope("main"), engine.PermRead); err != nil {
		t.Fatal(err)
	}
	sa := grantSession(t, db, all, "main")
	txa, err := sa.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := txa.Table(ctx, "pets"); err != nil {
		t.Errorf("a reader of main opening pets: %v", err)
	}
	if objs, err := sa.Objects(ctx); err != nil || len(objs) != 2 {
		t.Errorf("Objects for a reader of main = %v, %v, want both tables", objs, err)
	}

	if err := g.Revoke(reader.ID, engine.ObjectScope("main", "people"), engine.PermRead); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Table(ctx, "people"); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Errorf("opening people after the grant was revoked = %v, want ErrPermissionDenied", err)
	}
}

// A reader of one branch names commits by hash: a log from main's head
// commit and a diff between it and itself resolve for a principal granted
// read on main alone, as they do by the branch's name. (A hash was first
// tried as a branch's name, a lookup such a reader is denied, and the
// denial ended the resolution.)
func TestAReaderOfOneBranchNamesCommitsByHash(t *testing.T) {
	db, g := grantDB(t)
	reader := engine.Principal{ID: "user:mainreader"}
	if err := g.Grant(reader.ID, engine.BranchScope("main"), engine.PermRead); err != nil {
		t.Fatal(err)
	}
	s := grantSession(t, db, reader, "main")
	log, err := s.Log(ctx, "main", 1)
	if err != nil || len(log) != 1 {
		t.Fatalf("positive control: main's log by name = %v, %v", log, err)
	}
	head := engine.Ref(log[0].Hash.String())
	if got, err := s.Log(ctx, head, 1); err != nil || len(got) != 1 || got[0].Hash != log[0].Hash {
		t.Errorf("the log from main's head by its hash = %v, %v, want that commit", got, err)
	}
	if _, err := s.Diff(ctx, head, head, "people"); err != nil {
		t.Errorf("a diff between main's head and itself by hash: %v", err)
	}
}
