package kv_test

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

func counter(n int64) kv.Value { return kv.Value{Kind: kv.Counter, Counter: n} }

func set(ms ...kv.Member) kv.Value { return kv.Value{Kind: kv.Set, Members: ms} }

func member(elem string, tag uint64) kv.Member { return kv.Member{Elem: []byte(elem), Tag: tag} }

func fields(kv_ ...string) kv.Value {
	v := kv.Value{Kind: kv.Hash, Fields: map[string][]byte{}}
	for i := 0; i+1 < len(kv_); i += 2 {
		v.Fields[kv_[i]] = []byte(kv_[i+1])
	}
	return v
}

func zset(ss ...kv.Scored) kv.Value { return kv.Value{Kind: kv.SortedSet, Scores: ss} }

func scored(m string, score float64, tag uint64) kv.Scored {
	return kv.Scored{Member: []byte(m), Score: score, Tag: tag}
}

func seq(es ...string) kv.Value {
	v := kv.Value{Kind: kv.Sequence, Seq: [][]byte{}}
	for _, e := range es {
		v.Seq = append(v.Seq, []byte(e))
	}
	return v
}

// sameValue compares two values as their frames: the frame is canonical, so
// two values are the same value when their frames are the same bytes.
func sameValue(t *testing.T, a, b kv.Value) bool {
	t.Helper()
	fa, err := kv.EncodeValue(a)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	fb, err := kv.EncodeValue(b)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return bytes.Equal(fa, fb)
}

// E3: every kind's value round-trips through its frame, empty structures
// included, and decodes to a value whose frame is the same bytes.
func TestEveryKindRoundTrips(t *testing.T) {
	for name, v := range map[string]kv.Value{
		"counter zero":     counter(0),
		"counter negative": counter(-1 << 40),
		"counter max":      counter(math.MaxInt64),
		"empty set":        set(),
		"set":              set(member("a", 1), member("a", 2), member("\x00b", 7), member("c", 1)),
		"empty hash":       fields(),
		"hash":             fields("f1", "v1", "f0", "", "\xff", "bin\x00"),
		"empty sorted set": zset(),
		"sorted set":       zset(scored("m", 1.5, 1), scored("a", 1.5, 2), scored("z", -3, 9), scored("m", 1.5, 4)),
		"empty sequence":   seq(),
		"sequence":         seq("x", "y", "x", ""),
	} {
		f, err := kv.EncodeValue(v)
		if err != nil {
			t.Errorf("%s: encoding: %v", name, err)
			continue
		}
		if f[0] != byte(v.Kind) {
			t.Errorf("%s: the frame's kind byte is %#x, want %#x", name, f[0], byte(v.Kind))
		}
		got, err := kv.DecodeValue(f)
		if err != nil {
			t.Errorf("%s: decoding its own frame: %v", name, err)
			continue
		}
		if got.Kind != v.Kind {
			t.Errorf("%s: read back as kind %d", name, got.Kind)
		}
		again, err := kv.EncodeValue(got)
		if err != nil || !bytes.Equal(again, f) {
			t.Errorf("%s: the decoded value's frame is %x (%v), want the original %x", name, again, err, f)
		}
	}
}

// The documented layouts, by hand (docs/DESIGN.md §3): a counter is its
// int64 little-endian; a set is a count then (tag u64, member) in member
// then tag order; a hash is a count then (name, value) in name order; a
// sorted set is a count then (score bits u64, tag u64, member) in score,
// member, tag order; a sequence is a count then its elements in order.
func TestKindFramesAreTheDocumentedEncodings(t *testing.T) {
	u64 := func(v uint64) []byte { var b [8]byte; binary.LittleEndian.PutUint64(b[:], v); return b[:] }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	for name, tc := range map[string]struct {
		v    kv.Value
		want []byte
	}{
		"counter 5":      {counter(5), cat([]byte{2}, u64(5))},
		"counter -1":     {counter(-1), cat([]byte{2}, u64(math.MaxUint64))},
		"set {a@7, b@1}": {set(member("b", 1), member("a", 7)), cat([]byte{3, 2}, u64(7), []byte{1, 'a'}, u64(1), []byte{1, 'b'})},
		"hash {f: v}":    {fields("f", "v"), []byte{4, 1, 1, 'f', 1, 'v'}},
		"sorted set":     {zset(scored("m", 1.5, 2), scored("a", -1, 3)), cat([]byte{5, 2}, u64(math.Float64bits(-1)), u64(3), []byte{1, 'a'}, u64(math.Float64bits(1.5)), u64(2), []byte{1, 'm'})},
		"sequence x y":   {seq("x", "y"), []byte{6, 2, 1, 'x', 1, 'y'}},
	} {
		got, err := kv.EncodeValue(tc.v)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s: frame %x, want %x", name, got, tc.want)
		}
	}
}

