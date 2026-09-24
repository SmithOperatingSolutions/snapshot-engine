package kv

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"github.com/SmithOperatingSolutions/snapshot-core/model/mapobject"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

// resolve decides a key both sides changed from base, by the kind of its
// value (docs/specs/engine-layers.md, "model/kv"): bytes take one side or
// conflict; a counter adds both sides' deltas, even equal ones; a set merges as an
// observed-remove set over write tags; a hash per field; a sorted set per
// member; a sequence by element identity and position. A deletion on
// either side, a value changed to different kinds on both sides, or a kind
// changed on one side follow mapobject.Disagreement. The conflict's location
// is the key (the helper's); the field or member at fault is in the reason.
//
// A sequence merge the library flags (both sides inserted at one position;
// both blocks kept in key order) is clean here: the port has a conflict and
// nothing softer, the result is deterministic and documented (D9), and the
// engine API is where a flag would be surfaced to a caller who asks.
func resolve(key []byte, ours, theirs prolly.Change) (value []byte, put bool, reason string) {
	if ours.Kind == prolly.Removed || theirs.Kind == prolly.Removed {
		return mapobject.Disagreement(key, ours, theirs)
	}
	o, err := DecodeValue(ours.To)
	if err != nil {
		return nil, false, "our value is not a value: " + err.Error()
	}
	th, err := DecodeValue(theirs.To)
	if err != nil {
		return nil, false, "their value is not a value: " + err.Error()
	}
	if o.Kind != th.Kind {
		return nil, false, fmt.Sprintf("changed to different kinds on both sides (%d and %d)", o.Kind, th.Kind)
	}
	var base Value
	if ours.From != nil {
		if base, err = DecodeValue(ours.From); err != nil {
			return nil, false, "the base value is not a value: " + err.Error()
		}
	}
	if ours.From == nil || base.Kind != o.Kind || (o.Kind != Counter && bytes.Equal(ours.To, theirs.To)) {
		// Added on both sides, the kind changed on both, or the same value
		// on both: nothing to merge over, and Disagreement says whether the
		// two agree. Only a counter's equal changes are two changes, whose
		// deltas both count.
		return mapobject.Disagreement(key, ours, theirs)
	}
	var merged Value
	switch o.Kind {
	case Bytes:
		return mapobject.Disagreement(key, ours, theirs)
	case Counter:
		merged = Value{Kind: Counter, Counter: merge.Counter(base.Counter, o.Counter, th.Counter)}
	case Set:
		merged = Value{Kind: Set, Members: fromMembers(merge.Set(toMembers(base.Members), toMembers(o.Members), toMembers(th.Members)))}
	case Hash:
		merged, reason = mergeHash(base, o, th)
	case SortedSet:
		merged, reason = mergeSortedSet(base, o, th)
	case Sequence:
		merged, reason = mergeSequence(base, o, th)
	default:
		return nil, false, fmt.Sprintf("kind %d has no merge", o.Kind)
	}
	if reason != "" {
		return nil, false, reason
	}
	f, err := EncodeValue(merged)
	if err != nil {
		return nil, false, "the merged value cannot be framed: " + err.Error()
	}
	return f, true, ""
}

// toMembers is a set as the merge library sees it: tagged members.
func toMembers(ms []Member) merge.Members[string] {
	out := make(merge.Members[string], len(ms))
	for _, m := range ms {
		out[merge.Tagged[string]{Elem: string(m.Elem), Tag: merge.Tag(m.Tag)}] = true
	}
	return out
}

func fromMembers(ms merge.Members[string]) []Member {
	out := make([]Member, 0, len(ms))
	for m, present := range ms {
		if present {
			out = append(out, Member{Elem: []byte(m.Elem), Tag: uint64(m.Tag)})
		}
	}
	return sortedMembers(out)
}

// field is a hash field as a scalar the merge library compares: present
// or not, and its value.
type field struct {
	present bool
	value   string
}

