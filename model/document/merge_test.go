package document_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
)

func write(t *testing.T, s *memstore.Store, records map[string]merge.Node) model.Root {
	t.Helper()
	root, err := document.Write(ctx, s, cfg(), records)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func changes(t *testing.T, s *memstore.Store, from, to model.Root) []string {
	t.Helper()
	it, err := (document.Model{Config: cfg()}).Diff(ctx, from, to, s)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	var out []string
	for {
		c, ok, err := it.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		kind := map[model.ChangeKind]string{model.Added: "added", model.Removed: "removed", model.Modified: "modified"}[c.Kind]
		out = append(out, kind+" "+string(c.Location))
	}
}

// Diff is one change per record, located by its id, in id order; a record
// whose text is the same is no change.
func TestDiffIsOneChangePerRecord(t *testing.T) {
	s := memstore.New()
	from := write(t, s, map[string]merge.Node{"a": doc(t, `{"x": 1}`), "b": doc(t, `{"x": 2}`), "c": doc(t, `{"x": 3}`)})
	to := write(t, s, map[string]merge.Node{"a": doc(t, `{"x": 1}`), "b": doc(t, `{"x": 2, "y": 0}`), "d": doc(t, `{}`)})
	got := changes(t, s, from, to)
	want := []string{"modified b", "removed c", "added d"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("Diff = %v, want %v", got, want)
	}
	if got := changes(t, s, from, from); len(got) != 0 {
		t.Errorf("Diff of an object with itself = %v, want nothing", got)
	}
}

func mergeOf(t *testing.T, s *memstore.Store, base, ours, theirs model.Root) model.MergeResult {
	t.Helper()
	r, err := (document.Model{Config: cfg()}).Merge(ctx, base, ours, theirs, s)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return r
}

func readBack(t *testing.T, s *memstore.Store, root model.Root) map[string]merge.Node {
	t.Helper()
	got, err := document.Read(ctx, s, cfg(), root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return got
}

// Merge is per record and, within a record both sides changed, per field:
// two writers on different fields of one record both land, nested fields
// included; a field both set the same way is clean; a record only one side
// changed is that side's; a record added on both sides the same way is
// clean. The merged root reads back as a collection.
func TestMergeCombinesFieldsOfOneRecord(t *testing.T) {
	s := memstore.New()
	base := write(t, s, map[string]merge.Node{
		"u1": doc(t, `{"name": "ada", "age": 36, "address": {"city": "London", "zip": "N1"}}`),
		"u2": doc(t, `{"name": "grace"}`),
		"u3": doc(t, `{"name": "linus"}`),
	})
	ours := write(t, s, map[string]merge.Node{
		"u1": doc(t, `{"name": "ada", "age": 37, "address": {"city": "London", "zip": "N1"}, "tag": "x"}`), // age changed, tag added
		"u2": doc(t, `{"name": "grace hopper"}`),                                                           // only ours changed u2
		"u3": doc(t, `{"name": "linus"}`),
		"u4": doc(t, `{"name": "new"}`), // both add u4 the same way
	})
	theirs := write(t, s, map[string]merge.Node{
		"u1": doc(t, `{"name": "ada lovelace", "age": 36, "address": {"city": "London", "zip": "N1 9GU"}, "tag": "x"}`), // name and address/zip changed, tag added the same way
		"u2": doc(t, `{"name": "grace"}`),
		"u5": doc(t, `{"name": "theirs"}`), // only theirs: u3 deleted, u5 added
		"u4": doc(t, `{"name": "new"}`),
	})
	r := mergeOf(t, s, base, ours, theirs)
	if len(r.Conflicts) != 0 {
		t.Fatalf("edits to different fields conflicted: %+v", r.Conflicts)
	}
	got := readBack(t, s, r.Root)
	want := map[string]merge.Node{
		"u1": doc(t, `{"name": "ada lovelace", "age": 37, "address": {"city": "London", "zip": "N1 9GU"}, "tag": "x"}`),
		"u2": doc(t, `{"name": "grace hopper"}`),
		"u4": doc(t, `{"name": "new"}`),
		"u5": doc(t, `{"name": "theirs"}`),
	}
	if !sameRecords(got, want) {
		var lines []string
		for id, n := range got {
			lines = append(lines, fmt.Sprintf("%s=%s", id, n.Canonical()))
		}
		t.Errorf("merged = %v\nwant u1 with both sides' fields, u2 ours, u3 gone, u4 once, u5 theirs", lines)
	}
	if r.Root.Size != 4 || r.Root.Format != document.Format {
		t.Errorf("merged root claims %d records of format %d, want 4 of %d", r.Root.Size, r.Root.Format, document.Format)
	}
}

// One field changed two ways is a conflict located at that record and that
// field, and only that field; the rest of the collection is untouched by it
// (the result is ours, as the port asks with conflicts). A record deleted
// on one side and changed on the other conflicts at the record as a whole,
// and a field deleted on one side and changed on the other within a record
// at that field.
func TestOneFieldChangedTwoWaysConflictsAtThatRecordNamingTheField(t *testing.T) {
	s := memstore.New()
	base := write(t, s, map[string]merge.Node{
		"u1": doc(t, `{"name": "ada", "address": {"city": "London", "zip": "N1"}}`),
		"u2": doc(t, `{"name": "grace", "n": 1}`),
		"u3": doc(t, `{"name": "linus", "k": 1}`),
		"ok": doc(t, `{"a": 1}`),
	})
	ours := write(t, s, map[string]merge.Node{
		"u1": doc(t, `{"name": "ada", "address": {"city": "Paris", "zip": "N1"}}`), // address/city one way
		"u2": doc(t, `{"name": "grace", "n": 2}`),                                  // n one way
		"u3": doc(t, `{"name": "linus"}`),                                          // k deleted
		"ok": doc(t, `{"a": 1, "b": 2}`),
	})
	theirs := write(t, s, map[string]merge.Node{
		"u1": doc(t, `{"name": "ada", "address": {"city": "Rome", "zip": "N1"}}`), // address/city the other way
		// u2 deleted
		"u3": doc(t, `{"name": "linus", "k": 2}`), // k changed
		"ok": doc(t, `{"a": 1, "c": 3}`),
	})
	r := mergeOf(t, s, base, ours, theirs)
	at := map[string]string{} // "id path" -> reason
	for _, c := range r.Conflicts {
		id, path, err := document.ParseLocation(c.Location)
		if err != nil {
			t.Fatalf("a conflict located at %q: %v", c.Location, err)
		}
		at[string(id)+" "+path.String()] = c.Reason
	}
	if len(r.Conflicts) != 3 {
		t.Errorf("%d conflicts %v, want u1's address/city, u2 as a whole and u3's k", len(r.Conflicts), at)
	}
	if reason, ok := at["u1 address/city"]; !ok || reason == "" {
		t.Errorf("no conflict at u1's field address/city (and only there): %v", at)
	}
	if !strings.Contains(at["u2 "], "deleted") {
		t.Errorf("u2, deleted on one side and changed on the other, at the record as a whole: %v", at)
	}
	if !strings.Contains(at["u3 k"], "deleted") {
		t.Errorf("u3's field k, deleted on one side and changed on the other: %v", at)
	}
	if r.Root != ours {
		t.Errorf("with conflicts the result is %+v, want ours %+v", r.Root, ours)
	}
}

// Two fields of one record changed two ways are two conflicts, each at its
// own field; a record whose stored frame does not decode, met mid-merge,
// aborts the merge with an error: it is the store's problem, not a person's.
func TestEachFieldConflictIsLocatedAndABadRecordAbortsTheMerge(t *testing.T) {
	s := memstore.New()
	base := write(t, s, map[string]merge.Node{"u1": doc(t, `{"a": 1, "b": 1, "c": 1}`)})
	ours := write(t, s, map[string]merge.Node{"u1": doc(t, `{"a": 2, "b": 2, "c": 1}`)})
	theirs := write(t, s, map[string]merge.Node{"u1": doc(t, `{"a": 3, "b": 3, "c": 1}`)})
	r := mergeOf(t, s, base, ours, theirs)
	var got []string
	for _, c := range r.Conflicts {
		id, path, err := document.ParseLocation(c.Location)
		if err != nil {
			t.Fatalf("a conflict located at %q: %v", c.Location, err)
		}
		got = append(got, string(id)+" "+path.String())
	}
	if strings.Join(got, ", ") != "u1 a, u1 b" {
		t.Errorf("conflicts at %v, want one at u1's a and one at u1's b", got)
	}

	raw := func(frame []byte) model.Root { // a collection whose u1 is the given bytes
		t.Helper()
		pm, err := prolly.Empty(ctx, s, cfg())
		if err != nil {
			t.Fatal(err)
		}
		e := pm.Editor()
		if err := e.Put([]byte("u1"), frame); err != nil {
			t.Fatal(err)
		}
		if pm, err = e.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		return model.Root{Hash: pm.Root(), Size: pm.Count(), Format: document.Format}
	}
	o, err := document.EncodeRecord(doc(t, `{"a": 2}`))
	if err != nil {
		t.Fatal(err)
	}
	th, err := document.EncodeRecord(doc(t, `{"a": 3}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = document.Model{Config: cfg()}.Merge(ctx, raw([]byte("not a record")), raw(o), raw(th), s)
	if !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("a merge meeting a record that does not decode = %v, want ErrCorrupt", err)
	}
}

// A location carries any id (NUL bytes included) and any field path (a
// field name may hold '/'), and reads back as written; what Locate did not
// write is refused.
func TestLocationsRoundTripAndForgeriesAreRefused(t *testing.T) {
	for _, tc := range []struct {
		id   string
		path merge.Path
	}{
		{"u1", nil},
		{"u1", merge.Path{"address", "city"}},
		{"\x00id\x00", merge.Path{"a/b", ""}},
		{strings.Repeat("x", document.MaxIDSize), merge.Path{"k"}},
	} {
		id, path, err := document.ParseLocation(document.Locate([]byte(tc.id), tc.path))
		if err != nil || string(id) != tc.id || len(path) != len(tc.path) || strings.Join(path, "\x01") != strings.Join(tc.path, "\x01") {
			t.Errorf("Locate(%q, %q) read back as %q, %q, %v", tc.id, tc.path, id, path, err)
		}
	}
	good := document.Locate([]byte("u1"), merge.Path{"a"})
	for name, loc := range map[string][]byte{
		"empty":            {},
		"an empty id":      {0},
		"a truncated id":   {5, 'u'},
		"a truncated path": append(append([]byte{}, good...), 3, 'x'),
		"a long varint":    {0x82, 0x00, 'u', '1'},
		"an id too long":   append([]byte{0x81, 0x20}, make([]byte, document.MaxIDSize+1)...),
	} {
		if _, _, err := document.ParseLocation(loc); err == nil {
			t.Errorf("%s: a location Locate did not write was read", name)
		}
	}
}

// FuzzParseLocation reads locations: what parses names an id a collection
// can hold and a path no deeper than a document, and Locate writes it back
// as the same bytes, so a location has one spelling.
func FuzzParseLocation(f *testing.F) {
	for _, loc := range [][]byte{document.Locate([]byte("u1"), merge.Path{"address", "city"}), document.Locate([]byte("u1"), nil), {}, {0}, {5, 'u'}, {0x82, 0x00, 'u', '1'}, document.Locate([]byte("u1"), make(merge.Path, document.MaxDepth+1))} {
		f.Add(loc)
	}
	f.Fuzz(func(t *testing.T, loc []byte) {
		id, path, err := document.ParseLocation(loc)
		if err != nil {
			if !errors.Is(err, document.ErrID) {
				t.Fatalf("ParseLocation(%x): %v, want ErrID", loc, err)
			}
			return
		}
		if len(id) == 0 || len(id) > document.MaxIDSize || len(path) > document.MaxDepth {
			t.Fatalf("ParseLocation(%x) names an id of %d bytes and a path %d deep, which no collection holds", loc, len(id), len(path))
		}
		if back := document.Locate(id, path); !bytes.Equal(back, loc) {
			t.Fatalf("ParseLocation(%x) = %q, %q, which Locate writes as %x: one place, two spellings", loc, id, path, back)
		}
	})
}
