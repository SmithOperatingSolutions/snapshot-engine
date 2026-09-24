package engine_test

import (
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// A branch a session is on cannot be deleted from under it by another
// session (ErrInUse, the branch still listed); once the session moves to
// another branch or closes, the branch is free to delete.
func TestABranchASessionIsOnCannotBeDeleted(t *testing.T) {
	db, err := engine.Create(ctx, alice, dbOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	main, err := db.Session(ctx, alice, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"feature", "other"} {
		if err := main.CreateBranch(ctx, b, ""); err != nil {
			t.Fatal(err)
		}
	}
	on, err := db.Session(ctx, alice, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if err := main.DeleteBranch(ctx, "feature"); !errors.Is(err, engine.ErrInUse) {
		t.Fatalf("deleting a branch another session is on = %v, want ErrInUse", err)
	}
	if bs, err := main.Branches(ctx); err != nil || !has(bs, "feature") {
		t.Fatalf("after the refused delete the branches are %v, %v: feature must still be there", bs, err)
	}
	if err := on.Checkout(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if err := main.DeleteBranch(ctx, "feature"); err != nil {
		t.Errorf("deleting a branch the session has moved off: %v", err)
	}
	if err := main.DeleteBranch(ctx, "other"); !errors.Is(err, engine.ErrInUse) {
		t.Errorf("deleting the branch the session moved to = %v, want ErrInUse", err)
	}
	if err := on.Close(); err != nil {
		t.Fatal(err)
	}
	if err := main.DeleteBranch(ctx, "other"); err != nil {
		t.Errorf("deleting a branch whose session closed: %v", err)
	}
	if err := on.Close(); err != nil {
		t.Errorf("closing a session twice: %v", err)
	}
}

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
