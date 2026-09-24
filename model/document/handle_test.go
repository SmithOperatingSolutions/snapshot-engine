package document_test

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
)

func flushCollection(t *testing.T, e *document.CollectionEditor) *document.Collection {
	t.Helper()
	c, err := e.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return c
}

func storeChunks(t *testing.T, s *memstore.Store) int64 {
	t.Helper()
	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return st.Chunks
}

// A collection takes point edits, a document as a tree or as JSON text,
// and reads them back, as flushed and reopened from its root; deleting an
// id that is not there is a no-op.
func TestACollectionTakesPointEditsAndReadsThemBack(t *testing.T) {
	s := memstore.New()
	c, err := document.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatalf("Empty: %v", err)
	}
	e := c.Edit()
	want := map[string]merge.Node{
		"u1": doc(t, `{"name": "ada", "tags": ["math"]}`),
		"u2": doc(t, `{"name": "grace", "address": {"city": "Arlington"}}`),
	}
	if err := e.Put([]byte("u1"), want["u1"]); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := e.PutJSON([]byte("u2"), []byte(`{ "address" : { "city" : "Arlington" }, "name" : "grace" }`)); err != nil {
		t.Fatalf("PutJSON: %v", err)
	}
	if err := e.Put([]byte("gone"), doc(t, `1`)); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete([]byte("gone")); err != nil {
		t.Fatal(err)
	}
	c = flushCollection(t, e)
	for _, opened := range []string{"as flushed", "reopened from its root"} {
		if opened != "as flushed" {
			if c, err = document.Open(ctx, s, cfg(), c.Root()); err != nil {
				t.Fatalf("Open: %v", err)
			}
		}
		for id, n := range want {
			got, ok, err := c.Get(ctx, []byte(id))
			if err != nil || !ok || !got.Equal(n) {
				t.Errorf("%s: Get(%s) = %s, %t, %v; want %s", opened, id, got.Canonical(), ok, err, n.Canonical())
			}
		}
		for _, absent := range []string{"gone", "never"} {
			if _, ok, err := c.Get(ctx, []byte(absent)); ok || err != nil {
				t.Errorf("%s: Get(%s) found it (%t, %v); want absent", opened, absent, ok, err)
			}
		}
		if c.Root().Size != 2 || c.Root().Format != document.Format {
			t.Errorf("%s: the root claims %d records of format %d, want 2 of %d", opened, c.Root().Size, c.Root().Format, document.Format)
		}
	}
	before := c.Root()
	e = c.Edit()
	if err := e.Delete([]byte("never")); err != nil {
		t.Fatalf("deleting an id that is not there: %v, want a no-op", err)
	}
	if got := flushCollection(t, e).Root(); got != before {
		t.Errorf("deleting an id that is not there moved the root %+v to %+v", before, got)
	}
}

