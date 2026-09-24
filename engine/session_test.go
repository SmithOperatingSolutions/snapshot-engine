package engine_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/object"
	blobmodel "github.com/SmithOperatingSolutions/snapshot-core/model/blob"
	"github.com/SmithOperatingSolutions/snapshot-core/model/tree"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// sessAdmin is a principal the test authorizer always allows: it sets up
// history and inspects it, whatever alice is denied.
var sessAdmin = engine.Principal{ID: "user:admin"}

// sessGate allows every call but those it has been told to deny alice, and
// can be told again at any time, as a grant revoked mid-session is.
type sessGate struct {
	mu   sync.Mutex
	deny map[string]bool
}

func (g *sessGate) Authorize(_ context.Context, p auth.Principal, a auth.Action, resource string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if p.ID == alice.ID && g.deny[fmt.Sprintf("%d %s", a, resource)] {
		return fmt.Errorf("%w: action %d on %s", auth.ErrDenied, a, resource)
	}
	return nil
}

func (g *sessGate) set(a auth.Action, resource string, denied bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deny[fmt.Sprintf("%d %s", a, resource)] = denied
}

// sessDB is a database whose authorizer is a gate, and alice's session on
// main.
func sessDB(t *testing.T) (*engine.Database, *engine.Session, *sessGate) {
	t.Helper()
	g := &sessGate{deny: map[string]bool{}}
	o := dbOptions(t)
	o.Authorizer = g
	d, err := engine.Create(ctx, sessAdmin, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s, err := d.Session(ctx, alice, "main")
	if err != nil {
		t.Fatal(err)
	}
	return d, s, g
}

// sessKV writes a kv object of byte values at path on branch's working set.
func sessKV(t *testing.T, d *engine.Database, branch, path string, entries map[string]string) {
	t.Helper()
	vs := map[string]kv.Value{}
	for k, v := range entries {
		vs[k] = kv.Value{Kind: kv.Bytes, Bytes: []byte(v)}
	}
	root, err := kv.Write(ctx, engine.Chunks(d), engine.Prolly(d), vs)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.PutObject(ctx, d, sessAdmin, branch, path, &object.Ref{Model: kv.ID, Root: root}); err != nil {
		t.Fatal(err)
	}
}

// sessCommit commits branch's working set as the admin.
func sessCommit(t *testing.T, d *engine.Database, branch, message string) engine.Hash {
	t.Helper()
	s, err := d.Session(ctx, sessAdmin, branch)
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Commit(ctx, message)
	if err != nil {
		t.Fatalf("committing %s: %v", branch, err)
	}
	return h
}

// sessReadKV is the kv object at path in a commit, as strings.
func sessReadKV(t *testing.T, d *engine.Database, commit engine.Hash, path string) map[string]string {
	t.Helper()
	ref, ok, err := engine.ObjectAt(ctx, d, sessAdmin, commit, path)
	if err != nil || !ok {
		t.Fatalf("the object %s in commit %s: %v, %v", path, commit.Short(), ok, err)
	}
	vs, err := kv.Read(ctx, engine.Chunks(d), engine.Prolly(d), ref.Root)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for k, v := range vs {
		out[k] = string(v.Bytes)
	}
	return out
}

func sessHead(t *testing.T, d *engine.Database, branch string) engine.Hash {
	t.Helper()
	c, err := engine.HeadOf(ctx, d, sessAdmin, branch)
	if err != nil {
		t.Fatal(err)
	}
	return c.Hash
}

func sessWS(t *testing.T, d *engine.Database, branch string) engine.Hash {
	t.Helper()
	ws, err := engine.WorkingSetOf(ctx, d, sessAdmin, branch)
	if err != nil {
		t.Fatal(err)
	}
	return ws.Hash
}

func sessBranches(t *testing.T, d *engine.Database) []string {
	t.Helper()
	s, err := d.Session(ctx, sessAdmin, "main")
	if err != nil {
		t.Fatal(err)
	}
	bs, err := s.Branches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return bs
}

// E4: branches are created at a ref (the session's head, a branch, a tag
// or a commit hash, resolved in that order), listed, checked out (the
// session moves only when the branch exists) and deleted (never the
// session's own).
func TestBranchesAreCreatedCheckedOutAndDeleted(t *testing.T) {
	d, s, _ := sessDB(t)
	first := sessHead(t, d, "main")
	sessKV(t, d, "main", "config", map[string]string{"a": "1"})
	second := sessCommit(t, d, "main", "second")

	if err := s.CreateBranch(ctx, "feature", ""); err != nil {
		t.Fatalf("a branch at the session's head: %v", err)
	}
	if got := sessHead(t, d, "feature"); got != second {
		t.Errorf("a branch at an empty ref is at %s, want the session branch's head %s", got.Short(), second.Short())
	}
	if err := s.CreateBranch(ctx, "old", engine.Ref(first.String())); err != nil {
		t.Fatalf("a branch at a commit hash: %v", err)
	}
	if got := sessHead(t, d, "old"); got != first {
		t.Errorf("a branch at a commit hash is at %s, want %s", got.Short(), first.Short())
	}
	if err := engine.CreateTag(ctx, d, sessAdmin, "v1", first); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranch(ctx, "fromtag", "v1"); err != nil {
		t.Fatalf("a branch at a tag: %v", err)
	}
	if got := sessHead(t, d, "fromtag"); got != first {
		t.Errorf("a branch at tag v1 is at %s, want the tag's commit %s", got.Short(), first.Short())
	}
	// A name that is both a branch and a tag is the branch.
	if err := s.CreateBranch(ctx, "v1", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBranch(ctx, "frombranch", "v1"); err != nil {
		t.Fatal(err)
	}
	if got := sessHead(t, d, "frombranch"); got != second {
		t.Errorf("a ref naming both a branch and a tag resolved to %s, want the branch's %s", got.Short(), second.Short())
	}
	for name, tc := range map[string]struct {
		name string
		at   engine.Ref
		want error
	}{
		"an existing branch":         {"feature", "", engine.ErrExists},
		"a ref naming nothing":       {"x", "nowhere", engine.ErrNotFound},
		"a hash no commit has":       {"y", engine.Ref(strings.Repeat("ab", 32)), engine.ErrNotFound},
		"a name outside the grammar": {"bad name", "", engine.ErrInvalid},
	} {
		if err := s.CreateBranch(ctx, tc.name, tc.at); !errors.Is(err, tc.want) {
			t.Errorf("%s: CreateBranch = %v, want %v", name, err, tc.want)
		}
	}

	want := []string{"feature", "frombranch", "fromtag", "main", "old", "v1"}
	if got, err := s.Branches(ctx); err != nil || strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Branches = %v, %v; want %v", got, err, want)
	}

	if err := s.Checkout(ctx, "feature"); err != nil || s.Branch() != "feature" {
		t.Fatalf("Checkout(feature) = %v, the session on %q", err, s.Branch())
	}
	if err := s.Checkout(ctx, "nowhere"); !errors.Is(err, engine.ErrNotFound) || s.Branch() != "feature" {
		t.Errorf("Checkout(nowhere) = %v, the session on %q; want ErrNotFound and feature still", err, s.Branch())
	}
	if err := s.DeleteBranch(ctx, "feature"); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("deleting the session's own branch = %v, want ErrInvalid", err)
	}
	if err := s.DeleteBranch(ctx, "old"); err != nil {
		t.Fatalf("DeleteBranch(old): %v", err)
	}
	if err := s.DeleteBranch(ctx, "old"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("deleting a deleted branch = %v, want ErrNotFound", err)
	}
	if got := sessBranches(t, d); strings.Contains(strings.Join(got, " "), "old") {
		t.Errorf("after DeleteBranch(old) the branches are %v", got)
	}
}

