// Package kv is the key-value model (model id 6): an object is an ordered map
// from key bytes to a typed value, each value kind with a merge policy of its
// own (docs/specs/engine-layers.md). Bytes take one side or conflict;
// counters add both sides' deltas; sets merge as observed-remove sets over
// write tags; hashes merge per field; sorted sets per member; sequences by
// element identity and position.
package kv

import (
	"errors"
	"fmt"
)

// Kind is a value's type, the first byte of its frame.
type Kind uint8

// Kinds. 0 is never a kind.
const (
	Bytes     Kind = 1
	Counter   Kind = 2
	Set       Kind = 3
	Hash      Kind = 4
	SortedSet Kind = 5
	Sequence  Kind = 6
)

// Limits on a frame: the payload as a whole, and the members, fields and
// elements a structured value may hold.
const (
	MaxValueSize = 256 << 10 // the longest payload a value may carry
	MaxMembers   = 65536     // members of a set or sorted set, fields of a hash, elements of a sequence
	MaxMember    = 4096      // the longest member, field name or sequence element
)

// Member is a set member with the tag of the write that added it (D8): a
// member removed on one side and added again on the other is present.
type Member struct {
	Elem []byte
	Tag  uint64
}

// Scored is a sorted-set member: its bytes, its score and its write tag.
type Scored struct {
	Member []byte
	Score  float64
	Tag    uint64
}

// Value is one key's value: its kind and the field for that kind.
type Value struct {
	Kind    Kind
	Bytes   []byte            // Bytes
	Counter int64             // Counter
	Members []Member          // Set: distinct (Elem, Tag) pairs
	Fields  map[string][]byte // Hash
	Scores  []Scored          // SortedSet: distinct (Member, Tag) pairs
	Seq     [][]byte          // Sequence, in order
}

// ErrValue is a frame or a value that is not one of ours.
var ErrValue = errors.New("kv: not a value")

// EncodeValue returns a value's frame: kind u8 · payload, the payload the
// kind's canonical encoding (docs/DESIGN.md §3).
func EncodeValue(v Value) ([]byte, error) {
	if v.Kind != Bytes {
		return nil, fmt.Errorf("%w: kind %d", ErrValue, v.Kind)
	}
	if len(v.Bytes) > MaxValueSize {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrValue, len(v.Bytes), MaxValueSize)
	}
	f := make([]byte, 0, 1+len(v.Bytes))
	f = append(f, byte(v.Kind))
	return append(f, v.Bytes...), nil
}

// DecodeValue parses a frame; every other byte string is refused.
func DecodeValue(b []byte) (Value, error) {
	if len(b) == 0 {
		return Value{}, fmt.Errorf("%w: an empty frame", ErrValue)
	}
	if Kind(b[0]) != Bytes {
		return Value{}, fmt.Errorf("%w: kind %d", ErrValue, b[0])
	}
	if len(b)-1 > MaxValueSize {
		return Value{}, fmt.Errorf("%w: %d bytes, limit %d", ErrValue, len(b)-1, MaxValueSize)
	}
	return Value{Kind: Bytes, Bytes: append([]byte(nil), b[1:]...)}, nil
}
