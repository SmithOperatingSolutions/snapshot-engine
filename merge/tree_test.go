package merge_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

func f(name string, v merge.Node) merge.Field { return merge.Field{Name: name, Value: v} }

// E1: a JSON-like value merges by path. Different fields both land, nested
// or not; one field changed on both sides is a scalar merge there, so the
// same change lands once and different changes conflict at the field's
// path; a field deleted on one side and changed on the other conflicts, one
// deleted and kept is deleted; fields added on both sides land, and one
// name added twice with different values conflicts; a counter path adds
// deltas; arrays merge as sequences, flagged where both sides appended.
func TestTreesMergeByPath(t *testing.T) {
	counter := merge.TreeOptions{Counter: func(p merge.Path) bool { return p.String() == "n" }}
	for name, c := range map[string]struct {
		base, ours, theirs, want merge.Node
		conflictAt, flagAt       string
		opts                     merge.TreeOptions
	}{
		"different fields": {
			merge.Obj(f("a", merge.Num("1")), f("b", merge.Num("2"))),
			merge.Obj(f("a", merge.Num("10")), f("b", merge.Num("2"))),
			merge.Obj(f("a", merge.Num("1")), f("b", merge.Num("20"))),
			merge.Obj(f("a", merge.Num("10")), f("b", merge.Num("20"))), "", "", merge.TreeOptions{}},
		"nested fields": {
			merge.Obj(f("o", merge.Obj(f("x", merge.Num("1")), f("y", merge.Num("1"))))),
			merge.Obj(f("o", merge.Obj(f("x", merge.Num("2")), f("y", merge.Num("1"))))),
			merge.Obj(f("o", merge.Obj(f("x", merge.Num("1")), f("y", merge.Num("2"))))),
			merge.Obj(f("o", merge.Obj(f("x", merge.Num("2")), f("y", merge.Num("2"))))), "", "", merge.TreeOptions{}},
		"the same change once": {
			merge.Obj(f("a", merge.Str("old"))), merge.Obj(f("a", merge.Str("new"))), merge.Obj(f("a", merge.Str("new"))),
			merge.Obj(f("a", merge.Str("new"))), "", "", merge.TreeOptions{}},
		"one field, two changes": {
			merge.Obj(f("a", merge.Num("1"))), merge.Obj(f("a", merge.Num("2"))), merge.Obj(f("a", merge.Num("3"))),
			merge.Obj(f("a", merge.Num("1"))), "a", "", merge.TreeOptions{}},
		"deleted against changed": {
			merge.Obj(f("a", merge.Num("1")), f("b", merge.Num("1"))), merge.Obj(f("b", merge.Num("1"))), merge.Obj(f("a", merge.Num("2")), f("b", merge.Num("1"))),
			merge.Obj(f("a", merge.Num("1")), f("b", merge.Num("1"))), "a", "", merge.TreeOptions{}},
		"deleted against kept": {
			merge.Obj(f("a", merge.Num("1")), f("b", merge.Num("1"))), merge.Obj(f("b", merge.Num("1"))), merge.Obj(f("a", merge.Num("1")), f("b", merge.Num("1"))),
			merge.Obj(f("b", merge.Num("1"))), "", "", merge.TreeOptions{}},
		"added on both sides": {
			merge.Obj(), merge.Obj(f("a", merge.Num("1"))), merge.Obj(f("b", merge.Num("2"))),
			merge.Obj(f("a", merge.Num("1")), f("b", merge.Num("2"))), "", "", merge.TreeOptions{}},
		"one name added twice, differently": {
			merge.Obj(), merge.Obj(f("a", merge.Num("1"))), merge.Obj(f("a", merge.Num("2"))),
			merge.Obj(), "a", "", merge.TreeOptions{}},
		"a counter adds deltas": {
			merge.Obj(f("n", merge.Num("5"))), merge.Obj(f("n", merge.Num("7"))), merge.Obj(f("n", merge.Num("6"))),
			merge.Obj(f("n", merge.Num("8"))), "", "", counter},
		"an array merges as a sequence": {
			merge.Obj(f("l", merge.Arr(merge.Str("a"), merge.Str("b")))),
			merge.Obj(f("l", merge.Arr(merge.Str("a"), merge.Str("b"), merge.Str("c")))),
			merge.Obj(f("l", merge.Arr(merge.Str("a"), merge.Str("b"), merge.Str("d")))),
			merge.Obj(f("l", merge.Arr(merge.Str("a"), merge.Str("b"), merge.Str("c"), merge.Str("d")))), "", "l/2", merge.TreeOptions{}},
		"a scalar root, two changes": {merge.Num("1"), merge.Num("2"), merge.Num("3"), merge.Num("1"), "", "", merge.TreeOptions{}},
	} {
		got := merge.Tree(c.base, c.ours, c.theirs, c.opts)
		if name == "a scalar root, two changes" {
			if got.Clean() || len(got.Conflicts) != 1 || len(got.Conflicts[0].Path) != 0 {
				t.Errorf("%s: %+v, want one conflict at the root", name, got)
			}
			continue
		}
		if !got.Value.Equal(c.want) {
			t.Errorf("%s: Tree = %s, want %s", name, got.Value.Canonical(), c.want.Canonical())
		}
		switch {
		case c.conflictAt != "" && (len(got.Conflicts) != 1 || got.Conflicts[0].Path.String() != c.conflictAt):
			t.Errorf("%s: conflicts %v, want one at %q", name, got.Conflicts, c.conflictAt)
		case c.conflictAt == "" && !got.Clean():
			t.Errorf("%s: conflicts %v, want none", name, got.Conflicts)
		}
		switch {
		case c.flagAt != "" && (len(got.Flagged) != 1 || got.Flagged[0].Path.String() != c.flagAt):
			t.Errorf("%s: flagged %v, want the concurrent insert at %q", name, got.Flagged, c.flagAt)
		case c.flagAt == "" && len(got.Flagged) != 0:
			t.Errorf("%s: flagged %v, want nothing flagged", name, got.Flagged)
		}
	}
}