// A scan walks the records in id order from its start id, inclusive.
func TestACollectionScansInIDOrderFromAnID(t *testing.T) {
	s := memstore.New()
	c, err := document.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatal(err)
	}
	e := c.Edit()
	var ids []string
	for i := 0; i < 12; i++ {
		ids = append(ids, fmt.Sprintf("d%02d", i))
	}
	ids = append(ids, "\x00first")
	for i, id := range ids {
		if err := e.Put([]byte(id), doc(t, fmt.Sprintf(`{"n": %d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	c = flushCollection(t, e)
	sort.Strings(ids)
	scan := func(from []byte) []string {
		t.Helper()
		it, err := c.Scan(ctx, from)
		if err != nil {
			t.Fatalf("Scan(%q): %v", from, err)
		}
		var out []string
		for {
			id, _, ok, err := it.Next()
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if !ok {
				return out
			}
			out = append(out, string(id))
		}
	}
	if got := scan(nil); strings.Join(got, ",") != strings.Join(ids, ",") {
		t.Errorf("Scan(nil) = %q, want every id in byte order %q", got, ids)
	}
	if got := scan([]byte("d09")); strings.Join(got, ",") != "d09,d10,d11" {
		t.Errorf("Scan(d09) = %q, want d09 onward, d09 included", got)
	}
}

// An id or document the model refuses is refused at the edit, and nothing
// of it is written; a lookup of an id that cannot exist is refused too.
func TestARefusedCollectionEditWritesNothing(t *testing.T) {
	s := memstore.New()
	root, err := document.Write(ctx, s, cfg(), map[string]merge.Node{"a": doc(t, `1`)})
	if err != nil {
		t.Fatal(err)
	}
	c, err := document.Open(ctx, s, cfg(), root)
	if err != nil {
		t.Fatal(err)
	}
	n := storeChunks(t, s)
	e := c.Edit()
	deep := strings.Repeat("[", document.MaxDepth+1) + strings.Repeat("]", document.MaxDepth+1)
	for name, err := range map[string]error{
		"an empty id":                e.Put(nil, doc(t, `1`)),
		"an id over 4096 bytes":      e.Put(bytes.Repeat([]byte("i"), document.MaxIDSize+1), doc(t, `1`)),
		"malformed JSON":             e.PutJSON([]byte("b"), []byte(`{"a": }`)),
		"JSON nested too deep":       e.PutJSON([]byte("b"), []byte(deep)),
		"JSON for an empty id":       e.PutJSON(nil, []byte(`1`)),
		"a delete of no id":          e.Delete(nil),
		"a delete of an id too long": e.Delete(bytes.Repeat([]byte("i"), document.MaxIDSize+1)),
	} {
		if !errors.Is(err, document.ErrID) && !errors.Is(err, document.ErrDocument) {
			t.Errorf("%s: %v, want ErrID or ErrDocument", name, err)
		}
	}
	if got := flushCollection(t, e).Root(); got != root {
		t.Errorf("after refused edits the root moved from %+v to %+v", root, got)
	}
	if got := storeChunks(t, s); got != n {
		t.Errorf("refused edits wrote %d chunks", got-n)
	}
	if _, _, err := c.Get(ctx, nil); !errors.Is(err, document.ErrID) {
		t.Errorf("Get of an empty id = %v, want ErrID", err)
	}
}

// A collection built by point edits, in any order, flushed in steps, with
// records put and deleted on the way, has the root Write gives the same
// records.
func TestACollectionsRootIsWhatWriteWrites(t *testing.T) {
	s := memstore.New()
	records := sample(t)
	for i := 0; i < 120; i++ {
		records[fmt.Sprintf("r%03d", i)] = doc(t, fmt.Sprintf(`{"i": %d, "s": "x%d"}`, i, i*3))
	}
	want, err := document.Write(ctx, s, cfg(), records)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	c, err := document.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatal(err)
	}
	e := c.Edit()
	for i, id := range ids {
		if err := e.Put([]byte(id), records[id]); err != nil {
			t.Fatal(err)
		}
		if err := e.Put([]byte("tmp:"+id), doc(t, `true`)); err != nil {
			t.Fatal(err)
		}
		if i%40 == 39 {
			e = flushCollection(t, e).Edit()
		}
		if err := e.Delete([]byte("tmp:" + id)); err != nil {
			t.Fatal(err)
		}
	}
	if got := flushCollection(t, e).Root(); got != want {
		t.Errorf("the collection built by point edits has root %+v, Write gives %+v", got, want)
	}
}

// Open refuses what is not a document object, and a stored frame that is
// not a record fails the read that meets it, point or scan.
func TestACollectionRefusesWhatIsNotOurs(t *testing.T) {
	s := memstore.New()
	root, err := document.Write(ctx, s, cfg(), map[string]merge.Node{"a": doc(t, `1`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := document.Open(ctx, s, cfg(), model.Root{Hash: root.Hash, Size: root.Size, Format: 2}); !errors.Is(err, model.ErrUnknownModel) {
		t.Errorf("Open of another format = %v, want ErrUnknownModel", err)
	}
	pm, err := prolly.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatal(err)
	}
	pe := pm.Editor()
	if err := pe.Put([]byte("bad"), []byte("not a record")); err != nil {
		t.Fatal(err)
	}
	if pm, err = pe.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := document.Open(ctx, s, cfg(), model.Root{Hash: pm.Root(), Size: 1, Format: document.Format})
	if err != nil {
		t.Fatalf("Open of a collection whose record is not ours: %v (a point read or scan finds out)", err)
	}
	if _, _, err := c.Get(ctx, []byte("bad")); !errors.Is(err, chunk.ErrCorrupt) && !errors.Is(err, document.ErrDocument) {
		t.Errorf("Get of a frame that is not a record = %v, want ErrCorrupt", err)
	}
	it, err := c.Scan(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := it.Next(); !errors.Is(err, chunk.ErrCorrupt) && !errors.Is(err, document.ErrDocument) {
		t.Errorf("a scan meeting a frame that is not a record = %v, want ErrCorrupt", err)
	}
}
