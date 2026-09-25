package merge

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Kind is what a Node holds.
type Kind uint8

// The kinds of a JSON-like value.
const (
	Null Kind = iota
	Bool
	Number
	String
	Array
	Object
)

// Node is a JSON-like value as the merge sees it: the shape of a document, a
// JSON cell or a Redis hash. A Number is its canonical decimal text, as the
// model parsed it; an Object's fields are sorted by name and unique.
type Node struct {
	Kind   Kind
	Bool   bool
	Number string
	Text   string
	Elems  []Node
	Fields []Field
}

// Field is one named member of an Object.
type Field struct {
	Name  string
	Value Node
}

// Obj builds an Object; fields are sorted by name.
func Obj(fields ...Field) Node {
	fs := append([]Field(nil), fields...)
	sort.Slice(fs, func(i, j int) bool { return fs[i].Name < fs[j].Name })
	return Node{Kind: Object, Fields: fs}
}

// Arr builds an Array.
func Arr(elems ...Node) Node { return Node{Kind: Array, Elems: append([]Node(nil), elems...)} }

// Str builds a String.
func Str(s string) Node { return Node{Kind: String, Text: s} }

// Num builds a Number from its canonical text.
func Num(text string) Node { return Node{Kind: Number, Number: text} }

// Boolean builds a Bool.
func Boolean(b bool) Node { return Node{Kind: Bool, Bool: b} }

// MaxDepth is the deepest nesting of arrays and objects Tree merges, the
// document model's limit; a value nested deeper is a conflict at the root
// (snapshot-engine#6).
const MaxDepth = 64

// Equal reports whether two nodes are the same value.
func (n Node) Equal(m Node) bool { return n.Canonical() == m.Canonical() }

// Canonical is a deterministic text of the value, one per distinct value:
// what Sequence keys array elements by.
func (n Node) Canonical() string {
	var b strings.Builder
	n.canonical(&b)
	return b.String()
}

// canonical writes n's text with a stack of its own, one small frame per
// level, so a node nested however deep costs heap in proportion to its
// depth and never the goroutine's stack (snapshot-engine#6).
func (n Node) canonical(b *strings.Builder) {
	type frame struct {
		n    *Node
		next int // the next element or field to write
	}
	stack := []frame{{n: &n}}
	for len(stack) > 0 {
		f := &stack[len(stack)-1]
		var child *Node
		switch f.n.Kind {
		case Null:
			b.WriteString("null")
		case Bool:
			b.WriteString(strconv.FormatBool(f.n.Bool))
		case Number:
			b.WriteString(f.n.Number)
		case String:
			b.WriteString(strconv.Quote(f.n.Text))
		case Array:
			switch {
			case f.next == 0:
				b.WriteByte('[')
			case f.next < len(f.n.Elems):
				b.WriteByte(',')
			}
			if f.next == len(f.n.Elems) {
				b.WriteByte(']')
				break
			}
			child = &f.n.Elems[f.next]
		case Object:
			switch {
			case f.next == 0:
				b.WriteByte('{')
			case f.next < len(f.n.Fields):
				b.WriteByte(',')
			}
			if f.next == len(f.n.Fields) {
				b.WriteByte('}')
				break
			}
			b.WriteString(strconv.Quote(f.n.Fields[f.next].Name))
			b.WriteByte(':')
			child = &f.n.Fields[f.next].Value
		}
		if child == nil { // this node is written
			stack = stack[:len(stack)-1]
			continue
		}
		f.next++
		stack = append(stack, frame{n: child})
	}
}

// TreeOptions tunes a Tree merge.
type TreeOptions struct {
	// Counter says which paths hold counters: integer Numbers merged by
	// adding both sides' deltas instead of conflicting.
	Counter func(Path) bool
}

// Tree merges two sides of a JSON-like value by path: fields changed on
// different paths both land; a field changed on both sides is a Scalar merge
// at its path, a Counter merge where the options say so, a merge of fields
// when both sides hold objects, and a Sequence merge of elements when both
// hold arrays; a field deleted on one side and changed on the other is a
// conflict at its path, where the result keeps the base.
//
// Tree merges values nested no deeper than MaxDepth; one nested deeper is
// a conflict at the root, where the result keeps the base. It compares
// each node of the three once.
func Tree(base, ours, theirs Node, o TreeOptions) Result[Node] {
	for _, n := range []*Node{&base, &ours, &theirs} {
		if deeper(n, MaxDepth) {
			return Result[Node]{Value: base, Conflicts: []Conflict{{Reason: fmt.Sprintf("nested past %d arrays and objects", MaxDepth)}}}
		}
	}
	var r Result[Node]
	v, _ := mergeNode(&r, nil, &base, &ours, &theirs, o)
	r.Value = v
	return r
}

