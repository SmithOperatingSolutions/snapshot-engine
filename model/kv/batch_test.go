package kv_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"pgregory.net/rapid"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

// puts counts what is written through it.
type puts struct {
	*memstore.Store
	n atomic.Int64
}

func (p *puts) Put(ctx context.Context, b []byte) (hash.Hash, error) {
	p.n.Add(1)
	return p.Store.Put(ctx, b)
}

// A batch of sides applied to one target is their rebases one after
// another, in order, root for root: a side is taken when its keys are as
// its base had them in the target and no earlier side taken changed them,
// and refused (ErrChangedSince) otherwise, contributing nothing; a side
// checked but not taken contributes nothing either. Rebase, applied side
// by side, is the authority.
func TestABatchOfSidesIsTheirRebasesInOrder(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := memstore.New()
		m := kv.Model{Config: cfg()}
		n := rapid.IntRange(0, 300).Draw(rt, "keys")
		base := map[string]kv.Value{}
		for i := range n {
			base[fmt.Sprintf("k%04d", i)] = bytesValue(fmt.Sprintf("v%d-%060d", i, i))
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
		edits := func(label string) map[string]*kv.Value {
			e := map[string]*kv.Value{}
			for i := range rapid.IntRange(0, 8).Draw(rt, label+"-edits") {
				key := fmt.Sprintf("k%04d", rapid.IntRange(0, n+10).Draw(rt, fmt.Sprintf("%s-key%d", label, i)))
				e[key] = value(fmt.Sprintf("%s-v%d", label, i))
			}
			return e
		}
		b := write(t, s, base)
		onto := write(t, s, with(base, edits("onto")))
		count := rapid.IntRange(1, 12).Draw(rt, "sides")
		sides := make([]model.Root, count)
		for i := range sides {
			sides[i] = write(t, s, with(base, edits(fmt.Sprintf("side%d", i))))
		}
		skip := rapid.IntRange(-1, count-1).Draw(rt, "checked but not taken")

		// The authority: Rebase, side by side, onto what the ones before made.
		want := onto
		wantErr := make([]error, count)
		for i, side := range sides {
			next, err := m.Rebase(ctx, b, side, want, s)
			if errors.Is(err, kv.ErrChangedSince) {
				wantErr[i] = err
				continue
			}
			if err != nil {
				rt.Fatal(err)
			}
			if i != skip { // checked, refused or not, but not taken
				want = next
			}
		}

		batch, err := m.Batch(ctx, s, onto)
		if err != nil {
			rt.Fatal(err)
		}
		for i, side := range sides {
			p, err := batch.Check(ctx, b, side)
			if (wantErr[i] == nil) != (err == nil) || (err != nil && !errors.Is(err, kv.ErrChangedSince)) {
				rt.Fatalf("side %d of %d checked against the batch = %v, want %v: a member of a batch must be refused exactly where its rebase alone onto what the earlier members made would be", i, count, err, wantErr[i])
			}
			if err == nil && i != skip {
				batch.Take(p)
			}
		}
		got, err := batch.Flush(ctx)
		if err != nil {
			rt.Fatal(err)
		}
		if got != want {
			rt.Fatalf("the batch flushed %v, the rebases in order made %v: a batch of members would land another map than the same members committed one by one", got, want)
		}
	})
}

// A batch writes its sides with one flush: eight sides changing eight keys
// write fewer chunks than eight rebases, each flushing its own path.
func TestABatchWritesItsSidesWithOneFlush(t *testing.T) {
	s := memstore.New()
	m := kv.Model{Config: cfg()}
	base := map[string]kv.Value{}
	for i := range 3000 {
		base[fmt.Sprintf("k%04d", i)] = bytesValue(fmt.Sprintf("v%d-%060d", i, i))
	}
	b := write(t, s, base)
	const n = 8
	var sides [n]model.Root
	for i := range sides {
		sides[i] = write(t, s, with(base, map[string]*kv.Value{fmt.Sprintf("k%04d", i*370): val(fmt.Sprintf("side%d", i))}))
	}
	counted := &puts{Store: s}
	cur := b
	for _, side := range sides {
		var err error
		if cur, err = m.Rebase(ctx, b, side, cur, counted); err != nil {
			t.Fatal(err)
		}
	}
	oneByOne := counted.n.Swap(0)
	batch, err := m.Batch(ctx, counted, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, side := range sides {
		p, err := batch.Check(ctx, b, side)
		if err != nil {
			t.Fatal(err)
		}
		batch.Take(p)
	}
	got, err := batch.Flush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != cur {
		t.Fatalf("the batch made %v, the rebases one by one %v", got, cur)
	}
	if atOnce := counted.n.Load(); atOnce >= oneByOne {
		t.Errorf("a batch of %d sides wrote %d chunks, the same sides rebased one by one %d: the batch leader would still pay one tree rewrite per member", n, atOnce, oneByOne)
	}
}
