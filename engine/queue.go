package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
	"github.com/SmithOperatingSolutions/snapshot-core/core/object"
	"github.com/SmithOperatingSolutions/snapshot-core/core/vcs"
)

// Group commit (DESIGN D20). Every commit to a branch of one Database goes
// through that branch's queue: a transaction's commit, a session's commit
// and a session's merge. The call at the head leads: it takes the
// transactions waiting at the front, up to maxBatch of them, applies each to
// the working set in order, each checked against its own snapshot as a
// commit alone would be (the item rule, D18, and the models' merge), and
// publishes what they made with one root swap. A member that fails, fails
// alone; every member learns its own outcome. A session's commit or merge
// is a batch of its own, run by its own caller when its turn comes, so it
// records exactly what committed before it. The queue orders the calls of
// one process; another process's commits meet a batch at its swap, which,
// lost, rebuilds the batch from the new root.

// maxBatch is the most transactions one publish carries: past it, a
// publish's merges (a few milliseconds each) would hold every caller in
// it longer than the swap they share saves.
const maxBatch = 64

// queued is one call waiting its turn on a branch: a transaction's commit
// (tx, with ours, the namespace it made) or a session's commit or merge
// (op).
type queued struct {
	ctx  context.Context
	tx   *Txn
	ours *object.Namespace
	op   func(ctx context.Context) error

	lead chan struct{} // receives once: the call is at the head and leads
	done chan struct{} // closed once err is the call's outcome
	err  error

	taken   bool // guarded by Database.qmu: a leader holds it; it will learn its outcome
	leading bool // guarded by Database.qmu: told to lead
}

// finish gives the call its outcome.
func (c *queued) finish(err error) {
	c.err = err
	close(c.done)
}

// branchQueue is one branch's calls waiting their turn, oldest first.
type branchQueue struct {
	waiting []*queued
	busy    bool // a leader is at work
	bypass  bool // a test seam is running mid-publish: a commit goes through alone, as another process's would
	calls   int  // calls in the queue or running: the queue is dropped at zero
}

// enqueue waits for c's turn on branch and returns its outcome. The call
// at the head leads its batch; a call whose context ends while it waits
// leaves the queue with the context's error and changes nothing.
func (d *Database) enqueue(branch string, c *queued) error {
	c.lead, c.done = make(chan struct{}, 1), make(chan struct{})
	d.qmu.Lock()
	if d.queues == nil {
		d.queues = map[string]*branchQueue{}
	}
	q := d.queues[branch]
	if q == nil {
		q = &branchQueue{}
		d.queues[branch] = q
	}
	if q.bypass {
		d.qmu.Unlock()
		d.run(q, branch, []*queued{c})
		return c.err
	}
	q.calls++
	q.waiting = append(q.waiting, c)
	q.kick()
	d.qmu.Unlock()
	defer d.leave(q, branch)
	select {
	case <-c.lead:
	case <-c.ctx.Done():
		d.qmu.Lock()
		if c.taken { // a leader has it: its outcome is on its way
			d.qmu.Unlock()
			<-c.done
			return c.err
		}
		q.waiting = slices.DeleteFunc(q.waiting, func(w *queued) bool { return w == c })
		if c.leading { // told to lead, and leaving instead: the next call leads
			q.busy = false
		}
		q.kick()
		d.qmu.Unlock()
		return c.ctx.Err()
	case <-c.done:
		return c.err
	}
	d.qmu.Lock()
	batch := q.take()
	d.qmu.Unlock()
	d.lead(q, branch, batch)
	return c.err
}

// kick tells the call at the head to lead, when no leader is at work.
// Database.qmu is held.
func (q *branchQueue) kick() {
	if q.busy || len(q.waiting) == 0 {
		return
	}
	q.busy = true
	q.waiting[0].leading = true
	q.waiting[0].lead <- struct{}{}
}

// take removes the next batch from the head: a session's call alone, or
// the transactions in a row there, at most maxBatch. Database.qmu is held.
func (q *branchQueue) take() []*queued {
	n := 1
	if q.waiting[0].tx != nil {
		for n < len(q.waiting) && n < maxBatch && q.waiting[n].tx != nil {
			n++
		}
	}
	batch := slices.Clone(q.waiting[:n])
	q.waiting = slices.Delete(q.waiting, 0, n)
	for _, c := range batch {
		c.taken = true
	}
	return batch
}

// leave drops a finished call from the queue's count, and the queue from
// the database when nothing is left in it.
func (d *Database) leave(q *branchQueue, branch string) {
	d.qmu.Lock()
	defer d.qmu.Unlock()
	q.calls--
	if q.calls == 0 && d.queues[branch] == q {
		delete(d.queues, branch)
	}
}