// mergeHash merges per field: a field changed on one side lands, the same
// field changed differently on both is a conflict naming it.
func mergeHash(base, o, th Value) (Value, string) {
	names := map[string]bool{}
	for _, v := range []Value{base, o, th} {
		for n := range v.Fields {
			names[n] = true
		}
	}
	merged := Value{Kind: Hash, Fields: map[string][]byte{}}
	var conflicts []string
	for _, n := range sortedNames(names) {
		r := merge.Scalar(fieldOf(base, n), fieldOf(o, n), fieldOf(th, n))
		if !r.Clean() {
			conflicts = append(conflicts, fmt.Sprintf("field %q: %s", n, r.Conflicts[0].Reason))
			continue
		}
		if r.Value.present {
			merged.Fields[n] = []byte(r.Value.value)
		}
	}
	return merged, strings.Join(conflicts, "; ")
}

func fieldOf(v Value, name string) field {
	b, ok := v.Fields[name]
	return field{present: ok, value: string(b)}
}

func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// mergeSortedSet merges presence as a set of tagged members and, for every
// member present in the result, its score as a scalar: a score changed
// differently on both sides is a conflict naming the member.
func mergeSortedSet(base, o, th Value) (Value, string) {
	present := merge.Set(scoredMembers(base.Scores), scoredMembers(o.Scores), scoredMembers(th.Scores))
	merged := Value{Kind: SortedSet}
	var conflicts []string
	keys := make([]merge.Tagged[string], 0, len(present))
	for m, ok := range present {
		if ok {
			keys = append(keys, m)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Elem != keys[j].Elem {
			return keys[i].Elem < keys[j].Elem
		}
		return keys[i].Tag < keys[j].Tag
	})
	for _, m := range keys {
		r := merge.Scalar(scoreOf(base, m), scoreOf(o, m), scoreOf(th, m))
		if !r.Clean() {
			conflicts = append(conflicts, fmt.Sprintf("member %q: score %s", m.Elem, r.Conflicts[0].Reason))
			continue
		}
		s := r.Value
		if !s.present { // present in the set by tag but with no score on the side that has it: take whichever side holds it
			for _, side := range []Value{o, th} {
				if s = scoreOf(side, m); s.present {
					break
				}
			}
		}
		merged.Scores = append(merged.Scores, Scored{Member: []byte(m.Elem), Score: s.value, Tag: uint64(m.Tag)})
	}
	return merged, strings.Join(conflicts, "; ") // EncodeValue orders the scores
}

// score is a sorted-set member's score as a scalar the merge library
// compares: present or not, and its value.
type score struct {
	present bool
	value   float64
}

func scoredMembers(ss []Scored) merge.Members[string] {
	out := make(merge.Members[string], len(ss))
	for _, s := range ss {
		out[merge.Tagged[string]{Elem: string(s.Member), Tag: merge.Tag(s.Tag)}] = true
	}
	return out
}

func scoreOf(v Value, m merge.Tagged[string]) score {
	for _, s := range v.Scores {
		if uint64(m.Tag) == s.Tag && string(s.Member) == m.Elem {
			return score{present: true, value: s.Score}
		}
	}
	return score{}
}

// mergeSequence merges by element identity and position through the
// library's diff3; a flagged position (both sides inserted there) is clean
// and deterministic, two changes to one stretch a conflict.
func mergeSequence(base, o, th Value) (Value, string) {
	r := merge.Sequence(base.Seq, o.Seq, th.Seq, func(e []byte) string { return string(e) })
	if !r.Clean() {
		reasons := make([]string, 0, len(r.Conflicts))
		for _, c := range r.Conflicts {
			reasons = append(reasons, fmt.Sprintf("sequence at %s: %s", c.Path, c.Reason))
		}
		return Value{}, strings.Join(reasons, "; ")
	}
	return Value{Kind: Sequence, Seq: r.Value}, ""
}
