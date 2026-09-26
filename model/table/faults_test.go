package table_test

import (
	"context"
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

var errFault = errors.New("injected: the store is unreachable")

// faulty is a store whose reads or writes fail on demand.
type faulty struct {
	*memstore.Store
	reads, writes bool
}

func (f *faulty) Get(ctx context.Context, h hash.Hash) ([]byte, error) {
	if f.reads {
		return nil, errFault
	}
	return f.Store.Get(ctx, h)
}

func (f *faulty) Put(ctx context.Context, b []byte) (hash.Hash, error) {
	if f.writes {
		return hash.Hash{}, errFault
	}
	return f.Store.Put(ctx, b)
}

// A store fault is reported by every operation that meets it, never
// hidden as a missing row or an empty table: reads that fail fail Open,
// Get, a scan, an index lookup, Validate, Walk, Diff and Merge; writes
// that fail fail Create and Flush, and the table is as it was.
func TestStoreFaultsAreReported(t *testing.T) {
	f := &faulty{Store: memstore.New()}
	m := table.Model{Config: cfg()}
	tb := seeded(t, f, 3000) // several leaves, so a scan reads as it goes
	next := edit(t, tb, func(e *table.Editor) error {
		return e.Update(table.Key{int64(1)}, person(1, "changed", int32(21), "p1@x"))
	})
	scan, err := tb.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := tb.IndexLookup(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	f.reads = true
	check := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, errFault) {
			t.Errorf("%s with an unreachable store: %v, want the store's error", name, err)
		}
	}
	_, err = table.Open(ctx, f, cfg(), tb.Root())
	check("Open", err)
	_, _, err = tb.Get(ctx, table.Key{int64(1)})
	check("Get", err)
	drainErr := func(rows *table.Rows) error {
		for {
			_, _, ok, err := rows.Next()
			if err != nil || !ok {
				return err
			}
		}
	}
	check("Scan's Next", drainErr(scan))
	check("IndexLookup's Next", drainErr(lookup))
	check("Validate", m.Validate(ctx, tb.Root(), f))
	check("Walk", m.Walk(ctx, tb.Root(), f, func(hash.Hash, bool) (bool, error) { return true, nil }))
	_, err = m.Diff(ctx, tb.Root(), next.Root(), f)
	check("Diff", err)
	_, err = m.Merge(ctx, tb.Root(), next.Root(), next.Root(), f)
	check("Merge", err)
	// A key inside the tree: its duplicate check must read a leaf. A key
	// past the last one is answered from the root node the map holds
	// (core v0.3.0), and reads nothing.
	e := tb.Edit()
	_, err = e.Insert(person(1500, "mid", nil, "m@x"))
	check("Insert (its duplicate check reads)", err)
	f.reads = false
	f.writes = true
	_, err = table.Create(ctx, f, cfg(), people())
	check("Create", err)
	e = tb.Edit()
	if _, err := e.Insert(person(9000, "nine", nil, "n@x")); err != nil {
		t.Fatal(err)
	}
	_, err = e.Flush(ctx)
	check("Flush", err)
	f.writes = false
	if got, ok, err := tb.Get(ctx, table.Key{int64(9000)}); err != nil || ok {
		t.Fatalf("after a failed Flush, Get(9000) = %v, %t, %v; want no row", got, ok, err)
	}
	if err := m.Validate(ctx, tb.Root(), f); err != nil {
		t.Fatalf("positive control: the table validates once the store is back: %v", err)
	}
	// A root that is not a table's, and a root of another format or depth.
	if _, err := table.Open(ctx, f, cfg(), model.Root{Hash: hash.Sum([]byte("x")), Format: table.Format}); !errors.Is(err, chunk.ErrNotFound) {
		t.Errorf("Open of a missing root: %v, want ErrNotFound", err)
	}
	r := tb.Root()
	r.Format = 2
	if _, err := table.Open(ctx, f, cfg(), r); !errors.Is(err, model.ErrUnknownModel) {
		t.Errorf("Open of format 2: %v, want ErrUnknownModel", err)
	}
	if err := m.Walk(ctx, r, f, nil); !errors.Is(err, model.ErrUnknownModel) {
		t.Errorf("Walk of format 2: %v, want ErrUnknownModel", err)
	}
	r = tb.Root()
	r.Depth = 1
	if _, err := table.Open(ctx, f, cfg(), r); !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("Open of a root claiming depth 1: %v, want ErrCorrupt", err)
	}
	if err := m.Walk(ctx, r, f, nil); !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("Walk of a root claiming depth 1: %v, want ErrCorrupt", err)
	}
	r = tb.Root()
	r.Size++
	if _, err := table.Open(ctx, f, cfg(), r); !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("Open of a root claiming one row more: %v, want ErrCorrupt", err)
	}
	stopped := 0
	if err := m.Walk(ctx, tb.Root(), f, func(hash.Hash, bool) (bool, error) { stopped++; return false, nil }); err != nil || stopped != 1 {
		t.Errorf("a Walk told not to go into the root visited %d chunks, %v; want 1 and no error", stopped, err)
	}
}
