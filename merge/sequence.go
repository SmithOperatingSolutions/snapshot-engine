package merge

import "strconv"

// Sequence merges two sides of an ordered list, as diff3 does: elements are
// identified by key, stretches between elements all three lists share are
// merged one by one (a side that left a stretch as the base had it yields to
// the other; equal changes land once), both sides inserting at one position
// keep both blocks in the order of their keys and flag the position, and two
// different changes to one stretch are a conflict at its position, where the
// result keeps the base.
func Sequence[T any](base, ours, theirs []T, key func(T) string) Result[[]T] {
	kb, ko, kt := keysOf(base, key), keysOf(ours, key), keysOf(theirs, key)
	mo, mt := align(kb, ko), align(kb, kt)
	var r Result[[]T]
	i, j, k := 0, 0, 0
	for {
		// The next element all three share: matched in both alignments.
		s := i
		for s < len(base) && (mo[s] < 0 || mt[s] < 0) {
			s++
		}
		oj, tk := len(ours), len(theirs)
		if s < len(base) {
			oj, tk = mo[s], mt[s]
		}
		resolve(&r, base[i:s], ours[j:oj], theirs[k:tk], kb[i:s], ko[j:oj], kt[k:tk])
		if s == len(base) {
			return r
		}
		r.Value = append(r.Value, ours[oj])
		i, j, k = s+1, oj+1, tk+1
	}
}

// resolve appends the merge of one unstable stretch, at the position the
// result has reached.
func resolve[T any](r *Result[[]T], b, o, t []T, kb, ko, kt []string) {
	at := Path{strconv.Itoa(len(r.Value))}
	switch {
	case equalKeys(ko, kb):
		r.Value = append(r.Value, t...)
	case equalKeys(kt, kb):
		r.Value = append(r.Value, o...)
	case equalKeys(ko, kt):
		r.Value = append(r.Value, o...)
	case len(b) == 0: // both inserted here: both blocks, in key order
		if lessKeys(ko, kt) {
			r.Value = append(append(r.Value, o...), t...)
		} else {
			r.Value = append(append(r.Value, t...), o...)
		}
		r.Flagged = append(r.Flagged, Conflict{Path: at, Reason: "both sides inserted here; kept both, in key order"})
	default:
		r.Value = append(r.Value, b...)
		r.Conflicts = append(r.Conflicts, Conflict{Path: at, Reason: "both sides changed this stretch, differently"})
	}
}

func keysOf[T any](xs []T, key func(T) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = key(x)
	}
	return out
}

func equalKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lessKeys orders two blocks: by the first differing key, then the shorter
// first. Total on distinct blocks, so the order does not depend on which
// side is ours.
func lessKeys(a, b []string) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// align matches base to side by a longest common subsequence of keys: for
// each base position, the side position it is matched to, or -1.
func align(base, side []string) []int {
	n, m := len(base), len(side)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if base[i] == side[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	out := make([]int, n)
	for i := range out {
		out[i] = -1
	}
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case base[i] == side[j]:
			out[i] = j
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			i++
		default:
			j++
		}
	}
	return out
}

// MaxSequenceEdits is the most insertions and deletions Sequence aligns
// between the base and either side.
const MaxSequenceEdits = 1000