// E4: a session's Commit is one VCS commit of the branch's working set, by
// the session's principal, onto the head; a commit of an unchanged working
// set is recorded too (the core's rule: a commit records the working set),
// with the same namespace; a message the core cannot hold is refused and
// nothing is committed.
func TestCommitRecordsTheWorkingSet(t *testing.T) {
	d, s, _ := sessDB(t)
	before := sessHead(t, d, "main")
	sessKV(t, d, "main", "config", map[string]string{"a": "1"})
	h, err := s.Commit(ctx, "set a")
	if err != nil {
		t.Fatal(err)
	}
	log, err := s.Log(ctx, "main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[0].Hash != h || log[0].Message != "set a" || log[0].Author != alice.ID || len(log[0].Parents) != 1 || log[0].Parents[0] != before {
		t.Fatalf("after Commit the log is %+v, want the commit %s by %s onto %s", log, h.Short(), alice.ID, before.Short())
	}
	if got := sessReadKV(t, d, h, "config"); got["a"] != "1" {
		t.Errorf("the commit holds %v, want the working set's a=1", got)
	}
	again, err := s.Commit(ctx, "nothing changed")
	if err != nil {
		t.Fatalf("a commit of an unchanged working set: %v", err)
	}
	if again == h {
		t.Error("a commit of an unchanged working set returned the previous commit: it records a commit")
	}
	if log, err = s.Log(ctx, "main", 1); err != nil || len(log[0].Parents) != 1 || log[0].Parents[0] != h {
		t.Errorf("the unchanged commit's parents are %+v, %v; want %s", log, err, h.Short())
	}
	if _, err := s.Commit(ctx, strings.Repeat("x", 64<<10+1)); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("a message over 64 KiB = %v, want ErrInvalid", err)
	}
	if _, err := s.Commit(ctx, "\xff"); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("a message that is not UTF-8 = %v, want ErrInvalid", err)
	}
	if got := sessHead(t, d, "main"); got != again {
		t.Errorf("a refused commit moved the head to %s", got.Short())
	}
}

