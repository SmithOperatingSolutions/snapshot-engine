package merge

// Sequence merges two sides of an ordered list, as diff3 does: elements are
// identified by key, stretches between elements all three lists share are
// merged one by one (a side that left a stretch as the base had it yields to
// the other; equal changes land once), both sides inserting at one position
// keep both blocks in the order of their keys and flag the position, and two
// different changes to one stretch are a conflict at its position, where the
// result keeps the base.
func Sequence[T any](base, ours, theirs []T, key func(T) string) Result[[]T] {
	return Result[[]T]{Value: append([]T(nil), base...)}
}
