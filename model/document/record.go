package document

import (
	"fmt"

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
// string that is not UTF-8, a text over the limit) is refused. (Stub.)
func EncodeRecord(n merge.Node) ([]byte, error) {
	return nil, fmt.Errorf("%w: not implemented", ErrDocument)
}

// DecodeRecord reads a stored record: the format byte, then canonical text
// that parses and encodes back to the same bytes. Anything else is
// chunk.ErrCorrupt. (Stub.)
func DecodeRecord(b []byte) (merge.Node, error) {
	return merge.Node{}, fmt.Errorf("%w: not implemented", ErrDocument)
}
