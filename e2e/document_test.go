package e2e_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
	"github.com/SmithOperatingSolutions/snapshot-core/core/blob"
	"github.com/SmithOperatingSolutions/snapshot-core/core/blob/mem"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/object"
	"github.com/SmithOperatingSolutions/snapshot-core/core/repo"
	"github.com/SmithOperatingSolutions/snapshot-core/core/seal"
	"github.com/SmithOperatingSolutions/snapshot-core/core/vcs"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
)

// docHost is a repository opened with the document model registered, as a
// program using the engine would open it.
type docHost struct {
	t    *testing.T
	o    repo.Options
	r    *repo.Repo
	m    document.Model
	path string
}

func newDocHost(t *testing.T, bs blob.BlobStore) *docHost {
	t.Helper()
	keys, err := seal.NewKeyring()
	if err != nil {
		t.Fatal(err)
	}
	geometry := repo.DefaultGeometry()
	m := document.Model{Config: geometry.Prolly()}
	reg, err := model.NewRegistry(m)
	if err != nil {
		t.Fatal(err)
	}
	o := repo.Options{Blobs: bs, Keys: keys, Registry: reg, Authorizer: auth.AllowAll{}, Geometry: geometry}
	r, err := repo.Init(ctx, me, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return &docHost{t: t, o: o, r: r, m: m, path: "collections/users"}
}

func (h *docHost) reopen() *docHost {
	h.t.Helper()
	r, err := repo.Open(ctx, h.o)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = r.Close() })
	return &docHost{t: h.t, o: h.o, r: r, m: h.m, path: h.path}
}

// docs parses JSON records by id.
func (h *docHost) docs(pairs ...string) map[string]merge.Node {
	h.t.Helper()
	out := map[string]merge.Node{}
	for i := 0; i+1 < len(pairs); i += 2 {
		n, err := document.Parse([]byte(pairs[i+1]))
		if err != nil {
			h.t.Fatalf("Parse(%q): %v", pairs[i+1], err)
		}
		out[pairs[i]] = n
	}
	return out
}

// put writes records as a document object at h.path in branch's working set.
func (h *docHost) put(branch string, records map[string]merge.Node) {
	h.t.Helper()
	for {
		root, err := document.Write(ctx, h.r.Chunks(), h.m.Config, records)
		if err != nil {
			h.t.Fatalf("writing the document object: %v", err)
		}
		ws, err := h.r.WorkingSet(ctx, me, branch)
		if err != nil {
			h.t.Fatal(err)
		}
		n, err := h.r.Namespace(ctx, ws.Working)
		if err != nil {
			h.t.Fatal(err)
		}
		e := n.Editor()
		if err := e.Put(h.path, object.Ref{Model: document.ID, Root: root}); err != nil {
			h.t.Fatal(err)
		}
		if n, err = e.Flush(ctx); err != nil {
			h.t.Fatal(err)
		}
		next := ws
		next.Working, next.Staged = n.Root(), n.Root()
		if _, err = h.r.UpdateWorkingSet(ctx, me, branch, ws, next); !errors.Is(err, vcs.ErrConflict) {
			if err != nil {
				h.t.Fatal(err)
			}
			return
		}
	}
}

func (h *docHost) commit(branch, msg string) vcs.Commit {
	h.t.Helper()
	c, err := h.r.CommitWorkingSet(ctx, me, branch, msg)
	if err != nil {
		h.t.Fatalf("committing %q on %s: %v", msg, branch, err)
	}
	return c
}

// read returns the document object at h.path in branch's head commit.
func (h *docHost) read(branch string) map[string]merge.Node {
	h.t.Helper()
	head, err := h.r.Head(ctx, me, branch)
	if err != nil {
		h.t.Fatal(err)
	}
	n, err := h.r.Namespace(ctx, head.Namespace)
	if err != nil {
		h.t.Fatal(err)
	}
	ref, _, ok, err := n.Get(ctx, h.path)
	if err != nil {
		h.t.Fatal(err)
	}
	if !ok {
		h.t.Fatalf("no object at %s on %s", h.path, branch)
	}
	if ref.Model != document.ID {
		h.t.Fatalf("the object at %s is model %d, want document (%d)", h.path, ref.Model, document.ID)
	}
	records, err := document.Read(ctx, h.r.Chunks(), h.m.Config, ref.Root)
	if err != nil {
		h.t.Fatalf("reading the document object: %v", err)
	}
	return records
}