// A frame that is not a value of its kind is refused, never repaired: a
// counter of the wrong length, members out of order or repeated, hash
// fields out of order or repeated, a NaN or negative-zero score, a count
// past the limit, a member past its length, trailing bytes; and a value
// that cannot be framed is refused on encoding.
func TestKindFramesThatAreNotValuesAreRefused(t *testing.T) {
	u64 := func(v uint64) []byte { var b [8]byte; binary.LittleEndian.PutUint64(b[:], v); return b[:] }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	good := map[string][]byte{
		"counter":    cat([]byte{2}, u64(5)),
		"set":        cat([]byte{3, 1}, u64(7), []byte{1, 'a'}),
		"hash":       {4, 1, 1, 'f', 1, 'v'},
		"sorted set": cat([]byte{5, 1}, u64(math.Float64bits(1)), u64(1), []byte{1, 'm'}),
		"sequence":   {6, 1, 1, 'x'},
	}
	for name, f := range good {
		if _, err := kv.DecodeValue(f); err != nil {
			t.Fatalf("positive control: a %s frame does not decode: %v", name, err)
		}
	}
	for name, f := range map[string][]byte{
		"counter of seven bytes":        cat([]byte{2}, u64(5)[:7]),
		"counter with a trailing byte":  cat([]byte{2}, u64(5), []byte{0}),
		"set members out of order":      cat([]byte{3, 2}, u64(1), []byte{1, 'b'}, u64(1), []byte{1, 'a'}),
		"set member repeated":           cat([]byte{3, 2}, u64(1), []byte{1, 'a'}, u64(1), []byte{1, 'a'}),
		"set tags out of order":         cat([]byte{3, 2}, u64(2), []byte{1, 'a'}, u64(1), []byte{1, 'a'}),
		"set count over the limit":      {3, 0x81, 0x80, 0x04, 0},
		"set member over its length":    cat([]byte{3, 1}, u64(1), []byte{0x81, 0x20}, make([]byte, 4097)),
		"hash fields out of order":      {4, 2, 1, 'g', 1, 'v', 1, 'f', 1, 'v'},
		"hash field repeated":           {4, 2, 1, 'f', 1, 'v', 1, 'f', 1, 'w'},
		"hash with a trailing byte":     {4, 1, 1, 'f', 1, 'v', 0},
		"sorted set NaN score":          cat([]byte{5, 1}, u64(math.Float64bits(math.NaN())), u64(1), []byte{1, 'm'}),
		"sorted set negative zero":      cat([]byte{5, 1}, u64(math.Float64bits(math.Copysign(0, -1))), u64(1), []byte{1, 'm'}),
		"sorted set out of order":       cat([]byte{5, 2}, u64(math.Float64bits(2)), u64(1), []byte{1, 'a'}, u64(math.Float64bits(1)), u64(1), []byte{1, 'b'}),
		"sorted set entry repeated":     cat([]byte{5, 2}, u64(math.Float64bits(1)), u64(1), []byte{1, 'a'}, u64(math.Float64bits(1)), u64(1), []byte{1, 'a'}),
		"sequence count short":          {6, 2, 1, 'x'},
		"sequence with a trailing byte": {6, 1, 1, 'x', 0},
	} {
		if _, err := kv.DecodeValue(f); err == nil {
			t.Errorf("%s: decoded as a value", name)
		}
	}
	tooMany := make([]kv.Member, kv.MaxMembers+1)
	for i := range tooMany {
		tooMany[i] = kv.Member{Elem: []byte{byte(i), byte(i >> 8), byte(i >> 16)}, Tag: 1}
	}
	for name, v := range map[string]kv.Value{
		"set with a member repeated":       set(member("a", 1), member("a", 1)),
		"set with too many members":        {Kind: kv.Set, Members: tooMany},
		"set member over its length":       set(kv.Member{Elem: make([]byte, kv.MaxMember+1), Tag: 1}),
		"hash field over its length":       {Kind: kv.Hash, Fields: map[string][]byte{string(make([]byte, kv.MaxMember+1)): nil}},
		"sorted set NaN score":             zset(scored("m", math.NaN(), 1)),
		"sorted set entry repeated":        zset(scored("m", 1, 1), scored("m", 1, 1)),
		"sequence element over its length": {Kind: kv.Sequence, Seq: [][]byte{make([]byte, kv.MaxMember+1)}},
		"a payload past the cap":           {Kind: kv.Hash, Fields: map[string][]byte{"a": make([]byte, kv.MaxValueSize)}},
	} {
		if _, err := kv.EncodeValue(v); err == nil {
			t.Errorf("%s: encoded", name)
		}
	}
	if _, err := kv.EncodeValue(zset(scored("m", math.Copysign(0, -1), 1))); err != nil {
		t.Errorf("a negative-zero score is not refused on encoding but normalised: %v", err)
	} else if f, _ := kv.EncodeValue(zset(scored("m", math.Copysign(0, -1), 1))); !bytes.Equal(f, cat([]byte{5, 1}, u64(0), u64(1), []byte{1, 'm'})) {
		t.Errorf("a negative-zero score encodes as %x, want the canonical zero", f)
	}
}
