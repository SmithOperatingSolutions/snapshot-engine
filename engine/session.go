package engine

import "context"

// Session is a principal on a branch: the version operations, and the door
// to transactions. It is not safe for concurrent use; open one per caller.
type Session struct {
	db     *Database
	p      Principal
	branch string
}

// Branch is the branch the session is on.
func (s *Session) Branch() string { return s.branch }

// Checkout moves the session to another branch. (Stub.)
func (s *Session) Checkout(ctx context.Context, branch string) error { return errNotImplemented }

// CreateBranch makes a branch at ref (the session's branch head when ref is
// empty). (Stub.)
func (s *Session) CreateBranch(ctx context.Context, name string, at Ref) error {
	return errNotImplemented
}

// DeleteBranch deletes a branch other than the session's. (Stub.)
func (s *Session) DeleteBranch(ctx context.Context, name string) error { return errNotImplemented }

// Branches lists the branches. (Stub.)
func (s *Session) Branches(ctx context.Context) ([]string, error) { return nil, errNotImplemented }

// Objects lists the objects on the session's branch, by name, with their
// kinds. (Stub.)
func (s *Session) Objects(ctx context.Context) (map[string]Kind, error) {
	return nil, errNotImplemented
}

// Commit records the branch's working set as a commit. (Stub.)
func (s *Session) Commit(ctx context.Context, message string) (Hash, error) {
	return Hash{}, errNotImplemented
}

// Merge merges from into the session's branch through the models: clean, it
// is committed; with conflicts the branch is left mid-merge. (Stub.)
func (s *Session) Merge(ctx context.Context, from Ref, message string) (MergeResult, error) {
	return MergeResult{}, errNotImplemented
}

// AbortMerge puts the branch back as it was before a merge in progress.
// (Stub.)
func (s *Session) AbortMerge(ctx context.Context) error { return errNotImplemented }

// Diff yields the changes to the object at path between from and to, in its
// model's terms. (Stub.)
func (s *Session) Diff(ctx context.Context, from, to Ref, path string) (*DiffIter, error) {
	return nil, errNotImplemented
}

// Log lists commits reachable from ref, newest first, at most limit. (Stub.)
func (s *Session) Log(ctx context.Context, ref Ref, limit int) ([]CommitMeta, error) {
	return nil, errNotImplemented
}

// Begin opens a transaction on the session's branch, reading its working
// set as of now. (Stub.)
func (s *Session) Begin(ctx context.Context) (*Txn, error) { return begin(ctx, s) }

// Close ends the session; every call after it is ErrClosed. (Stub.)
func (s *Session) Close() error { return errNotImplemented }

// DiffIter walks one object's changes.
type DiffIter struct{}

// Next is the next change; ok is false at the end. (Stub.)
func (d *DiffIter) Next(ctx context.Context) (c Change, ok bool, err error) {
	return Change{}, false, errNotImplemented
}
