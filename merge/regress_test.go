package merge_test

import (
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

// elems is n distinct elements, e0 to e(n-1).
func elems(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "e" + strconv.Itoa(i)
	}
	return out
}

// A sequence merge costs memory in proportion to its lists: aligning three
// versions of an 8 KB list took 1.15 GB, because the alignment built an
// (n+1)×(m+1) table (snapshot-engine#4). Here a thousand-element list,
// one side replacing every twentieth element and the other inserting
// after every twentieth, must merge to both sets of changes under 1 MiB.
func TestRegression_SE4_ASequenceMergeCostsLinearMemory(t *testing.T) {
	base := elems(1000)
	var ours, theirs, want []string
	for i, e := range base {
		o := e
		if i%20 == 0 {
			o = "r" + strconv.Itoa(i)
		}
		ours, theirs, want = append(ours, o), append(theirs, e), append(want, o)
		if i%20 == 9 {
			theirs, want = append(theirs, "t"+strconv.Itoa(i)), append(want, "t"+strconv.Itoa(i))
		}
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := merge.Sequence(base, ours, theirs, ident)
	runtime.ReadMemStats(&after)
	if !got.Clean() || len(got.Flagged) != 0 || !slices.Equal(got.Value, want) {
		t.Errorf("a thousand-element list, 50 elements replaced on one side and 50 inserted on the other, merged to %d elements with conflicts %v and flags %v: want both sides' changes, %d elements, clean", len(got.Value), got.Conflicts, got.Flagged, len(want))
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("merging a thousand-element list allocated %d bytes: memory grows with the square of a list's length, and a list at kv's limit asks for 69 GB", grew)
	}
}

// Aligning a side with the base is bounded work: at most MaxSequenceEdits
// insertions and deletions (a replaced element is one of each) between the
// base and either side, and a merge past it is a conflict at the list, the
// base kept, whichever side is ours. At the limit it merges; a side that
// did not change the list still yields to the other however much that
// one changed (snapshot-engine#4).
func TestRegression_SE4_ASequenceMergePastTheEditBudgetIsAConflict(t *testing.T) {
	base := elems(600)
	replaced := func(n int) []string { // the first n elements replaced: 2n edits
		out := slices.Clone(base)
		for i := range n {
			out[i] = "r" + strconv.Itoa(i)
		}
		return out
	}
	atLimit := replaced(merge.MaxSequenceEdits / 2)
	overLimit := slices.Delete(replaced(merge.MaxSequenceEdits/2), 550, 551) // one edit more
	appended := append(slices.Clone(base), "z")

	// Positive controls: at the limit, and one side unchanged.
	if got := merge.Sequence(base, atLimit, appended, ident); !got.Clean() || !slices.Equal(got.Value, append(slices.Clone(atLimit), "z")) {
		t.Errorf("one side with exactly %d edits merged with an append on the other: conflicts %v, %d elements; want both changes, clean", merge.MaxSequenceEdits, got.Conflicts, len(got.Value))
	}
	if got := merge.Sequence(base, overLimit, base, ident); !got.Clean() || !slices.Equal(got.Value, overLimit) {
		t.Errorf("one side past the budget merged with an unchanged side: conflicts %v, %d elements; want that side, clean", got.Conflicts, len(got.Value))
	}

	for _, sides := range [][2][]string{{overLimit, appended}, {appended, overLimit}} {
		got := merge.Sequence(base, sides[0], sides[1], ident)
		if got.Clean() || len(got.Conflicts) != 1 || len(got.Conflicts[0].Path) != 0 || !slices.Equal(got.Value, base) {
			t.Errorf("one side with %d edits, one more than the budget, merged with an append on the other: conflicts %v, %d elements; want one conflict at the list, the base kept", merge.MaxSequenceEdits+1, got.Conflicts, len(got.Value))
			continue
		}
		if !strings.Contains(got.Conflicts[0].Reason, strconv.Itoa(merge.MaxSequenceEdits)) {
			t.Errorf("the conflict says %q: want it to name the budget, %d", got.Conflicts[0].Reason, merge.MaxSequenceEdits)
		}
	}
}

// FuzzSequence merges three lists of up to 64 elements drawn from eight
// keys: Sequence is deterministic, symmetric in value, conflicts and flags,
// yields to a side when the other made no change, and lands one change
// once when both made it.
func FuzzSequence(f *testing.F) {
	f.Add([]byte("abc"), []byte("abcd"), []byte("axbc"))
	f.Add([]byte("abc"), []byte("ac"), []byte("ayc"))
	f.Add([]byte(""), []byte("ab"), []byte("ba"))
	f.Add([]byte("aaaa"), []byte("aa"), []byte("aaaaaa"))
	f.Fuzz(func(t *testing.T, b, o, th []byte) {
		list := func(bs []byte) []string {
			out := make([]string, 0, min(len(bs), 64))
			for _, c := range bs[:min(len(bs), 64)] {
				out = append(out, string(rune('a'+c%8)))
			}
			return out
		}
		base, ours, theirs := list(b), list(o), list(th)
		same := func(x, y merge.Result[[]string]) bool {
			return slices.Equal(x.Value, y.Value) && len(x.Conflicts) == len(y.Conflicts) && len(x.Flagged) == len(y.Flagged)
		}
		got := merge.Sequence(base, ours, theirs, ident)
		if !same(got, merge.Sequence(base, ours, theirs, ident)) {
			t.Fatalf("Sequence(%v, %v, %v) is not deterministic", base, ours, theirs)
		}
		if !same(got, merge.Sequence(base, theirs, ours, ident)) {
			t.Fatalf("Sequence(%v, %v, %v) is not symmetric", base, ours, theirs)
		}
		if r := merge.Sequence(base, ours, base, ident); !r.Clean() || !slices.Equal(r.Value, ours) {
			t.Fatalf("Sequence(%v, %v, base) = %+v, want ours", base, ours, r)
		}
		if r := merge.Sequence(base, ours, ours, ident); !r.Clean() || !slices.Equal(r.Value, ours) {
			t.Fatalf("Sequence(%v, %v, the same) = %+v, want it once", base, ours, r)
		}
	})
}

// nest wraps leaf in depth objects, each holding the next under "next"
// beside pad.
func nest(depth int, leaf, pad merge.Node) merge.Node {
	n := leaf
	for range depth {
		n = merge.Obj(merge.Field{Name: "next", Value: n}, merge.Field{Name: "pad", Value: pad})
	}
	return n
}

// A tree merge compares each node once: it compared whole subtrees, by
// building their canonical text, at every level it descended, so a
// document's cost grew with its depth times its size (snapshot-engine#6).
// Here 16 levels over a 96 KB string, the two sides changing two leaves
// at the bottom, must merge to both changes under 256 KiB: less than the
// text of the string, so no comparison builds it.
func TestRegression_SE6_ATreeMergeComparesEachNodeOnce(t *testing.T) {
	leaf := func(x, y string) merge.Node {
		return merge.Obj(merge.Field{Name: "big", Value: merge.Str(strings.Repeat("b", 96<<10))}, merge.Field{Name: "x", Value: merge.Num(x)}, merge.Field{Name: "y", Value: merge.Num(y)})
	}
	pad := merge.Str(strings.Repeat("p", 256))
	base, ours, theirs, want := nest(16, leaf("1", "1"), pad), nest(16, leaf("2", "1"), pad), nest(16, leaf("1", "2"), pad), nest(16, leaf("2", "2"), pad)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := merge.Tree(base, ours, theirs, merge.TreeOptions{})
	runtime.ReadMemStats(&after)
	if !got.Clean() || !got.Value.Equal(want) {
		t.Fatalf("two leaves changed 16 levels down merged with conflicts %v, or not to both changes", got.Conflicts)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 256<<10 {
		t.Errorf("merging a 100 KB value 16 levels deep allocated %d bytes: every level compares its whole subtree again, building its text", grew)
	}
}

// Tree merges values nested no deeper than MaxDepth arrays and objects, the
// document model's limit; values a caller built deeper are a conflict at
// the root, the base kept, before any recursion into them
// (snapshot-engine#6).
func TestRegression_SE6_ATreeMergeRefusesNestingPastMaxDepth(t *testing.T) {
	pad := merge.Num("0")
	for _, depth := range []int{merge.MaxDepth, merge.MaxDepth + 1, 10 * merge.MaxDepth} {
		leaf := func(x, y string) merge.Node {
			return merge.Obj(merge.Field{Name: "x", Value: merge.Num(x)}, merge.Field{Name: "y", Value: merge.Num(y)})
		}
		levels := depth - 1 // the leaf object is a level too
		base, ours, theirs := nest(levels, leaf("1", "1"), pad), nest(levels, leaf("2", "1"), pad), nest(levels, leaf("1", "2"), pad)
		got := merge.Tree(base, ours, theirs, merge.TreeOptions{})
		if depth <= merge.MaxDepth { // the positive control, at the limit
			if !got.Clean() || !got.Value.Equal(nest(levels, leaf("2", "2"), pad)) {
				t.Errorf("a value nested %d deep, the limit, merged with conflicts %v, or not to both changes", depth, got.Conflicts)
			}
			continue
		}
		if len(got.Conflicts) != 1 || len(got.Conflicts[0].Path) != 0 || !got.Value.Equal(base) {
			t.Errorf("a value nested %d deep, past the limit of %d, merged with conflicts %v: want one at the root, the base kept", depth, merge.MaxDepth, got.Conflicts)
		}
	}
}

// Writing out a node's canonical text takes no stack in proportion to its
// depth: it recursed once per level, so a caller's node nested deeply
// enough could exhaust the goroutine's stack, which no recover catches
// (snapshot-engine#6). A node nested 20,000 deep must be written out with
// the stack growing under 1 MiB.
func TestRegression_SE6_CanonicalTakesNoStackInProportionToDepth(t *testing.T) {
	const depth = 20000
	n := merge.Num("1")
	for range depth {
		n = merge.Arr(n)
	}
	want := strings.Repeat("[", depth) + "1" + strings.Repeat("]", depth)
	done := make(chan struct{})
	var grew uint64
	var got string
	go func() {
		defer close(done)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got = n.Canonical()
		runtime.ReadMemStats(&after)
		grew = after.StackInuse - min(after.StackInuse, before.StackInuse)
	}()
	<-done
	if got != want {
		t.Fatalf("a node nested %d deep wrote out as %d bytes, want %d", depth, len(got), len(want))
	}
	if grew > 1<<20 {
		t.Errorf("writing out a node nested %d deep grew the stack by %d bytes: a deep enough node a caller builds crashes the process", depth, grew)
	}
}

// node builds a small value from fuzz bytes: at most four levels, four
// elements or fields a level, fields named a to d.
func node(b []byte, depth int) (merge.Node, []byte) {
	if len(b) == 0 {
		return merge.Num("0"), b
	}
	c, b := b[0], b[1:]
	switch c % 6 {
	case 0:
		return merge.Node{Kind: merge.Null}, b
	case 1:
		return merge.Boolean(c&0x80 != 0), b
	case 2:
		return merge.Num(strconv.Itoa(int(c >> 4))), b
	case 3:
		return merge.Str(string(rune('x' + c>>6))), b
	}
	if depth == 0 || len(b) == 0 {
		return merge.Num("0"), b
	}
	count := int(b[0] % 5)
	b = b[1:]
	if c%6 == 4 {
		var es []merge.Node
		for range count {
			var e merge.Node
			e, b = node(b, depth-1)
			es = append(es, e)
		}
		return merge.Arr(es...), b
	}
	var fs []merge.Field
	seen := map[string]bool{}
	for i := range count {
		var v merge.Node
		v, b = node(b, depth-1)
		if name := string(rune('a' + (int(c)+i)%4)); !seen[name] {
			seen[name] = true
			fs = append(fs, merge.Field{Name: name, Value: v})
		}
	}
	return merge.Obj(fs...), b
}

// FuzzTree merges three small values built from fuzz bytes: Tree is
// deterministic, symmetric in value, conflicts and flags, yields to a side
// when the other made no change, and lands one change once.
func FuzzTree(f *testing.F) {
	f.Add([]byte{5, 3, 2, 3, 4, 2, 2, 2}, []byte{5, 3, 18, 3, 4, 2, 2, 34}, []byte{5, 3, 2, 3, 4, 3, 2, 2, 2})
	f.Add([]byte{4, 3, 2, 18, 34}, []byte{4, 2, 2, 50}, []byte{4, 4, 2, 18, 34, 66})
	f.Add([]byte{2}, []byte{18}, []byte{34})
	f.Fuzz(func(t *testing.T, bb, ob, tb []byte) {
		base, _ := node(bb, 4)
		ours, _ := node(ob, 4)
		theirs, _ := node(tb, 4)
		counter := merge.TreeOptions{Counter: func(p merge.Path) bool { return len(p) > 0 && p[len(p)-1] == "c" }}
		same := func(x, y merge.Result[merge.Node]) bool {
			return x.Value.Equal(y.Value) && len(x.Conflicts) == len(y.Conflicts) && len(x.Flagged) == len(y.Flagged)
		}
		for _, o := range []merge.TreeOptions{{}, counter} {
			got := merge.Tree(base, ours, theirs, o)
			if !same(got, merge.Tree(base, ours, theirs, o)) {
				t.Fatalf("Tree(%s, %s, %s) is not deterministic", base.Canonical(), ours.Canonical(), theirs.Canonical())
			}
			if !same(got, merge.Tree(base, theirs, ours, o)) {
				t.Fatalf("Tree(%s, %s, %s) is not symmetric", base.Canonical(), ours.Canonical(), theirs.Canonical())
			}
			if r := merge.Tree(base, ours, base, o); !r.Clean() || !r.Value.Equal(ours) {
				t.Fatalf("Tree(%s, %s, base) = %s %v, want ours", base.Canonical(), ours.Canonical(), r.Value.Canonical(), r.Conflicts)
			}
			if r := merge.Tree(base, ours, ours, o); !r.Clean() || !r.Value.Equal(ours) {
				t.Fatalf("Tree(%s, %s, the same) = %s %v, want it once", base.Canonical(), ours.Canonical(), r.Value.Canonical(), r.Conflicts)
			}
		}
	})
}

// A tree merge costs time in proportion to its objects' fields: every
// field was found by scanning its object, once per name per side, so an
// object of 12,000 fields took most of a second to merge
// (snapshot-engine#6). A timing guard with headroom: the best of three
// merges must take under 100 ms (ten times that under the race detector).
func TestRegression_SE6_ATreeMergeIsNotQuadraticInFields(t *testing.T) {
	const n = 12000
	obj := func(changed int, to string) merge.Node {
		fs := make([]merge.Field, n)
		for i := range fs {
			fs[i] = merge.Field{Name: "f" + strconv.Itoa(i), Value: merge.Num("1")}
		}
		if changed >= 0 {
			fs[changed].Value = merge.Num(to)
		}
		return merge.Obj(fs...)
	}
	base, ours, theirs, want := obj(-1, ""), obj(0, "2"), obj(1, "3"), obj(0, "2")
	want.Fields[1].Value = merge.Num("3")
	best := time.Duration(1<<63 - 1)
	for range 3 {
		start := time.Now()
		got := merge.Tree(base, ours, theirs, merge.TreeOptions{})
		best = min(best, time.Since(start))
		if !got.Clean() || !got.Value.Equal(want) {
			t.Fatalf("two fields of %d changed on different sides merged with conflicts %v, or not to both", n, got.Conflicts)
		}
	}
	if ceiling := raceScale * 100 * time.Millisecond; best > ceiling {
		t.Errorf("merging an object of %d fields changed on both sides took %v at best, over %v: a merge grows with the square of an object's fields", n, best, ceiling)
	}
}
