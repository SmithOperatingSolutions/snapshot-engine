package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	coremerge "github.com/SmithOperatingSolutions/snapshot-core/core/merge"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/object"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"github.com/SmithOperatingSolutions/snapshot-core/core/vcs"
	blobmodel "github.com/SmithOperatingSolutions/snapshot-core/model/blob"
	"github.com/SmithOperatingSolutions/snapshot-core/model/tree"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// Session is a principal on a branch: the version operations, and the door
// to transactions. It is not safe for concurrent use; open one per caller.
// Every call passes the session's principal to the core, which asks the
// authorizer when the call is made, so a grant revoked mid-session blocks
// the next call.
type Session struct {
	db     *Database
	p      Principal
	branch string
	closed bool
}

// Branch is the branch the session is on.
func (s *Session) Branch() string { return s.branch }

// ready refuses a call on a closed session or a session of a closed
// database.
func (s *Session) ready() error {
	if s.closed || s.db.isClosed() {
		return ErrClosed
	}
	return nil
}

// resolve is the commit a ref names: a branch's head, else a tag's commit,
// else the commit whose hash the ref spells in hex.
func (s *Session) resolve(ctx context.Context, ref Ref) (hash.Hash, error) {
	name := string(ref)
	c, err := s.db.r.Head(ctx, s.p, name)
	if err == nil {
		return c.Hash, nil
	}
	if !errors.Is(err, vcs.ErrBranchNotFound) && !errors.Is(err, vcs.ErrInvalidName) {
		return hash.Hash{}, translate(err)
	}
	t, err := s.db.r.Tag(ctx, s.p, name)
	if err == nil {
		return t.Target, nil
	}
	if !errors.Is(err, vcs.ErrTagNotFound) && !errors.Is(err, vcs.ErrInvalidName) {
		return hash.Hash{}, translate(err)
	}
	h, err := hash.Parse(name)
	if err != nil {
		return hash.Hash{}, fmt.Errorf("%w: no branch, tag or commit by that name", ErrNotFound)
	}
	if c, err = s.db.r.ReadCommit(ctx, s.p, h); err != nil {
		if errors.Is(err, chunk.ErrNotFound) {
			return hash.Hash{}, fmt.Errorf("%w: no commit by that hash", ErrNotFound)
		}
		return hash.Hash{}, translate(err)
	}
	return c.Hash, nil
}

// Checkout moves the session to another branch, which must exist and be
// readable; the session stays where it was otherwise.
func (s *Session) Checkout(ctx context.Context, branch string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if _, err := s.db.r.Head(ctx, s.p, branch); err != nil {
		return translate(err)
	}
	s.branch = branch
	return nil
}

// CreateBranch makes a branch at ref: the session branch's head when ref is
// empty, else a branch, a tag or a commit hash, resolved in that order.
func (s *Session) CreateBranch(ctx context.Context, name string, at Ref) error {
	if err := s.ready(); err != nil {
		return err
	}
	if at == "" {
		at = Ref(s.branch)
	}
	h, err := s.resolve(ctx, at)
	if err != nil {
		return err
	}
	return translate(s.db.r.CreateBranch(ctx, s.p, name, h))
}

// DeleteBranch deletes a branch other than the session's.
func (s *Session) DeleteBranch(ctx context.Context, name string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if name == s.branch {
		return fmt.Errorf("%w: a session cannot delete the branch it is on", ErrInvalid)
	}
	return translate(s.db.r.DeleteBranch(ctx, s.p, name))
}

// Branches lists the branches.
func (s *Session) Branches(ctx context.Context) ([]string, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	bs, err := s.db.r.Branches(ctx, s.p)
	return bs, translate(err)
}

// Objects lists the objects on the session's branch (its working set), by
// name, with their kinds.
func (s *Session) Objects(ctx context.Context) (map[string]Kind, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	ws, err := s.db.r.WorkingSet(ctx, s.p, s.branch)
	if err != nil {
		return nil, translate(err)
	}
	ns, err := s.db.r.Namespace(ctx, ws.Working)
	if err != nil {
		return nil, err
	}
	empty, err := object.New(ctx, s.db.scratch(), s.db.models.geometry.Prolly(), s.db.models.registry)
	if err != nil {
		return nil, err
	}
	it, err := object.Diff(ctx, empty, ns)
	if err != nil {
		return nil, err
	}
	out := map[string]Kind{}
	for {
		c, ok, err := it.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		out[c.Path] = kindOf(c.To.Model)
	}
}

// kindOf is the kind of an object of model id.
func kindOf(id model.ID) Kind {
	switch id {
	case table.ID:
		return KindTable
	case kv.ID:
		return KindKV
	case document.ID:
		return KindDocuments
	case blobmodel.ID:
		return KindFile
	case tree.ID:
		return KindFolder
	}
	return 0
}

// message refuses a commit message the core cannot hold.
func message(m string) error {
	if len(m) > vcs.MaxMessageLen || !utf8.ValidString(m) {
		return fmt.Errorf("%w: a commit message must be UTF-8 of at most %d bytes", ErrInvalid, vcs.MaxMessageLen)
	}
	return nil
}

