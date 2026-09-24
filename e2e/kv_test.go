package e2e_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
	"github.com/SmithOperatingSolutions/snapshot-core/core/blob"
	"github.com/SmithOperatingSolutions/snapshot-core/core/blob/mem"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/object"
	"github.com/SmithOperatingSolutions/snapshot-core/core/repo"
	"github.com/SmithOperatingSolutions/snapshot-core/core/seal"
	"github.com/SmithOperatingSolutions/snapshot-core/core/vcs"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

var (
	ctx = context.Background()
	me  = auth.Principal{ID: "user:me"}
)

// host is a repository opened with the kv model registered, as a program
// using the engine would open it.
type host struct {
	t    *testing.T
	o    repo.Options
	r    *repo.Repo
	kv   kv.Model
	path string
}

func newHost(t *testing.T, bs blob.BlobStore) *host {
	t.Helper()
	keys, err := seal.NewKeyring()
	if err != nil {
		t.Fatal(err)
	}
	geometry := repo.DefaultGeometry()
	m := kv.Model{Config: geometry.Prolly()}
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
	return &host{t: t, o: o, r: r, kv: m, path: "config/settings"}
}

// reopen opens the same repository again on the same backend, as a fresh
// process would.
func (h *host) reopen() *host {
	h.t.Helper()
	r, err := repo.Open(ctx, h.o)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = r.Close() })
	return &host{t: h.t, o: h.o, r: r, kv: h.kv, path: h.path}
}

// put writes entries as a kv object at h.path in branch's working set.
func (h *host) put(branch string, entries map[string]kv.Value) {
	h.t.Helper()
	for {
		root, err := kv.Write(ctx, h.r.Chunks(), h.kv.Config, entries)
		if err != nil {
			h.t.Fatalf("writing the kv object: %v", err)
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
		if err := e.Put(h.path, object.Ref{Model: kv.ID, Root: root}); err != nil {
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

func (h *host) commit(branch, msg string) vcs.Commit {
	h.t.Helper()
	c, err := h.r.CommitWorkingSet(ctx, me, branch, msg)
	if err != nil {
		h.t.Fatalf("committing %q on %s: %v", msg, branch, err)
	}
	return c
}

// read returns the kv object at h.path in branch's head commit.
func (h *host) read(branch string) map[string]kv.Value {
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
	if ref.Model != kv.ID {
		h.t.Fatalf("the object at %s is model %d, want kv (%d)", h.path, ref.Model, kv.ID)
	}
	entries, err := kv.Read(ctx, h.r.Chunks(), h.kv.Config, ref.Root)
	if err != nil {
		h.t.Fatalf("reading the kv object: %v", err)
	}
	return entries
}

func sameEntries(a, b map[string]kv.Value) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || w.Kind != v.Kind || !bytes.Equal(w.Bytes, v.Bytes) {
			return false
		}
	}
	return true
}

func settings(vals ...string) map[string]kv.Value {
	m := map[string]kv.Value{}
	for i := 0; i+1 < len(vals); i += 2 {
		m[vals[i]] = kv.Value{Kind: kv.Bytes, Bytes: []byte(vals[i+1])}
	}
	return m
}

// E0: a kv object written through a repository and committed reads back
// from a fresh open of the same backend, entry for entry, byte for byte.
func TestAKVObjectRoundTripsThroughARepository(t *testing.T) {
	bs := mem.New()
	h := newHost(t, bs)
	want := settings("theme", "dark", "lang", "en", "retries", "3", "\x00binary", "\xff\xfe")
	h.put(vcs.MainBranch, want)
	h.commit(vcs.MainBranch, "settings")
	fresh := h.reopen()
	got := fresh.read(vcs.MainBranch)
	if !sameEntries(got, want) {
		t.Fatalf("after a fresh open the object at %s has %d entries differing from the %d committed", h.path, len(got), len(want))
	}
}

// E0: two branches setting different keys of one kv object merge clean,
// and the merged object holds both sides' keys.
func TestBranchesSettingDifferentKeysMergeClean(t *testing.T) {
	h := newHost(t, mem.New())
	h.put(vcs.MainBranch, settings("a", "1", "b", "2"))
	base := h.commit(vcs.MainBranch, "base")
	if err := h.r.CreateBranch(ctx, me, "feature", base.Hash); err != nil {
		t.Fatal(err)
	}
	h.put(vcs.MainBranch, settings("a", "1", "b", "2", "c", "3"))
	h.commit(vcs.MainBranch, "main adds c")
	h.put("feature", settings("a", "1", "b", "2", "d", "4"))
	feature := h.commit("feature", "feature adds d")
	res, err := h.r.Merge(ctx, me, vcs.MainBranch, feature.Hash)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("two branches setting different keys conflicted: %+v", res.Conflicts)
	}
	h.commit(vcs.MainBranch, "merge feature")
	if got, want := h.read(vcs.MainBranch), settings("a", "1", "b", "2", "c", "3", "d", "4"); !sameEntries(got, want) {
		t.Fatalf("after the merge main holds %d entries, want a, b, c and d", len(got))
	}
}

// E0: two branches setting one key to two values conflict on that key
// alone: one conflict, at the object's path, naming the key, and no other.
func TestBranchesSettingOneKeyToTwoValuesConflictOnThatKeyAlone(t *testing.T) {
	h := newHost(t, mem.New())
	h.put(vcs.MainBranch, settings("a", "1", "b", "2"))
	base := h.commit(vcs.MainBranch, "base")
	if err := h.r.CreateBranch(ctx, me, "feature", base.Hash); err != nil {
		t.Fatal(err)
	}
	h.put(vcs.MainBranch, settings("a", "main", "b", "2", "x", "1"))
	h.commit(vcs.MainBranch, "main sets a and x")
	h.put("feature", settings("a", "feature", "b", "2", "y", "1"))
	feature := h.commit("feature", "feature sets a and y")
	res, err := h.r.Merge(ctx, me, vcs.MainBranch, feature.Hash)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].Path != h.path {
		t.Fatalf("the merge reported %d conflicts %+v, want one at %s", len(res.Conflicts), res.Conflicts, h.path)
	}
	if c := res.Conflicts[0].Model; len(c) != 1 || string(c[0].Location) != "a" {
		t.Fatalf("the conflict names keys %+v, want the one key a", c)
	}
}
