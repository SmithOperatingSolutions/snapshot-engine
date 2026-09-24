package kv_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

func write(t *testing.T, s *memstore.Store, entries map[string]kv.Value) model.Root {
	t.Helper()
	r, err := kv.Write(ctx, s, cfg(), entries)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func with(base map[string]kv.Value, edits map[string]*kv.Value) map[string]kv.Value {
	out := map[string]kv.Value{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range edits {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = *v
		}
	}
	return out
}

func val(s string) *kv.Value { v := bytesValue(s); return &v }

// Merge is three-way per key: a key changed on one side takes that side's
// value; set to the same value on both is clean; set to different values
// on both is a conflict at that key alone, the rest of the object merging;
// deleted on one side and changed on the other is a conflict; deleted on
// both is a deletion; the merged object holds every key both sides agree
// on and is written through the store.
func TestMergeIsThreeWayPerKey(t *testing.T) {
	s := memstore.New()
	m := kv.Model{Config: cfg()}
	base := map[string]kv.Value{"keep": bytesValue("k"), "ours-only": bytesValue("o0"), "theirs-only": bytesValue("t0"),
		"both-same": bytesValue("s0"), "both-differ": bytesValue("d0"), "del-vs-edit": bytesValue("e0"), "del-both": bytesValue("x")}
	b := write(t, s, base)
	ours := write(t, s, with(base, map[string]*kv.Value{"ours-only": val("o1"), "both-same": val("s1"), "both-differ": val("d-ours"),
		"del-vs-edit": nil, "del-both": nil, "added-ours": val("a")}))
	theirs := write(t, s, with(base, map[string]*kv.Value{"theirs-only": val("t1"), "both-same": val("s1"), "both-differ": val("d-theirs"),
		"del-vs-edit": val("e1"), "del-both": nil, "added-theirs": val("b")}))
	res, err := m.Merge(ctx, b, ours, theirs, s)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	locs := map[string]string{}
	for _, c := range res.Conflicts {
		locs[string(c.Location)] = c.Reason
	}
	if len(res.Conflicts) != 2 || locs["both-differ"] == "" || locs["del-vs-edit"] == "" {
		t.Fatalf("conflicts at %v, want exactly both-differ and del-vs-edit, each with a reason", locs)
	}
	// Without the two conflicting keys, the same sides merge clean into
	// what both agree on.
	clean := func(entries map[string]kv.Value) map[string]kv.Value {
		return with(entries, map[string]*kv.Value{"both-differ": nil, "del-vs-edit": nil})
	}
	b2, o2, t2 := write(t, s, clean(base)), write(t, s, clean(with(base, map[string]*kv.Value{"ours-only": val("o1"), "both-same": val("s1"), "del-both": nil, "added-ours": val("a")}))),
		write(t, s, clean(with(base, map[string]*kv.Value{"theirs-only": val("t1"), "both-same": val("s1"), "del-both": nil, "added-theirs": val("b")})))
	res, err = m.Merge(ctx, b2, o2, t2, s)
	if err != nil {
		t.Fatalf("Merge (clean): %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("a clean merge reported conflicts %v", res.Conflicts)
	}
	got, err := kv.Read(ctx, s, cfg(), res.Root)
	if err != nil {
		t.Fatalf("the merged object does not read: %v", err)
	}
	want := map[string]kv.Value{"keep": bytesValue("k"), "ours-only": bytesValue("o1"), "theirs-only": bytesValue("t1"),
		"both-same": bytesValue("s1"), "added-ours": bytesValue("a"), "added-theirs": bytesValue("b")}
	if !equal(got, want) {
		t.Fatalf("merged to %d entries %v, want %v", len(got), keysOf(got), keysOf(want))
	}
	if res.Root.Size != uint64(len(want)) || res.Root.Format != kv.Format {
		t.Fatalf("the merged root claims %d entries and format %d", res.Root.Size, res.Root.Format)
	}
}

func keysOf(m map[string]kv.Value) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// What theirs changed is checked before it lands in the merged object: a
// side holding a frame that is not a value is refused, not copied.
func TestMergeAppliesOnlyValuesOfOurs(t *testing.T) {
	s := memstore.New()
	m := kv.Model{Config: cfg()}
	base := write(t, s, map[string]kv.Value{"a": bytesValue("1")})
	ours := write(t, s, map[string]kv.Value{"a": bytesValue("1"), "b": bytesValue("2")})
	// theirs, built by hand, sets c to a frame of no kind.
	pm, err := prolly.Open(ctx, s, cfg(), base.Hash)
	if err != nil {
		t.Fatal(err)
	}
	e := pm.Editor()
	if err := e.Put([]byte("c"), []byte{0x7f, 'x'}); err != nil {
		t.Fatal(err)
	}
	if pm, err = e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	theirs := model.Root{Hash: pm.Root(), Size: 2, Format: kv.Format}
	if _, err := m.Merge(ctx, base, ours, theirs, s); !errors.Is(err, kv.ErrValue) {
		t.Fatalf("merging a side holding a frame of no kind: %v, want ErrValue", err)
	}
}

// The two cases the main merge test leaves out: a key theirs deleted and
// ours kept is gone from the merged object, and a key both sides added with
// different values is a conflict that says so. And every side is held to
// its root's claims: a base of another format is refused.
func TestMergeDeletesForTheirsAndNamesAnAddedTwiceKey(t *testing.T) {
	s := memstore.New()
	m := kv.Model{Config: cfg()}
	base := write(t, s, map[string]kv.Value{"keep": bytesValue("k"), "gone": bytesValue("g")})
	ours := write(t, s, map[string]kv.Value{"keep": bytesValue("k"), "gone": bytesValue("g"), "new": bytesValue("ours")})
	theirs := write(t, s, map[string]kv.Value{"keep": bytesValue("k"), "new": bytesValue("theirs")})
	res, err := m.Merge(ctx, base, ours, theirs, s)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.Conflicts) != 1 || string(res.Conflicts[0].Location) != "new" || !strings.Contains(res.Conflicts[0].Reason, "added") {
		t.Fatalf("conflicts %+v, want one at new saying both sides added it", res.Conflicts)
	}
	theirs = write(t, s, map[string]kv.Value{"keep": bytesValue("k")})
	ours = write(t, s, map[string]kv.Value{"keep": bytesValue("k"), "gone": bytesValue("g"), "mine": bytesValue("m")})
	res, err = m.Merge(ctx, base, ours, theirs, s)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("a deletion on one side conflicted: %+v", res.Conflicts)
	}
	got, err := kv.Read(ctx, s, cfg(), res.Root)
	if err != nil {
		t.Fatal(err)
	}
	if !equal(got, map[string]kv.Value{"keep": bytesValue("k"), "mine": bytesValue("m")}) {
		t.Fatalf("merged to %v, want keep and mine: the key theirs deleted must be gone", keysOf(got))
	}
	other := base
	other.Format = 2
	if _, err := m.Merge(ctx, other, ours, theirs, s); !errors.Is(err, model.ErrUnknownModel) {
		t.Fatalf("merging from a base of format 2: %v, want ErrUnknownModel", err)
	}
}
