package kv_test

import (
	"context"
	"sort"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

func changesOf(t *testing.T, m kv.Model, s *memstore.Store, from, to model.Root) map[string]model.ChangeKind {
	t.Helper()
	d, err := m.Diff(ctx, from, to, s)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	out := map[string]model.ChangeKind{}
	var order []string
	for {
		c, ok, err := d.Next(context.Background())
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		if !ok {
			break
		}
		out[string(c.Location)] = c.Kind
		order = append(order, string(c.Location))
	}
	if !sort.StringsAreSorted(order) {
		t.Fatalf("changes came in the order %q, want key order", order)
	}
	return out
}

// Diff is one change per key, located by the key: added, removed, modified;
// a key set to the same value on both sides is no change, and an object
// diffed with itself has none.
func TestDiffIsOneChangePerKey(t *testing.T) {
	s := memstore.New()
	m := kv.Model{Config: cfg()}
	from, err := kv.Write(ctx, s, cfg(), map[string]kv.Value{
		"a": bytesValue("1"), "b": bytesValue("2"), "c": bytesValue("3"), "same": bytesValue("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	to, err := kv.Write(ctx, s, cfg(), map[string]kv.Value{
		"a": bytesValue("1"), "b": bytesValue("changed"), "d": bytesValue("4"), "same": bytesValue("x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := changesOf(t, m, s, from, to)
	want := map[string]model.ChangeKind{"b": model.Modified, "c": model.Removed, "d": model.Added}
	if len(got) != len(want) {
		t.Fatalf("Diff found %d changes %v, want %d: %v", len(got), got, len(want), want)
	}
	for k, kind := range want {
		if got[k] != kind {
			t.Errorf("key %q: change kind %d, want %d", k, got[k], kind)
		}
	}
	if n := len(changesOf(t, m, s, from, from)); n != 0 {
		t.Fatalf("an object diffed with itself has %d changes", n)
	}
	if _, err := m.Diff(ctx, from, model.Root{Hash: to.Hash, Size: to.Size, Format: 2}, s); err == nil {
		t.Fatal("diffing against another format was accepted")
	}
}
