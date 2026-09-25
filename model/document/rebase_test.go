package document_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
)

func rebaseRecords(t *testing.T, s *memstore.Store, n int, edits map[string]string) model.Root {
	t.Helper()
	records := map[string]merge.Node{}
	for i := range n {
		id := fmt.Sprintf("r%04d", i)
		records[id] = mustParse(t, fmt.Sprintf(`{"n":%d,"bio":"%0200d"}`, i, i))
	}
	for id, text := range edits {
		if text == "" {
			delete(records, id)
			continue
		}
		records[id] = mustParse(t, text)
	}
	r, err := document.Write(ctx, s, cfg(), records)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustParse(t *testing.T, text string) merge.Node {
	t.Helper()
	n, err := document.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Rebase is the merge of two sides that changed different records:
// Rebase(base, side, onto) is Merge(base, onto, side), byte for byte, with
// records changed, added and deleted on each side; a record both sides
// changed is ErrChangedSince, even the same change or a change to
// different fields, which a merge would combine.
func TestRebaseIsTheMergeOfChangesToDifferentRecords(t *testing.T) {
	s := memstore.New()
	m := document.Model{Config: cfg()}
	sideEdits := map[string]string{"r0003": `{"n":-3}`, "r0100": "", "new-side": `{"a":[1,2]}`}
	ontoEdits := map[string]string{"r0004": `{"n":4,"x":true}`, "r0200": "", "new-onto": `{"b":null}`}
	both := map[string]string{}
	for _, e := range []map[string]string{sideEdits, ontoEdits} {
		for k, v := range e {
			both[k] = v
		}
	}
	b := rebaseRecords(t, s, 300, nil)
	side := rebaseRecords(t, s, 300, sideEdits)
	onto := rebaseRecords(t, s, 300, ontoEdits)
	want, err := m.Merge(ctx, b, onto, side, s)
	if err != nil || len(want.Conflicts) > 0 {
		t.Fatalf("the merge: %v, %v", want.Conflicts, err)
	}
	got, err := m.Rebase(ctx, b, side, onto, s)
	if err != nil || got != want.Root || got != rebaseRecords(t, s, 300, both) {
		t.Errorf("Rebase = %v, %v; Merge = %v: a transaction rebased onto the batch would land another collection than the merge it replaces", got, err, want.Root)
	}
	for name, edits := range map[string][2]map[string]string{
		"the same change":        {{"r0005": `{"n":50}`}, {"r0005": `{"n":50}`}},
		"different fields":       {{"r0005": `{"n":5,"bio":"x"}`}, {"r0005": `{"n":6,"bio":"` + fmt.Sprintf("%0200d", 5) + `"}`}},
		"a delete against a put": {{"r0005": ""}, {"r0005": `{"n":7}`}},
		"an addition on both":    {{"new": `{"a":1}`}, {"new": `{"a":1}`}},
	} {
		side := rebaseRecords(t, s, 300, edits[0])
		onto := rebaseRecords(t, s, 300, edits[1])
		if _, err := m.Rebase(ctx, b, side, onto, s); !errors.Is(err, document.ErrChangedSince) {
			t.Errorf("rebasing a record both sides changed (%s) = %v, want ErrChangedSince", name, err)
		}
	}
}
