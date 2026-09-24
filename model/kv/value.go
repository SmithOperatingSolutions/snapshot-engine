// Package kv is the key-value model (model id 6): an object is an ordered map
// from key bytes to a typed value, each value kind with a merge policy of its
// own (docs/specs/engine-layers.md). Bytes take one side or conflict;
// counters add both sides' deltas; sets merge as observed-remove sets over
// write tags; hashes merge per field; sorted sets per member; sequences by
// element identity and position.
package kv

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/SmithOperatingSolutions/snapshot-core/core/wire"
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
// kind's canonical encoding (docs/DESIGN.md §3): a counter is its int64
// little-endian; a set a count then (tag u64, member) in member then tag
// order; a hash a count then (name, value) in name order; a sorted set a
// count then (score bits u64, tag u64, member) in score, member, tag order,
// a negative zero written as zero; a sequence a count then its elements.
func EncodeValue(v Value) ([]byte, error) {
	var w wire.Writer
	w.U8(byte(v.Kind))
	switch v.Kind {
	case Bytes:
		w.Raw(v.Bytes)
	case Counter:
		w.U64(uint64(v.Counter))
	case Set:
		ms := sortedMembers(v.Members)
		if err := checkCount(len(ms)); err != nil {
			return nil, err
		}
		w.Uvarint(uint64(len(ms)))
		for i, m := range ms {
			if i > 0 && sameMember(ms[i-1], m) {
				return nil, fmt.Errorf("%w: set member %q with tag %d twice", ErrValue, m.Elem, m.Tag)
			}
			if err := checkMember(m.Elem); err != nil {
				return nil, err
			}
			w.U64(m.Tag)
			w.LenBytes(m.Elem)
		}
	case Hash:
		names := make([]string, 0, len(v.Fields))
		for n := range v.Fields {
			names = append(names, n)
		}
		sort.Strings(names)
		if err := checkCount(len(names)); err != nil {
			return nil, err
		}
		w.Uvarint(uint64(len(names)))
		for _, n := range names {
			if err := checkMember([]byte(n)); err != nil {
				return nil, err
			}
			w.LenBytes([]byte(n))
			w.LenBytes(v.Fields[n])
		}
	case SortedSet:
		ss := sortedScores(v.Scores)
		if err := checkCount(len(ss)); err != nil {
			return nil, err
		}
		w.Uvarint(uint64(len(ss)))
		for i, s := range ss {
			if math.IsNaN(s.Score) {
				return nil, fmt.Errorf("%w: sorted-set member %q scored NaN", ErrValue, s.Member)
			}
			if i > 0 && sameScored(ss[i-1], s) {
				return nil, fmt.Errorf("%w: sorted-set member %q with tag %d twice", ErrValue, s.Member, s.Tag)
			}
			if err := checkMember(s.Member); err != nil {
				return nil, err
			}
			w.U64(math.Float64bits(s.Score))
			w.U64(s.Tag)
			w.LenBytes(s.Member)
		}
	case Sequence:
		if err := checkCount(len(v.Seq)); err != nil {
			return nil, err
		}
		w.Uvarint(uint64(len(v.Seq)))
		for _, e := range v.Seq {
			if err := checkMember(e); err != nil {
				return nil, err
			}
			w.LenBytes(e)
		}
	default:
		return nil, fmt.Errorf("%w: kind %d", ErrValue, v.Kind)
	}
	f := w.Bytes()
	if len(f)-1 > MaxValueSize {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrValue, len(f)-1, MaxValueSize)
	}
	return f, nil
}

