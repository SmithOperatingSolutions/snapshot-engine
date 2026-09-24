package merge

import (
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

// Equal reports whether two nodes are the same value.
func (n Node) Equal(m Node) bool { return n.Canonical() == m.Canonical() }

// Canonical is a deterministic text of the value, one per distinct value:
// what Sequence keys array elements by.
func (n Node) Canonical() string {
	var b strings.Builder
	n.canonical(&b)
	return b.String()
}

func (n Node) canonical(b *strings.Builder) {
	switch n.Kind {
	case Null:
		b.WriteString("null")
	case Bool:
		b.WriteString(strconv.FormatBool(n.Bool))
	case Number:
		b.WriteString(n.Number)
	case String:
		b.WriteString(strconv.Quote(n.Text))
	case Array:
		b.WriteByte('[')
		for i, e := range n.Elems {
			if i > 0 {
				b.WriteByte(',')
			}
			e.canonical(b)
		}
		b.WriteByte(']')
	case Object:
		b.WriteByte('{')
		for i, f := range n.Fields {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(f.Name))
			b.WriteByte(':')
			f.Value.canonical(b)
		}
		b.WriteByte('}')
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
func Tree(base, ours, theirs Node, o TreeOptions) Result[Node] {
	var r Result[Node]
	v, _ := mergeNode(&r, nil, &base, &ours, &theirs, o)
	r.Value = v
	return r
}

// mergeNode merges one position; nil is an absent value. It returns the
// merged value and whether it is present.
func mergeNode(r *Result[Node], path Path, b, o, t *Node, opts TreeOptions) (Node, bool) {
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
	case o.Kind == Object && t.Kind == Object && (b == nil || b.Kind == Object):
		return mergeFields(r, path, b, o, t, opts), true
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

// mergeFields merges two objects field by field over the union of names.
func mergeFields(r *Result[Node], path Path, b, o, t *Node, opts TreeOptions) Node {
	names := map[string]bool{}
	for _, n := range []*Node{b, o, t} {
		if n != nil {
			for _, f := range n.Fields {
				names[f.Name] = true
			}
		}
	}
	var out []Field
	for _, name := range sortedKeys(names) {
		v, ok := mergeNode(r, append(append(Path(nil), path...), name), field(b, name), field(o, name), field(t, name), opts)
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
	return a.Equal(*b)
}

func deref(n *Node) (Node, bool) {
	if n == nil {
		return Node{}, false
	}
	return *n, true
}

func field(n *Node, name string) *Node {
	if n == nil {
		return nil
	}
	for i := range n.Fields {
		if n.Fields[i].Name == name {
			return &n.Fields[i].Value
		}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