// E4: Objects names every object on the session's branch with its kind:
// a table, a key-value map, a document collection, a file and a folder.
func TestObjectsNameEveryKind(t *testing.T) {
	d, s, _ := sessDB(t)
	if got, err := s.Objects(ctx); err != nil || len(got) != 0 {
		t.Fatalf("a new database's objects = %v, %v; want none", got, err)
	}
	put := func(path string, id model.ID, root model.Root) {
		t.Helper()
		if err := engine.PutObject(ctx, d, sessAdmin, "main", path, &object.Ref{Model: id, Root: root}); err != nil {
			t.Fatal(err)
		}
	}
	tb, err := table.Create(ctx, engine.Chunks(d), engine.Prolly(d), table.Schema{
		Columns:    []table.Column{{Tag: 1, Name: "id", Type: table.TypeInt8}},
		PrimaryKey: []table.Tag{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	put("people", table.ID, tb.Root())
	sessKV(t, d, "main", "settings", map[string]string{"k": "v"})
	dr, err := document.Write(ctx, engine.Chunks(d), engine.Prolly(d), map[string]merge.Node{"u1": merge.Obj()})
	if err != nil {
		t.Fatal(err)
	}
	put("users", document.ID, dr)
	br, err := blobmodel.Write(ctx, engine.Chunks(d), bytes.NewReader([]byte("hello")), engine.StreamConfig(d))
	if err != nil {
		t.Fatal(err)
	}
	put("readme", blobmodel.ID, br)
	tr, err := tree.Write(ctx, engine.Chunks(d), engine.Prolly(d), map[string]tree.Entry{"a.txt": {Mode: 0o644, Content: br}})
	if err != nil {
		t.Fatal(err)
	}
	put("docs", tree.ID, tr)

	got, err := s.Objects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]engine.Kind{"people": engine.KindTable, "settings": engine.KindKV, "users": engine.KindDocuments, "readme": engine.KindFile, "docs": engine.KindFolder}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Objects = %v, want %v", got, want)
	}
}

// E4: Merge merges a branch into the session's branch through the models:
// a clean merge is committed with two parents and holds both sides'
// changes; merging what the branch already holds does nothing; a
// collision leaves the branch mid-merge with the conflict at its object
// and, inside it, at the model's location (a kv key), and the branch takes
// no commit or second merge until the merge is aborted.
func TestMergeCommitsACleanMergeAndReportsConflicts(t *testing.T) {
	d, s, _ := sessDB(t)
	sessKV(t, d, "main", "config", map[string]string{"a": "1"})
	sessCommit(t, d, "main", "base")
	if err := s.CreateBranch(ctx, "feature", ""); err != nil {
		t.Fatal(err)
	}
	sessKV(t, d, "main", "config", map[string]string{"a": "1", "b": "2"})
	mainHead := sessCommit(t, d, "main", "main adds b")
	sessKV(t, d, "feature", "config", map[string]string{"a": "1", "c": "3"})
	featureHead := sessCommit(t, d, "feature", "feature adds c")

	r, err := s.Merge(ctx, "feature", "merge feature")
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(r.Conflicts) != 0 {
		t.Fatalf("a clean merge reported conflicts %+v", r.Conflicts)
	}
	log, err := s.Log(ctx, "main", 1)
	if err != nil {
		t.Fatal(err)
	}
	if log[0].Hash != r.Commit || len(log[0].Parents) != 2 || log[0].Parents[0] != mainHead || log[0].Parents[1] != featureHead || log[0].Message != "merge feature" {
		t.Fatalf("the merge commit is %+v, want %s with parents %s and %s", log[0], r.Commit.Short(), mainHead.Short(), featureHead.Short())
	}
	if got := sessReadKV(t, d, r.Commit, "config"); fmt.Sprint(got) != fmt.Sprint(map[string]string{"a": "1", "b": "2", "c": "3"}) {
		t.Errorf("the merge holds %v, want both sides' keys", got)
	}
	again, err := s.Merge(ctx, "feature", "again")
	if err != nil || len(again.Conflicts) != 0 || again.Commit != r.Commit {
		t.Errorf("merging what the branch holds = %+v, %v; want the head %s and nothing new", again, err, r.Commit.Short())
	}
	if _, err := s.Merge(ctx, "nowhere", ""); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("merging a ref naming nothing = %v, want ErrNotFound", err)
	}

	sessKV(t, d, "main", "config", map[string]string{"a": "main", "b": "2", "c": "3"})
	sessCommit(t, d, "main", "main sets a")
	sessKV(t, d, "feature", "config", map[string]string{"a": "feature", "c": "3"})
	sessCommit(t, d, "feature", "feature sets a")
	head := sessHead(t, d, "main")
	r, err = s.Merge(ctx, "feature", "collide")
	if err != nil {
		t.Fatalf("a colliding Merge: %v", err)
	}
	if len(r.Conflicts) != 1 || r.Conflicts[0].Path != "config" || r.Conflicts[0].Why == "" || len(r.Conflicts[0].Parts) != 1 {
		t.Fatalf("the colliding merge reported %+v, want one conflict at config with one part", r.Conflicts)
	}
	key, sub, err := kv.ParseLocation(r.Conflicts[0].Parts[0].Location)
	if err != nil || string(key) != "a" || len(sub) != 0 || r.Conflicts[0].Parts[0].Reason == "" {
		t.Errorf("the conflict's part is at %q (%q, %q, %v) for %q, want the key a with a reason", r.Conflicts[0].Parts[0].Location, key, sub, err, r.Conflicts[0].Parts[0].Reason)
	}
	if r.Commit != (engine.Hash{}) {
		t.Errorf("a merge with conflicts committed %s", r.Commit.Short())
	}
	if _, err := s.Commit(ctx, "too soon"); !errors.Is(err, engine.ErrMergeInProgress) {
		t.Errorf("a commit mid-merge = %v, want ErrMergeInProgress", err)
	}
	if _, err := s.Merge(ctx, "feature", "twice"); !errors.Is(err, engine.ErrMergeInProgress) {
		t.Errorf("a second merge mid-merge = %v, want ErrMergeInProgress", err)
	}
	if got := sessHead(t, d, "main"); got != head {
		t.Errorf("mid-merge the head moved to %s", got.Short())
	}
	if err := s.AbortMerge(ctx); err != nil {
		t.Fatalf("AbortMerge: %v", err)
	}
	if err := s.AbortMerge(ctx); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("aborting with no merge in progress = %v, want ErrNotFound", err)
	}
	if _, err := s.Commit(ctx, "after abort"); err != nil {
		t.Errorf("a commit after the merge was aborted: %v", err)
	}
}

