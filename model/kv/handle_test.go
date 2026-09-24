package kv_test

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

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

func flushMap(t *testing.T, e *kv.MapEditor) *kv.Map {
	t.Helper()
	m, err := e.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return m
}

func chunks(t *testing.T, s *memstore.Store) int64 {
	t.Helper()
	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return st.Chunks
}

// A map takes point edits and reads them back, of every kind, and so does
// the map opened again from its root; deleting a key that is not there is
// a no-op.
func TestAMapTakesPointEditsAndReadsThemBack(t *testing.T) {
	s := memstore.New()
	m, err := kv.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatalf("Empty: %v", err)
	}
	e := m.Edit()
	want := map[string]kv.Value{
		"a":       bytesValue("alpha"),
		"hits":    {Kind: kv.Counter, Counter: -7},
		"profile": {Kind: kv.Hash, Fields: map[string][]byte{"name": []byte("ada"), "city": []byte("London")}},
		"queue":   {Kind: kv.Sequence, Seq: [][]byte{[]byte("x"), []byte("y")}},
	}
	for k, v := range want {
		if err := e.Set([]byte(k), v); err != nil {
			t.Fatalf("Set(%s): %v", k, err)
		}
	}
	if err := e.Set([]byte("gone"), bytesValue("soon")); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete([]byte("gone")); err != nil {
		t.Fatal(err)
	}
	m = flushMap(t, e)
	for _, opened := range []string{"as flushed", "reopened from its root"} {
		if opened != "as flushed" {
			if m, err = kv.Open(ctx, s, cfg(), m.Root()); err != nil {
				t.Fatalf("Open: %v", err)
			}
		}
		for k, v := range want {
			got, ok, err := m.Get(ctx, []byte(k))
			if err != nil || !ok || !sameValue(t, got, v) {
				t.Errorf("%s: Get(%s) = %+v, %t, %v; want %+v", opened, k, got, ok, err, v)
			}
		}
		for _, absent := range []string{"gone", "never"} {
			if _, ok, err := m.Get(ctx, []byte(absent)); ok || err != nil {
				t.Errorf("%s: Get(%s) found it (%t, %v); want absent", opened, absent, ok, err)
			}
		}
		if m.Root().Size != uint64(len(want)) || m.Root().Format != kv.Format {
			t.Errorf("%s: the root claims %d entries of format %d, want %d of %d", opened, m.Root().Size, m.Root().Format, len(want), kv.Format)
		}
	}
	before := m.Root()
	e = m.Edit()
	if err := e.Delete([]byte("never")); err != nil {
		t.Fatalf("deleting a key that is not there: %v, want a no-op", err)
	}
	if got := flushMap(t, e).Root(); got != before {
		t.Errorf("deleting a key that is not there moved the root %+v to %+v", before, got)
	}
}

// A scan walks the entries in key order from its start key, inclusive;
// past the last key it walks nothing.
func TestAMapScansInKeyOrderFromAKey(t *testing.T) {
	s := memstore.New()
	m, err := kv.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatal(err)
	}
	e := m.Edit()
	var keys []string
	for i := 0; i < 20; i++ {
		keys = append(keys, fmt.Sprintf("k%02d", i))
	}
	keys = append(keys, string([]byte{0x00, 0x01}), "\xff")
	for _, k := range keys {
		if err := e.Set([]byte(k), bytesValue("v"+k)); err != nil {
			t.Fatal(err)
		}
	}
	m = flushMap(t, e)
	sort.Strings(keys)
	scan := func(from []byte) []string {
		t.Helper()
		it, err := m.Scan(ctx, from)
		if err != nil {
			t.Fatalf("Scan(%q): %v", from, err)
		}
		var out []string
		for {
			k, v, ok, err := it.Next()
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			if !ok {
				return out
			}
			if string(v.Bytes) != "v"+string(k) {
				t.Errorf("the entry at %q holds %q", k, v.Bytes)
			}
			out = append(out, string(k))
		}
	}
	if got := scan(nil); strings.Join(got, ",") != strings.Join(keys, ",") {
		t.Errorf("Scan(nil) = %q, want every key in byte order %q", got, keys)
	}
	if got := scan([]byte("k10")); strings.Join(got, ",") != "k10,k11,k12,k13,k14,k15,k16,k17,k18,k19,\xff" {
		t.Errorf("Scan(k10) = %q, want k10 onward, k10 included", got)
	}
	if got := scan([]byte("k105")); len(got) == 0 || got[0] != "k11" {
		t.Errorf("Scan(k105) = %q, want to start at the next key, k11", got)
	}
	if got := scan([]byte("\xff\xff")); len(got) != 0 {
		t.Errorf("Scan past the last key = %q, want nothing", got)
	}
}

