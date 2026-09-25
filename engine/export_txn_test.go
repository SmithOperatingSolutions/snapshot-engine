package engine

import (
	"context"
	"errors"

	"github.com/SmithOperatingSolutions/snapshot-core/core/object"
	"github.com/SmithOperatingSolutions/snapshot-core/core/vcs"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

// MaxCommitAttempts is how many times Commit re-reads, merges and swaps
// before it gives up with ErrSerialization.
const MaxCommitAttempts = maxCommitAttempts

// SetBeforeSwap makes Commit call f after it has read the working set and
// merged, just before it swaps, on every attempt.
func SetBeforeSwap(t *Txn, f func()) { t.beforeSwap = f }

// WorkingSetHash is the hash of the session's branch's working set as
// stored: it changes when anything is written to it.
func WorkingSetHash(ctx context.Context, s *Session) (Hash, error) {
	ws, err := s.db.r.WorkingSet(ctx, s.p, s.branch)
	return ws.Hash, err
}

// PutKV writes an empty key-value map named name into the session's
// branch's working set, through the core.
func PutKV(ctx context.Context, s *Session, name string) error {
	r := s.db.r
	for {
		ws, err := r.WorkingSet(ctx, s.p, s.branch)
		if err != nil {
			return err
		}
		root, err := kv.Write(ctx, r.Chunks(), s.db.models.kv.Config, map[string]kv.Value{})
		if err != nil {
			return err
		}
		n, err := r.Namespace(ctx, ws.Working)
		if err != nil {
			return err
		}
		e := n.Editor()
		if err := e.Put(name, object.Ref{Model: kv.ID, Root: root}); err != nil {
			return err
		}
		if n, err = e.Flush(ctx); err != nil {
			return err
		}
		next := ws
		next.Working, next.Staged = n.Root(), n.Root()
		if _, err := r.UpdateWorkingSet(ctx, s.p, s.branch, ws, next); !errors.Is(err, vcs.ErrConflict) {
			return err
		}
	}
}

// CommitWorkingSet commits the session's branch's working set, through the
// core.
func CommitWorkingSet(ctx context.Context, s *Session, message string) error {
	_, err := s.db.r.CommitWorkingSet(ctx, s.p, s.branch, message)
	return err
}

// CreateBranchHere makes branch name at the session's branch's head,
// through the core.
func CreateBranchHere(ctx context.Context, s *Session, name string) error {
	head, err := s.db.r.Head(ctx, s.p, s.branch)
	if err != nil {
		return err
	}
	return s.db.r.CreateBranch(ctx, s.p, name, head.Hash)
}

// MergeBranch merges branch from into the session's branch, through the
// core, and returns how many paths conflict.
func MergeBranch(ctx context.Context, s *Session, from string) (int, error) {
	head, err := s.db.r.Head(ctx, s.p, from)
	if err != nil {
		return 0, err
	}
	res, err := s.db.r.Merge(ctx, s.p, s.branch, head.Hash)
	return len(res.Conflicts), err
}

// MaxBatch is the most transactions one publish carries.
const MaxBatch = maxBatch

// SetBeforePublish makes every publish of transactions call f with how many
// it carries, after the batch is built and just before its swap, on every
// attempt.
func SetBeforePublish(d *Database, f func(members int)) { d.beforePublish = f }

// Queued is how many calls wait in branch's commit queue, the ones a
// leader is publishing aside.
func Queued(d *Database, branch string) int { return d.queued(branch) }
