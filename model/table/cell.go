package table

import (
	"errors"
	"fmt"
)

// Value types a cell holds, by column type: Bool bool; Int2 int16; Int4
// int32; Int8 int64; Float4 float32; Float8 float64; Numeric Numeric; Text
// and Varchar string; Bytea []byte; Date Date; Timestamp Timestamp;
// TimestampTZ TimestampTZ; UUID UUID; JSONB JSONB; and nil for NULL in a
// nullable column. A cell of any other Go type is refused.

// Numeric is an arbitrary-precision decimal in its canonical text: an
// optional '-', digits with no leading zero (but "0"), an optional '.' and
// fraction digits with no trailing zero. Zero is "0".
type Numeric string

// Date is days since 1970-01-01.
type Date int32

// Timestamp is microseconds since 1970-01-01 00:00:00, with no zone.
type Timestamp int64

// TimestampTZ is microseconds since 1970-01-01 00:00:00 UTC.
type TimestampTZ int64

// UUID is a 16-byte identifier.
type UUID [16]byte

// JSONB is a JSON document's bytes: valid UTF-8, not empty. (A parser comes
// with the document model; here the bytes are stored and compared as bytes.)
type JSONB []byte

// Limits on a cell.
const (
	MaxCellLen     = 64 << 10 // the longest encoded cell
	MaxNumericLen  = 1000     // digits in a numeric's text
	maxNumericDigs = 1000
)

// ErrValue is a value that does not fit its column.
var ErrValue = errors.New("table: value does not fit its column")

// encodeCell appends v encoded for column c so that byte order is the
// type's order, NULLs first: 0x00 for NULL, else 0x01 then the type's
// encoding. It refuses a value that does not fit the column.
func encodeCell(b []byte, c Column, v any) ([]byte, error) {
	return nil, fmt.Errorf("%w: not implemented", ErrValue)
}

// decodeCell reads one cell of column c from b and returns the value and
// the rest of b.
func decodeCell(b []byte, c Column) (any, []byte, error) {
	return nil, nil, errors.New("not implemented")
}

// ParseNumeric reads a decimal in any of its spellings ("1.50", "-0", "01",
// "2e3") into its canonical text. Anything else is ErrValue.
func ParseNumeric(s string) (Numeric, error) {
	return "", fmt.Errorf("%w: not implemented", ErrValue)
}
