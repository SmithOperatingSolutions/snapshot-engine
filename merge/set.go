package merge

// Tag names the write that added a member to a set: a member added twice, in
// two writes, is two tagged members. A model that keeps no such history uses
// one tag for every member, and then a member removed on one side and added
// again on the other reads as unchanged, not re-added.
type Tag uint64

// Tagged is a set member with the tag of the write that added it.
type Tagged[T comparable] struct {
	Elem T
	Tag  Tag
}

// Members is a set as the merge sees it: every tagged member present.
type Members[T comparable] map[Tagged[T]]bool

// Has reports whether elem is present under any tag.
func (m Members[T]) Has(elem T) bool {
	for t := range m {
		if t.Elem == elem {
			return true
		}
	}
	return false
}

// Set merges two sides of a set: a tagged member is present when both sides
// have it, or when one side added it (it is not in the base); a member one
// side removed and the other kept is gone; a member one side removed and the
// other added again, under a new tag, is present (observed remove).
func Set[T comparable](base, ours, theirs Members[T]) Members[T] {
	out := Members[T]{}
	for t := range ours {
		if theirs[t] || !base[t] { // kept by both, or added here
			out[t] = true
		}
	}
	for t := range theirs {
		if !base[t] { // added there
			out[t] = true
		}
	}
	return out
}