// Commit records the branch's working set as a commit by the session's
// principal onto the branch's head. An unchanged working set is committed
// too: a commit records the working set, as the core does, whether or not
// it changed. Mid-merge, with conflicts unresolved, it is
// ErrMergeInProgress.
func (s *Session) Commit(ctx context.Context, msg string) (Hash, error) {
	if err := s.ready(); err != nil {
		return Hash{}, err
	}
	if err := message(msg); err != nil {
		return Hash{}, err
	}
	c, err := s.db.r.CommitWorkingSet(ctx, s.p, s.branch, msg)
	if err != nil {
		return Hash{}, translate(err)
	}
	return c.Hash, nil
}

// Merge merges from into the session's branch through the models: clean, it
// is committed with message, the merge commit's parents the branch's head
// and from's commit, as one call (refused or failed at the commit, the
// merge is undone); with conflicts the branch is left mid-merge (a Commit
// or another Merge is ErrMergeInProgress until AbortMerge); a commit the
// branch already holds merges to nothing and returns the head.
func (s *Session) Merge(ctx context.Context, from Ref, msg string) (MergeResult, error) {
	if err := s.ready(); err != nil {
		return MergeResult{}, err
	}
	if err := message(msg); err != nil {
		return MergeResult{}, err
	}
	ws, err := s.db.r.WorkingSet(ctx, s.p, s.branch)
	if err != nil {
		return MergeResult{}, translate(err)
	}
	if ws.Merge != nil {
		return MergeResult{}, ErrMergeInProgress
	}
	theirs, err := s.resolve(ctx, from)
	if err != nil {
		return MergeResult{}, err
	}
	res, err := s.db.r.Merge(ctx, s.p, s.branch, theirs)
	if err != nil {
		return MergeResult{}, translate(err)
	}
	if len(res.Conflicts) > 0 {
		out := MergeResult{Conflicts: make([]Conflict, 0, len(res.Conflicts))}
		for _, c := range res.Conflicts {
			out.Conflicts = append(out.Conflicts, conflictOf(c))
		}
		return out, nil
	}
	if ws, err = s.db.r.WorkingSet(ctx, s.p, s.branch); err != nil {
		return MergeResult{}, translate(err)
	}
	if ws.Merge == nil { // the branch held from already: nothing merged
		head, err := s.db.r.Head(ctx, s.p, s.branch)
		if err != nil {
			return MergeResult{}, translate(err)
		}
		return MergeResult{Commit: head.Hash}, nil
	}
	c, err := s.db.r.CommitWorkingSet(ctx, s.p, s.branch, msg)
	if err != nil { // refused or failed at the commit: the merge is undone, the branch as it was
		if aerr := s.db.r.AbortMerge(ctx, s.p, s.branch); aerr != nil {
			return MergeResult{}, errors.Join(translate(err), translate(aerr))
		}
		return MergeResult{}, translate(err)
	}
	return MergeResult{Commit: c.Hash}, nil
}

// conflictOf is a core merge conflict as a caller sees it.
func conflictOf(c coremerge.Conflict) Conflict {
	out := Conflict{Path: c.Path, Why: why(c.Kind)}
	for _, m := range c.Model {
		out.Parts = append(out.Parts, ConflictPart{Location: m.Location, Reason: m.Reason})
	}
	return out
}

// why says a conflict's kind in words.
func why(k coremerge.Kind) string {
	switch k {
	case coremerge.BothChanged:
		return "changed on both sides in ways its model could not combine"
	case coremerge.DeleteEdit:
		return "deleted on one side and changed on the other"
	case coremerge.AddAdd:
		return "added differently on both sides"
	case coremerge.ModelChange:
		return "the sides disagree about what kind of object it is"
	}
	return "the sides could not be combined"
}

// AbortMerge puts the branch back as it was before a merge in progress;
// with none in progress it is ErrNotFound.
func (s *Session) AbortMerge(ctx context.Context) error {
	if err := s.ready(); err != nil {
		return err
	}
	err := s.db.r.AbortMerge(ctx, s.p, s.branch)
	if errors.Is(err, vcs.ErrNoMerge) {
		return fmt.Errorf("%w: no merge in progress", ErrNotFound)
	}
	return translate(err)
}

