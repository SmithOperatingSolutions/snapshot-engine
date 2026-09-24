package table_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

var ctx = context.Background()

func cfg() prolly.Config { return prolly.DefaultConfig() }

func create(t *testing.T, s chunk.ReadWriter, schema table.Schema) *table.Table {
	t.Helper()
	tb, err := table.Create(ctx, s, cfg(), schema)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return tb
}

func person(id int64, name string, age any, email string) table.Row {
	r := table.Row{1: id, 2: name, 4: email}
	if age != nil {
		r[3] = age
	}
	return r
}

func flush(t *testing.T, e *table.Editor) *table.Table {
	t.Helper()
	tb, err := e.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return tb
}

func all(t *testing.T, rows *table.Rows, err error) (keys []table.Key, out []table.Row) {
	t.Helper()
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return drain(t, rows)
}

func scanAll(t *testing.T, tb *table.Table) ([]table.Key, []table.Row) {
	t.Helper()
	rows, err := tb.Scan(ctx)
	return all(t, rows, err)
}

func lookupAll(t *testing.T, tb *table.Table, index table.Tag, values ...any) ([]table.Key, []table.Row) {
	t.Helper()
	rows, err := tb.IndexLookup(ctx, index, values...)
	return all(t, rows, err)
}

func drain(t *testing.T, rows *table.Rows) (keys []table.Key, out []table.Row) {
	t.Helper()
	for {
		k, r, ok, err := rows.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			return keys, out
		}
		keys = append(keys, k)
		out = append(out, r)
	}
}

func sameRow(a, b table.Row) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !sameValue(v, b[k]) {
			return false
		}
	}
	return true
}

