package engine

import (
	"context"
	"testing"
	"time"
)

type ctxKey struct{}

// A batch's shared work runs until the latest of its members' deadlines,
// with none when any member has none; it keeps the leader's values and is
// not cancelled by any member's cancellation.
func TestABatchRunsUntilItsLatestMembersDeadline(t *testing.T) {
	base := context.WithValue(context.Background(), ctxKey{}, "leader")
	now := time.Now()
	soon, cancelSoon := context.WithDeadline(base, now.Add(time.Minute))
	defer cancelSoon()
	later, cancelLater := context.WithDeadline(context.Background(), now.Add(time.Hour))
	defer cancelLater()
	mid, cancelMid := context.WithDeadline(context.Background(), now.Add(10*time.Minute))
	defer cancelMid()
	members := func(ctxs ...context.Context) []*queued {
		out := make([]*queued, len(ctxs))
		for i, c := range ctxs {
			out[i] = &queued{ctx: c}
		}
		return out
	}

	ctx, cancel := batchContext(members(soon, later, mid))
	dl, ok := ctx.Deadline()
	cancel()
	if !ok || !dl.Equal(now.Add(time.Hour)) {
		t.Errorf("a batch of members due in a minute, an hour and ten minutes runs until %v (a deadline: %v), want the hour: a later member's publish would be cut short by an earlier one's", dl.Sub(now), ok)
	}

	ctx, cancel = batchContext(members(soon, context.Background(), later))
	_, ok = ctx.Deadline()
	cancel()
	if ok {
		t.Error("a batch with a member that has no deadline has one, want none: that member's commit would fail at another's deadline")
	}

	cancelSoon() // the leader's caller gives up
	ctx, cancel = batchContext(members(soon, later))
	defer cancel()
	if err := ctx.Err(); err != nil {
		t.Errorf("the batch's work after its leader's caller cancelled = %v, want it running: one member leaving must not fail the others", err)
	}
	if got := ctx.Value(ctxKey{}); got != "leader" {
		t.Errorf("the batch's context carries %v, want the leader's values", got)
	}
}
