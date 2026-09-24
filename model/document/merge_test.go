package document_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"

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

// One field changed two ways is a conflict at that record, its reason
// naming the field's path and only that field; the rest of the collection
// is untouched by it (the result is ours, as the port asks with conflicts).
// A record deleted on one side and changed on the other conflicts too, and
// a field deleted on one side and changed on the other within a record.
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
	byID := map[string]string{}
	for _, c := range r.Conflicts {
		byID[string(c.Location)] = c.Reason
	}
	if len(r.Conflicts) != 3 {
		t.Errorf("%d conflicts %+v, want u1, u2 and u3", len(r.Conflicts), r.Conflicts)
	}
	if !strings.Contains(byID["u1"], "address/city") || strings.Contains(byID["u1"], "zip") {
		t.Errorf("u1's conflict reason %q should name the field address/city and no other", byID["u1"])
	}
	if !strings.Contains(byID["u2"], "deleted") {
		t.Errorf("u2, deleted on one side and changed on the other: %q", byID["u2"])
	}
	if !strings.Contains(byID["u3"], "k") || !strings.Contains(byID["u3"], "deleted") {
		t.Errorf("u3, field k deleted on one side and changed on the other: %q", byID["u3"])
	}
	if _, ok := byID["ok"]; ok {
		t.Errorf("the record both sides edited on different fields conflicted: %q", byID["ok"])
	}
	if r.Root != ours {
		t.Errorf("with conflicts the result is %+v, want ours %+v", r.Root, ours)
	}
}