// sessChanges is every change a diff yields, as "location:kind".
func sessChanges(t *testing.T, it *engine.DiffIter) []string {
	t.Helper()
	var out []string
	for {
		c, ok, err := it.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		out = append(out, fmt.Sprintf("%s:%d", c.Location, c.Kind))
	}
}

// E4: Diff yields one object's changes between two commits in its model's
// terms (kv: one per key, at the key); a ref names a commit (a branch's
// head, a tag, a hash); an object only one side holds is every part added
// or removed; an object neither holds is ErrNotFound; one of another kind
// on each side is ErrWrongKind.
func TestDiffYieldsTheModelsChanges(t *testing.T) {
	d, s, _ := sessDB(t)
	sessKV(t, d, "main", "config", map[string]string{"a": "1", "b": "2"})
	c1 := sessCommit(t, d, "main", "one")
	sessKV(t, d, "main", "config", map[string]string{"a": "9", "c": "3"})
	sessKV(t, d, "main", "fresh", map[string]string{"x": "1", "y": "2"})
	c2 := sessCommit(t, d, "main", "two")
	it, err := s.Diff(ctx, engine.Ref(c1.String()), "main", "config")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{fmt.Sprintf("a:%d", engine.Modified), fmt.Sprintf("b:%d", engine.Removed), fmt.Sprintf("c:%d", engine.Added)}
	if got := sessChanges(t, it); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Diff(c1, main, config) = %v, want %v", got, want)
	}
	for name, tc := range map[string]struct {
		from, to engine.Ref
		want     []string
	}{
		"created":  {engine.Ref(c1.String()), engine.Ref(c2.String()), []string{fmt.Sprintf("x:%d", engine.Added), fmt.Sprintf("y:%d", engine.Added)}},
		"removed":  {engine.Ref(c2.String()), engine.Ref(c1.String()), []string{fmt.Sprintf("x:%d", engine.Removed), fmt.Sprintf("y:%d", engine.Removed)}},
		"the same": {"main", engine.Ref(c2.String()), nil},
	} {
		it, err := s.Diff(ctx, tc.from, tc.to, "fresh")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := sessChanges(t, it); strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s: Diff = %v, want %v", name, got, tc.want)
		}
	}
	if _, err := s.Diff(ctx, engine.Ref(c1.String()), "main", "nothing"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("a diff of an object neither side holds = %v, want ErrNotFound", err)
	}
	if _, err := s.Diff(ctx, "nowhere", "main", "config"); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("a diff from a ref naming nothing = %v, want ErrNotFound", err)
	}
	br, err := blobmodel.Write(ctx, engine.Chunks(d), bytes.NewReader([]byte("text")), engine.StreamConfig(d))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.PutObject(ctx, d, sessAdmin, "main", "config", &object.Ref{Model: blobmodel.ID, Root: br}); err != nil {
		t.Fatal(err)
	}
	c3 := sessCommit(t, d, "main", "config becomes a file")
	if _, err := s.Diff(ctx, engine.Ref(c2.String()), engine.Ref(c3.String()), "config"); !errors.Is(err, engine.ErrWrongKind) {
		t.Errorf("a diff of an object that changed kind = %v, want ErrWrongKind", err)
	}
}

