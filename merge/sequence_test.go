package merge_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

func ident(s string) string { return s }

func words(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}

// E1: an ordered list merges by element identity and position. Changes to
// different stretches both land; a deletion on one side lands over the other
// side's unchanged stretch; the same change on both sides lands once; both
// sides inserting at one position keep both blocks, in the order of their
// keys whichever side is ours, and the position is flagged; two different
// changes to one stretch are a conflict at that position, and a deletion
// against a change is one too.
func TestSequencesMergeByIdentityAndPosition(t *testing.T) {
	for name, c := range map[string]struct {
		base, ours, theirs, want string
		conflictAt, flagAt       string // "" for none
	}{
		"different stretches":        {"a b c", "a b c d", "a x b c", "a x b c d", "", ""},
		"a deletion lands":           {"a b c", "a c", "a b c", "a c", "", ""},
		"the same change once":       {"a b c", "a x c", "a x c", "a x c", "", ""},
		"both append: flagged":       {"a b c", "a b c d", "a b c e", "a b c d e", "", "3"},
		"both append, swapped sides": {"a b c", "a b c e", "a b c d", "a b c d e", "", "3"},
		"both insert at the front":   {"a", "z a", "y a", "y z a", "", "0"},
		"two changes to one stretch": {"a b c", "a x c", "a y c", "a b c", "1", ""},
		"deleted against changed":    {"a b c", "a c", "a y c", "a b c", "1", ""},
		"both empty from base":       {"a b", "", "", "", "", ""},
	} {
		got := merge.Sequence(words(c.base), words(c.ours), words(c.theirs), ident)
		if s := strings.Join(got.Value, " "); s != c.want {
			t.Errorf("%s: Sequence(%q, %q, %q) = %q, want %q", name, c.base, c.ours, c.theirs, s, c.want)
		}
		switch {
		case c.conflictAt != "" && (len(got.Conflicts) != 1 || got.Conflicts[0].Path.String() != c.conflictAt):
			t.Errorf("%s: conflicts %v, want one at position %s", name, got.Conflicts, c.conflictAt)
		case c.conflictAt == "" && !got.Clean():
			t.Errorf("%s: conflicts %v, want none", name, got.Conflicts)
		}
		switch {
		case c.flagAt != "" && (len(got.Flagged) != 1 || got.Flagged[0].Path.String() != c.flagAt):
			t.Errorf("%s: flagged %v, want the concurrent insert at position %s", name, got.Flagged, c.flagAt)
		case c.flagAt == "" && len(got.Flagged) != 0:
			t.Errorf("%s: flagged %v, want nothing flagged", name, got.Flagged)
		}
	}
}

// Sequence is deterministic, returns a side unchanged when the other made no
// change, and is symmetric in value, conflicts and flags.
func TestSequenceProperties(t *testing.T) {
	gen := func(rt *rapid.T, name string) []string {
		return rapid.SliceOfN(rapid.SampledFrom([]string{"a", "b", "c", "d"}), 0, 5).Draw(rt, name)
	}
	same := func(a, b merge.Result[[]string]) bool {
		return strings.Join(a.Value, " ") == strings.Join(b.Value, " ") && len(a.Conflicts) == len(b.Conflicts) && len(a.Flagged) == len(b.Flagged)
	}
	rapid.Check(t, func(rt *rapid.T) {
		base, ours, theirs := gen(rt, "base"), gen(rt, "ours"), gen(rt, "theirs")
		if !same(merge.Sequence(base, ours, theirs, ident), merge.Sequence(base, ours, theirs, ident)) {
			rt.Fatal("Sequence is not deterministic")
		}
		if got := merge.Sequence(base, ours, base, ident); !got.Clean() || strings.Join(got.Value, " ") != strings.Join(ours, " ") {
			rt.Fatalf("Sequence(%v, %v, base) = %+v, want ours", base, ours, got)
		}
		if got := merge.Sequence(base, base, theirs, ident); !got.Clean() || strings.Join(got.Value, " ") != strings.Join(theirs, " ") {
			rt.Fatalf("Sequence(%v, base, %v) = %+v, want theirs", base, theirs, got)
		}
		if a, b := merge.Sequence(base, ours, theirs, ident), merge.Sequence(base, theirs, ours, ident); !same(a, b) {
			rt.Fatalf("Sequence is not symmetric: %+v against %+v", a, b)
		}
	})
}