// deeper reports whether n nests more than limit arrays and objects,
// looking no deeper than that.
func deeper(n *Node, limit int) bool {
	if n.Kind != Array && n.Kind != Object {
		return false
	}
	if limit == 0 {
		return true
	}
	for i := range n.Elems {
		if deeper(&n.Elems[i], limit-1) {
			return true
		}
	}
	for i := range n.Fields {
		if deeper(&n.Fields[i].Value, limit-1) {
			return true
		}
	}
	return false
}

// mergeNode merges one position; nil is an absent value. It returns the
// merged value and whether it is present. Two objects over an object or
// nothing merge field by field straight away: where two of the three are
// equal the field merge yields exactly what comparing them first would,
// and comparing whole subtrees at every level cost depth times size.
func mergeNode(r *Result[Node], path Path, b, o, t *Node, opts TreeOptions) (Node, bool) {
	if o != nil && t != nil && o.Kind == Object && t.Kind == Object && (b == nil || b.Kind == Object) {
		return mergeFields(r, path, b, o, t, opts), true
	}
	switch {
	case same(o, t):
		return deref(o)
	case same(o, b):
		return deref(t)
	case same(t, b):
		return deref(o)
	case o == nil || t == nil:
		r.Conflicts = append(r.Conflicts, Conflict{Path: path, Reason: "deleted on one side and changed on the other"})
		return deref(b)
	case o.Kind == Number && t.Kind == Number && (b == nil || b.Kind == Number) && opts.Counter != nil && opts.Counter(path):
		return mergeCounter(r, path, b, o, t)
	case b != nil && o.Kind == Array && t.Kind == Array && b.Kind == Array:
		seq := Sequence(b.Elems, o.Elems, t.Elems, Node.Canonical)
		for _, c := range seq.Conflicts {
			r.Conflicts = append(r.Conflicts, Conflict{Path: append(append(Path(nil), path...), c.Path...), Reason: c.Reason})
		}
		for _, c := range seq.Flagged {
			r.Flagged = append(r.Flagged, Conflict{Path: append(append(Path(nil), path...), c.Path...), Reason: c.Reason})
		}
		return Node{Kind: Array, Elems: seq.Value}, true
	}
	r.Conflicts = append(r.Conflicts, Conflict{Path: path, Reason: "both sides changed it, to different values"})
	return deref(b)
}

// mergeFields merges two objects field by field over the union of names,
// each object's fields indexed once by name.
func mergeFields(r *Result[Node], path Path, b, o, t *Node, opts TreeOptions) Node {
	var byName [3]map[string]*Node
	names := map[string]bool{}
	for i, n := range []*Node{b, o, t} {
		if n == nil {
			continue
		}
		byName[i] = make(map[string]*Node, len(n.Fields))
		for j := range n.Fields {
			if f := &n.Fields[j]; byName[i][f.Name] == nil { // the first of a name, if a caller repeated one
				byName[i][f.Name] = &f.Value
				names[f.Name] = true
			}
		}
	}
	var out []Field
	for _, name := range sortedKeys(names) {
		v, ok := mergeNode(r, append(append(Path(nil), path...), name), byName[0][name], byName[1][name], byName[2][name], opts)
		if ok {
			out = append(out, Field{Name: name, Value: v})
		}
	}
	return Obj(out...)
}

// mergeCounter adds both sides' deltas to an integer; a number that is not
// an integer is a conflict, since a counter it is not.
func mergeCounter(r *Result[Node], path Path, b, o, t *Node) (Node, bool) {
	var base int64
	if b != nil {
		var err error
		if base, err = strconv.ParseInt(b.Number, 10, 64); err != nil {
			r.Conflicts = append(r.Conflicts, Conflict{Path: path, Reason: "a counter that is not an integer"})
			return deref(b)
		}
	}
	ours, err1 := strconv.ParseInt(o.Number, 10, 64)
	theirs, err2 := strconv.ParseInt(t.Number, 10, 64)
	if err1 != nil || err2 != nil {
		r.Conflicts = append(r.Conflicts, Conflict{Path: path, Reason: "a counter that is not an integer"})
		return deref(b)
	}
	return Num(strconv.FormatInt(Counter(base, ours, theirs), 10)), true
}

func same(a, b *Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	return equal(a, b)
}

// equal is whether two nodes are the same value, compared part by part
// without building their text. Tree calls it only on values no deeper than
// MaxDepth.
func equal(a, b *Node) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case Bool:
		return a.Bool == b.Bool
	case Number:
		return a.Number == b.Number
	case String:
		return a.Text == b.Text
	case Array:
		if len(a.Elems) != len(b.Elems) {
			return false
		}
		for i := range a.Elems {
			if !equal(&a.Elems[i], &b.Elems[i]) {
				return false
			}
		}
	case Object:
		if len(a.Fields) != len(b.Fields) {
			return false
		}
		for i := range a.Fields {
			if a.Fields[i].Name != b.Fields[i].Name || !equal(&a.Fields[i].Value, &b.Fields[i].Value) {
				return false
			}
		}
	}
	return true
}

func deref(n *Node) (Node, bool) {
	if n == nil {
		return Node{}, false
	}
	return *n, true
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
