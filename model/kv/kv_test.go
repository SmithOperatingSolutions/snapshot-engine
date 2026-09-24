package kv_test

import (
	"bytes"
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

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

var ctx = context.Background()

func cfg() prolly.Config { return prolly.DefaultConfig() }

func bytesValue(s string) kv.Value { return kv.Value{Kind: kv.Bytes, Bytes: []byte(s)} }

// sample is a map with binary keys, an empty value and a value of a few KiB.
func sample() map[string]kv.Value {
	m := map[string]kv.Value{
		"user:1:name":             bytesValue("ada"),
		"user:2:name":             bytesValue("grace"),
		"counter:hits":            bytesValue("42"),
		"empty":                   bytesValue(""),
		string([]byte{0, 1, 255}): bytesValue("binary key"),
		"big":                     bytesValue(strings.Repeat("b", 3<<10)),
	}
	for i := 0; i < 200; i++ {
		m[fmt.Sprintf("session:%04d", i)] = bytesValue(fmt.Sprintf("token-%d", i*7))
	}
	return m
}

func equal(a, b map[string]kv.Value) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || w.Kind != v.Kind || !bytes.Equal(w.Bytes, v.Bytes) {
			return false
		}
	}
	return true
}

// An object round-trips: what was written reads back, entry for entry, its
// root claims the model's format and the number of entries, and it
// validates.
func TestAnObjectIsWrittenAndReadBack(t *testing.T) {
	s := memstore.New()
	want := sample()
	root, err := kv.Write(ctx, s, cfg(), want)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if root.Format != kv.Format || root.Size != uint64(len(want)) || root.Depth != 0 {
		t.Fatalf("the root claims format %d, %d entries, depth %d; want %d, %d, 0", root.Format, root.Size, root.Depth, kv.Format, len(want))
	}
	if err := (kv.Model{Config: cfg()}).Validate(ctx, root, s); err != nil {
		t.Fatalf("a freshly written object does not validate: %v", err)
	}
	got, err := kv.Read(ctx, s, cfg(), root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !equal(got, want) {
		t.Fatalf("read back %d entries differing from the %d written", len(got), len(want))
	}
	empty, err := kv.Write(ctx, s, cfg(), nil)
	if err != nil {
		t.Fatalf("writing an empty object: %v", err)
	}
	if got, err := kv.Read(ctx, s, cfg(), empty); err != nil || len(got) != 0 {
		t.Fatalf("an empty object reads back as %d entries, %v", len(got), err)
	}
}

// A key is 1 to 4096 bytes and a value one of ours: Write refuses an empty
// key, a key over the limit and a value of no kind, and writes nothing.
func TestWriteRefusesABadKeyOrValue(t *testing.T) {
	s := memstore.New()
	if _, err := kv.Write(ctx, s, cfg(), map[string]kv.Value{"k": bytesValue("v")}); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	for name, entries := range map[string]map[string]kv.Value{
		"an empty key":       {"": bytesValue("v")},
		"a key over 4 KiB":   {strings.Repeat("k", kv.MaxKeySize+1): bytesValue("v")},
		"a value of no kind": {"k": {Kind: 0, Bytes: []byte("v")}},
	} {
		_, err := kv.Write(ctx, s, cfg(), entries)
		if err == nil {
			t.Errorf("%s: written", name)
		}
		if strings.HasPrefix(name, "a key") && !errors.Is(err, kv.ErrKey) {
			t.Errorf("%s: %v, want ErrKey", name, err)
		}
		if strings.HasPrefix(name, "a value") && !errors.Is(err, kv.ErrValue) {
			t.Errorf("%s: %v, want ErrValue", name, err)
		}
	}
}

// Validate refuses what is not an object of this model: another format, a
// root claiming a stream depth, a root whose entry count disagrees with the
// map, a missing root chunk, and a map whose value is not a frame.
func TestValidateRefusesWhatIsNotAnObject(t *testing.T) {
	s := memstore.New()
	m := kv.Model{Config: cfg()}
	root, err := kv.Write(ctx, s, cfg(), sample())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(ctx, root, s); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	other := root
	other.Format = 2
	if err := m.Validate(ctx, other, s); !errors.Is(err, model.ErrUnknownModel) {
		t.Errorf("format 2 validates: %v, want ErrUnknownModel", err)
	}
	deep := root
	deep.Depth = 1
	if err := m.Validate(ctx, deep, s); !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("a root claiming depth 1 validates: %v, want ErrCorrupt", err)
	}
	short := root
	short.Size--
	if err := m.Validate(ctx, short, s); !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("a root claiming one entry fewer validates: %v, want ErrCorrupt", err)
	}
	missing := root
	missing.Hash = hash.Sum([]byte("never stored"))
	if err := m.Validate(ctx, missing, s); err == nil {
		t.Error("a missing root validates")
	}
	// A map built by hand whose value is not a frame: the model must look at
	// every value, not only the map's shape.
	pm, err := prolly.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatal(err)
	}
	e := pm.Editor()
	if err := e.Put([]byte("k"), []byte{0x7f, 'x'}); err != nil {
		t.Fatal(err)
	}
	if pm, err = e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	bad := model.Root{Hash: pm.Root(), Size: 1, Format: kv.Format}
	if err := m.Validate(ctx, bad, s); !errors.Is(err, kv.ErrValue) {
		t.Errorf("a map holding a frame of an unknown kind validates: %v, want ErrValue", err)
	}
	if _, err := kv.Read(ctx, s, cfg(), bad); err == nil {
		t.Error("a map holding a frame of an unknown kind reads")
	}
	// A map built by hand with an empty key: not a key of ours, however
	// well its frame decodes.
	pm, err = prolly.Empty(ctx, s, cfg())
	if err != nil {
		t.Fatal(err)
	}
	e = pm.Editor()
	if err := e.Put([]byte{}, []byte{byte(kv.Bytes), 'v'}); err != nil {
		t.Fatal(err)
	}
	if pm, err = e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	empty := model.Root{Hash: pm.Root(), Size: 1, Format: kv.Format}
	if err := m.Validate(ctx, empty, s); !errors.Is(err, chunk.ErrCorrupt) || !errors.Is(err, kv.ErrKey) {
		t.Errorf("a map holding an empty key validates: %v, want ErrCorrupt wrapping ErrKey", err)
	}
}

