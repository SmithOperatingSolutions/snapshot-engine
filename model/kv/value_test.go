package kv_test

import (
	"bytes"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

// A value's frame is its kind byte then its payload: it round-trips, an
// empty payload included, and the documented layout is what is written.
func TestAValueFrameRoundTrips(t *testing.T) {
	for _, payload := range [][]byte{nil, {}, []byte("x"), bytes.Repeat([]byte{0xab}, 4<<10), bytes.Repeat([]byte{0}, kv.MaxValueSize)} {
		v := kv.Value{Kind: kv.Bytes, Bytes: payload}
		f, err := kv.EncodeValue(v)
		if err != nil {
			t.Fatalf("encoding a %d-byte value: %v", len(payload), err)
		}
		if len(f) != 1+len(payload) || f[0] != byte(kv.Bytes) || !bytes.Equal(f[1:], payload) {
			t.Fatalf("a %d-byte value framed as %d bytes with kind byte %#x: want kind 0x01 then the payload", len(payload), len(f), f[0])
		}
		got, err := kv.DecodeValue(f)
		if err != nil {
			t.Fatalf("decoding the frame of a %d-byte value: %v", len(payload), err)
		}
		if got.Kind != kv.Bytes || !bytes.Equal(got.Bytes, payload) {
			t.Fatalf("a %d-byte value read back as kind %d, %d bytes", len(payload), got.Kind, len(got.Bytes))
		}
	}
}

// A frame that is not a value is refused, never guessed at: no bytes at
// all, a kind nobody defined, a payload past the limit, and a value asked
// to be encoded with such a kind or size.
func TestAFrameThatIsNotAValueIsRefused(t *testing.T) {
	if _, err := kv.DecodeValue([]byte{byte(kv.Bytes), 'o', 'k'}); err != nil {
		t.Fatalf("positive control: a bytes frame does not decode: %v", err)
	}
	for name, f := range map[string][]byte{
		"no bytes at all":        {},
		"kind 0":                 {0, 'x'},
		"a kind nobody defined":  {0x7f, 'x'},
		"a payload past the cap": append([]byte{byte(kv.Bytes)}, make([]byte, kv.MaxValueSize+1)...),
	} {
		if _, err := kv.DecodeValue(f); err == nil {
			t.Errorf("%s: decoded as a value", name)
		}
	}
	for name, v := range map[string]kv.Value{
		"kind 0":                 {Kind: 0, Bytes: []byte("x")},
		"a kind nobody defined":  {Kind: 0x7f},
		"a payload past the cap": {Kind: kv.Bytes, Bytes: make([]byte, kv.MaxValueSize+1)},
	} {
		if _, err := kv.EncodeValue(v); err == nil {
			t.Errorf("%s: encoded", name)
		}
	}
}

// Every frame the decoder accepts re-encodes to the same bytes, and the
// decoder never panics or reads past its input.
func FuzzDecodeValue(f *testing.F) {
	f.Add([]byte{byte(kv.Bytes)})
	f.Add([]byte{byte(kv.Bytes), 'a', 'b'})
	f.Add([]byte{0})
	f.Add([]byte{})
	f.Add([]byte{byte(kv.Counter), 5, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{byte(kv.Set), 1, 7, 0, 0, 0, 0, 0, 0, 0, 1, 'a'})
	f.Add([]byte{byte(kv.Hash), 1, 1, 'f', 1, 'v'})
	f.Add([]byte{byte(kv.SortedSet), 1, 0, 0, 0, 0, 0, 0, 0xf0, 0x3f, 1, 0, 0, 0, 0, 0, 0, 0, 1, 'm'})
	f.Add([]byte{byte(kv.Sequence), 2, 1, 'x', 1, 'y'})
	f.Fuzz(func(t *testing.T, b []byte) {
		v, err := kv.DecodeValue(b)
		if err != nil {
			return
		}
		again, err := kv.EncodeValue(v)
		if err != nil {
			t.Fatalf("a decoded value does not encode: %v", err)
		}
		if !bytes.Equal(again, b) {
			t.Fatalf("frame %x decoded and encoded again as %x", b, again)
		}
	})
}