// An object only one side holds diffs as every part added, whatever its
// kind: a table's rows, a collection's records, a folder's files, a file.
func TestAnObjectOneSideHoldsIsEveryPartAdded(t *testing.T) {
	d, s, _ := sessDB(t)
	c1 := sessHead(t, d, "main")
	put := func(path string, id model.ID, root model.Root) {
		t.Helper()
		if err := engine.PutObject(ctx, d, sessAdmin, "main", path, &object.Ref{Model: id, Root: root}); err != nil {
			t.Fatal(err)
		}
	}
	tb, err := table.Create(ctx, engine.Chunks(d), engine.Prolly(d), table.Schema{
		Columns:    []table.Column{{Tag: 1, Name: "id", Type: table.TypeInt8}, {Tag: 2, Name: "name", Type: table.TypeText, Nullable: true}},
		PrimaryKey: []table.Tag{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := tb.Edit()
	for i := range 3 {
		if _, err := e.Insert(table.Row{1: int64(i), 2: fmt.Sprintf("n%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if tb, err = e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	put("people", table.ID, tb.Root())
	dr, err := document.Write(ctx, engine.Chunks(d), engine.Prolly(d), map[string]merge.Node{"u1": merge.Obj(), "u2": merge.Obj()})
	if err != nil {
		t.Fatal(err)
	}
	put("users", document.ID, dr)
	br, err := blobmodel.Write(ctx, engine.Chunks(d), bytes.NewReader([]byte("hello")), engine.StreamConfig(d))
	if err != nil {
		t.Fatal(err)
	}
	put("readme", blobmodel.ID, br)
	tr, err := tree.Write(ctx, engine.Chunks(d), engine.Prolly(d), map[string]tree.Entry{"a.txt": {Mode: 0o644, Content: br}, "b.txt": {Mode: 0o644, Content: br}})
	if err != nil {
		t.Fatal(err)
	}
	put("docs", tree.ID, tr)
	c2 := sessCommit(t, d, "main", "four objects")
	for path, parts := range map[string]int{"people": 3, "users": 2, "docs": 2, "readme": 1} {
		it, err := s.Diff(ctx, engine.Ref(c1.String()), engine.Ref(c2.String()), path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		got := sessChanges(t, it)
		if len(got) != parts {
			t.Errorf("%s created: %d changes %v, want %d", path, len(got), got, parts)
		}
		for _, c := range got {
			if !strings.HasSuffix(c, fmt.Sprintf(":%d", engine.Added)) {
				t.Errorf("%s created: a change %q that is not an addition", path, c)
			}
		}
	}
}

// E4: Log lists the commits reachable from a ref, newest first, at most
// limit; a limit outside 1..10,000 and a ref naming nothing are refused.
func TestLogListsCommitsNewestFirst(t *testing.T) {
	d, s, _ := sessDB(t)
	var hs []engine.Hash
	for i := range 3 {
		sessKV(t, d, "main", "config", map[string]string{"n": fmt.Sprint(i)})
		hs = append(hs, sessCommit(t, d, "main", fmt.Sprintf("c%d", i)))
	}
	log, err := s.Log(ctx, "main", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[0].Hash != hs[2] || log[1].Hash != hs[1] || log[0].Message != "c2" || log[0].Time == 0 {
		t.Errorf("Log(main, 2) = %+v, want c2 then c1, timed", log)
	}
	if log, err = s.Log(ctx, engine.Ref(hs[0].String()), 10); err != nil || len(log) != 2 || log[0].Hash != hs[0] {
		t.Errorf("Log from the first commit = %+v, %v; want it and the initial commit", log, err)
	}
	for _, limit := range []int{0, -1, 10_001} {
		if _, err := s.Log(ctx, "main", limit); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("Log with limit %d = %v, want ErrInvalid", limit, err)
		}
	}
	if _, err := s.Log(ctx, "nowhere", 1); !errors.Is(err, engine.ErrNotFound) {
		t.Errorf("Log from a ref naming nothing = %v, want ErrNotFound", err)
	}
}

// A closed session, and a session of a closed database, refuse every call
// with ErrClosed; closing twice is not an error.
func TestAClosedSessionRefusesEveryCall(t *testing.T) {
	d, s, _ := sessDB(t)
	calls := func(s *engine.Session) map[string]error {
		_, objects := s.Objects(ctx)
		_, branches := s.Branches(ctx)
		_, commit := s.Commit(ctx, "m")
		_, merge := s.Merge(ctx, "main", "m")
		_, diff := s.Diff(ctx, "main", "main", "x")
		_, log := s.Log(ctx, "main", 1)
		_, begin := s.Begin(ctx)
		return map[string]error{
			"Checkout": s.Checkout(ctx, "main"), "CreateBranch": s.CreateBranch(ctx, "b", ""),
			"DeleteBranch": s.DeleteBranch(ctx, "b"), "Branches": branches, "Objects": objects,
			"Commit": commit, "Merge": merge, "AbortMerge": s.AbortMerge(ctx), "Diff": diff, "Log": log, "Begin": begin,
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("closing a closed session: %v", err)
	}
	for name, err := range calls(s) {
		if !errors.Is(err, engine.ErrClosed) {
			t.Errorf("%s on a closed session = %v, want ErrClosed", name, err)
		}
	}
	open, err := d.Session(ctx, alice, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	for name, err := range calls(open) {
		if !errors.Is(err, engine.ErrClosed) {
			t.Errorf("%s on a session of a closed database = %v, want ErrClosed", name, err)
		}
	}
}

// E4: every version operation is authorized when it is made: denied the
// action it needs, alice's call is ErrPermissionDenied and nothing
// changes (the branches, the head, the working set).
func TestEachVersionOperationNeedsItsGrant(t *testing.T) {
	d, s, g := sessDB(t)
	sessKV(t, d, "main", "config", map[string]string{"a": "1"})
	sessCommit(t, d, "main", "base")
	if err := s.CreateBranch(ctx, "feature", ""); err != nil {
		t.Fatal(err)
	}
	sessKV(t, d, "feature", "config", map[string]string{"a": "2"})
	sessCommit(t, d, "feature", "feature")
	sessKV(t, d, "main", "config", map[string]string{"a": "1", "b": "1"})
	for name, tc := range map[string]struct {
		action   auth.Action
		resource string
		call     func() error
	}{
		"Checkout":     {auth.Read, "branch:feature", func() error { return s.Checkout(ctx, "feature") }},
		"CreateBranch": {auth.Manage, "branch:new", func() error { return s.CreateBranch(ctx, "new", "") }},
		"DeleteBranch": {auth.Manage, "branch:feature", func() error { return s.DeleteBranch(ctx, "feature") }},
		"Branches":     {auth.Read, "repo", func() error { _, err := s.Branches(ctx); return err }},
		"Objects":      {auth.Read, "branch:main", func() error { _, err := s.Objects(ctx); return err }},
		"Commit":       {auth.Write, "branch:main", func() error { _, err := s.Commit(ctx, "m"); return err }},
		"Merge":        {auth.Write, "branch:main", func() error { _, err := s.Merge(ctx, "feature", "m"); return err }},
		"AbortMerge":   {auth.Write, "branch:main", func() error { return s.AbortMerge(ctx) }},
		"Diff":         {auth.Read, "branch:feature", func() error { _, err := s.Diff(ctx, "main", "feature", "config"); return err }},
		"Log":          {auth.Read, "repo", func() error { _, err := s.Log(ctx, "main", 1); return err }},
	} {
		branches, head, ws := sessBranches(t, d), sessHead(t, d, "main"), sessWS(t, d, "main")
		g.set(tc.action, tc.resource, true)
		err := tc.call()
		g.set(tc.action, tc.resource, false)
		if !errors.Is(err, engine.ErrPermissionDenied) {
			t.Errorf("%s denied %d on %s = %v, want ErrPermissionDenied", name, tc.action, tc.resource, err)
		}
		if got := sessBranches(t, d); strings.Join(got, " ") != strings.Join(branches, " ") {
			t.Errorf("%s denied changed the branches to %v", name, got)
		}
		if sessHead(t, d, "main") != head || sessWS(t, d, "main") != ws {
			t.Errorf("%s denied changed main's head or working set", name)
		}
		if s.Branch() != "main" {
			t.Errorf("%s denied moved the session to %q", name, s.Branch())
		}
	}
}

// E4: a session's authorization is checked on every call, not at open: a
// grant revoked mid-session blocks the next call, and restored, the call
// after that works.
func TestARevokedGrantBlocksTheNextCall(t *testing.T) {
	_, s, g := sessDB(t)
	if _, err := s.Log(ctx, "main", 1); err != nil {
		t.Fatalf("positive control: Log with the grant: %v", err)
	}
	g.set(auth.Read, "repo", true)
	if _, err := s.Log(ctx, "main", 1); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Errorf("Log after the grant was revoked = %v, want ErrPermissionDenied", err)
	}
	g.set(auth.Read, "repo", false)
	if _, err := s.Log(ctx, "main", 1); err != nil {
		t.Errorf("Log after the grant was restored: %v", err)
	}
}

// A conflict says why its object conflicts: deleted on one side and
// changed on the other, added differently on both sides, or held by
// another kind of object on each side; each reason is its own.
func TestMergeConflictsSayWhy(t *testing.T) {
	d, s, _ := sessDB(t)
	sessKV(t, d, "main", "gone", map[string]string{"a": "1"})
	sessKV(t, d, "main", "shape", map[string]string{"a": "1"})
	sessCommit(t, d, "main", "base")
	if err := s.CreateBranch(ctx, "feature", ""); err != nil {
		t.Fatal(err)
	}
	if err := engine.PutObject(ctx, d, sessAdmin, "main", "gone", nil); err != nil {
		t.Fatal(err)
	}
	sessKV(t, d, "main", "fresh", map[string]string{"a": "main"})
	sessKV(t, d, "main", "shape", map[string]string{"a": "2"})
	sessCommit(t, d, "main", "main")
	sessKV(t, d, "feature", "gone", map[string]string{"a": "changed"})
	sessKV(t, d, "feature", "fresh", map[string]string{"a": "feature"})
	br, err := blobmodel.Write(ctx, engine.Chunks(d), bytes.NewReader([]byte("now a file")), engine.StreamConfig(d))
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.PutObject(ctx, d, sessAdmin, "feature", "shape", &object.Ref{Model: blobmodel.ID, Root: br}); err != nil {
		t.Fatal(err)
	}
	sessCommit(t, d, "feature", "feature")
	r, err := s.Merge(ctx, "feature", "m")
	if err != nil {
		t.Fatal(err)
	}
	why := map[string]string{}
	for _, c := range r.Conflicts {
		why[c.Path] = c.Why
	}
	for path, want := range map[string]string{"gone": "deleted on one side", "fresh": "added differently", "shape": "what kind of object"} {
		if !strings.Contains(why[path], want) {
			t.Errorf("the conflict at %s says %q, want it to say %q", path, why[path], want)
		}
	}
	if len(why) != 3 {
		t.Errorf("conflicts %v, want gone, fresh and shape", why)
	}
}

// A ref is resolved under the grants its kind needs, and a denied lookup
// stops there: a tag needs read on the tag, a commit hash read on the
// repository, a branch read on the branch; a merge reads the branch's
// working set first; a message the core cannot hold is refused before a
// merge begins.
func TestRefsAreResolvedUnderTheirGrants(t *testing.T) {
	d, s, g := sessDB(t)
	sessKV(t, d, "main", "config", map[string]string{"a": "1"})
	c1 := sessCommit(t, d, "main", "one")
	if err := engine.CreateTag(ctx, d, sessAdmin, "v1", c1); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		action   auth.Action
		resource string
		call     func() error
	}{
		"a tag":                 {auth.Read, "tag:v1", func() error { return s.CreateBranch(ctx, "b1", "v1") }},
		"a commit hash":         {auth.Read, "repo", func() error { return s.CreateBranch(ctx, "b2", engine.Ref(c1.String())) }},
		"a diff's commits":      {auth.Read, "repo", func() error { _, err := s.Diff(ctx, "main", "main", "config"); return err }},
		"a merge's working set": {auth.Read, "branch:main", func() error { _, err := s.Merge(ctx, "v1", "m"); return err }},
	} {
		g.set(tc.action, tc.resource, true)
		err := tc.call()
		g.set(tc.action, tc.resource, false)
		if !errors.Is(err, engine.ErrPermissionDenied) {
			t.Errorf("%s, denied %d on %s: %v, want ErrPermissionDenied", name, tc.action, tc.resource, err)
		}
	}
	if got := sessBranches(t, d); strings.Join(got, " ") != "main" {
		t.Errorf("denied lookups created branches: %v", got)
	}
	if _, err := s.Merge(ctx, "v1", "\xff"); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("a merge with a message that is not UTF-8 = %v, want ErrInvalid", err)
	}
}
