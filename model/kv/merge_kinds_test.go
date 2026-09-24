package kv_test

import (
	"errors"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

// merged writes base, ours and theirs (each the base with its edits) and
// merges them; it returns the result and the merged object's entries.
func merged(t *testing.T, base map[string]kv.Value, ours, theirs map[string]*kv.Value) (model.MergeResult, map[string]kv.Value) {
	t.Helper()
	s := memstore.New()
	m := kv.Model{Config: cfg()}
	b := write(t, s, base)
	o := write(t, s, with(base, ours))
	th := write(t, s, with(base, theirs))
	res, err := m.Merge(ctx, b, o, th, s)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	got, err := kv.Read(ctx, s, cfg(), res.Root)
	if err != nil {
		t.Fatalf("reading the merged object: %v", err)
	}
	return res, got
}

func ptr(v kv.Value) *kv.Value { return &v }

func clean(t *testing.T, res model.MergeResult, what string) {
	t.Helper()
	if len(res.Conflicts) != 0 {
		t.Fatalf("%s conflicted: %+v", what, res.Conflicts)
	}
}

// oneConflict wants exactly one conflict, at key and below it at sub (a
// hash field, a sorted-set member; "" for the key as a whole), its reason
// saying reason.
func oneConflict(t *testing.T, res model.MergeResult, key, sub, reason string) {
	t.Helper()
	if len(res.Conflicts) != 1 {
		t.Fatalf("%d conflicts %+v, want one at %q/%q", len(res.Conflicts), res.Conflicts, key, sub)
	}
	c := res.Conflicts[0]
	k, below, err := kv.ParseLocation(c.Location)
	if err != nil || string(k) != key || string(below) != sub || !strings.Contains(c.Reason, reason) {
		t.Fatalf("conflict at %q (%q/%q, %v): %q; want at %q/%q saying %q", c.Location, k, below, err, c.Reason, key, sub, reason)
	}
}

// E3: a counter incremented on two branches merges to the sum of both
// increments over the base: 10, +3 and +5, is 18.
func TestACounterIncrementedOnTwoBranchesMergesToTheSum(t *testing.T) {
	base := map[string]kv.Value{"visits": counter(10), "other": bytesValue("x")}
	res, got := merged(t, base, map[string]*kv.Value{"visits": ptr(counter(13))}, map[string]*kv.Value{"visits": ptr(counter(15))})
	clean(t, res, "two increments of one counter")
	if got["visits"].Kind != kv.Counter || got["visits"].Counter != 18 {
		t.Fatalf("visits merged to %+v, want a counter of 18: base 10 plus both sides' increments", got["visits"])
	}
	res, got = merged(t, base, map[string]*kv.Value{"visits": ptr(counter(7))}, map[string]*kv.Value{"visits": ptr(counter(7))})
	clean(t, res, "the same decrement on both sides")
	if got["visits"].Counter != 4 {
		t.Fatalf("visits decremented by 3 on both sides merged to %d, want 4: two decrements of a counter are two", got["visits"].Counter)
	}
}

// E3: a set added to on one branch and removed from on the other keeps the
// observed-remove rule: a member removed on one side stays removed unless
// the other side added it again under a new tag; an addition lands.
func TestASetAddedToAndRemovedFromKeepsTheObservedRemoveRule(t *testing.T) {
	base := map[string]kv.Value{"tags": set(member("a", 1), member("b", 2))}
	res, got := merged(t, base,
		map[string]*kv.Value{"tags": ptr(set(member("b", 2), member("c", 4)))},                 // ours removes a, adds c
		map[string]*kv.Value{"tags": ptr(set(member("a", 1), member("a", 3), member("b", 2)))}) // theirs adds a again under tag 3
	clean(t, res, "a set removed from and added to")
	if want := set(member("a", 3), member("b", 2), member("c", 4)); !sameValue(t, got["tags"], want) {
		t.Fatalf("tags merged to %+v, want a (re-added under tag 3), b and c", got["tags"])
	}
	res, got = merged(t, base,
		map[string]*kv.Value{"tags": ptr(set(member("b", 2)))},                                 // ours removes a
		map[string]*kv.Value{"tags": ptr(set(member("a", 1), member("b", 2), member("d", 5)))}) // theirs kept a as it was
	clean(t, res, "a removal beside an addition")
	if want := set(member("b", 2), member("d", 5)); !sameValue(t, got["tags"], want) {
		t.Fatalf("tags merged to %+v, want b and d: a was removed on one side and left alone on the other", got["tags"])
	}
}

// E3: a hash merges per field: fields changed on one side land, the same
// field changed differently on both is a conflict at the key naming the
// field, and a field deleted on one side and changed on the other too.
func TestAHashMergesPerField(t *testing.T) {
	base := map[string]kv.Value{"user:1": fields("name", "ann", "mail", "a@x", "age", "30")}
	res, got := merged(t, base,
		map[string]*kv.Value{"user:1": ptr(fields("name", "ann", "mail", "ann@x", "age", "30", "city", "oslo"))}, // ours changes mail, adds city
		map[string]*kv.Value{"user:1": ptr(fields("name", "ann", "mail", "a@x"))})                                // theirs deletes age
	clean(t, res, "different fields of one hash")
	if want := fields("name", "ann", "mail", "ann@x", "city", "oslo"); !sameValue(t, got["user:1"], want) {
		t.Fatalf("user:1 merged to %+v, want mail changed, city added and age gone", got["user:1"])
	}
	res, _ = merged(t, base,
		map[string]*kv.Value{"user:1": ptr(fields("name", "ann", "mail", "ours@x", "age", "30"))},
		map[string]*kv.Value{"user:1": ptr(fields("name", "ann", "mail", "theirs@x", "age", "30"))})
	oneConflict(t, res, "user:1", "mail", "changed differently")
	res, _ = merged(t, base,
		map[string]*kv.Value{"user:1": ptr(fields("name", "ann", "mail", "a@x"))},              // ours deletes age
		map[string]*kv.Value{"user:1": ptr(fields("name", "ann", "mail", "a@x", "age", "31"))}) // theirs changes it
	oneConflict(t, res, "user:1", "age", "deleted")
	res, _ = merged(t, base, // two fields changed two ways: two conflicts, one at each field
		map[string]*kv.Value{"user:1": ptr(fields("name", "o", "mail", "o@x", "age", "30"))},
		map[string]*kv.Value{"user:1": ptr(fields("name", "t", "mail", "t@x", "age", "30"))})
	at := map[string]bool{}
	for _, c := range res.Conflicts {
		k, sub, err := kv.ParseLocation(c.Location)
		if err != nil || string(k) != "user:1" {
			t.Fatalf("a field conflict at %q (%v), want below user:1", c.Location, err)
		}
		at[string(sub)] = true
	}
	if len(res.Conflicts) != 2 || !at["name"] || !at["mail"] {
		t.Fatalf("conflicts %+v, want one at name and one at mail", res.Conflicts)
	}
}

// E3: a sorted set merges per member and keeps its order by score then
// member: a member added on one side and a score changed on the other both
// land in order; a score changed differently on both sides is a conflict
// at the key naming the member; a member removed on one side and added
// again on the other is present, as in a set.
func TestASortedSetKeepsItsOrderAcrossAMerge(t *testing.T) {
	base := map[string]kv.Value{"board": zset(scored("ann", 1, 1), scored("bob", 2, 2))}
	res, got := merged(t, base,
		map[string]*kv.Value{"board": ptr(zset(scored("ann", 1, 1), scored("bob", 2, 2), scored("cid", 1.5, 3)))}, // ours adds cid
		map[string]*kv.Value{"board": ptr(zset(scored("ann", 1, 1), scored("bob", 5, 2)))})                        // theirs raises bob
	clean(t, res, "an addition beside a score change")
	want := zset(scored("ann", 1, 1), scored("cid", 1.5, 3), scored("bob", 5, 2))
	if !sameValue(t, got["board"], want) {
		t.Fatalf("board merged to %+v, want ann 1, cid 1.5, bob 5 in that order", got["board"])
	}
	if s := got["board"].Scores; len(s) != 3 || string(s[0].Member) != "ann" || string(s[1].Member) != "cid" || string(s[2].Member) != "bob" {
		t.Fatalf("board reads back in the order %+v, want by score then member", s)
	}
	res, _ = merged(t, base,
		map[string]*kv.Value{"board": ptr(zset(scored("ann", 1, 1), scored("bob", 3, 2)))},
		map[string]*kv.Value{"board": ptr(zset(scored("ann", 1, 1), scored("bob", 4, 2)))})
	oneConflict(t, res, "board", "bob", "changed differently")
	res, got = merged(t, base,
		map[string]*kv.Value{"board": ptr(zset(scored("bob", 2, 2)))},                                           // ours removes ann
		map[string]*kv.Value{"board": ptr(zset(scored("ann", 1, 1), scored("ann", 9, 7), scored("bob", 2, 2)))}) // theirs adds ann again under tag 7
	clean(t, res, "a removal beside a re-addition")
	if want := zset(scored("bob", 2, 2), scored("ann", 9, 7)); !sameValue(t, got["board"], want) {
		t.Fatalf("board merged to %+v, want bob and ann re-added under tag 7 with score 9", got["board"])
	}
}

// E3: a sequence with concurrent inserts at one position merges
// deterministically, both blocks kept in key order, and is clean; two
// different changes to one stretch are a conflict at the key.
func TestASequenceWithConcurrentInsertsMergesDeterministically(t *testing.T) {
	base := map[string]kv.Value{"queue": seq("a", "b", "c")}
	ours := map[string]*kv.Value{"queue": ptr(seq("a", "x", "b", "c"))}
	theirs := map[string]*kv.Value{"queue": ptr(seq("a", "y", "b", "c"))}
	res, got := merged(t, base, ours, theirs)
	clean(t, res, "concurrent inserts at one position")
	if want := seq("a", "x", "y", "b", "c"); !sameValue(t, got["queue"], want) {
		t.Fatalf("queue merged to %+v, want a x y b c: both inserts kept, in key order", got["queue"])
	}
	res2, got2 := merged(t, base, theirs, ours) // the sides swapped
	clean(t, res2, "concurrent inserts, sides swapped")
	if !sameValue(t, got2["queue"], got["queue"]) {
		t.Fatalf("with the sides swapped the queue merged to %+v, not the same %+v", got2["queue"], got["queue"])
	}
	res, _ = merged(t, base, map[string]*kv.Value{"queue": ptr(seq("a", "B", "c"))}, map[string]*kv.Value{"queue": ptr(seq("a", "c"))})
	oneConflict(t, res, "queue", "", "sequence")
}

// E3: a value changed to different kinds on both sides is a conflict; the
// same new kind and value on both is clean; a kind changed on one side and
// the value changed on the other is a conflict.
func TestAKindChangedOnOneSideConflicts(t *testing.T) {
	base := map[string]kv.Value{"k": bytesValue("1")}
	res, _ := merged(t, base, map[string]*kv.Value{"k": ptr(counter(1))}, map[string]*kv.Value{"k": ptr(bytesValue("2"))})
	oneConflict(t, res, "k", "", "kind")
	res, _ = merged(t, base, map[string]*kv.Value{"k": ptr(counter(1))}, map[string]*kv.Value{"k": ptr(set(member("m", 1)))})
	oneConflict(t, res, "k", "", "kind")
	res, got := merged(t, base, map[string]*kv.Value{"k": ptr(counter(1))}, map[string]*kv.Value{"k": ptr(counter(1))})
	clean(t, res, "the same new kind and value on both sides")
	if !sameValue(t, got["k"], counter(1)) {
		t.Fatalf("k merged to %+v, want the counter both sides set", got["k"])
	}
	res, _ = merged(t, base, map[string]*kv.Value{"k": ptr(counter(1))}, map[string]*kv.Value{"k": ptr(counter(2))})
	oneConflict(t, res, "k", "", "differently")
}

// A stored value that does not decode is a broken object, not a
// disagreement: met mid-merge on a key both sides changed, the merge fails
// with ErrValue and nothing is merged.
func TestAFrameThatDoesNotDecodeMidMergeIsAnError(t *testing.T) {
	s := memstore.New()
	m := kv.Model{Config: cfg()}
	base := write(t, s, map[string]kv.Value{"k": bytesValue("b")})
	theirs := write(t, s, map[string]kv.Value{"k": bytesValue("t")})
	pm, err := prolly.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatal(err)
	}
	e := pm.Editor()
	if err := e.Put([]byte("k"), []byte{0x7f, 'x'}); err != nil {
		t.Fatal(err)
	}
	if pm, err = e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	ours := model.Root{Hash: pm.Root(), Size: 1, Format: kv.Format}
	res, err := m.Merge(ctx, base, ours, theirs, s)
	if !errors.Is(err, kv.ErrValue) {
		t.Fatalf("a merge meeting a frame that does not decode = %+v, %v; want ErrValue", res, err)
	}
}