// Insert, update, delete and point lookup by primary key; a scan walks
// rows in key order, and the root's size is the row count.
func TestRowsRoundTripByPrimaryKey(t *testing.T) {
	s := memstore.New()
	tb := create(t, s, people())
	if tb.Root().Size != 0 || tb.Root().Format != table.Format {
		t.Fatalf("an empty table's root is %+v, want size 0, format %d", tb.Root(), table.Format)
	}
	e := tb.Edit()
	rows := []table.Row{person(30, "cara", int32(41), "c@x"), person(10, "ann", nil, "a@x"), person(20, "bob", int32(7), "b@x")}
	for _, r := range rows {
		k, err := e.Insert(r)
		if err != nil {
			t.Fatalf("Insert(%v): %v", r, err)
		}
		if len(k) != 1 || k[0] != r[1] {
			t.Fatalf("Insert returned key %v, want the id %v", k, r[1])
		}
	}
	tb = flush(t, e)
	if tb.Root().Size != 3 {
		t.Fatalf("after three inserts the root says %d rows", tb.Root().Size)
	}
	got, ok, err := tb.Get(ctx, table.Key{int64(10)})
	if err != nil || !ok || !sameRow(got, rows[1]) {
		t.Fatalf("Get(10) = %v, %t, %v; want ann's row", got, ok, err)
	}
	if _, ok, err := tb.Get(ctx, table.Key{int64(99)}); err != nil || ok {
		t.Fatalf("Get(99) = %t, %v; want no row", ok, err)
	}
	keys, scanned := scanAll(t, tb)
	if len(scanned) != 3 || keys[0][0] != int64(10) || keys[1][0] != int64(20) || keys[2][0] != int64(30) {
		t.Fatalf("Scan gave keys %v, want 10, 20, 30 in key order", keys)
	}
	if !sameRow(scanned[2], rows[0]) {
		t.Fatalf("Scan's third row is %v, want cara's", scanned[2])
	}
	e = tb.Edit()
	if err := e.Update(table.Key{int64(20)}, person(20, "bob", int32(9), "bob@x")); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := e.Update(table.Key{int64(20)}, person(20, "bob", int32(8), "bob@x")); err != nil { // the second edit of a row in one editor sees the first
		t.Fatalf("a second Update: %v", err)
	}
	if err := e.Delete(table.Key{int64(30)}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := e.Insert(person(30, "cara again", nil, "c2@x")); err != nil { // a key deleted in this editor may be inserted again
		t.Fatalf("Insert after Delete in one editor: %v", err)
	}
	if err := e.Delete(table.Key{int64(30)}); err != nil {
		t.Fatalf("Delete again: %v", err)
	}
	tb = flush(t, e)
	if got, ok, _ := tb.Get(ctx, table.Key{int64(20)}); !ok || got[3] != int32(8) || got[4] != "bob@x" {
		t.Fatalf("after Update, Get(20) = %v, %t", got, ok)
	}
	if _, ok, _ := tb.Get(ctx, table.Key{int64(30)}); ok {
		t.Fatal("after Delete, Get(30) finds the row")
	}
	if tb.Root().Size != 2 {
		t.Fatalf("after an update and a delete the root says %d rows, want 2", tb.Root().Size)
	}
	reopened, err := table.Open(ctx, s, cfg(), tb.Root())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, again := scanAll(t, reopened); len(again) != 2 || !sameRow(again[0], rows[1]) {
		t.Fatalf("a reopened table scans %v", again)
	}
}

// A table without a declared primary key gives each row a hidden 16-byte
// row id: two inserts of equal rows are two rows, found by the ids
// Insert returned.
func TestATableWithoutAKeyGetsHiddenRowIDs(t *testing.T) {
	s := memstore.New()
	schema := table.Schema{Columns: []table.Column{{Tag: 1, Name: "note", Type: table.TypeText}}}
	tb := create(t, s, schema)
	e := tb.Edit()
	k1, err := e.Insert(table.Row{1: "same"})
	if err != nil {
		t.Fatal(err)
	}
	k2, err := e.Insert(table.Row{1: "same"})
	if err != nil {
		t.Fatal(err)
	}
	id1, ok1 := k1[0].(table.RowID)
	id2, ok2 := k2[0].(table.RowID)
	if len(k1) != 1 || !ok1 || !ok2 || id1 == id2 || id1 == (table.RowID{}) {
		t.Fatalf("the keys are %v and %v, want two distinct nonzero row ids", k1, k2)
	}
	tb = flush(t, e)
	if tb.Root().Size != 2 {
		t.Fatalf("two equal rows inserted into a keyless table make %d rows, want 2", tb.Root().Size)
	}
	if got, ok, err := tb.Get(ctx, k2); err != nil || !ok || got[1] != "same" {
		t.Fatalf("Get by the returned row id = %v, %t, %v", got, ok, err)
	}
	e = tb.Edit()
	if err := e.Delete(k1); err != nil {
		t.Fatal(err)
	}
	if tb = flush(t, e); tb.Root().Size != 1 {
		t.Fatalf("after deleting one of two equal rows %d remain", tb.Root().Size)
	}
}

// A value that does not fit its column is refused on write with ErrValue,
// and nothing is written: a Flush after the refusal leaves the table as it
// was. A row missing a non-nullable column, a duplicate key, an update or
// delete of a missing key, and a key over the map's limit are refused too.
func TestWritesThatDoNotFitAreRefusedAndWriteNothing(t *testing.T) {
	s := memstore.New()
	tb := create(t, s, people())
	e := tb.Edit()
	if _, err := e.Insert(person(1, "ann", nil, "a@x")); err != nil {
		t.Fatal(err)
	}
	tb = flush(t, e)
	before := tb.Root()
	e = tb.Edit()
	long := strings.Repeat("x", 300)
	if _, err := e.Insert(person(2, "bob", nil, long)); !errors.Is(err, table.ErrValue) {
		t.Fatalf("300 characters into varchar(255): %v, want ErrValue", err)
	}
	if err := e.Update(table.Key{int64(1)}, person(1, "ann", nil, long)); !errors.Is(err, table.ErrValue) {
		t.Fatalf("Update with 300 characters into varchar(255): %v, want ErrValue", err)
	}
	if _, err := e.Insert(table.Row{1: int64(3), 4: "c@x"}); !errors.Is(err, table.ErrValue) {
		t.Fatalf("a row without its non-nullable name: %v, want ErrValue", err)
	}
	if _, err := e.Insert(table.Row{1: int64(3), 2: "c", 4: "c@x", 9: "extra"}); !errors.Is(err, table.ErrValue) {
		t.Fatalf("a row with a column the schema lacks: %v, want ErrValue", err)
	}
	if _, err := e.Insert(person(1, "ann again", nil, "a2@x")); !errors.Is(err, table.ErrDuplicate) {
		t.Fatalf("a second row with key 1: %v, want ErrDuplicate", err)
	}
	if err := e.Update(table.Key{int64(7)}, person(7, "x", nil, "x@x")); !errors.Is(err, table.ErrNotFound) {
		t.Fatalf("Update of a missing key: %v, want ErrNotFound", err)
	}
	if err := e.Delete(table.Key{int64(7)}); !errors.Is(err, table.ErrNotFound) {
		t.Fatalf("Delete of a missing key: %v, want ErrNotFound", err)
	}
	if err := e.Update(table.Key{"one"}, person(1, "ann", nil, "a@x")); !errors.Is(err, table.ErrValue) {
		t.Fatalf("a key of the wrong type: %v, want ErrValue", err)
	}
	after := flush(t, e)
	if after.Root() != before {
		t.Fatalf("refused writes changed the table: root %+v, was %+v", after.Root(), before)
	}
	if got, ok, _ := after.Get(ctx, table.Key{int64(1)}); !ok || got[4] != "a@x" {
		t.Fatalf("ann's row is %v after the refused writes", got)
	}
	wide := table.Schema{Columns: []table.Column{{Tag: 1, Name: "k", Type: table.TypeText}}, PrimaryKey: []table.Tag{1}}
	e = create(t, s, wide).Edit()
	if _, err := e.Insert(table.Row{1: strings.Repeat("k", prolly.MaxKeySize)}); !errors.Is(err, table.ErrValue) {
		t.Fatalf("a key over the map's limit: %v, want ErrValue", err)
	}
}

// A secondary index returns the rows a full scan with a filter returns,
// in index order, after inserts, updates and deletes alike: the index is
// written with the primary map, never apart from it.
func TestAnIndexReturnsWhatAScanWithAFilterReturns(t *testing.T) {
	s := memstore.New()
	tb := create(t, s, people())
	e := tb.Edit()
	for i := int64(0); i < 60; i++ {
		var age any
		if i%7 != 0 {
			age = int32(20 + i%5)
		}
		if _, err := e.Insert(person(i, fmt.Sprintf("p%02d", i), age, fmt.Sprintf("p%d@x", i))); err != nil {
			t.Fatal(err)
		}
	}
	tb = flush(t, e)
	check := func(tb *table.Table, want any) {
		t.Helper()
		var filtered []int64
		_, rows := scanAll(t, tb)
		for _, r := range rows {
			if sameValue(r[3], want) {
				filtered = append(filtered, r[1].(int64))
			}
		}
		sort.Slice(filtered, func(i, j int) bool { return filtered[i] < filtered[j] })
		var indexed []int64
		_, viaIndex := lookupAll(t, tb, 10, want)
		for _, r := range viaIndex {
			indexed = append(indexed, r[1].(int64))
		}
		if fmt.Sprint(indexed) != fmt.Sprint(filtered) {
			t.Fatalf("IndexLookup(age = %v) = %v, a scan with that filter gives %v", want, indexed, filtered)
		}
	}
	for _, age := range []any{int32(20), int32(24), int32(99), nil} {
		check(tb, age)
	}
	e = tb.Edit()
	if err := e.Update(table.Key{int64(1)}, person(1, "p01", int32(99), "p1@x")); err != nil {
		t.Fatal(err)
	}
	if err := e.Update(table.Key{int64(2)}, person(2, "p02", nil, "p2@x")); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete(table.Key{int64(3)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Insert(person(100, "new", int32(24), "n@x")); err != nil {
		t.Fatal(err)
	}
	tb = flush(t, e)
	for _, age := range []any{int32(20), int32(22), int32(23), int32(24), int32(99), nil} {
		check(tb, age)
	}
	m := table.Model{Config: cfg()}
	if err := m.Validate(ctx, tb.Root(), s); err != nil {
		t.Fatalf("a table whose indexes were maintained through updates and deletes does not validate: %v", err)
	}
}

// The root record is the documented layout, and a record that is not what
// the encoder writes is refused (the fuzz target holds the decoder to it).
func TestTheRootIsTheDocumentedRecord(t *testing.T) {
	catalog, err := table.EncodeCatalog(people())
	if err != nil {
		t.Fatal(err)
	}
	p := hash.Sum([]byte("primary"))
	ix := hash.Sum([]byte("index"))
	got := table.EncodeRoot(catalog, p, 7, []table.IndexRoot{{Tag: 10, Root: ix, Count: 7}})
	var want []byte
	want = append(want, "VDTR"...)
	want = binary.LittleEndian.AppendUint16(want, 1)
	want = binary.AppendUvarint(want, uint64(len(catalog)))
	want = append(want, catalog...)
	want = append(want, p[:]...)
	want = binary.LittleEndian.AppendUint64(want, 7)
	want = append(want, 1)
	want = binary.LittleEndian.AppendUint16(want, 10)
	want = append(want, ix[:]...)
	want = binary.LittleEndian.AppendUint64(want, 7)
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeRoot =\n%x\nwant\n%x", got, want)
	}
	c, pr, n, ixs, err := table.DecodeRoot(want)
	wantIx := table.IndexRoot{Tag: 10, Root: ix, Count: 7}
	if err != nil || !bytes.Equal(c, catalog) || pr != p || n != 7 || len(ixs) != 1 || ixs[0] != wantIx {
		t.Fatalf("DecodeRoot = %x, %x, %d, %v, %v", c, pr, n, ixs, err)
	}
	forged := map[string][]byte{
		"empty":         {},
		"wrong magic":   append([]byte("VDTX"), want[4:]...),
		"version 2":     append(append([]byte("VDTR"), 2, 0), want[6:]...),
		"trailing byte": append(bytes.Clone(want), 0),
		"indexes over the limit": func() []byte {
			i := len(want) - (2 + 32 + 8) - 1 // the index count, one byte before the one index
			return append(binary.AppendUvarint(bytes.Clone(want[:i]), table.MaxIndexes+1), want[i+1:]...)
		}(),
		"fewer indexes than the catalog": table.EncodeRoot(catalog, p, 7, nil),
		"a catalog that is garbage": func() []byte {
			b := bytes.Clone(want)
			b[7] ^= 0xFF
			return b
		}(),
		"an index the catalog lacks": func() []byte {
			b := bytes.Clone(want)
			b[len(b)-42] = 11
			return b
		}(),
		"an index count that disagrees with the rows": func() []byte {
			b := bytes.Clone(want)
			b[len(b)-1] = 1
			return b
		}(),
	}
	for i := 1; i < len(want); i += 5 {
		forged[fmt.Sprintf("truncated to %d bytes", i)] = want[:i]
	}
	for name, b := range forged {
		if _, _, _, _, err := table.DecodeRoot(b); !errors.Is(err, chunk.ErrCorrupt) {
			t.Errorf("%s: DecodeRoot = %v, want ErrCorrupt", name, err)
		}
	}
}

func FuzzDecodeRoot(f *testing.F) {
	catalog, _ := table.EncodeCatalog(people())
	f.Add(table.EncodeRoot(catalog, hash.Sum([]byte("p")), 3, []table.IndexRoot{{Tag: 10, Root: hash.Sum([]byte("i")), Count: 3}}))
	f.Add([]byte("VDTR"))
	f.Fuzz(func(t *testing.T, b []byte) {
		c, p, n, ixs, err := table.DecodeRoot(b)
		if err != nil {
			return
		}
		if back := table.EncodeRoot(c, p, n, ixs); !bytes.Equal(back, b) {
			t.Fatalf("a root record that decoded re-encodes differently")
		}
	})
}

// An index that drifted from the rows is refused: an entry naming a row the
// table lacks, or an entry carrying a value, fails Validate and the lookup
// that meets it; a root record whose count disagrees with its map, or a root
// chunk that is not a record at all, fails Open and Walk.
func TestAnIndexThatDriftedFromTheRowsIsRefused(t *testing.T) {
	s := memstore.New()
	m := table.Model{Config: cfg()}
	tb := seeded(t, s, 4)
	rec, err := s.Get(ctx, tb.Root().Hash)
	if err != nil {
		t.Fatal(err)
	}
	catalog, primary, rows, indexes, err := table.DecodeRoot(rec)
	if err != nil {
		t.Fatal(err)
	}
	ixMap, err := prolly.Open(ctx, s, cfg(), indexes[0].Root)
	if err != nil {
		t.Fatal(err)
	}
	it, err := ixMap.IterRange(ctx, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _, err := it.Next()
	if err != nil {
		t.Fatal(err)
	}
	forge := func(name string, edit func(e *prolly.Editor) error, wantErr string) {
		t.Helper()
		e := ixMap.Editor()
		if err := edit(e); err != nil {
			t.Fatal(err)
		}
		forged, err := e.Flush(ctx)
		if err != nil {
			t.Fatal(err)
		}
		root := table.EncodeRoot(catalog, primary, rows, []table.IndexRoot{{Tag: 10, Root: forged.Root(), Count: forged.Count()}})
		h, err := s.Put(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		err = m.Validate(ctx, model.Root{Hash: h, Size: rows, Format: table.Format}, s)
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%s: Validate = %v, want an error saying %q", name, err, wantErr)
		}
	}
	fake := append(bytes.Clone(first[:len(first)-9]), first[len(first)-9:]...) // the same age cell, then a key of our own
	binary.BigEndian.PutUint64(fake[len(fake)-8:], (1<<63)|77)                 // key 77, no such row
	forge("an entry naming a row the table lacks", func(e *prolly.Editor) error {
		if err := e.Delete(first); err != nil {
			return err
		}
		return e.Put(fake, nil)
	}, "names a row that is not in the table")
	forge("an entry with a value", func(e *prolly.Editor) error { return e.Put(first, []byte{1}) }, "an index entry with a value")
	lying := table.EncodeRoot(catalog, primary, rows+1, []table.IndexRoot{{Tag: 10, Root: indexes[0].Root, Count: rows + 1}})
	h, err := s.Put(ctx, lying)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.Open(ctx, s, cfg(), model.Root{Hash: h, Size: rows + 1, Format: table.Format}); !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("Open of a root record claiming one row more than its map holds: %v, want ErrCorrupt", err)
	}
	garbage, err := s.Put(ctx, []byte("not a table"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Walk(ctx, model.Root{Hash: garbage, Format: table.Format}, s, func(hash.Hash, bool) (bool, error) { return true, nil }); !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("Walk of a root chunk that is not a record: %v, want ErrCorrupt", err)
	}
	if got := table.Type(99).String(); got != "type(99)" {
		t.Errorf("Type(99).String() = %q", got)
	}
}
