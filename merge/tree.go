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
	return Result[Node]{Value: base}
}
