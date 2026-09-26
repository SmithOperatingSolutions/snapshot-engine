package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// A member whose object the working set dropped, or remade as another
// kind, since its snapshot is a serialization failure, as a commit alone
// would be (D23: the run declines it to the merge), never an internal error
// or a write onto the wrong object.
func TestAMemberWhoseObjectWasDroppedOrRemadeSinceItsSnapshotSerializes(t *testing.T) {
	for name, remake := range map[string]func(ctx context.Context, tx *engine.Txn) error{
		"dropped": func(ctx context.Context, tx *engine.Txn) error { return tx.Drop(ctx, "people") },
		"remade as a kv map": func(ctx context.Context, tx *engine.Txn) error {
			if err := tx.Drop(ctx, "people"); err != nil {
				return err
			}
			_, err := tx.CreateKV(ctx, "people")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			db, _, _ := queueDB(t, 3)
			member := queueWrite(t, db, alice, "people", map[int64]string{1: "late"})
			other := txnBegin(t, txnSession(t, db, "main"))
			if err := remake(ctx, other); err != nil {
				t.Fatal(err)
			}
			if err := other.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := member.Commit(ctx); !errors.Is(err, engine.ErrSerialization) {
				t.Errorf("a member writing a row of a table the working set %s since its snapshot = %v, want ErrSerialization", name, err)
			}
		})
	}
}

// A batch whose members hold different grants publishes: each member is
// asked for what it changes, and the batch's swap, made as one member, is
// allowed exactly those answers (D20), so bob's table lands although
// alice, who leads, may not write it.
func TestABatchOfMembersWithDifferentGrantsPublishes(t *testing.T) {
	db, g := grantDB(t)
	grantOK(t, g.Grant("user:bob", engine.BranchScope("main"), engine.PermRead))
	grantOK(t, g.Grant("user:bob", engine.ObjectScope("main", "pets"), engine.PermWrite))
	grantOK(t, g.Revoke("user:alice", engine.Scope{}, engine.PermWrite))
	grantOK(t, g.Grant("user:alice", engine.BranchScope("main"), engine.PermRead))
	grantOK(t, g.Grant("user:alice", engine.ObjectScope("main", "people"), engine.PermWrite))
	alicePeople := queueWrite(t, db, alice, "people", map[int64]string{1: "ann"})
	bobPets := queueWrite(t, db, bob, "pets", map[int64]string{1: "max"})
	h, held := holdTheQueue(t, db, 2, nil)
	outs, queued := enqueueInOrder(db, committing(ctx, alicePeople), committing(ctx, bobPets))
	released(t, h, held)
	if err := <-outs[0]; err != nil {
		t.Errorf("alice's write to people, which she may write = %v, want it committed", err)
	}
	if err := <-outs[1]; err != nil {
		t.Errorf("bob's write to pets, which he may write, in a batch alice leads = %v, want it committed: the swap must be allowed what each member was, not what the leader is", err)
	}
	if !queued {
		t.Fatal("fixture: the two commits did not share a batch")
	}
	wantNames(t, db, map[int64]string{1: "ann", 2: "held"})
}

// A member the run declines (here one creating a table) is asked for its
// writes like any other, and one denied fails alone: its table does not
// land under the leader's grants, and the member beside it commits.
func TestADeclinedMemberDeniedWriteFailsAloneInItsBatch(t *testing.T) {
	db, g := grantDB(t)
	grantOK(t, g.Grant("user:bob", engine.BranchScope("main"), engine.PermRead))
	alicePeople := queueWrite(t, db, alice, "people", map[int64]string{1: "ann"})
	bobSession, err := db.Session(ctx, bob, "main")
	if err != nil {
		t.Fatal(err)
	}
	bobNotes := txnBegin(t, bobSession)
	if _, err := bobNotes.CreateTable(ctx, "notes", txnPeople()); err != nil {
		t.Fatal(err)
	}
	h, held := holdTheQueue(t, db, 2, nil)
	outs, queued := enqueueInOrder(db, committing(ctx, alicePeople), committing(ctx, bobNotes))
	released(t, h, held)
	if err := <-outs[0]; err != nil {
		t.Errorf("alice's write = %v, want it committed", err)
	}
	if err := <-outs[1]; !errors.Is(err, engine.ErrPermissionDenied) {
		t.Errorf("bob's new table, which he may not write, in a batch alice leads = %v, want ErrPermissionDenied: it would land under alice's grants", err)
	}
	if !queued {
		t.Fatal("fixture: the two commits did not share a batch")
	}
	if _, err := txnBegin(t, txnSession(t, db, "main")).Table(ctx, "notes"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("the table a denied member created: %v, want not found", err)
	}
}

// A member that collides with what landed since its snapshot fails
// without reading what landed: its refusal reads its own changes and one
// item of the working set per change, like a member that lands (D23), so
// a retry storm on hot rows costs the leader no more per member than the
// members that commit.
func TestACollidingMemberFailsWithoutReadingWhatMoved(t *testing.T) {
	small, large := collidingReads(t, 300), collidingReads(t, 3000)
	t.Logf("backend reads of a colliding one-row commit: %d rows moved: %d; %d rows moved: %d", 300, small, 3000, large)
	if large > 2*small+8 {
		t.Errorf("a colliding one-row commit read %d objects with 3000 rows moved and %d with 300: the leader diffed the member against everything that landed to refuse it", large, small)
	}
}

// collidingReads is movedReads with the other process changing the
// member's row too, so the member's commit is ErrSerialization.
func collidingReads(t *testing.T, moved int) int64 {
	t.Helper()
	reads, err := movedReadsOf(t, moved, true)
	if !errors.Is(err, engine.ErrSerialization) {
		t.Fatalf("fixture: a commit whose row another process changed = %v, want ErrSerialization", err)
	}
	return reads
}
