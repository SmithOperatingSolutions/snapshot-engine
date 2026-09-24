package document

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

// Format is the format version this package writes: the record frame's
// first byte.
const Format = 1

// MaxRecord is the longest stored record: the frame byte and a document at
// the text limit.
const MaxRecord = 1 + MaxDocument

// EncodeRecord is a record as stored: the format byte, then the canonical
// text of the node. A node that is not a document (a number that is not
// canonical text, fields out of order or twice, nesting past the limit, a
// string that is not UTF-8, a text over the limit) is refused.
func EncodeRecord(n merge.Node) ([]byte, error) {
	if err := check(n, 0); err != nil {
		return nil, err
	}
	text := Encode(n)
	if len(text) > MaxDocument {
		return nil, fmt.Errorf("%w: %d bytes of text, the limit is %d", ErrDocument, len(text), MaxDocument)
	}
	return append([]byte{Format}, text...), nil
}

// check is whether a node is a document this model stores: what Parse would
// have produced.
func check(n merge.Node, depth int) error {
	switch n.Kind {
	case merge.Null, merge.Bool:
		return nil
	case merge.Number:
		got, err := Parse([]byte(n.Number))
		if err != nil || got.Kind != merge.Number || got.Number != n.Number {
			return fmt.Errorf("%w: %q is not a number's canonical text", ErrDocument, n.Number)
		}
		return nil
	case merge.String:
		if !utf8.ValidString(n.Text) {
			return fmt.Errorf("%w: a string that is not UTF-8", ErrDocument)
		}
		return nil
	case merge.Array:
		if depth+1 > MaxDepth {
			return fmt.Errorf("%w: nested past %d levels", ErrDocument, MaxDepth)
		}
		for _, e := range n.Elems {
			if err := check(e, depth+1); err != nil {
				return err
			}
		}
		return nil
	case merge.Object:
		if depth+1 > MaxDepth {
			return fmt.Errorf("%w: nested past %d levels", ErrDocument, MaxDepth)
		}
		for i, f := range n.Fields {
			if !utf8.ValidString(f.Name) {
				return fmt.Errorf("%w: a field name that is not UTF-8", ErrDocument)
			}
			if i > 0 && n.Fields[i-1].Name >= f.Name {
				return fmt.Errorf("%w: the field %q out of order or twice", ErrDocument, f.Name)
			}
			if err := check(f.Value, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%w: a node of kind %d", ErrDocument, n.Kind)
}

// DecodeRecord reads a stored record: the format byte, then canonical text
// that parses and encodes back to the same bytes. Anything else is
// chunk.ErrCorrupt.
func DecodeRecord(b []byte) (merge.Node, error) {
	if len(b) == 0 {
		return merge.Node{}, fmt.Errorf("%w: an empty record", chunk.ErrCorrupt)
	}
	if b[0] != Format {
		return merge.Node{}, fmt.Errorf("%w: a record of format %d", chunk.ErrCorrupt, b[0])
	}
	n, err := Parse(b[1:])
	if err != nil {
		return merge.Node{}, fmt.Errorf("%w: a record that is not a document: %w", chunk.ErrCorrupt, err)
	}
	if !bytes.Equal(Encode(n), b[1:]) {
		return merge.Node{}, fmt.Errorf("%w: a record that is not in its canonical text", chunk.ErrCorrupt)
	}
	return n, nil
}
