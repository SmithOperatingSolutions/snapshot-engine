package table_test

import (
	"fmt"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"pgregory.net/rapid"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// An editor reads the table it would flush: a Get sees its own inserts,
// updates and deletes over the stored rows, a Scan walks them in key
// order with the stored ones, an index lookup finds the pending rows
// whose indexed column matches and no longer the ones it moved away or
// removed, and none writes a chunk. The table the same edits flush is the
// authority.
func TestAnEditorReadsWhatItWouldFlush(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := memstore.New()
		n := rapid.IntRange(0, 150).Draw(rt, "rows")
		tb := create(t, s, people())
		e0 := tb.Edit()
		for i := range n {
			if _, err := e0.Insert(person(int64(i*2), fmt.Sprintf("p%d", i), int32(i%5), "e")); err != nil {
				rt.Fatal(err)
			}
		}
		tb = flush(t, e0)
		counted := &puts{Store: s}
		var err error
		if tb, err = table.Open(ctx, counted, cfg(), tb.Root()); err != nil {
			rt.Fatal(err)
		}
		type edit struct {
			id  int64
			row table.Row // nil: a delete
		}
		var edits []edit
		seen := map[int64]bool{}
		for i := range rapid.IntRange(0, 30).Draw(rt, "edits") {
			id := int64(rapid.IntRange(0, 2*n+6).Draw(rt, fmt.Sprintf("id%d", i))) // odd: an insert
			if seen[id] {
				continue
			}
			seen[id] = true
			ed := edit{id: id}
			if rapid.Bool().Draw(rt, fmt.Sprintf("write%d", i)) {
				ed.row = person(id, fmt.Sprintf("w%d", i), int32(rapid.IntRange(0, 5).Draw(rt, fmt.Sprintf("age%d", i))), "e")
			}
			edits = append(edits, ed)
		}
		apply := func(e *table.Editor) {
			for _, ed := range edits {
				_, stored, err := tb.Get(ctx, table.Key{ed.id})
				if err != nil {
					rt.Fatal(err)
				}
				switch {
				case ed.row == nil && stored:
					err = e.Delete(table.Key{ed.id})
				case ed.row == nil:
					continue
				case stored:
					err = e.Update(table.Key{ed.id}, ed.row)
				default:
					_, err = e.Insert(ed.row)
				}
				if err != nil {
					rt.Fatal(err)
				}
			}
		}
		e := tb.Edit()
		apply(e)
		authority := tb.Edit()
		apply(authority)
		flushed, err := authority.Flush(ctx)
		if err != nil {
			rt.Fatal(err)
		}
		counted.n.Store(0)

		for id := int64(-1); id <= int64(2*n+7); id++ {
			want, wantOK, err := flushed.Get(ctx, table.Key{id})
			if err != nil {
				rt.Fatal(err)
			}
			got, ok, err := e.Get(ctx, table.Key{id})
			if err != nil {
				rt.Fatal(err)
			}
			if ok != wantOK || fmt.Sprint(got) != fmt.Sprint(want) {
				rt.Fatalf("the editor reads row %d as %v, %t; the table it would flush holds %v, %t: a transaction would not read its own write", id, got, ok, want, wantOK)
			}
		}
		wantRows, err := flushed.Scan(ctx)
		if err != nil {
			rt.Fatal(err)
		}
		gotRows, err := e.Scan(ctx)
		if err != nil {
			rt.Fatal(err)
		}
		if want, got := rowsAll(rt, wantRows), rowsAll(rt, gotRows); fmt.Sprint(got) != fmt.Sprint(want) {
			rt.Fatalf("the editor's scan walks %v; the table it would flush walks %v", got, want)
		}
		age := int32(rapid.IntRange(0, 5).Draw(rt, "lookup age"))
		if wantRows, err = flushed.IndexLookup(ctx, 10, age); err != nil {
			rt.Fatal(err)
		}
		if gotRows, err = e.IndexLookup(ctx, 10, age); err != nil {
			rt.Fatal(err)
		}
		if want, got := rowsAll(rt, wantRows), rowsAll(rt, gotRows); fmt.Sprint(got) != fmt.Sprint(want) {
			rt.Fatalf("the editor's lookup of age %d finds %v; the table it would flush finds %v", age, got, want)
		}
		if n := counted.n.Load(); n != 0 {
			rt.Fatalf("the editor's reads wrote %d chunks: reading your own writes flushed them", n)
		}
	})
}

func rowsAll(rt *rapid.T, rows *table.Rows) []string {
	var out []string
	for {
		k, r, ok, err := rows.Next()
		if err != nil {
			rt.Fatal(err)
		}
		if !ok {
			return out
		}
		out = append(out, fmt.Sprintf("%v=%v", k, r))
	}
}
