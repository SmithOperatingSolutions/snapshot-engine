package merge_test

import (
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

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
