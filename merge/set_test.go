package merge_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

func members(pairs ...any) merge.Members[string] {
	m := merge.Members[string]{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[merge.Tagged[string]{Elem: pairs[i].(string), Tag: merge.Tag(pairs[i+1].(int))}] = true
	}
	return m
}

// E1: a set merges by what each side did to its tagged members: what both
// kept stays, what either added arrives, what one side removed while the
// other kept it goes, and what one side removed while the other added again
// under a new tag stays (observed remove).
func TestSetsKeepAdditionsDropRemovalsAndKeepReAdds(t *testing.T) {
	base := members("a", 1, "b", 2, "c", 3)
	ours := members("a", 1, "c", 3, "d", 4)           // removed b, added d
	theirs := members("a", 1, "b", 2, "c", 3, "e", 5) // added e
	got := merge.Set(base, ours, theirs)
	for elem, want := range map[string]bool{"a": true, "b": false, "c": true, "d": true, "e": true} {
		if got.Has(elem) != want {
			t.Errorf("after ours removed b and added d, theirs added e: %q present = %t, want %t", elem, got.Has(elem), want)
		}
	}
	readd := merge.Set(members("x", 1), members(), members("x", 1, "x", 9))
	if !readd.Has("x") {
		t.Error("x removed on one side and added again under a new tag on the other is absent, want present (observed remove)")
	}
	kept := merge.Set(members("x", 1), members(), members("x", 1))
	if kept.Has("x") {
		t.Error("x removed on one side and merely kept on the other is present, want absent: the remover observed it")
	}
	both := merge.Set(members(), members("y", 7), members("y", 7))
	if !both.Has("y") || len(both) != 1 {
		t.Errorf("y added on both sides under one tag = %v, want present once", both)
	}
}

// Set is deterministic, returns a side unchanged when the other made no
// change, and is symmetric.
func TestSetProperties(t *testing.T) {
	gen := func(rt *rapid.T, name string) merge.Members[string] {
		m := merge.Members[string]{}
		for _, i := range rapid.SliceOfN(rapid.IntRange(0, 9), 0, 6).Draw(rt, name) {
			m[merge.Tagged[string]{Elem: string(rune('a' + i%5)), Tag: merge.Tag(i)}] = true
		}
		return m
	}
	equal := func(a, b merge.Members[string]) bool {
		if len(a) != len(b) {
			return false
		}
		for t := range a {
			if !b[t] {
				return false
			}
		}
		return true
	}
	rapid.Check(t, func(rt *rapid.T) {
		base, ours, theirs := gen(rt, "base"), gen(rt, "ours"), gen(rt, "theirs")
		if !equal(merge.Set(base, ours, theirs), merge.Set(base, ours, theirs)) {
			rt.Fatal("Set is not deterministic")
		}
		if got := merge.Set(base, ours, base); !equal(got, ours) {
			rt.Fatalf("Set with theirs unchanged = %v, want ours %v", got, ours)
		}
		if got := merge.Set(base, base, theirs); !equal(got, theirs) {
			rt.Fatalf("Set with ours unchanged = %v, want theirs %v", got, theirs)
		}
		if !equal(merge.Set(base, ours, theirs), merge.Set(base, theirs, ours)) {
			rt.Fatal("Set is not symmetric")
		}
	})
}