// lead runs batch and hands the queue to the next call. A panic in the
// run fails every member not yet told its outcome, frees the queue, and
// goes on up: a caller that recovers must not leave the branch's commits
// waiting on a leader that is gone.
func (d *Database) lead(q *branchQueue, branch string, batch []*queued) {
	defer func() {
		r := recover()
		if r != nil {
			for _, c := range batch {
				select {
				case <-c.done:
				default:
					c.finish(fmt.Errorf("%w: a commit's leader panicked", ErrInternal))
				}
			}
		}
		d.qmu.Lock()
		q.busy = false
		q.kick()
		d.qmu.Unlock()
		if r != nil {
			panic(r)
		}
	}()
	d.run(q, branch, batch)
}

// run gives every call in batch its outcome: a session's call runs under
// its own context; transactions publish together.
func (d *Database) run(q *branchQueue, branch string, batch []*queued) {
	if batch[0].tx == nil {
		c := batch[0]
		c.finish(c.op(c.ctx))
		return
	}
	errs := d.publish(q, branch, batch)
	for i, c := range batch {
		c.finish(errs[i])
	}
}

// publish applies batch's transactions to branch's working set in order
// and swaps the result in once. A swap lost to another process rebuilds
// the whole batch from the new root, up to maxCommitAttempts times; each
// member's outcome is the last attempt's.
func (d *Database) publish(q *branchQueue, branch string, batch []*queued) []error {
	ctx, cancel := batchContext(batch)
	defer cancel()
	errs := make([]error, len(batch))
	for range maxCommitAttempts {
		if !d.attempt(ctx, q, branch, batch, errs) {
			return errs
		}
	}
	for i := range errs {
		if errs[i] == nil {
			errs[i] = fmt.Errorf("%w: the working set moved under %d attempts in a row", ErrSerialization, maxCommitAttempts)
		}
	}
	return errs
}

// batchContext is what a batch's shared work runs under: the leader's
// context's values without its cancellation (one member leaving must not
// fail the others), until the latest of the members' deadlines, or none
// when a member has none.
func batchContext(batch []*queued) (context.Context, context.CancelFunc) {
	ctx := context.WithoutCancel(batch[0].ctx)
	var latest time.Time
	for _, c := range batch {
		dl, ok := c.ctx.Deadline()
		if !ok {
			return context.WithCancel(ctx)
		}
		if dl.After(latest) {
			latest = dl
		}
	}
	return context.WithDeadline(ctx, latest)
}

// attempt is one try at publishing batch: it reads the working set,
// applies each member in order (errs[i] its failure, nil once applied)
// and swaps. It reports whether the swap was lost to another writer, in
// which case nothing it applied landed.
func (d *Database) attempt(ctx context.Context, q *branchQueue, branch string, batch []*queued, errs []error) (lost bool) {
	g := &granted{}
	var lead Principal // a member allowed to read the branch: the core's calls are made as it
	for i, c := range batch {
		errs[i] = translate(auth.Check(c.ctx, d.o.Authorizer, c.tx.s.p, auth.Read, "branch:"+branch))
		if errs[i] == nil && lead.ID == "" {
			lead = c.tx.s.p
			g.allow(auth.Read, "branch:"+branch)
		}
	}
	if lead.ID == "" {
		return false
	}
	failAll := func(err error) {
		for i := range errs {
			if errs[i] == nil {
				errs[i] = err
			}
		}
	}
	cur, err := d.r.WorkingSet(g.on(ctx), lead, branch)
	if err != nil {
		failAll(translate(err))
		return false
	}
	if cur.Merge != nil {
		failAll(ErrMergeInProgress)
		return false
	}
	w, err := d.r.Namespace(ctx, cur.Working)
	if err != nil {
		failAll(translate(err))
		return false
	}
	// Members are applied in runs (D23): a run takes members one after
	// another, checking each against the working set it began on and the
	// members taken before it, and writes every object the run touched
	// once when it closes. A member the run declines closes it and is
	// merged onto what the run made, on its own, as a commit alone is.
	applied := 0
	r := d.newRun(branch, g, w)
	closeRun := func() {
		next, err := r.close(ctx)
		if err != nil {
			for _, i := range r.members {
				errs[i] = err
			}
			return
		}
		applied += len(r.members)
		w = next
	}
	for i, c := range batch {
		if errs[i] != nil {
			continue
		}
		err := r.take(c.ctx, i, c)
		if err == nil {
			continue
		}
		if !errors.Is(err, errDeclined) {
			errs[i] = err
			continue
		}
		closeRun()
		next, err := c.tx.onto(c.ctx, c.ours, w)
		if err == nil {
			err = d.mayWrite(c.ctx, c.tx.s.p, branch, w, next, g)
		}
		if err != nil {
			errs[i] = err
		} else {
			w = next
			applied++
		}
		r = d.newRun(branch, g, w) // the next run begins on what the merge made
	}
	closeRun()
	if applied == 0 || w.Root() == cur.Working {
		return false // nothing to publish: the members applied changed nothing
	}
	d.beforeSwap(q, batch, errs)
	if d.beforePublish != nil {
		d.beforePublish(applied)
	}
	next := cur
	next.Working, next.Staged = w.Root(), w.Root() // the engine keeps no staging area
	_, err = d.r.UpdateWorkingSet(g.on(ctx), lead, branch, cur, next)
	if errors.Is(err, vcs.ErrConflict) {
		return true
	}
	if err != nil {
		failAll(translate(err))
	}
	return false
}

