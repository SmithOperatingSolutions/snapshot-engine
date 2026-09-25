package kv_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"pgregory.net/rapid"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

// Rebase is the merge of two sides that changed different keys, reached by
// reading one side's changes and a point read of the other per key:
// Rebase(base, side, onto) is Merge(base, onto, side), byte for byte, for
// any base and any two sets of edits (sets, deletes, additions, every kind
// of value) that touch no key in common.
func TestRebaseIsTheMergeOfChangesToDifferentKeys(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := memstore.New()
		m := kv.Model{Config: cfg()}
		n := rapid.IntRange(0, 400).Draw(rt, "keys")
		base := map[string]kv.Value{}
		for i := range n {
			base[fmt.Sprintf("k%04d", i)] = bytesValue(fmt.Sprintf("v%d-%0100d", i, i))
		}
		value := func(label string) *kv.Value {
			switch rapid.IntRange(0, 2).Draw(rt, label) {
			case 0:
				return nil
			case 1:
				v := bytesValue(rapid.StringN(1, 40, -1).Draw(rt, label+"-bytes"))
				return &v
			}
			v := kv.Value{Kind: kv.Counter, Counter: rapid.Int64().Draw(rt, label+"-counter")}
			return &v
		}
		side, onto := map[string]*kv.Value{}, map[string]*kv.Value{}
		for i := range rapid.IntRange(0, 30).Draw(rt, "edits") {
			key := fmt.Sprintf("k%04d", rapid.IntRange(0, n+20).Draw(rt, fmt.Sprintf("key%d", i))) // past n: an addition
			if _, taken := side[key]; taken {
				continue
			}
			if _, taken := onto[key]; taken {
				continue
			}
			if rapid.Bool().Draw(rt, fmt.Sprintf("side%d", i)) {
				side[key] = value(fmt.Sprintf("v%d", i))
			} else {
				onto[key] = value(fmt.Sprintf("v%d", i))
			}
		}
		b := write(t, s, base)
		sr := write(t, s, with(base, side))
		or := write(t, s, with(base, onto))
		want, err := m.Merge(ctx, b, or, sr, s)
		if err != nil || len(want.Conflicts) > 0 {
			rt.Fatalf("the merge of changes to different keys: %v, %v", want.Conflicts, err)
		}
		got, err := m.Rebase(ctx, b, sr, or, s)
		if err != nil {
			rt.Fatalf("Rebase of changes to different keys = %v, want the merge's result", err)
		}
		if got != want.Root {
			rt.Fatalf("Rebase = %v, Merge = %v: a transaction rebased onto the batch would land another map than the merge it replaces", got, want.Root)
		}
		if got != write(t, s, with(with(base, onto), side)) {
			rt.Fatal("Rebase is not the base with both sides' edits applied")
		}
	})
}

// A key the side changed that the target changed too since the base is
// ErrChangedSince, whatever the two wrote: the same value, an addition on
// both, a delete against a set, a counter on both. A key only one of them
// changed rebases (the positive control, on the same fixture).
func TestRebaseRefusesAKeyBothSidesChanged(t *testing.T) {
	s := memstore.New()
	m := kv.Model{Config: cfg()}
	base := map[string]kv.Value{"a": bytesValue("1"), "b": bytesValue("2"), "hits": {Kind: kv.Counter, Counter: 10}}
	b := write(t, s, base)
	control := write(t, s, with(base, map[string]*kv.Value{"b": val("theirs")}))
	side := write(t, s, with(base, map[string]*kv.Value{"a": val("ours")}))
	if got, err := m.Rebase(ctx, b, side, control, s); err != nil || got != write(t, s, with(base, map[string]*kv.Value{"a": val("ours"), "b": val("theirs")})) {
		t.Fatalf("positive control: rebasing a change to a onto a change to b = %v, %v, want both", got, err)
	}
	two := kv.Value{Kind: kv.Counter, Counter: 11}
	for name, edits := range map[string][2]map[string]*kv.Value{
		"the same value":         {{"a": val("x")}, {"a": val("x")}},
		"different values":       {{"a": val("x")}, {"a": val("y")}},
		"an addition on both":    {{"new": val("x")}, {"new": val("x")}},
		"a delete against a set": {{"a": nil}, {"a": val("y")}},
		"a counter on both":      {{"hits": &two}, {"hits": &two}},
	} {
		side := write(t, s, with(base, edits[0]))
		onto := write(t, s, with(base, edits[1]))
		if _, err := m.Rebase(ctx, b, side, onto, s); !errors.Is(err, kv.ErrChangedSince) {
			t.Errorf("rebasing a key both sides changed (%s) = %v, want ErrChangedSince: two read-modify-writes of one key would both land", name, err)
		}
	}
}
