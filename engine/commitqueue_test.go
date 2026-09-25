package engine_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// Group commit (DESIGN D20, issue #1). Commits to one branch of one
// Database take turns in that branch's queue: a leader takes the
// transactions waiting, applies each to the working set in order, each
// checked against its own snapshot by the item rule, and publishes them
// with one root swap. Before it every transaction swapped the root alone,
// and N sessions on one branch shared one disk's swap rate.
//
// The tests hold the first publish (the seam runs just before its swap),
// start the commits they want batched so they wait behind it, then let it
// go: which commits share a publish is then the queue's doing, not the
// scheduler's.

// swapCounter is a backend that counts the root swaps that land: one per
// publish.
type swapCounter struct {
	engine.Blobs
	swaps atomic.Int64
}

func (b *swapCounter) SwapRoot(ctx context.Context, expected engine.BlobVersion, next []byte) (engine.BlobVersion, error) {
	v, err := b.Blobs.SwapRoot(ctx, expected, next)
	if err == nil {
		b.swaps.Add(1)
	}
	return v, err
}

// queueDB is a database on a counted backend whose main holds the table
// people, rows 1 to n, row i named p<i> and aged i, in the working set.
// Its options open the same backend again, as another process would.
func queueDB(t *testing.T, n int) (*engine.Database, *swapCounter, engine.Options) {
	t.Helper()
	o := dbOptions(t)
	blobs := &swapCounter{Blobs: o.Blobs}
	o.Blobs = blobs
	db, err := engine.Create(ctx, alice, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tx := txnBegin(t, txnSession(t, db, "main"))
	people, err := tx.CreateTable(ctx, "people", txnPeople())
	if err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= int64(n); id++ {
		if _, err := people.Insert(ctx, txnPerson(id, fmt.Sprintf("p%d", id), id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("committing the fixture: %v", err)
	}
	return db, blobs, o
}

// queueWrite begins a transaction for p on a new session of db's main and
// renames each row it is given; the test commits it later.
func queueWrite(t *testing.T, db *engine.Database, p engine.Principal, tableName string, names map[int64]string) *engine.Txn {
	t.Helper()
	s, err := db.Session(ctx, p, "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	tx := txnBegin(t, s)
	tb := txnTable(t, tx, tableName)
	for id, name := range names {
		if err := tb.Update(ctx, engine.Key{id}, txnPerson(id, name, id)); err != nil {
			t.Fatal(err)
		}
	}
	return tx
}

// commitAsync commits tx on a goroutine of its own; the outcome arrives on
// the channel.
func commitAsync(ctx context.Context, tx *engine.Txn) <-chan error {
	out := make(chan error, 1)
	go func() { out <- tx.Commit(ctx) }()
	return out
}

// publishHold holds db's first publish of transactions until release is
// closed, and records how many transactions every publish carried.
type publishHold struct {
	mu      sync.Mutex
	carried []int
	held    chan struct{}
	release chan struct{}
}

func (h *publishHold) sizes() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.carried)
}

// holdTheQueue commits a transaction renaming row id to "held" and returns
// once its publish is held: a commit on main started after it waits behind
// it. then, when given, runs before every later publish, with its number
// (the held one is 1).
func holdTheQueue(t *testing.T, db *engine.Database, id int64, then func(publish int)) (*publishHold, <-chan error) {
	t.Helper()
	h := &publishHold{held: make(chan struct{}), release: make(chan struct{})}
	engine.SetBeforePublish(db, func(members int) {
		h.mu.Lock()
		h.carried = append(h.carried, members)
		n := len(h.carried)
		h.mu.Unlock()
		if n == 1 {
			close(h.held)
			<-h.release
			return
		}
		if then != nil {
			then(n)
		}
	})
	done := commitAsync(ctx, queueWrite(t, db, alice, "people", map[int64]string{id: "held"}))
	select {
	case <-h.held:
	case err := <-done:
		t.Fatalf("the holding commit returned (%v) without publishing", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the holding commit never reached its publish")
	}
	return h, done
}

// queuedBehind waits up to two seconds for exactly n commits to wait in
// main's queue, and says whether they did.
func queuedBehind(db *engine.Database, n int) bool {
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if engine.Queued(db, "main") == n {
			return true
		}
	}
	return false
}

// enqueueInOrder starts each commit and waits for it to queue before the
// next, so the queue holds them in the order given; queued says whether
// every one of them waited.
func enqueueInOrder(db *engine.Database, commits ...func() <-chan error) (outs []<-chan error, queued bool) {
	queued = true
	for i, c := range commits {
		outs = append(outs, c())
		queued = queuedBehind(db, i+1) && queued
	}
	return outs, queued
}

func committing(ctx context.Context, tx *engine.Txn) func() <-chan error {
	return func() <-chan error { return commitAsync(ctx, tx) }
}

// allCommitted reports the commits among outs that failed.
func allCommitted(t *testing.T, what string, outs []<-chan error) {
	t.Helper()
	var failed []error
	for _, out := range outs {
		if err := <-out; err != nil {
			failed = append(failed, err)
		}
	}
	if len(failed) > 0 {
		t.Errorf("%d of %d %s failed, the first with %v: each wrote a row no other touched, and an application would have to retry it", len(failed), len(outs), what, failed[0])
	}
}

// released lets the held publish go and checks that it landed.
func released(t *testing.T, h *publishHold, held <-chan error) {
	t.Helper()
	close(h.release)
	if err := <-held; err != nil {
		t.Fatalf("the holding commit: %v", err)
	}
}

// wantNames checks rows of people as main's working set holds them now.
func wantNames(t *testing.T, db *engine.Database, want map[int64]string) {
	t.Helper()
	s := txnSession(t, db, "main")
	defer func() { _ = s.Close() }()
	var wrong []string
	for _, id := range slices.Sorted(maps.Keys(want)) {
		if got, _ := txnName(t, s, id); got != want[id] {
			wrong = append(wrong, fmt.Sprintf("row %d is %q, want %q", id, got, want[id]))
		}
	}
	if len(wrong) > 0 {
		t.Errorf("%d of %d rows of people are not what the commits reported (%s)", len(wrong), len(want), strings.Join(wrong[:min(len(wrong), 3)], "; "))
	}
}

// rowAt is row id's name in people as commit recorded it.
func rowAt(t *testing.T, db *engine.Database, commit engine.Hash, id int64) string {
	t.Helper()
	ref, ok, err := engine.ObjectAt(ctx, db, alice, commit, "people")
	if err != nil || !ok {
		t.Fatalf("people at commit %s: %v, %v", commit.Short(), ok, err)
	}
	tb, err := table.Open(ctx, engine.Chunks(db), engine.Prolly(db), ref.Root)
	if err != nil {
		t.Fatal(err)
	}
	row, ok, err := tb.Get(ctx, table.Key{id})
	if err != nil || !ok {
		t.Fatalf("row %d at commit %s: %v, %v", id, commit.Short(), ok, err)
	}
	return row[2].(string)
}

// N transactions committed at once on rows no other touches all land, and
// the store sees one root swap per batch, not one per transaction.
func TestConcurrentTransactionsShareAPublish(t *testing.T) {
	const n = 32
	db, blobs, _ := queueDB(t, n+1)
	txs := make([]*engine.Txn, n)
	want := map[int64]string{n + 1: "held"}
	for i := range txs {
		id := int64(i + 1)
		want[id] = fmt.Sprintf("w%d", id)
		txs[i] = queueWrite(t, db, alice, "people", map[int64]string{id: want[id]})
	}
	before := blobs.swaps.Load()
	h, held := holdTheQueue(t, db, n+1, nil)
	outs := make([]<-chan error, n)
	for i, tx := range txs {
		outs[i] = commitAsync(ctx, tx)
	}
	queued := queuedBehind(db, n)
	released(t, h, held)
	allCommitted(t, "transactions", outs)
	wantNames(t, db, want)
	most := int64(1 + (n+engine.MaxBatch-1)/engine.MaxBatch)
	if swaps := blobs.swaps.Load() - before; !queued || swaps > most {
		t.Errorf("%d transactions committed at once took %d root swaps (the %d started behind a held publish queued: %v), want at most %d: "+
			"each published alone, so a branch commits at one disk's swap rate however many sessions write to it", n+1, swaps, n, queued, most)
	}
}

// A member whose item an earlier member of its batch wrote, from the same
// snapshot, gets ErrSerialization and writes nothing, even when it wrote the
// same value (D18); the members before and after it commit in the same
// publish.
func TestAMemberThatCollidesFailsAloneAndTheRestOfItsBatchCommits(t *testing.T) {
	db, blobs, _ := queueDB(t, 5)
	a := queueWrite(t, db, alice, "people", map[int64]string{1: "a"})
	b := queueWrite(t, db, alice, "people", map[int64]string{1: "a", 4: "b"}) // the same value: only the item rule refuses it
	c := queueWrite(t, db, alice, "people", map[int64]string{2: "c"})
	before := blobs.swaps.Load()
	h, held := holdTheQueue(t, db, 5, nil)
	outs, queued := enqueueInOrder(db, committing(ctx, a), committing(ctx, b), committing(ctx, c))
	released(t, h, held)
	if err := <-outs[0]; err != nil {
		t.Errorf("the first writer of row 1 in the batch = %v, want it committed", err)
	}
	if err := <-outs[1]; !errors.Is(err, engine.ErrSerialization) {
		t.Errorf("a second writer of row 1, from the same snapshot, in the same batch = %v, want ErrSerialization: two read-modify-writes of one row would both commit and one would be lost", err)
	}
	if err := <-outs[2]; err != nil {
		t.Errorf("a member writing another row, after the one that failed = %v, want it committed: a collision fails its own transaction alone", err)
	}
	wantNames(t, db, map[int64]string{1: "a", 2: "c", 4: "p4", 5: "held"})
	if swaps := blobs.swaps.Load() - before; !queued || swaps != 2 {
		t.Errorf("the held commit and a batch of three took %d root swaps (queued: %v), want 2: one for the held commit, one for the batch", swaps, queued)
	}
}

// A member is checked against its own snapshot: what landed after it began,
// in an earlier publish, is a collision for it (D18), and a member that
// began after that landed writes the same row and commits, in the same
// batch; a member with the old snapshot writing another row commits too.
func TestAMemberWithAnOlderSnapshotSeesWhatLandedSince(t *testing.T) {
	db, blobs, _ := queueDB(t, 5)
	old := queueWrite(t, db, alice, "people", map[int64]string{3: "old"})
	older := queueWrite(t, db, alice, "people", map[int64]string{2: "older"})
	if err := queueWrite(t, db, alice, "people", map[int64]string{3: "landed"}).Commit(ctx); err != nil {
		t.Fatalf("the commit landing after they began: %v", err)
	}
	fresh := queueWrite(t, db, alice, "people", map[int64]string{3: "fresh"})
	before := blobs.swaps.Load()
	h, held := holdTheQueue(t, db, 5, nil)
	outs, queued := enqueueInOrder(db, committing(ctx, old), committing(ctx, fresh), committing(ctx, older))
	released(t, h, held)
	if err := <-outs[0]; !errors.Is(err, engine.ErrSerialization) {
		t.Errorf("a member writing row 3, which changed after it began = %v, want ErrSerialization: its write would replace one it never read", err)
	}
	if err := <-outs[1]; err != nil {
		t.Errorf("a member writing row 3 from a snapshot that holds the change = %v, want it committed", err)
	}
	if err := <-outs[2]; err != nil {
		t.Errorf("a member with the old snapshot writing row 2 = %v, want it committed: its snapshot is older, its row untouched", err)
	}
	wantNames(t, db, map[int64]string{2: "older", 3: "fresh", 5: "held"})
	if swaps := blobs.swaps.Load() - before; !queued || swaps != 2 {
		t.Errorf("the held commit and a batch of three took %d root swaps (queued: %v), want 2", swaps, queued)
	}
}

// A batch whose swap is lost to another process rebuilds from the new root:
// a member that collides with what the other process wrote fails, the rest
// commit in one publish, and nothing the other process wrote is lost or
// written twice.
func TestALostSwapRebuildsTheBatchFromTheNewRoot(t *testing.T) {
	db, blobs, o := queueDB(t, 6)
	other, err := engine.Open(ctx, o) // another process on the same backend
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	a := queueWrite(t, db, alice, "people", map[int64]string{1: "a"})
	b := queueWrite(t, db, alice, "people", map[int64]string{2: "b"})
	outsider := queueWrite(t, other, alice, "people", map[int64]string{1: "out", 5: "out"})
	var outsiderErr error
	before := blobs.swaps.Load()
	h, held := holdTheQueue(t, db, 6, func(publish int) {
		if publish == 2 { // the batch of a and b has been built: the other process lands first
			outsiderErr = outsider.Commit(ctx)
		}
	})
	outs, queued := enqueueInOrder(db, committing(ctx, a), committing(ctx, b))
	released(t, h, held)
	errA, errB := <-outs[0], <-outs[1]
	if outsiderErr != nil {
		t.Fatalf("the other process's commit: %v", outsiderErr)
	}
	if !errors.Is(errA, engine.ErrSerialization) {
		t.Errorf("a member writing row 1, which the other process wrote before the batch could land = %v, want ErrSerialization", errA)
	}
	if errB != nil {
		t.Errorf("a member writing row 2, untouched by the other process = %v, want it committed after the rebuild", errB)
	}
	wantNames(t, db, map[int64]string{1: "out", 2: "b", 5: "out", 6: "held"})
	if got, want := h.sizes(), []int{1, 2, 1}; !queued || !slices.Equal(got, want) {
		t.Errorf("publishes carried %v transactions (queued: %v), want %v: the held one, the batch of two whose swap was lost, the batch rebuilt on the other process's root without the member it collides with", got, queued, want)
	}
	if swaps := blobs.swaps.Load() - before; swaps != 3 {
		t.Errorf("%d root swaps landed, want 3: the held commit, the other process's, and the rebuilt batch once", swaps)
	}
}

// A session's commit and merge wait their turn with the transactions on
// their branch: the commit records exactly the transactions queued before
// it, the merge commit exactly those queued before it, and a transaction
// queued after either is in neither.
func TestSessionCommitsAndMergesTakeTheirTurnWithTransactions(t *testing.T) {
	db, _, _ := queueDB(t, 8)
	s := txnSession(t, db, "main")
	if _, err := s.Commit(ctx, "the fixture"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranch(ctx, "feature", ""); err != nil {
		t.Fatal(err)
	}
	fs := txnSession(t, db, "feature")
	ft := txnBegin(t, fs)
	if err := txnTable(t, ft, "people").Update(ctx, engine.Key{int64(7)}, txnPerson(7, "f7", 7)); err != nil {
		t.Fatal(err)
	}
	if err := ft.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Commit(ctx, "feature"); err != nil {
		t.Fatal(err)
	}
	var tx [5]*engine.Txn
	for id := int64(1); id <= 4; id++ {
		tx[id] = queueWrite(t, db, alice, "people", map[int64]string{id: fmt.Sprintf("t%d", id)})
	}
	cs, ms := txnSession(t, db, "main"), txnSession(t, db, "main")
	var commit engine.Hash
	var merged engine.MergeResult
	h, held := holdTheQueue(t, db, 8, nil)
	outs, queued := enqueueInOrder(db,
		committing(ctx, tx[1]), committing(ctx, tx[2]),
		func() <-chan error {
			out := make(chan error, 1)
			go func() {
				var err error
				commit, err = cs.Commit(ctx, "after t1 and t2")
				out <- err
			}()
			return out
		},
		committing(ctx, tx[3]),
		func() <-chan error {
			out := make(chan error, 1)
			go func() {
				var err error
				merged, err = ms.Merge(ctx, "feature", "feature, after t3")
				out <- err
			}()
			return out
		},
		committing(ctx, tx[4]))
	released(t, h, held)
	for i, out := range outs {
		if err := <-out; err != nil {
			t.Fatalf("call %d of 6 in the queue: %v", i+1, err)
		}
	}
	if len(merged.Conflicts) > 0 {
		t.Fatalf("the merge of feature conflicts: %v", merged.Conflicts)
	}
	if !queued {
		t.Error("the calls did not all wait behind the held publish: nothing orders them")
	}
	for id, want := range map[int64]string{8: "held", 1: "t1", 2: "t2", 3: "p3", 4: "p4", 7: "p7"} {
		if got := rowAt(t, db, commit, id); got != want {
			t.Errorf("the session commit queued after t1 and t2 recorded row %d as %q, want %q: a commit records what committed before it and nothing after", id, got, want)
		}
	}
	for id, want := range map[int64]string{8: "held", 1: "t1", 2: "t2", 3: "t3", 4: "p4", 7: "f7"} {
		if got := rowAt(t, db, merged.Commit, id); got != want {
			t.Errorf("the merge commit queued after t3 recorded row %d as %q, want %q", id, got, want)
		}
	}
	wantNames(t, db, map[int64]string{4: "t4", 7: "f7"})
}

// A commit whose caller cancels while it waits in the queue leaves it with
// the caller's error and writes nothing; the commits before and after it
// commit, in one publish.
func TestACommitCancelledWhileQueuedLeavesAndTheOthersCommit(t *testing.T) {
	db, blobs, _ := queueDB(t, 4)
	a := queueWrite(t, db, alice, "people", map[int64]string{1: "a"})
	b := queueWrite(t, db, alice, "people", map[int64]string{2: "b"})
	c := queueWrite(t, db, alice, "people", map[int64]string{3: "c"})
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	before := blobs.swaps.Load()
	h, held := holdTheQueue(t, db, 4, nil)
	outs, queued := enqueueInOrder(db, committing(ctx, a), committing(cctx, b), committing(ctx, c))
	cancel()
	select {
	case err := <-outs[1]:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("a commit whose caller cancelled while it waited = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a commit whose caller cancelled while it waited is still waiting")
	}
	if got := engine.Queued(db, "main"); got != 2 {
		t.Errorf("%d commits wait after one of three left, want 2: the cancelled one kept its place", got)
	}
	released(t, h, held)
	allCommitted(t, "commits queued around the cancelled one", []<-chan error{outs[0], outs[2]})
	wantNames(t, db, map[int64]string{1: "a", 2: "p2", 3: "c", 4: "held"})
	if swaps := blobs.swaps.Load() - before; !queued || swaps != 2 {
		t.Errorf("the held commit and the two left took %d root swaps (queued: %v), want 2", swaps, queued)
	}
}

// A commit whose caller cancels once a leader has taken it into a publish
// learns the publish's outcome: it does not return early with the
// context's error while its change is being published.
func TestACommitCancelledOnceItsPublishIsUnderWayLearnsItsOutcome(t *testing.T) {
	db, _, _ := queueDB(t, 3)
	x := queueWrite(t, db, alice, "people", map[int64]string{1: "x"}) // leads the batch
	a := queueWrite(t, db, alice, "people", map[int64]string{2: "a"}) // waits in it
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	var out <-chan error
	early := make(chan error, 1)
	h, held := holdTheQueue(t, db, 3, func(publish int) {
		if publish != 2 { // the batch of x and a, a taken into it
			return
		}
		cancel()
		select {
		case err := <-out:
			early <- err
		case <-time.After(200 * time.Millisecond): // a waits for its outcome, which cannot come before this publish's swap
		}
	})
	outs, queued := enqueueInOrder(db, committing(ctx, x), committing(actx, a))
	out = outs[1]
	released(t, h, held)
	select {
	case err := <-early:
		t.Fatalf("a commit whose caller cancelled mid-publish returned %v before the publish finished: the caller is told it failed while its change lands", err)
	default:
	}
	if err := <-outs[0]; err != nil {
		t.Errorf("the leader's commit = %v, want nil", err)
	}
	if err := <-out; err != nil || !queued {
		t.Errorf("a commit whose caller cancelled after its publish began = %v (queued: %v), want nil: the publish landed it", err, queued)
	}
	wantNames(t, db, map[int64]string{1: "x", 2: "a", 3: "held"})
}

// No publish carries more than MaxBatch transactions: a queue longer than
// that drains in batches of MaxBatch, in order, and everything commits.
func TestAPublishCarriesAtMostMaxBatchTransactions(t *testing.T) {
	n := 2*engine.MaxBatch + 3
	db, blobs, _ := queueDB(t, n+1)
	want := map[int64]string{int64(n + 1): "held"}
	txs := make([]*engine.Txn, n)
	for i := range txs {
		id := int64(i + 1)
		want[id] = fmt.Sprintf("w%d", id)
		txs[i] = queueWrite(t, db, alice, "people", map[int64]string{id: want[id]})
	}
	before := blobs.swaps.Load()
	h, held := holdTheQueue(t, db, int64(n+1), nil)
	outs := make([]<-chan error, n)
	for i, tx := range txs {
		outs[i] = commitAsync(ctx, tx)
	}
	queued := queuedBehind(db, n)
	released(t, h, held)
	allCommitted(t, "transactions", outs)
	wantNames(t, db, want)
	wantSizes := []int{1, engine.MaxBatch, engine.MaxBatch, 3}
	if got := h.sizes(); !queued || !slices.Equal(got, wantSizes) {
		t.Errorf("publishes carried %v transactions (the %d queued: %v), want %v: a batch is bounded, or one publish's merges hold every caller in it", got, n, queued, wantSizes)
	}
	if swaps := blobs.swaps.Load() - before; swaps != int64(len(wantSizes)) {
		t.Errorf("%d root swaps landed, want %d, one a publish", swaps, len(wantSizes))
	}
}

// A member whose principal may not write what it changed fails alone with
// ErrPermissionDenied and writes nothing, though another member of its
// batch, allowed to, writes the same object; the rest publish together.
func TestAMemberDeniedWriteFailsAloneInItsBatch(t *testing.T) {
	db, g := grantDB(t)
	grantOK(t, g.Grant("user:bob", engine.BranchScope("main"), engine.PermRead))
	grantOK(t, g.Grant("user:bob", engine.ObjectScope("main", "people"), engine.PermWrite))
	setup := txnBegin(t, txnSession(t, db, "main"))
	if _, err := txnTable(t, setup, "pets").Insert(ctx, txnPerson(2, "tom", 4)); err != nil {
		t.Fatal(err)
	}
	if err := setup.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	alicePets := queueWrite(t, db, alice, "pets", map[int64]string{2: "tim"})
	bobPets := queueWrite(t, db, bob, "pets", map[int64]string{1: "max"})
	bobPeople := queueWrite(t, db, bob, "people", map[int64]string{2: "bea"})
	h, held := holdTheQueue(t, db, 1, nil)
	outs, queued := enqueueInOrder(db, committing(ctx, alicePets), committing(ctx, bobPets), committing(ctx, bobPeople))
	released(t, h, held)
	if err := <-outs[0]; err != nil {
		t.Errorf("alice's write to pets = %v, want it committed", err)
	}
	if err := <-outs[1]; !errors.Is(err, engine.ErrPermissionDenied) {
		t.Errorf("bob's write to pets, which he may not write, in a batch where alice writes pets = %v, want ErrPermissionDenied", err)
	}
	if err := <-outs[2]; err != nil {
		t.Errorf("bob's write to people, which he may write = %v, want it committed", err)
	}
	if got := grantRow(t, db, "main", "pets", 1); got != "rex" {
		t.Errorf("row 1 of pets is %q, want rex: a denied member rode its batch into the working set", got)
	}
	if got := grantRow(t, db, "main", "pets", 2); got != "tim" {
		t.Errorf("row 2 of pets is %q, want alice's tim", got)
	}
	if got := grantRow(t, db, "main", "people", 2); got != "bea" {
		t.Errorf("row 2 of people is %q, want bob's bea", got)
	}
	if got, want := h.sizes(), []int{1, 2}; !queued || !slices.Equal(got, want) {
		t.Errorf("publishes carried %v transactions (queued: %v), want %v: the denied member left out, the others published together", got, queued, want)
	}
}
