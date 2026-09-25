package kv_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

// claimingRoot stores a level-1 prolly node over leaf, the real one-entry
// map under key "a", whose second entry, under "b", names a chunk that is
// not there and claims claim entries under it; the root's size agrees, so
// the map opens.
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
	return model.Root{Hash: h, Size: 1 + claim, Format: kv.Format}
}

// Reading an object sizes nothing from what its root claims: a 76-byte
// root claiming 2^23 entries over one real one allocated 1.4 GB before the
// missing child was found (snapshot-engine#5). Here the claim is 2^16, the
// read must fail on the missing chunk, and it must cost under 1 MiB.
func TestRegression_SE5_ReadingAnObjectSizesNothingFromItsRootsClaim(t *testing.T) {
	s := memstore.New()
	leaf := write(t, s, map[string]kv.Value{"a": bytesValue("x")})
	// Positive control: the real one-entry object reads back.
	if got, err := kv.Read(ctx, s, cfg(), leaf); err != nil || len(got) != 1 || string(got["a"].Bytes) != "x" {
		t.Fatalf("the one-entry object read back as %v, %v", got, err)
	}
	root := claimingRoot(t, s, leaf, 1<<16)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := kv.Read(ctx, s, cfg(), root)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, chunk.ErrNotFound) {
		t.Errorf("reading an object whose second child is missing = %d entries, %v; want chunk.ErrNotFound", len(got), err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("reading an object whose root claims %d entries over one real one allocated %d bytes: a forged root's claim costs memory without bound", 1+1<<16, grew)
	}
}

// A value's frame sizes nothing from the count it claims either: four
// bytes claiming MaxMembers members, fields or elements, and nothing
// after, allocated megabytes before running out of bytes
// (snapshot-engine#5). Each must be refused with ErrValue under 64 KiB.
func TestRegression_SE5_AFrameSizesNothingFromItsClaimedCount(t *testing.T) {
	for _, k := range []kv.Kind{kv.Set, kv.Hash, kv.SortedSet, kv.Sequence} {
		// Positive control: a real frame of the kind decodes.
		v := map[kv.Kind]kv.Value{kv.Set: set(member("a", 1)), kv.Hash: fields("f", "v"), kv.SortedSet: zset(scored("m", 1, 1)), kv.Sequence: seq("e")}[k]
		f, err := kv.EncodeValue(v)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := kv.DecodeValue(f); err != nil || !sameValue(t, got, v) {
			t.Fatalf("a kind %d frame of one member decoded as %+v, %v", k, got, err)
		}
		frame := binary.AppendUvarint([]byte{byte(k)}, kv.MaxMembers)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err = kv.DecodeValue(frame)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, kv.ErrValue) {
			t.Errorf("a kind %d frame claiming %d members and holding none: %v, want ErrValue", k, kv.MaxMembers, err)
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<10 {
			t.Errorf("decoding a %d-byte kind %d frame claiming %d members allocated %d bytes: a corrupt frame's claim costs memory", len(frame), k, kv.MaxMembers, grew)
		}
	}
}

