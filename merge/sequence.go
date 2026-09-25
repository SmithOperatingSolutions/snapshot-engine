package merge

import (
	"fmt"
	"strconv"
)

// MaxSequenceEdits is the most insertions and deletions (a replaced
// element is one of each) Sequence aligns between the base and either
// side. Past it the merge is a conflict at the list, never an allocation
// or a search that grows with the square of the list (DESIGN D18,
// snapshot-engine#4).
const MaxSequenceEdits = 1000

// Sequence merges two sides of an ordered list, as diff3 does: elements are
// identified by key, stretches between elements all three lists share are
// merged one by one (a side that left a stretch as the base had it yields to
// the other; equal changes land once), both sides inserting at one position
// keep both blocks in the order of their keys and flag the position, and two
// different changes to one stretch are a conflict at its position, where the
// result keeps the base. A side more than MaxSequenceEdits insertions and
// deletions from the base, when the other side changed the list too, is a
// conflict at the list (an empty Path), where the result keeps the base.
func Sequence[T any](base, ours, theirs []T, key func(T) string) Result[[]T] {
	kb, ko, kt := keysOf(base, key), keysOf(ours, key), keysOf(theirs, key)
	switch { // a side that left the list as it was yields to the other, whatever it did
	case equalKeys(kt, kb), equalKeys(ko, kt):
		return Result[[]T]{Value: append([]T(nil), ours...)}
	case equalKeys(ko, kb):
		return Result[[]T]{Value: append([]T(nil), theirs...)}
	}
	mo, okO := align(kb, ko)
	mt, okT := align(kb, kt)
	if !okO || !okT {
		return Result[[]T]{Value: append([]T(nil), base...), Conflicts: []Conflict{{
			Reason: fmt.Sprintf("a side changed the list by more than %d insertions and deletions, too many to align", MaxSequenceEdits),
		}}}
	}
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
	switch {
	case equalKeys(ko, kb):
		r.Value = append(r.Value, t...)
		return
	case equalKeys(kt, kb), equalKeys(ko, kt):
		r.Value = append(r.Value, o...)
		return
	}
	at := Path{strconv.Itoa(len(r.Value))} // named only where a flag or a conflict needs it
	switch {
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
// each base position, the side position it is matched to, or -1. It is
// Myers' difference algorithm in linear space (the middle snake, divide
// and conquer) over the keys interned as integers, after the common prefix
// and suffix: memory in proportion to the lists, work at most about
// (n+m)·MaxSequenceEdits. It reports false, having done no more than that,
// when the side is more than MaxSequenceEdits insertions and deletions
// from the base.
func align(base, side []string) ([]int, bool) {
	ids := make(map[string]int, len(base))
	intern := func(ks []string) []int {
		out := make([]int, len(ks))
		for i, k := range ks {
			id, ok := ids[k]
			if !ok {
				id = len(ids)
				ids[k] = id
			}
			out[i] = id
		}
		return out
	}
	half := (min(len(base)+len(side), MaxSequenceEdits) + 1) / 2
	s := aligner{a: intern(base), b: intern(side), out: make([]int, len(base)), off: half + 1}
	for i := range s.out {
		s.out[i] = -1
	}
	s.fwd, s.bwd = make([]int, 2*s.off+1), make([]int, 2*s.off+1)
	if !s.diff(0, len(base), 0, len(side), MaxSequenceEdits) {
		return nil, false
	}
	return s.out, true
}

// aligner holds one alignment: the keys, the matches found so far, and the
// furthest-reaching paths of the middle-snake search, by diagonal k at
// index off+k.
type aligner struct {
	a, b     []int
	out      []int
	fwd, bwd []int
	off      int
}

// diff matches a[x0:x1] against b[y0:y1], which differ by at most limit
// insertions and deletions, or reports false.
func (s *aligner) diff(x0, x1, y0, y1, limit int) bool {
	for x0 < x1 && y0 < y1 && s.a[x0] == s.b[y0] {
		s.out[x0] = y0
		x0, y0 = x0+1, y0+1
	}
	for x0 < x1 && y0 < y1 && s.a[x1-1] == s.b[y1-1] {
		x1, y1 = x1-1, y1-1
		s.out[x1] = y1
	}
	if x0 == x1 || y0 == y1 { // what is left is all insertions or all deletions
		return true
	}
	d, sx, sy, ex, ey := s.middle(x0, x1, y0, y1)
	if d > limit {
		return false
	}
	for x, y := sx, sy; x < ex; x, y = x+1, y+1 {
		s.out[x] = y
	}
	// With the common ends gone d is at least 2, and each half holds fewer
	// than d edits, so the recursion ends, about log2(d) deep.
	return s.diff(x0, sx, y0, sy, d) && s.diff(ex, x1, ey, y1, d)
}

// middle finds the middle snake of a[x0:x1] against b[y0:y1]: the length d
// of a shortest edit script, and a run of matches (sx,sy)-(ex,ey) that one
// such script passes through with about half its edits on either side.
// It searches no further than the arrays reach, MaxSequenceEdits, and
// returns a d past that when the script is longer.
func (s *aligner) middle(x0, x1, y0, y1 int) (d, sx, sy, ex, ey int) {
	n, m := x1-x0, y1-y0
	delta := n - m
	odd := delta&1 != 0
	fwd, bwd, off := s.fwd, s.bwd, s.off
	fwd[off+1], bwd[off+1] = 0, 0
	for dd := 0; dd < off; dd++ {
		for k := -dd; k <= dd; k += 2 {
			x := fwd[off+k+1]
			if k != -dd && (k == dd || fwd[off+k-1] >= fwd[off+k+1]) {
				x = fwd[off+k-1] + 1
			}
			y := x - k
			bx, by := x, y
			for x < n && y < m && s.a[x0+x] == s.b[y0+y] {
				x, y = x+1, y+1
			}
			fwd[off+k] = x
			if kr := delta - k; odd && kr >= -(dd-1) && kr <= dd-1 && x+bwd[off+kr] >= n {
				return 2*dd - 1, x0 + bx, y0 + by, x0 + x, y0 + y
			}
		}
		for k := -dd; k <= dd; k += 2 {
			x := bwd[off+k+1]
			if k != -dd && (k == dd || bwd[off+k-1] >= bwd[off+k+1]) {
				x = bwd[off+k-1] + 1
			}
			y := x - k
			bx, by := x, y
			for x < n && y < m && s.a[x1-1-x] == s.b[y1-1-y] {
				x, y = x+1, y+1
			}
			bwd[off+k] = x
			if kf := delta - k; !odd && kf >= -dd && kf <= dd && x+fwd[off+kf] >= n {
				return 2 * dd, x1 - x, y1 - y, x1 - bx, y1 - by
			}
		}
	}
	return MaxSequenceEdits + 1, 0, 0, 0, 0
}
