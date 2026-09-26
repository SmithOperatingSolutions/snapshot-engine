package document_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"pgregory.net/rapid"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
)

// An editor reads the collection it would flush: a Get sees its own puts,
// deletes and additions over the stored records, a Scan from any id walks
// them in id order with the stored ones, and neither writes a chunk. The
// collection the same edits flush is the authority.
func TestAnEditorReadsWhatItWouldFlush(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := memstore.New()
		n := rapid.IntRange(0, 200).Draw(rt, "records")
		base := map[string]merge.Node{}
		for i := range n {
			base[fmt.Sprintf("r%03d", i*2)] = mustParse(t, fmt.Sprintf(`{"n":%d}`, i))
		}
		root := write(t, s, base)
		counted := &puts{Store: s}
		c, err := document.Open(ctx, counted, cfg(), root)
		if err != nil {
			rt.Fatal(err)
		}
		edits := map[string]*merge.Node{}
		for i := range rapid.IntRange(0, 40).Draw(rt, "edits") {
			id := fmt.Sprintf("r%03d", rapid.IntRange(0, 2*n+6).Draw(rt, fmt.Sprintf("id%d", i))) // odd: an addition
			edits[id] = nil
			if rapid.Bool().Draw(rt, fmt.Sprintf("put%d", i)) {
				node := mustParse(t, fmt.Sprintf(`{"e":%d}`, rapid.IntRange(0, 1000).Draw(rt, fmt.Sprintf("v%d", i))))
				edits[id] = &node
			}
		}
		apply := func(e *document.CollectionEditor) {
			for id, node := range edits {
				var err error
				if node == nil {
					err = e.Delete([]byte(id))
				} else {
					err = e.Put([]byte(id), *node)
				}
				if err != nil {
					rt.Fatal(err)
				}
			}
		}
		e := c.Edit()
		apply(e)
		authority := c.Edit()
		apply(authority)
		flushed, err := authority.Flush(ctx)
		if err != nil {
			rt.Fatal(err)
		}
		counted.n.Store(0)

		var ids []string
		for id := range base {
			ids = append(ids, id)
		}
		for id := range edits {
			ids = append(ids, id)
		}
		ids = append(ids, "r000", "r999", "a")
		sort.Strings(ids)
		for _, id := range ids {
			want, wantOK, err := flushed.Get(ctx, []byte(id))
			if err != nil {
				rt.Fatal(err)
			}
			got, ok, err := e.Get(ctx, []byte(id))
			if err != nil {
				rt.Fatal(err)
			}
			if ok != wantOK || string(document.Encode(got)) != string(document.Encode(want)) {
				rt.Fatalf("the editor reads %q as %s, %t; the collection it would flush holds %s, %t: a transaction would not read its own write", id, document.Encode(got), ok, document.Encode(want), wantOK)
			}
		}
		from := []byte(ids[rapid.IntRange(0, len(ids)-1).Draw(rt, "from")])
		if rapid.Bool().Draw(rt, "from the start") {
			from = nil
		}
		wantIt, err := flushed.Scan(ctx, from)
		if err != nil {
			rt.Fatal(err)
		}
		gotIt, err := e.Scan(ctx, from)
		if err != nil {
			rt.Fatal(err)
		}
		want, got := scanAll(rt, wantIt), scanAll(rt, gotIt)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			rt.Fatalf("the editor's scan from %q walks %v; the collection it would flush walks %v", from, got, want)
		}
		if n := counted.n.Load(); n != 0 {
			rt.Fatalf("the editor's reads wrote %d chunks: reading your own writes flushed them", n)
		}
	})
}

func scanAll(rt *rapid.T, it *document.Records) []string {
	var out []string
	for {
		id, node, ok, err := it.Next()
		if err != nil {
			rt.Fatal(err)
		}
		if !ok {
			return out
		}
		out = append(out, string(id)+"="+string(document.Encode(node)))
	}
}
