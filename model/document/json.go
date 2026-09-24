// Package document is the document model (model id 4): a collection of
// records by id, each a JSON-like tree of fields, as one object; a prolly map
// from record id to the record's canonical text behind a versioned frame. A
// record is merged by field path through the merge library: two writers on
// different fields both land, one field changed two ways is a conflict at
// that record naming the field. Input is JSON, parsed by this package's own
// bounded parser and stored canonical: one text per value, fields sorted,
// numbers in one spelling, so equal records are equal bytes.
package document

import (
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

// Limits on a document: its text, its nesting and a number's digits. A
// document past any of them is refused before anything is built.
const (
	MaxDocument     = 1 << 20 // bytes of text, as given and as stored
	MaxDepth        = 64      // nested arrays and objects
	MaxNumberDigits = 1000    // digits of a number's canonical text, either side of the point
)

// ErrDocument is JSON that is not a document this model takes: malformed,
// over a limit, a field twice in one object, text that is not UTF-8.
var ErrDocument = errors.New("document: not a document")

// Parse reads one JSON value (RFC 8259) into a merge.Node: strings with every
// escape, numbers as their canonical decimal text, objects with unique
// fields sorted by name. Anything else is ErrDocument. (Stub.)
func Parse(text []byte) (merge.Node, error) {
	return merge.Node{}, fmt.Errorf("%w: not implemented", ErrDocument)
}

// Encode is the canonical JSON text of a node: fields in the node's order,
// numbers as their text, strings escaped the JSON way with control characters
// as \u00XX, no whitespace. Parse(Encode(n)) is n, and equal values encode to
// equal bytes. (Stub.)
func Encode(n merge.Node) []byte { return nil }