// DecodeValue parses a frame; every other byte string is refused: an
// unknown kind, a payload past the limit, a count or member past its
// limit, members or fields out of their order or repeated, a NaN or
// negative-zero score, and trailing bytes.
func DecodeValue(b []byte) (Value, error) {
	if len(b) == 0 {
		return Value{}, fmt.Errorf("%w: an empty frame", ErrValue)
	}
	if len(b)-1 > MaxValueSize {
		return Value{}, fmt.Errorf("%w: %d bytes, limit %d", ErrValue, len(b)-1, MaxValueSize)
	}
	v := Value{Kind: Kind(b[0])}
	r := wire.NewReader(b[1:])
	switch v.Kind {
	case Bytes:
		v.Bytes = append([]byte(nil), b[1:]...)
		return v, nil
	case Counter:
		v.Counter = int64(r.U64())
	case Set:
		n, err := count(r)
		if err != nil {
			return Value{}, err
		}
		v.Members = make([]Member, 0, n)
		for i := 0; i < n; i++ {
			m := Member{Tag: r.U64(), Elem: bytes.Clone(r.LenBytes(MaxMember))}
			if r.Err() != nil {
				break
			}
			if i > 0 && !lessMember(v.Members[i-1], m) {
				return Value{}, fmt.Errorf("%w: set members out of order or repeated at %q", ErrValue, m.Elem)
			}
			v.Members = append(v.Members, m)
		}
	case Hash:
		n, err := count(r)
		if err != nil {
			return Value{}, err
		}
		v.Fields = make(map[string][]byte, n)
		var last string
		for i := 0; i < n; i++ {
			name := string(r.LenBytes(MaxMember))
			val := bytes.Clone(r.LenBytes(MaxValueSize))
			if r.Err() != nil {
				break
			}
			if i > 0 && name <= last {
				return Value{}, fmt.Errorf("%w: hash fields out of order or repeated at %q", ErrValue, name)
			}
			v.Fields[name], last = val, name
		}
	case SortedSet:
		n, err := count(r)
		if err != nil {
			return Value{}, err
		}
		v.Scores = make([]Scored, 0, n)
		for i := 0; i < n; i++ {
			bits := r.U64()
			s := Scored{Score: math.Float64frombits(bits), Tag: r.U64(), Member: bytes.Clone(r.LenBytes(MaxMember))}
			if r.Err() != nil {
				break
			}
			if math.IsNaN(s.Score) || (s.Score == 0 && bits != 0) {
				return Value{}, fmt.Errorf("%w: sorted-set member %q with a score that is not a score", ErrValue, s.Member)
			}
			if i > 0 && !lessScored(v.Scores[i-1], s) {
				return Value{}, fmt.Errorf("%w: sorted-set members out of order or repeated at %q", ErrValue, s.Member)
			}
			v.Scores = append(v.Scores, s)
		}
	case Sequence:
		n, err := count(r)
		if err != nil {
			return Value{}, err
		}
		v.Seq = make([][]byte, 0, n)
		for i := 0; i < n; i++ {
			e := bytes.Clone(r.LenBytes(MaxMember))
			if r.Err() != nil {
				break
			}
			v.Seq = append(v.Seq, e)
		}
	default:
		return Value{}, fmt.Errorf("%w: kind %d", ErrValue, b[0])
	}
	if err := r.Done(); err != nil {
		return Value{}, fmt.Errorf("%w: a kind %d frame: %w", ErrValue, v.Kind, err)
	}
	return v, nil
}

// count reads a structure's count, bounded by MaxMembers.
func count(r *wire.Reader) (int, error) {
	n := r.Uvarint()
	if err := r.Err(); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrValue, err)
	}
	if n > MaxMembers {
		return 0, fmt.Errorf("%w: %d members, limit %d", ErrValue, n, MaxMembers)
	}
	return int(n), nil
}

func checkCount(n int) error {
	if n > MaxMembers {
		return fmt.Errorf("%w: %d members, limit %d", ErrValue, n, MaxMembers)
	}
	return nil
}

func checkMember(m []byte) error {
	if len(m) > MaxMember {
		return fmt.Errorf("%w: a member of %d bytes, limit %d", ErrValue, len(m), MaxMember)
	}
	return nil
}

// lessMember orders set members by element then tag.
func lessMember(a, b Member) bool {
	if c := bytes.Compare(a.Elem, b.Elem); c != 0 {
		return c < 0
	}
	return a.Tag < b.Tag
}

func sameMember(a, b Member) bool { return a.Tag == b.Tag && bytes.Equal(a.Elem, b.Elem) }

func sortedMembers(ms []Member) []Member {
	out := append([]Member(nil), ms...)
	sort.Slice(out, func(i, j int) bool { return lessMember(out[i], out[j]) })
	return out
}

// lessScored orders sorted-set members by score, then member, then tag.
func lessScored(a, b Scored) bool {
	if a.Score != b.Score {
		return a.Score < b.Score
	}
	if c := bytes.Compare(a.Member, b.Member); c != 0 {
		return c < 0
	}
	return a.Tag < b.Tag
}

func sameScored(a, b Scored) bool {
	return a.Score == b.Score && a.Tag == b.Tag && bytes.Equal(a.Member, b.Member)
}

func sortedScores(ss []Scored) []Scored {
	out := make([]Scored, 0, len(ss))
	for _, s := range ss {
		if s.Score == 0 {
			s.Score = 0 // a negative zero is written as zero
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return lessScored(out[i], out[j]) })
	return out
}
