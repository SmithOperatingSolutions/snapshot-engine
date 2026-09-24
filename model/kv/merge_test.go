package kv_test

import (
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"

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
