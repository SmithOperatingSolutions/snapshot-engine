package kv_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"pgregory.net/rapid"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

// An editor reads the map it would flush: a Get sees its own sets, deletes
// and additions over the stored map, a Scan from any key walks them in key
// order with the stored entries, and neither writes a chunk. The map the
// same edits flush is the authority.
func TestAnEditorReadsWhatItWouldFlush(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := memstore.New()
		n := rapid.IntRange(0, 200).Draw(rt, "keys")
		base := map[string]kv.Value{}
		for i := range n {
			base[fmt.Sprintf("k%03d", i*2)] = bytesValue(fmt.Sprintf("v%d", i))
		}
		root := write(t, s, base)
		counted := &puts{Store: s}
		m, err := kv.Open(ctx, counted, cfg(), root)
		if err != nil {
			rt.Fatal(err)
		}
		edits := map[string]*kv.Value{}
		for i := range rapid.IntRange(0, 40).Draw(rt, "edits") {
			key := fmt.Sprintf("k%03d", rapid.IntRange(0, 2*n+6).Draw(rt, fmt.Sprintf("key%d", i))) // odd: an addition
			edits[key] = nil
			if rapid.Bool().Draw(rt, fmt.Sprintf("set%d", i)) {
				v := bytesValue(rapid.StringN(0, 20, -1).Draw(rt, fmt.Sprintf("v%d", i)))
				edits[key] = &v
			}
		}
		apply := func(e *kv.MapEditor) {
			for k, v := range edits {
				var err error
				if v == nil {
					err = e.Delete([]byte(k))
				} else {
					err = e.Set([]byte(k), *v)
				}
				if err != nil {
					rt.Fatal(err)
				}
			}
		}
		e := m.Edit()
		apply(e)
		authority := m.Edit()
		apply(authority)
		flushed, err := authority.Flush(ctx)
		if err != nil {
			rt.Fatal(err)
		}
		counted.n.Store(0)

		var keys []string
		for k := range base {
			keys = append(keys, k)
		}
		for k := range edits {
			keys = append(keys, k)
		}
		keys = append(keys, "k000", "k999", "a")
		sort.Strings(keys)
		for _, k := range keys {
			want, wantOK, err := flushed.Get(ctx, []byte(k))
			if err != nil {
				rt.Fatal(err)
			}
			got, ok, err := e.Get(ctx, []byte(k))
			if err != nil {
				rt.Fatal(err)
			}
			if ok != wantOK || string(got.Bytes) != string(want.Bytes) {
				rt.Fatalf("the editor reads %q as %q, %t; the map it would flush holds %q, %t: a transaction would not read its own write", k, got.Bytes, ok, want.Bytes, wantOK)
			}
		}
		from := []byte(keys[rapid.IntRange(0, len(keys)-1).Draw(rt, "from")])
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
			rt.Fatalf("the editor's scan from %q walks %v; the map it would flush walks %v", from, got, want)
		}
		if n := counted.n.Load(); n != 0 {
			rt.Fatalf("the editor's reads wrote %d chunks: reading your own writes flushed them", n)
		}
	})
}

func scanAll(rt *rapid.T, it *kv.Entries) []string {
	var out []string
	for {
		k, v, ok, err := it.Next()
		if err != nil {
			rt.Fatal(err)
		}
		if !ok {
			return out
		}
		out = append(out, string(k)+"="+string(v.Bytes))
	}
}