// A key or value the model refuses is refused at the edit, and nothing of
// it is written: the flushed root is the root before, and the store holds
// no new chunk. A lookup of a key that cannot exist is refused too.
func TestARefusedEditWritesNothing(t *testing.T) {
	s := memstore.New()
	root, err := kv.Write(ctx, s, cfg(), map[string]kv.Value{"a": bytesValue("1")})
	if err != nil {
		t.Fatal(err)
	}
	m, err := kv.Open(ctx, s, cfg(), root)
	if err != nil {
		t.Fatal(err)
	}
	n := chunks(t, s)
	e := m.Edit()
	for name, err := range map[string]error{
		"an empty key":           e.Set(nil, bytesValue("x")),
		"a key over 4096 bytes":  e.Set(bytes.Repeat([]byte("k"), kv.MaxKeySize+1), bytesValue("x")),
		"a value of no kind":     e.Set([]byte("b"), kv.Value{}),
		"a delete of no key":     e.Delete(nil),
		"a delete too long":      e.Delete(bytes.Repeat([]byte("k"), kv.MaxKeySize+1)),
		"a value over the limit": e.Set([]byte("c"), bytesValue(strings.Repeat("v", kv.MaxValueSize+1))),
	} {
		if !errors.Is(err, kv.ErrKey) && !errors.Is(err, kv.ErrValue) {
			t.Errorf("%s: %v, want ErrKey or ErrValue", name, err)
		}
	}
	if got := flushMap(t, e).Root(); got != root {
		t.Errorf("after refused edits the root moved from %+v to %+v", root, got)
	}
	if got := chunks(t, s); got != n {
		t.Errorf("refused edits wrote %d chunks", got-n)
	}
	if _, _, err := m.Get(ctx, nil); !errors.Is(err, kv.ErrKey) {
		t.Errorf("Get of an empty key = %v, want ErrKey", err)
	}
}

// A map built by point edits, in any order, flushed in several steps, with
// keys set and deleted on the way, has the root Write gives the same
// entries.
func TestAHandlesRootIsWhatWriteWrites(t *testing.T) {
	s := memstore.New()
	entries := sample()
	want, err := kv.Write(ctx, s, cfg(), entries)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	m, err := kv.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatal(err)
	}
	e := m.Edit()
	for i, k := range keys {
		if err := e.Set([]byte(k), entries[k]); err != nil {
			t.Fatal(err)
		}
		if err := e.Set([]byte("tmp:"+k), bytesValue("temporary")); err != nil {
			t.Fatal(err)
		}
		if i%50 == 49 {
			e = flushMap(t, e).Edit()
		}
		if err := e.Delete([]byte("tmp:" + k)); err != nil {
			t.Fatal(err)
		}
	}
	if got := flushMap(t, e).Root(); got != want {
		t.Errorf("the map built by point edits has root %+v, Write gives %+v", got, want)
	}
}

// Open refuses what is not a kv object, and a stored frame that is not a
// value fails the read that meets it, point or scan.
func TestAMapRefusesWhatIsNotOurs(t *testing.T) {
	s := memstore.New()
	root, err := kv.Write(ctx, s, cfg(), map[string]kv.Value{"a": bytesValue("1")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Open(ctx, s, cfg(), model.Root{Hash: root.Hash, Size: root.Size, Format: 2}); !errors.Is(err, model.ErrUnknownModel) {
		t.Errorf("Open of another format = %v, want ErrUnknownModel", err)
	}
	pm, err := prolly.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatal(err)
	}
	pe := pm.Editor()
	if err := pe.Put([]byte("bad"), []byte{0xee}); err != nil {
		t.Fatal(err)
	}
	if pm, err = pe.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := kv.Open(ctx, s, cfg(), model.Root{Hash: pm.Root(), Size: 1, Format: kv.Format})
	if err != nil {
		t.Fatalf("Open of a map whose value is not ours: %v (a point read or scan finds out)", err)
	}
	if _, _, err := m.Get(ctx, []byte("bad")); !errors.Is(err, kv.ErrValue) && !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("Get of a frame that is not a value = %v, want ErrValue", err)
	}
	it, err := m.Scan(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := it.Next(); !errors.Is(err, kv.ErrValue) && !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("a scan meeting a frame that is not a value = %v, want ErrValue", err)
	}
}