func sameDocs(a, b map[string]merge.Node) bool {
	if len(a) != len(b) {
		return false
	}
	for id, n := range a {
		if m, ok := b[id]; !ok || !m.Equal(n) {
			return false
		}
	}
	return true
}

// E5: a collection written through a repository and committed reads back
// from a fresh open of the same backend, record for record.
func TestADocumentObjectRoundTripsThroughARepository(t *testing.T) {
	bs := mem.New()
	h := newDocHost(t, bs)
	want := h.docs("u1", `{"name": "ada", "tags": ["math"], "age": 36}`, "u2", `{"name": "grace", "address": {"city": "Arlington"}}`, "\x00id", `[1, 2.5, null]`)
	h.put(vcs.MainBranch, want)
	h.commit(vcs.MainBranch, "users")
	got := h.reopen().read(vcs.MainBranch)
	if !sameDocs(got, want) {
		t.Fatalf("after a fresh open the collection at %s has %d records differing from the %d committed", h.path, len(got), len(want))
	}
}

// E5: two branches editing different fields of one record merge clean, and
// the merged record holds both fields.
func TestBranchesEditingDifferentFieldsOfOneRecordMerge(t *testing.T) {
	h := newDocHost(t, mem.New())
	h.put(vcs.MainBranch, h.docs("u1", `{"name": "ada", "age": 36}`))
	base := h.commit(vcs.MainBranch, "base")
	if err := h.r.CreateBranch(ctx, me, "feature", base.Hash); err != nil {
		t.Fatal(err)
	}
	h.put(vcs.MainBranch, h.docs("u1", `{"name": "ada", "age": 37}`))
	h.commit(vcs.MainBranch, "main ages ada")
	h.put("feature", h.docs("u1", `{"name": "ada lovelace", "age": 36}`))
	feature := h.commit("feature", "feature names ada")
	res, err := h.r.Merge(ctx, me, vcs.MainBranch, feature.Hash)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("edits to different fields of one record conflicted: %+v", res.Conflicts)
	}
	h.commit(vcs.MainBranch, "merge feature")
	if got, want := h.read(vcs.MainBranch), h.docs("u1", `{"name": "ada lovelace", "age": 37}`); !sameDocs(got, want) {
		t.Fatalf("after the merge u1 is %s, want both sides' fields", got["u1"].Canonical())
	}
}

// E5: one field of one record changed two ways conflicts at that record
// alone, the conflict naming the field, and no other record.
func TestOneFieldChangedTwoWaysConflictsAtThatRecordAlone(t *testing.T) {
	h := newDocHost(t, mem.New())
	h.put(vcs.MainBranch, h.docs("u1", `{"name": "ada", "age": 36}`, "u2", `{"name": "grace"}`))
	base := h.commit(vcs.MainBranch, "base")
	if err := h.r.CreateBranch(ctx, me, "feature", base.Hash); err != nil {
		t.Fatal(err)
	}
	h.put(vcs.MainBranch, h.docs("u1", `{"name": "ada", "age": 37}`, "u2", `{"name": "grace", "x": 1}`))
	h.commit(vcs.MainBranch, "main")
	h.put("feature", h.docs("u1", `{"name": "ada", "age": 38}`, "u2", `{"name": "grace", "y": 1}`))
	feature := h.commit("feature", "feature")
	res, err := h.r.Merge(ctx, me, vcs.MainBranch, feature.Hash)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].Path != h.path {
		t.Fatalf("the merge reported %d conflicts %+v, want one at %s", len(res.Conflicts), res.Conflicts, h.path)
	}
	c := res.Conflicts[0].Model
	if len(c) != 1 || string(c[0].Location) != "u1" || !strings.Contains(c[0].Reason, "age") {
		t.Fatalf("the conflict is %+v, want the one record u1 naming the field age", c)
	}
}
