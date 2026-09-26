package engine_test

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/blob"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// readingBlobs is a backend with its object reads counted.
type readingBlobs struct {
	blob.BlobStore
	gets atomic.Int64
}

func (b *readingBlobs) Get(ctx context.Context, name string, off, n int64) (io.ReadCloser, error) {
	b.gets.Add(1)
	return b.BlobStore.Get(ctx, name, off, n)
}

// movedReads is the backend reads a one-row commit makes when, between its
// snapshot and its commit, another process updated moved rows of the
// table: a process opens the database and begins the transaction, the
// other process moves the rows, and the reads of the commit alone are
// counted.
func movedReads(t *testing.T, moved int) int64 {
	t.Helper()
	reads, err := movedReadsOf(t, moved, false)
	if err != nil {
		t.Fatalf("a one-row commit onto a working set another process moved: %v", err)
	}
	return reads
}

// movedReadsOf is movedReads returning the commit's outcome; with collide
// the other process changes the member's own row too.
func movedReadsOf(t *testing.T, moved int, collide bool) (int64, error) {
	t.Helper()
	o := dbOptions(t)
	writer, err := engine.Create(ctx, alice, o)
	if err != nil {
		t.Fatal(err)
	}
	seed := txnBegin(t, txnSession(t, writer, "main"))
	people, err := seed.CreateTable(ctx, "people", txnPeople())
	if err != nil {
		t.Fatal(err)
	}
	for i := range moved {
		if _, err := people.Insert(ctx, txnPerson(int64(i), fmt.Sprintf("p%d", i), 30)); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	rb := &readingBlobs{BlobStore: o.Blobs}
	ro := o
	ro.Blobs = rb
	reader, err := engine.Open(ctx, ro)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	tx := txnBegin(t, txnSession(t, reader, "main"))
	mine, err := tx.Table(ctx, "people")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mine.Insert(ctx, txnPerson(int64(moved), "new", 30)); err != nil {
		t.Fatal(err)
	}

	move := txnBegin(t, txnSession(t, writer, "main"))
	theirs, err := move.Table(ctx, "people")
	if err != nil {
		t.Fatal(err)
	}
	for i := range moved {
		if err := theirs.Update(ctx, engine.Key{int64(i)}, txnPerson(int64(i), fmt.Sprintf("m%d", i), 31)); err != nil {
			t.Fatal(err)
		}
	}
	if collide { // the row the member adds, added here too
		if _, err := theirs.Insert(ctx, txnPerson(int64(moved), "theirs", 31)); err != nil {
			t.Fatal(err)
		}
	}
	if err := move.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	rb.gets.Store(0)
	err = tx.Commit(ctx)
	return rb.gets.Load(), err
}

// A commit onto a working set that moved since the transaction's snapshot
// reads the transaction's own changes and the target's item for each, not
// what moved: ten times the rows moved is not ten times the backend reads
// of a one-row commit (D20, #8). The budget is the growth between two
// sizes, with headroom for the target's deeper paths.
func TestACommitOntoAMovedWorkingSetReadsItsOwnChangesNotWhatMoved(t *testing.T) {
	small, large := movedReads(t, 300), movedReads(t, 3000)
	t.Logf("backend reads of a one-row commit: %d rows moved: %d; %d rows moved: %d", 300, small, 3000, large)
	if large > 2*small+8 {
		t.Errorf("a one-row commit read %d objects with 3000 rows moved and %d with 300: the leader's work grows with what landed since the snapshot, not with the transaction, and a batch's later members pay for the earlier ones", large, small)
	}
}