// Diff yields the changes to the object at path between two commits, in
// its model's terms (a kv key, a table row then cell, a record id). A ref
// names a commit (a branch's head, a tag, a hash), never a working set. An
// object only one side holds is compared with an empty object of its
// kind, so every part of it is added or removed; one neither holds is
// ErrNotFound; one that changed kind is ErrWrongKind.
func (s *Session) Diff(ctx context.Context, from, to Ref, path string) (*DiffIter, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var refs [2]object.Ref
	var ok [2]bool
	var m model.Model
	for i, ref := range []Ref{from, to} {
		h, err := s.resolve(ctx, ref)
		if err != nil {
			return nil, err
		}
		c, err := s.db.r.ReadCommit(ctx, s.p, h)
		if err != nil {
			return nil, translate(err)
		}
		ns, err := s.db.r.Namespace(ctx, c.Namespace)
		if err != nil {
			return nil, err
		}
		var mi model.Model
		if refs[i], mi, ok[i], err = ns.Get(ctx, path); err != nil {
			return nil, translate(err)
		}
		if ok[i] {
			m = mi
		}
	}
	switch {
	case !ok[0] && !ok[1]:
		return nil, fmt.Errorf("%w: no object by that name on either side", ErrNotFound)
	case ok[0] && ok[1] && refs[0].Model != refs[1].Model:
		return nil, fmt.Errorf("%w: the object is of another kind on each side", ErrWrongKind)
	}
	sc := s.db.scratch()
	for i := range refs {
		if !ok[i] {
			empty, err := s.db.empty(ctx, sc, m, refs[1-i])
			if err != nil {
				return nil, err
			}
			refs[i] = object.Ref{Model: refs[1-i].Model, Root: empty}
		}
	}
	it, err := m.Diff(ctx, refs[0].Root, refs[1].Root, sc)
	if err != nil {
		return nil, err
	}
	d := &DiffIter{it: it}
	switch {
	case !ok[0]: // created: every part is an addition, whatever the model calls it against an empty object
		d.only = Added
	case !ok[1]:
		d.only = Removed
	}
	return d, nil
}

// empty is an empty object of like's kind, written in sc: a table of like's
// schema, a file of no bytes, a map of no entries (kv, document, folder).
func (d *Database) empty(ctx context.Context, sc chunk.ReadWriter, m model.Model, like object.Ref) (model.Root, error) {
	cfg := d.models.geometry.Prolly()
	switch like.Model {
	case table.ID:
		t, err := table.Open(ctx, sc, cfg, like.Root)
		if err != nil {
			return model.Root{}, err
		}
		e, err := table.Create(ctx, sc, cfg, t.Schema())
		if err != nil {
			return model.Root{}, err
		}
		return e.Root(), nil
	case blobmodel.ID:
		return blobmodel.Write(ctx, sc, bytes.NewReader(nil), d.models.geometry.Stream())
	}
	pm, err := prolly.Empty(ctx, sc, cfg)
	if err != nil {
		return model.Root{}, err
	}
	return model.Root{Hash: pm.Root(), Format: m.FormatVersion()}, nil
}

// Log lists commits reachable from ref, newest first, at most limit
// (1..10,000).
func (s *Session) Log(ctx context.Context, ref Ref, limit int) ([]CommitMeta, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > vcs.MaxLog {
		return nil, fmt.Errorf("%w: a log limit must be 1 to %d", ErrInvalid, vcs.MaxLog)
	}
	h, err := s.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	cs, err := s.db.r.Log(ctx, s.p, h, limit)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]CommitMeta, 0, len(cs))
	for _, c := range cs {
		out = append(out, CommitMeta{Hash: c.Hash, Parents: c.Parents, Author: c.Author, Message: c.Message, Time: c.Time.UnixNano()})
	}
	return out, nil
}

// Begin opens a transaction on the session's branch, reading its working
// set as of now.
func (s *Session) Begin(ctx context.Context) (*Txn, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return begin(ctx, s)
}

// Close ends the session; every call after it is ErrClosed. Closing twice
// is not an error.
func (s *Session) Close() error {
	s.closed = true
	return nil
}

// DiffIter walks one object's changes.
type DiffIter struct {
	it   model.DiffIter
	only ChangeKind // for an object one side holds: every change is this
}

// Next is the next change; ok is false at the end.
func (d *DiffIter) Next(ctx context.Context) (c Change, ok bool, err error) {
	mc, ok, err := d.it.Next(ctx)
	if err != nil || !ok {
		return Change{}, false, err
	}
	c = Change{Kind: changeKinds[mc.Kind], Location: mc.Location}
	if d.only != 0 {
		c.Kind = d.only
	}
	return c, true, nil
}

var changeKinds = map[model.ChangeKind]ChangeKind{model.Added: Added, model.Removed: Removed, model.Modified: Modified}

// scratch is a store that reads the database's chunks and keeps what it is
// given in memory: the empty objects and namespaces a read compares with
// are written there, and never reach the repository.
func (d *Database) scratch() chunk.ReadWriter {
	return scratch{mem: memstore.New(), base: d.r.Chunks()}
}

type scratch struct {
	mem  *memstore.Store
	base chunk.Reader
}

func (s scratch) Get(ctx context.Context, h hash.Hash) ([]byte, error) {
	b, err := s.mem.Get(ctx, h)
	if errors.Is(err, chunk.ErrNotFound) {
		return s.base.Get(ctx, h)
	}
	return b, err
}

func (s scratch) Has(ctx context.Context, hs []hash.Hash) (map[hash.Hash]bool, error) {
	out, err := s.base.Has(ctx, hs)
	if err != nil {
		return nil, err
	}
	mine, err := s.mem.Has(ctx, hs)
	for h, ok := range mine {
		out[h] = out[h] || ok
	}
	return out, err
}

func (s scratch) Put(ctx context.Context, data []byte) (hash.Hash, error) {
	return s.mem.Put(ctx, data)
}
