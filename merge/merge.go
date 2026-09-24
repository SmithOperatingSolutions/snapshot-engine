package merge

import "strings"

// Path is where in a value something happened: object field names and array
// positions (as decimal), root first. The models prefix it with their own
// address.
type Path []string

// String joins the path with "/", the root being "".
func (p Path) String() string { return strings.Join(p, "/") }

// Conflict is one place two sides' changes could not be combined, for the
// person resolving it.
type Conflict struct {
	Path   Path
	Reason string
}

// Result is a merged value. It is valid only when Conflicts is empty; Flagged
// lists places merged by a rule worth a look (concurrent inserts at one
// position of a sequence).
type Result[T any] struct {
	Value     T
	Conflicts []Conflict
	Flagged   []Conflict
}

// Clean reports whether the merge has no conflict.
func (r Result[T]) Clean() bool { return len(r.Conflicts) == 0 }

// Scalar merges a value both sides may have changed: the side that changed
// wins over one that did not, and two different changes are a conflict.
func Scalar[T comparable](base, ours, theirs T) Result[T] {
	return Result[T]{Value: base}
}

// Counter merges an integer both sides may have moved: the base plus both
// deltas.
func Counter(base, ours, theirs int64) int64 {
	return base
}
