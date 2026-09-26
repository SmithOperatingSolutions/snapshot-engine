package document_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
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

// A batch of sides applied to one collection is their rebases one after
// another, in order, root for root: a side is taken when its records are
// as its base had them in the target and no earlier side taken changed
// them, refused (ErrChangedSince) otherwise, contributing nothing; a side
// checked but not taken contributes nothing either. Rebase, side by side,
// is the authority.
func TestABatchOfSidesIsTheirRebasesInOrder(t *testing.T) {
	s := memstore.New()
	m := document.Model{Config: cfg()}
	const n = 300
	base := rebaseRecords(t, s, n, nil)
	onto := rebaseRecords(t, s, n, map[string]string{"r0007": `{"n":7,"onto":true}`, "r0200": ""})
	sides := []map[string]string{
		{"r0001": `{"n":1,"side":0}`, "r0002": ""},            // taken
		{"r0007": `{"n":7,"side":1}`},                         // refused: onto changed it
		{"r0001": `{"n":1,"side":2}`},                         // refused: side 0 changed it
		{"r0300": `{"new":3}`, "r0150": `{"n":150,"side":3}`}, // taken (an addition too)
		{"r0200": ""},                  // refused: onto removed it
		{"r0002": `{"n":2,"side":5}`},  // refused: side 0 removed it
		{"r0050": `{"n":50,"side":6}`}, // checked, not taken
		{"r0050": `{"n":50,"side":7}`}, // taken: side 6 was not
	}
	const skip = 6
	roots := make([]model.Root, len(sides))
	for i, e := range sides {
		roots[i] = rebaseRecords(t, s, n, e)
	}
	want := onto
	wantErr := make([]error, len(sides))
	for i, r := range roots {
		next, err := m.Rebase(ctx, base, r, want, s)
		if errors.Is(err, document.ErrChangedSince) {
			wantErr[i] = err
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if i != skip {
			want = next
		}
	}
	for i, e := range wantErr {
		if (e == nil) != (i == 0 || i == 3 || i == 6 || i == 7) {
			t.Fatalf("fixture: side %d's rebase = %v", i, e)
		}
	}
	batch, err := m.Batch(ctx, s, onto)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range roots {
		p, err := batch.Check(ctx, base, r)
		if (wantErr[i] == nil) != (err == nil) || (err != nil && !errors.Is(err, document.ErrChangedSince)) {
			t.Fatalf("side %d checked against the batch = %v, want %v: a member of a batch must be refused exactly where its rebase alone onto what the earlier members made would be", i, err, wantErr[i])
		}
		if err == nil && i != skip {
			batch.Take(p)
		}
	}
	got, err := batch.Flush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("the batch flushed %v, the rebases in order made %v: a batch of members would land another collection than the same members committed one by one", got, want)
	}
}

// A batch writes its sides with one flush: eight sides changing eight
// records write fewer chunks than eight rebases, each flushing its own path.
func TestABatchWritesItsSidesWithOneFlush(t *testing.T) {
	s := memstore.New()
	m := document.Model{Config: cfg()}
	const n, k = 3000, 8
	base := rebaseRecords(t, s, n, nil)
	var sides [k]model.Root
	for i := range sides {
		sides[i] = rebaseRecords(t, s, n, map[string]string{fmt.Sprintf("r%04d", i*370): fmt.Sprintf(`{"side":%d}`, i)})
	}
	counted := &puts{Store: s}
	cur := base
	for _, side := range sides {
		var err error
		if cur, err = m.Rebase(ctx, base, side, cur, counted); err != nil {
			t.Fatal(err)
		}
	}
	oneByOne := counted.n.Swap(0)
	batch, err := m.Batch(ctx, counted, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, side := range sides {
		p, err := batch.Check(ctx, base, side)
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
		t.Errorf("a batch of %d sides wrote %d chunks, the same sides rebased one by one %d: the batch leader would still pay one tree rewrite per member", k, atOnce, oneByOne)
	}
}
