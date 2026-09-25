package engine_test

import (
	"errors"
	"testing"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// A publish that panics (here the seam before its swap) takes down its
// leader's call, the panic going on up to the caller that can recover it;
// every other member of the batch gets ErrInternal rather than waiting
// forever, nothing of the batch lands, and the call queued behind the batch
// and the branch's next commit both go through: the queue does not stay
// with a leader that is gone.
func TestAPublishThatPanicsAnswersEveryMemberAndFreesTheQueue(t *testing.T) {
	db, _, _ := queueDB(t, 4)
	x := queueWrite(t, db, alice, "people", map[int64]string{1: "x"}) // leads the batch
	y := queueWrite(t, db, alice, "people", map[int64]string{2: "y"}) // waits in it
	h, held := holdTheQueue(t, db, 4, func(publish int) {
		if publish == 2 {
			panic("a publish that panics")
		}
	})
	cs := txnSession(t, db, "main")
	type outcome struct {
		err       error
		recovered any
	}
	xOut := make(chan outcome, 1)
	outs, queued := enqueueInOrder(db,
		func() <-chan error {
			go func() {
				var o outcome
				defer func() {
					o.recovered = recover()
					xOut <- o
				}()
				o.err = x.Commit(ctx)
			}()
			return make(chan error) // x's outcome comes on xOut
		},
		committing(ctx, y),
		func() <-chan error { // queued behind the batch: it must be handed the queue
			out := make(chan error, 1)
			go func() {
				_, err := cs.Commit(ctx, "after the panic")
				out <- err
			}()
			return out
		})
	released(t, h, held)
	select {
	case o := <-xOut:
		if o.recovered == nil {
			t.Errorf("the leader's commit returned %v from a publish that panicked, want the panic to reach its caller: a panic swallowed hides the fault", o.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the leader's commit never returned from a publish that panicked")
	}
	select {
	case err := <-outs[1]:
		if !errors.Is(err, engine.ErrInternal) {
			t.Errorf("a member of a batch whose publish panicked = %v, want ErrInternal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a member of a batch whose publish panicked is still waiting for its outcome")
	}
	select {
	case err := <-outs[2]:
		if err != nil {
			t.Errorf("a session commit queued behind the batch that panicked = %v, want it committed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a session commit queued behind a batch that panicked never ran: the queue is held by a leader that is gone")
	}
	wantNames(t, db, map[int64]string{1: "p1", 2: "p2", 4: "held"})
	z := queueWrite(t, db, alice, "people", map[int64]string{3: "z"})
	done := commitAsync(ctx, z)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the branch's next commit after a publish panicked = %v, want it committed", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the branch's next commit after a publish panicked never returned: the queue is held by a leader that is gone")
	}
	wantNames(t, db, map[int64]string{1: "p1", 2: "p2", 3: "z", 4: "held"})
	if !queued {
		t.Error("the batch did not queue behind the held publish")
	}
}
