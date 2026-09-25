package document_test

import (
	"encoding/binary"
	"errors"
	"runtime"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
)

// claimingRoot stores a level-1 prolly node over leaf, the real one-record
// collection under id "a", whose second entry, under "b", names a chunk
// that is not there and claims claim records under it; the root's size
// agrees, so the collection opens.
func claimingRoot(t *testing.T, s *memstore.Store, leaf model.Root, claim uint64) model.Root {
	t.Helper()
	var missing hash.Hash
	missing[0] = 0xAB
	b := []byte{0x01, 0x01, 0x02} // a node, level 1, two entries
	for _, e := range []struct {
		key   string
		child hash.Hash
		count uint64
	}{{"a", leaf.Hash, 1}, {"b", missing, claim}} {
		b = binary.AppendUvarint(b, uint64(len(e.key)))
		b = append(append(b, e.key...), e.child[:]...)
		b = binary.AppendUvarint(b, e.count)
	}
	h, err := s.Put(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	return model.Root{Hash: h, Size: 1 + claim, Format: document.Format}
}

// Reading a collection sizes nothing from what its root claims: a 76-byte
// root claiming 2^23 records over one real one allocated 1.4 GB before the
// missing child was found (snapshot-engine#5). Here the claim is 2^16, the
// read must fail on the missing chunk, and it must cost under 1 MiB.
func TestRegression_SE5_ReadingACollectionSizesNothingFromItsRootsClaim(t *testing.T) {
	s := memstore.New()
	leaf := write(t, s, map[string]merge.Node{"a": merge.Obj(merge.Field{Name: "x", Value: merge.Num("1")})})
	// Positive control: the real one-record collection reads back.
	if got, err := document.Read(ctx, s, cfg(), leaf); err != nil || len(got) != 1 || string(document.Encode(got["a"])) != `{"x":1}` {
		t.Fatalf("the one-record collection read back as %v, %v", got, err)
	}
	root := claimingRoot(t, s, leaf, 1<<16)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := document.Read(ctx, s, cfg(), root)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, chunk.ErrNotFound) {
		t.Errorf("reading a collection whose second child is missing = %d records, %v; want chunk.ErrNotFound", len(got), err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("reading a collection whose root claims %d records over one real one allocated %d bytes: a forged root's claim costs memory without bound", 1+1<<16, grew)
	}
}
