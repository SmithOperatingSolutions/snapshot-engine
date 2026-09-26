package engine_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// A batch's publish writes each object its members changed once, not once
// per member: 32 members renaming 32 rows of one table write about what
// one member alone does, not 32 times it (#22). The budget is the bytes
// the backend takes for the batch's publish against one member's, with
// headroom for the rows themselves.
func TestABatchWritesEachObjectOnce(t *testing.T) {
	const n = 32
	db, blobs, _ := queueDB(t, n+2)
	before := blobs.bytes.Load()
	if err := queueWrite(t, db, alice, "people", map[int64]string{n + 1: "alone"}).Commit(ctx); err != nil {
		t.Fatal(err)
	}
	alone := blobs.bytes.Load() - before
	txs := make([]*engine.Txn, n)
	want := map[int64]string{n + 1: "alone", n + 2: "held"}
	for i := range txs {
		id := int64(i + 1)
		want[id] = fmt.Sprintf("w%d", id)
		txs[i] = queueWrite(t, db, alice, "people", map[int64]string{id: want[id]})
	}
	var atBatch atomic.Int64
	h, held := holdTheQueue(t, db, n+2, func(publish int) {
		if publish == 2 {
			atBatch.Store(blobs.bytes.Load())
		}
	})
	outs := make([]<-chan error, n)
	for i, tx := range txs {
		outs[i] = commitAsync(ctx, tx)
	}
	queued := queuedBehind(db, n)
	released(t, h, held)
	allCommitted(t, "transactions", outs)
	wantNames(t, db, want)
	if sizes := h.sizes(); !queued || len(sizes) != 2 || sizes[1] != n {
		t.Fatalf("fixture: the publishes carried %v members (queued: %v), want the held one then all %d", sizes, queued, n)
	}
	batch := blobs.bytes.Load() - atBatch.Load()
	t.Logf("one member's publish wrote %d bytes; a batch of %d members' wrote %d", alone, n, batch)
	if batch > 8*alone {
		t.Errorf("a batch of %d members writing 32 rows of one table published %d bytes, one member alone %d: the leader rewrote the table once per member, not once for the batch", n, batch, alone)
	}
}
