package merge_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

// E1: a scalar both sides may have changed. Equal is clean; the side that
// changed wins over the side that did not; two different changes are a
// conflict at the root, and the value is then not a merge.
func TestScalarsMergeByWhoChanged(t *testing.T) {
	for name, c := range map[string]struct {
		base, ours, theirs string
		want               string
		conflict           bool
	}{
		"nobody changed":       {"a", "a", "a", "a", false},
		"ours changed":         {"a", "b", "a", "b", false},
		"theirs changed":       {"a", "a", "c", "c", false},
		"both changed alike":   {"a", "b", "b", "b", false},
		"both changed apart":   {"a", "b", "c", "", true},
		"theirs back to base":  {"a", "b", "a", "b", false},
		"ours deleted (empty)": {"a", "", "a", "", false},
	} {
		got := merge.Scalar(c.base, c.ours, c.theirs)
		if c.conflict {
			if got.Clean() || len(got.Conflicts) != 1 || len(got.Conflicts[0].Path) != 0 {
				t.Errorf("%s: Scalar(%q, %q, %q) = %+v, want one conflict at the root", name, c.base, c.ours, c.theirs, got)
			}
			continue
		}
		if !got.Clean() || got.Value != c.want {
			t.Errorf("%s: Scalar(%q, %q, %q) = %+v, want %q clean", name, c.base, c.ours, c.theirs, got, c.want)
		}
	}
	got := merge.Scalar(1, 2, 3)
	if got.Clean() {
		t.Errorf("Scalar(1, 2, 3) merged to %d: two different changes are a conflict", got.Value)
	}
}

// E1: a counter both sides moved merges to the base plus both deltas, never
// to one side's value.
func TestCountersAddTheirDeltas(t *testing.T) {
	for _, c := range []struct{ base, ours, theirs, want int64 }{
		{10, 10, 10, 10},
		{10, 13, 10, 13},
		{10, 10, 7, 7},
		{10, 13, 7, 10},
		{10, 13, 14, 17},
		{-5, -5, -8, -8},
	} {
		if got := merge.Counter(c.base, c.ours, c.theirs); got != c.want {
			t.Errorf("Counter(%d, %d, %d) = %d, want %d (the base plus both deltas)", c.base, c.ours, c.theirs, got, c.want)
		}
	}
}

// Every policy: deterministic, and a side that made no change leaves the
// other side's value as the result. Scalar and Counter are symmetric.
func TestScalarAndCounterProperties(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		base := rapid.IntRange(-20, 20).Draw(rt, "base")
		ours := rapid.IntRange(-20, 20).Draw(rt, "ours")
		theirs := rapid.IntRange(-20, 20).Draw(rt, "theirs")
		once, twice := merge.Scalar(base, ours, theirs), merge.Scalar(base, ours, theirs)
		if once.Value != twice.Value || once.Clean() != twice.Clean() {
			rt.Fatalf("Scalar(%d, %d, %d) gave %+v then %+v: not deterministic", base, ours, theirs, once, twice)
		}
		if r := merge.Scalar(base, ours, base); !r.Clean() || r.Value != ours {
			rt.Fatalf("Scalar(%d, %d, %d) with theirs unchanged = %+v, want ours", base, ours, base, r)
		}
		if r := merge.Scalar(base, base, theirs); !r.Clean() || r.Value != theirs {
			rt.Fatalf("Scalar(%d, %d, %d) with ours unchanged = %+v, want theirs", base, base, theirs, r)
		}
		if a, b := merge.Scalar(base, ours, theirs), merge.Scalar(base, theirs, ours); a.Clean() != b.Clean() || (a.Clean() && a.Value != b.Value) {
			rt.Fatalf("Scalar is not symmetric: %+v against %+v", a, b)
		}
		c := merge.Counter(int64(base), int64(ours), int64(theirs))
		if c != int64(base)+(int64(ours)-int64(base))+(int64(theirs)-int64(base)) {
			rt.Fatalf("Counter(%d, %d, %d) = %d, want the base plus both deltas", base, ours, theirs, c)
		}
		if merge.Counter(int64(base), int64(ours), int64(base)) != int64(ours) || merge.Counter(int64(base), int64(base), int64(theirs)) != int64(theirs) {
			rt.Fatalf("Counter with one side unchanged did not return the other side")
		}
		if merge.Counter(int64(base), int64(ours), int64(theirs)) != merge.Counter(int64(base), int64(theirs), int64(ours)) {
			rt.Fatalf("Counter is not symmetric")
		}
	})
}