// A sequence one side changed by more than merge.MaxSequenceEdits while
// the other changed it too is a conflict at the key, whose reason says the
// list was changed too much to align rather than naming a position; at the
// budget it merges (snapshot-engine#4, DESIGN D18).
func TestRegression_SE4_AKVSequencePastTheEditBudgetConflictsAtTheKey(t *testing.T) {
	elems := make([]string, 600)
	for i := range elems {
		elems[i] = "e" + strconv.Itoa(i)
	}
	replaced := func(n int) []string {
		out := append([]string(nil), elems...)
		for i := range n {
			out[i] = "r" + strconv.Itoa(i)
		}
		return out
	}
	base := map[string]kv.Value{"queue": seq(elems...)}
	theirs := map[string]*kv.Value{"queue": ptr(seq(append(append([]string(nil), elems...), "z")...))}

	// Positive control: one side at the budget, the other appending.
	atLimit := replaced(merge.MaxSequenceEdits / 2)
	res, got := merged(t, base, map[string]*kv.Value{"queue": ptr(seq(atLimit...))}, theirs)
	clean(t, res, "a sequence changed by exactly the edit budget on one side and appended to on the other")
	if want := seq(append(atLimit, "z")...); !sameValue(t, got["queue"], want) {
		t.Fatalf("the queue merged to %d elements, want %d: both sides' changes", len(got["queue"].Seq), len(want.Seq))
	}

	res, _ = merged(t, base, map[string]*kv.Value{"queue": ptr(seq(replaced(merge.MaxSequenceEdits/2 + 1)...))}, theirs)
	oneConflict(t, res, "queue", "", "sequence")
	if r := res.Conflicts[0].Reason; !strings.HasPrefix(r, "sequence: ") || !strings.Contains(r, strconv.Itoa(merge.MaxSequenceEdits)) {
		t.Errorf("the conflict says %q: want \"sequence: \" and why, naming the budget of %d edits, not a position", r, merge.MaxSequenceEdits)
	}
}

// A sorted-set merge costs time in proportion to the set: every member's
// score was found by scanning the whole set, so a set at the frame limit,
// 11,000 members, took a second per key merged (snapshot-engine#6). A
// timing guard with headroom: the best of three merges must take under a
// quarter of a second (ten times that under the race detector), where the
// scan took four times as long.
func TestRegression_SE6_ASortedSetMergeIsNotQuadratic(t *testing.T) {
	const n = 11000
	var ss []kv.Scored
	for i := range n {
		ss = append(ss, scored("m"+strconv.Itoa(i), float64(i), 1))
	}
	ours, theirs := append([]kv.Scored(nil), ss...), append([]kv.Scored(nil), ss...)
	ours[0].Score, theirs[1].Score = -1, -2
	s := memstore.New()
	b := write(t, s, map[string]kv.Value{"z": zset(ss...)})
	o := write(t, s, map[string]kv.Value{"z": zset(ours...)})
	th := write(t, s, map[string]kv.Value{"z": zset(theirs...)})
	best := time.Duration(1<<63 - 1)
	for range 3 {
		start := time.Now()
		res, err := kv.Model{Config: cfg()}.Merge(ctx, b, o, th, s)
		best = min(best, time.Since(start))
		if err != nil || len(res.Conflicts) != 0 {
			t.Fatalf("two score changes to different members merged with %v, conflicts %+v", err, res.Conflicts)
		}
	}
	if ceiling := raceScale * 250 * time.Millisecond; best > ceiling {
		t.Errorf("merging a sorted set of %d members changed on both sides took %v at best, over %v: a key's merge grows with the square of its set", n, best, ceiling)
	}
}

// A kv location has one spelling: ParseLocation took a key length written
// with more varint bytes than it needs, and a key longer than any object
// holds, so two byte strings named one place and a location could name a
// key that cannot exist. FuzzParseLocation found the first (#6's fuzz
// targets). Both are refused; the longest key there is reads back.
func TestRegression_SE6_AKVLocationHasOneSpelling(t *testing.T) {
	long := bytes.Repeat([]byte("k"), kv.MaxKeySize)
	if key, sub, err := kv.ParseLocation(kv.Location(long, []byte("f"))); err != nil || !bytes.Equal(key, long) || string(sub) != "f" {
		t.Fatalf("the location of a %d-byte key, the longest, read back as a %d-byte key, %q, %v", kv.MaxKeySize, len(key), sub, err)
	}
	for name, loc := range map[string][]byte{
		"a key length in two bytes where one will do": {0x81, 0x00, 'a'},
		"a key one byte longer than any object holds": kv.Location(append(long, 'k'), nil),
	} {
		if key, _, err := kv.ParseLocation(loc); !errors.Is(err, kv.ErrKey) {
			t.Errorf("%s: ParseLocation read a %d-byte key, %v; want ErrKey", name, len(key), err)
		}
	}
}