// Walk names the root first and then every chunk the object is made of:
// copied alone into an empty store, what Walk names holds the object.
func TestWalkNamesEveryChunkOfAnObject(t *testing.T) {
	s := memstore.New()
	m := kv.Model{Config: cfg()}
	root, err := kv.Write(ctx, s, cfg(), sample())
	if err != nil {
		t.Fatal(err)
	}
	var first hash.Hash
	kept := memstore.New()
	n := 0
	err = m.Walk(ctx, root, s, func(h hash.Hash, leaf bool) (bool, error) {
		if n == 0 {
			first = h
		}
		n++
		b, err := s.Get(ctx, h)
		if err != nil {
			return false, err
		}
		_, err = kept.Put(ctx, b)
		return true, err
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if first != root.Hash {
		t.Fatalf("Walk named %s first, want the root %s", first.Short(), root.Hash.Short())
	}
	if n < 2 {
		t.Fatalf("Walk named %d chunks for a map of %d entries over several nodes", n, root.Size)
	}
	if err := m.Validate(ctx, root, kept); err != nil {
		t.Fatalf("the chunks Walk named do not hold the object: %v", err)
	}
	if err := m.Walk(ctx, model.Root{Hash: root.Hash, Size: root.Size, Format: 2}, s, func(hash.Hash, bool) (bool, error) { return true, nil }); !errors.Is(err, model.ErrUnknownModel) {
		t.Errorf("walking format 2: %v, want ErrUnknownModel", err)
	}
	if err := m.Walk(ctx, model.Root{Hash: root.Hash, Size: root.Size, Depth: 1, Format: kv.Format}, s, func(hash.Hash, bool) (bool, error) { return true, nil }); !errors.Is(err, chunk.ErrCorrupt) {
		t.Errorf("walking a root claiming depth 1: %v, want ErrCorrupt", err)
	}
}