// beforeSwap runs the applied members' test seams with the queue open: a
// commit a seam makes goes through alone, as another process's would.
func (d *Database) beforeSwap(q *branchQueue, batch []*queued, errs []error) {
	for i, c := range batch {
		if errs[i] != nil || c.tx.beforeSwap == nil {
			continue
		}
		d.qmu.Lock()
		q.bypass = true
		d.qmu.Unlock()
		c.tx.beforeSwap()
		d.qmu.Lock()
		q.bypass = false
		d.qmu.Unlock()
	}
}

// mayWrite asks whether p may write branch and every object that differs
// between from and to: the questions the core asks a commit's principal at
// its swap. Each one allowed is recorded in g, so the batch's swap, made
// as one member, is allowed exactly what its members were.
func (d *Database) mayWrite(ctx context.Context, p Principal, branch string, from, to *object.Namespace, g *granted) error {
	if err := auth.Check(ctx, d.o.Authorizer, p, auth.Write, "branch:"+branch); err != nil {
		return translate(err)
	}
	it, err := object.Diff(ctx, from, to)
	if err != nil {
		return translate(err)
	}
	var paths []string
	for {
		c, ok, err := it.Next()
		if err != nil {
			return translate(err)
		}
		if !ok {
			break
		}
		paths = append(paths, c.Path)
	}
	return d.mayWritePaths(ctx, p, branch, paths, g)
}

// mayWritePaths is mayWrite for objects named by path: the ones a member
// changed, by its own diff (a run materializes no namespace per member).
func (d *Database) mayWritePaths(ctx context.Context, p Principal, branch string, paths []string, g *granted) error {
	if err := auth.Check(ctx, d.o.Authorizer, p, auth.Write, "branch:"+branch); err != nil {
		return translate(err)
	}
	for _, path := range paths {
		if err := auth.Check(ctx, d.o.Authorizer, p, auth.Write, "path:"+branch+":"+path); err != nil {
			return translate(err)
		}
	}
	g.allow(auth.Write, "branch:"+branch)
	for _, path := range paths {
		g.allow(auth.Write, "path:"+branch+":"+path)
	}
	return nil
}

// queued is how many calls wait in branch's commit queue, the ones a
// leader holds aside.
func (d *Database) queued(branch string) int {
	d.qmu.Lock()
	defer d.qmu.Unlock()
	if q := d.queues[branch]; q != nil {
		return len(q.waiting)
	}
	return 0
}

// granted is the questions a batch's members were each allowed: the
// batch's own calls to the core, made as one member, are allowed those
// and asked of the database's Authorizer otherwise.
type granted struct{ allowed map[question]bool }

type question struct {
	a        Action
	resource string
}

func (g *granted) allow(a Action, resource string) {
	if g.allowed == nil {
		g.allowed = map[question]bool{}
	}
	g.allowed[question{a, resource}] = true
}

type grantedKey struct{}

// on is ctx carrying g to the database's authorizer.
func (g *granted) on(ctx context.Context) context.Context {
	return context.WithValue(ctx, grantedKey{}, g)
}

// batchAuthorizer is the authorizer the core is given: the database's,
// except that a batch's own call is allowed what its members were (the
// context it carries says so, and only the engine can make one).
type batchAuthorizer struct{ inner Authorizer }

func (b batchAuthorizer) Authorize(ctx context.Context, p Principal, a Action, resource string) error {
	if g, ok := ctx.Value(grantedKey{}).(*granted); ok && g.allowed[question{a, resource}] {
		return nil
	}
	if b.inner == nil {
		return auth.ErrDenied
	}
	return b.inner.Authorize(ctx, p, a, resource)
}