// Tree is deterministic, returns a side unchanged when the other made no
// change, and is symmetric.
func TestTreeProperties(t *testing.T) {
	var gen func(rt *rapid.T, depth int, name string) merge.Node
	gen = func(rt *rapid.T, depth int, name string) merge.Node {
		kind := rapid.IntRange(0, 3).Draw(rt, name+".kind")
		switch {
		case kind == 0 || depth == 0:
			return merge.Num(rapid.SampledFrom([]string{"1", "2", "3"}).Draw(rt, name+".num"))
		case kind == 1:
			return merge.Str(rapid.SampledFrom([]string{"x", "y"}).Draw(rt, name+".str"))
		case kind == 2:
			var fs []merge.Field
			for _, n := range rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"a", "b", "c"}), 0, 3, rapid.ID[string]).Draw(rt, name+".names") {
				fs = append(fs, f(n, gen(rt, depth-1, name+"."+n)))
			}
			return merge.Obj(fs...)
		default:
			var es []merge.Node
			for i := range rapid.IntRange(0, 3).Draw(rt, name+".n") {
				es = append(es, gen(rt, depth-1, name+".e"+string(rune('0'+i))))
			}
			return merge.Arr(es...)
		}
	}
	same := func(a, b merge.Result[merge.Node]) bool {
		return a.Value.Equal(b.Value) && len(a.Conflicts) == len(b.Conflicts) && len(a.Flagged) == len(b.Flagged)
	}
	rapid.Check(t, func(rt *rapid.T) {
		base, ours, theirs := gen(rt, 2, "base"), gen(rt, 2, "ours"), gen(rt, 2, "theirs")
		none := merge.TreeOptions{}
		if !same(merge.Tree(base, ours, theirs, none), merge.Tree(base, ours, theirs, none)) {
			rt.Fatal("Tree is not deterministic")
		}
		if got := merge.Tree(base, ours, base, none); !got.Clean() || !got.Value.Equal(ours) {
			rt.Fatalf("Tree(%s, %s, base) = %s %v, want ours", base.Canonical(), ours.Canonical(), got.Value.Canonical(), got.Conflicts)
		}
		if got := merge.Tree(base, base, theirs, none); !got.Clean() || !got.Value.Equal(theirs) {
			rt.Fatalf("Tree(%s, base, %s) = %s %v, want theirs", base.Canonical(), theirs.Canonical(), got.Value.Canonical(), got.Conflicts)
		}
		if a, b := merge.Tree(base, ours, theirs, none), merge.Tree(base, theirs, ours, none); !same(a, b) {
			rt.Fatalf("Tree is not symmetric: %s %v against %s %v", a.Value.Canonical(), a.Conflicts, b.Value.Canonical(), b.Conflicts)
		}
	})
}
