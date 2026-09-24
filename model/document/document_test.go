package document_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
)

var ctx = context.Background()

func cfg() prolly.Config { return prolly.DefaultConfig() }

// sample is a collection with a binary id, an empty record, a scalar
// record and nested ones.
func sample(t *testing.T) map[string]merge.Node {
	t.Helper()
	return map[string]merge.Node{
		"user:1":     doc(t, `{"name": "ada", "age": 36, "tags": ["math", "engines"]}`),
		"user:2":     doc(t, `{"name": "grace", "age": 45, "address": {"city": "Arlington", "zip": "22201"}}`),
		"\x00binary": doc(t, `{}`),
		"scalar":     doc(t, `"just a string"`),
		"nulls":      doc(t, `[null, null]`),
	}
}

func sameRecords(a, b map[string]merge.Node) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || !w.Equal(v) {
			return false
		}
	}
	return true
}

// A collection written as an object reads back record for record, from a
// root claiming this model's format, the record count and no stream depth.
func TestAnObjectIsWrittenAndReadBack(t *testing.T) {
	s := memstore.New()
	want := sample(t)
	root, err := document.Write(ctx, s, cfg(), want)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if root.Format != document.Format || root.Size != uint64(len(want)) || root.Depth != 0 {
		t.Errorf("root claims format %d, %d records, depth %d; want %d, %d, 0", root.Format, root.Size, root.Depth, document.Format, len(want))
	}
	got, err := document.Read(ctx, s, cfg(), root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !sameRecords(got, want) {
		t.Errorf("read back %d records differing from the %d written", len(got), len(want))
	}
	if err := (document.Model{Config: cfg()}).Validate(ctx, root, s); err != nil {
		t.Errorf("Validate of an object just written: %v", err)
	}
	empty, err := document.Write(ctx, s, cfg(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := document.Read(ctx, s, cfg(), empty); err != nil || len(got) != 0 {
		t.Errorf("an empty collection reads back as %d records, %v", len(got), err)
	}
}

// Write refuses an id that is empty or over the limit, and a record that is
// not a document, and stores nothing when it does.
func TestWriteRefusesABadIDOrRecord(t *testing.T) {
	s := memstore.New()
	before, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	one := doc(t, `{"ok": true}`)
	for name, bad := range map[string]struct {
		records map[string]merge.Node
		want    error
	}{
		"an empty id":                     {map[string]merge.Node{"": one}, document.ErrID},
		"an id over the limit":            {map[string]merge.Node{strings.Repeat("k", document.MaxIDSize+1): one}, document.ErrID},
		"a record that is not a document": {map[string]merge.Node{"x": merge.Num("1.0")}, document.ErrDocument},
		"a record over the limit":         {map[string]merge.Node{"x": merge.Str(strings.Repeat("y", document.MaxDocument))}, document.ErrDocument},
	} {
		if _, err := document.Write(ctx, s, cfg(), bad.records); !errors.Is(err, bad.want) {
			t.Errorf("%s: Write = %v, want %v", name, err, bad.want)
		}
	}
	after, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Chunks != before.Chunks {
		t.Errorf("refused writes stored %d chunks", after.Chunks-before.Chunks)
	}
}

// Validate refuses what is not one of this model's objects: another format,
// a root claiming a stream depth or the wrong count, a map whose values are
// not records.
func TestValidateRefusesWhatIsNotAnObject(t *testing.T) {
	s := memstore.New()
	m := document.Model{Config: cfg()}
	root, err := document.Write(ctx, s, cfg(), sample(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]struct {
		root model.Root
		want error
	}{
		"another format":    {model.Root{Hash: root.Hash, Size: root.Size, Format: 2}, model.ErrUnknownModel},
		"a stream depth":    {model.Root{Hash: root.Hash, Size: root.Size, Format: document.Format, Depth: 1}, chunk.ErrCorrupt},
		"a count that lies": {model.Root{Hash: root.Hash, Size: root.Size + 1, Format: document.Format}, chunk.ErrCorrupt},
	} {
		if err := m.Validate(ctx, bad.root, s); !errors.Is(err, bad.want) {
			t.Errorf("%s: Validate = %v, want %v", name, err, bad.want)
		}
	}
	// A map of the right shape whose values are not records.
	pm, err := prolly.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatal(err)
	}
	e := pm.Editor()
	if err := e.Put([]byte("id"), []byte("not a record")); err != nil {
		t.Fatal(err)
	}
	if pm, err = e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	forged := model.Root{Hash: pm.Root(), Size: 1, Format: document.Format}
	if err := m.Validate(ctx, forged, s); !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("a map holding a value that is not a record validated: %v", err)
	}
	if _, err := document.Read(ctx, s, cfg(), forged); !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("Read of a map holding a value that is not a record: %v", err)
	}
}

// Walk names the object's root and every chunk of its map, all of which the
// store holds; a root of another format is refused.
func TestWalkNamesEveryChunkOfAnObject(t *testing.T) {
	s := memstore.New()
	m := document.Model{Config: cfg()}
	records := map[string]merge.Node{}
	for i := range 2000 {
		records[fmt.Sprintf("rec:%05d", i)] = doc(t, fmt.Sprintf(`{"i": %d, "text": "%s"}`, i, strings.Repeat("x", 100)))
	}
	root, err := document.Write(ctx, s, cfg(), records)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[hash.Hash]bool{}
	err = m.Walk(ctx, root, s, func(h hash.Hash, leaf bool) (bool, error) {
		seen[h] = true
		return true, nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !seen[root.Hash] {
		t.Error("Walk did not name the root")
	}
	if len(seen) < 2 {
		t.Errorf("Walk named %d chunks for 2000 records; the map has more than one", len(seen))
	}
	for h := range seen {
		if _, err := s.Get(ctx, h); err != nil {
			t.Errorf("Walk named %s, which the store does not hold", h.Short())
		}
	}
	if err := m.Walk(ctx, model.Root{Hash: root.Hash, Size: root.Size, Format: 2}, s, func(hash.Hash, bool) (bool, error) { return true, nil }); !errors.Is(err, model.ErrUnknownModel) {
		t.Errorf("Walk of another format = %v, want ErrUnknownModel", err)
	}
}
