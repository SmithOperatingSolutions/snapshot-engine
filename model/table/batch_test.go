package table_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
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

// A batch of sides applied to one table is their rebases one after
// another, in order, root for root: a side is taken when its rows are as
// its base had them in the target and no earlier side taken changed them,
// refused (ErrChangedSince) otherwise, contributing nothing; a side checked
// but not taken contributes nothing either; a side whose schema is not the
// target's is ErrSchema. Rebase, side by side, is the authority.
func TestABatchOfSidesIsTheirRebasesInOrder(t *testing.T) {
	s := memstore.New()
	m := table.Model{Config: cfg()}
	base, keys := rebaseTable(t, s, people())
	onto := edited(t, base, func(e *table.Editor) error {
		if err := e.Update(keys[7], rebaseRow(7, "onto")); err != nil {
			return err
		}
		return e.Delete(keys[200])
	})
	sides := []*table.Table{
		edited(t, base, func(e *table.Editor) error { // taken
			if err := e.Update(keys[1], rebaseRow(1, "side 0")); err != nil {
				return err
			}
			return e.Delete(keys[2])
		}),
		edited(t, base, func(e *table.Editor) error { return e.Update(keys[7], rebaseRow(7, "side 1")) }), // refused: onto changed it
		edited(t, base, func(e *table.Editor) error { return e.Update(keys[1], rebaseRow(1, "side 2")) }), // refused: side 0 changed it
		edited(t, base, func(e *table.Editor) error { // taken, an addition too
			if _, err := e.Insert(rebaseRow(900, "side 3")); err != nil {
				return err
			}
			return e.Update(keys[150], rebaseRow(150, "side 3"))
		}),
		edited(t, base, func(e *table.Editor) error { return e.Delete(keys[200]) }),                         // refused: onto removed it
		edited(t, base, func(e *table.Editor) error { return e.Update(keys[2], rebaseRow(2, "side 5")) }),   // refused: side 0 removed it
		edited(t, base, func(e *table.Editor) error { return e.Update(keys[50], rebaseRow(50, "side 6")) }), // checked, not taken
		edited(t, base, func(e *table.Editor) error { return e.Update(keys[50], rebaseRow(50, "side 7")) }), // taken: side 6 was not
	}
	const skip = 6
	want := onto.Root()
	wantErr := make([]error, len(sides))
	for i, side := range sides {
		next, err := m.Rebase(ctx, base.Root(), side.Root(), want, s)
		if errors.Is(err, table.ErrChangedSince) {
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
	batch, err := m.Batch(ctx, s, onto.Root())
	if err != nil {
		t.Fatal(err)
	}
	for i, side := range sides {
		p, err := batch.Check(ctx, base.Root(), side.Root())
		if (wantErr[i] == nil) != (err == nil) || (err != nil && !errors.Is(err, table.ErrChangedSince)) {
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
		t.Fatalf("the batch flushed %v, the rebases in order made %v: a batch of members would land another table than the same members committed one by one", got, want)
	}

	// Another schema on the side is ErrSchema, as Rebase has it, and the
	// batch is untouched by it.
	next := people()
	next.Columns = append(next.Columns, table.Column{Tag: 9, Name: "note", Type: table.TypeText, Nullable: true})
	altered, err := base.WithSchema(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	batch, err = m.Batch(ctx, s, onto.Root())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := batch.Check(ctx, base.Root(), altered.Root()); !errors.Is(err, table.ErrSchema) {
		t.Errorf("a side whose schema changed checked against the batch = %v, want ErrSchema: its rows would be written under a schema they were not read under", err)
	}
	if got, err := batch.Flush(ctx); err != nil || got != onto.Root() {
		t.Errorf("a batch that took nothing flushed %v, %v, want the target unchanged", got, err)
	}
}

// A batch writes its sides with one flush: eight sides changing eight rows
// write fewer chunks than eight rebases, each flushing its own path.
func TestABatchWritesItsSidesWithOneFlush(t *testing.T) {
	s := memstore.New()
	m := table.Model{Config: cfg()}
	base, keys := rebaseTable(t, s, people())
	const k = 8
	var sides [k]*table.Table
	for i := range sides {
		sides[i] = edited(t, base, func(e *table.Editor) error {
			return e.Update(keys[i*60], rebaseRow(int64(i*60), fmt.Sprintf("side %d", i)))
		})
	}
	counted := &puts{Store: s}
	cur := base.Root()
	for _, side := range sides {
		var err error
		if cur, err = m.Rebase(ctx, base.Root(), side.Root(), cur, counted); err != nil {
			t.Fatal(err)
		}
	}
	oneByOne := counted.n.Swap(0)
	batch, err := m.Batch(ctx, counted, base.Root())
	if err != nil {
		t.Fatal(err)
	}
	for _, side := range sides {
		p, err := batch.Check(ctx, base.Root(), side.Root())
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
