package table

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
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
	MaxCellLen     = 1 << 20 // the longest encoded cell; a row past the map's inline limit is a stream
	MaxNumericLen  = 1000    // digits in a numeric's text
	maxNumericDigs = 1000
	// maxExponent bounds a numeric's written exponent, six digits as the
	// document model's: far past the exponent any numeric of MaxNumericLen
	// bytes of text can need (about 2,000), and small enough that the
	// arithmetic on it cannot overflow (snapshot-engine#3).
	maxExponent = 999999
)

// ErrValue is a value that does not fit its column.
var ErrValue = errors.New("table: value does not fit its column")

// encodeCell appends v encoded for column c so that byte order is the
// type's order, NULLs first: 0x00 for NULL, else 0x01 then the type's
// encoding. It refuses a value that does not fit the column.
func encodeCell(b []byte, c Column, v any) ([]byte, error) {
	if v == nil {
		if !c.Nullable {
			return nil, fmt.Errorf("%w: NULL in column %q, which is not nullable", ErrValue, c.Name)
		}
		return append(b, 0x00), nil
	}
	start := len(b)
	b = append(b, 0x01)
	var err error
	switch c.Type {
	case TypeBool:
		x, ok := v.(bool)
		if !ok {
			return nil, wrongType(c, v)
		}
		if x {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
	case TypeInt2:
		x, ok := v.(int16)
		if !ok {
			return nil, wrongType(c, v)
		}
		b = binary.BigEndian.AppendUint16(b, uint16(x)^0x8000)
	case TypeInt4:
		x, ok := v.(int32)
		if !ok {
			return nil, wrongType(c, v)
		}
		b = binary.BigEndian.AppendUint32(b, uint32(x)^(1<<31))
	case TypeInt8:
		x, ok := v.(int64)
		if !ok {
			return nil, wrongType(c, v)
		}
		b = binary.BigEndian.AppendUint64(b, uint64(x)^(1<<63))
	case TypeFloat4:
		x, ok := v.(float32)
		if !ok {
			return nil, wrongType(c, v)
		}
		b = binary.BigEndian.AppendUint32(b, orderedFloat32(x))
	case TypeFloat8:
		x, ok := v.(float64)
		if !ok {
			return nil, wrongType(c, v)
		}
		b = binary.BigEndian.AppendUint64(b, orderedFloat64(x))
	case TypeNumeric:
		x, ok := v.(Numeric)
		if !ok {
			return nil, wrongType(c, v)
		}
		if b, err = encodeNumeric(b, x); err != nil {
			return nil, fmt.Errorf("%w: column %q: %w", ErrValue, c.Name, err)
		}
	case TypeText, TypeVarchar:
		x, ok := v.(string)
		if !ok {
			return nil, wrongType(c, v)
		}
		if !utf8.ValidString(x) {
			return nil, fmt.Errorf("%w: column %q: text that is not UTF-8", ErrValue, c.Name)
		}
		if c.Type == TypeVarchar {
			if n := utf8.RuneCountInString(x); n > int(c.MaxLen) {
				return nil, fmt.Errorf("%w: %d characters in column %q, varchar(%d)", ErrValue, n, c.Name, c.MaxLen)
			}
		}
		b = escape(b, []byte(x))
	case TypeBytea:
		x, ok := v.([]byte)
		if !ok {
			return nil, wrongType(c, v)
		}
		b = escape(b, x)
	case TypeDate:
		x, ok := v.(Date)
		if !ok {
			return nil, wrongType(c, v)
		}
		b = binary.BigEndian.AppendUint32(b, uint32(x)^(1<<31))
	case TypeTimestamp:
		x, ok := v.(Timestamp)
		if !ok {
			return nil, wrongType(c, v)
		}
		b = binary.BigEndian.AppendUint64(b, uint64(x)^(1<<63))
	case TypeTimestampTZ:
		x, ok := v.(TimestampTZ)
		if !ok {
			return nil, wrongType(c, v)
		}
		b = binary.BigEndian.AppendUint64(b, uint64(x)^(1<<63))
	case TypeUUID:
		x, ok := v.(UUID)
		if !ok {
			return nil, wrongType(c, v)
		}
		b = append(b, x[:]...)
	case TypeJSONB:
		x, ok := v.(JSONB)
		if !ok {
			return nil, wrongType(c, v)
		}
		if len(x) == 0 || !utf8.Valid(x) {
			return nil, fmt.Errorf("%w: column %q: a JSONB must be UTF-8 and not empty", ErrValue, c.Name)
		}
		b = escape(b, x)
	default:
		return nil, fmt.Errorf("%w: column %q has type %s", ErrValue, c.Name, c.Type)
	}
	if len(b)-start > MaxCellLen {
		return nil, fmt.Errorf("%w: column %q: a cell of %d bytes, over %d", ErrValue, c.Name, len(b)-start, MaxCellLen)
	}
	return b, nil
}

func wrongType(c Column, v any) error {
	return fmt.Errorf("%w: column %q is %s, given a %T", ErrValue, c.Name, c.Type, v)
}

// orderedFloat64 maps a float to an unsigned integer whose order is the
// float's: positives have the sign bit flipped, negatives every bit; -0 is
// +0 and every NaN is one NaN, which sorts above everything (SQL's order).
func orderedFloat64(f float64) uint64 {
	if math.IsNaN(f) {
		return math.MaxUint64
	}
	if f == 0 {
		f = 0 // -0 is +0
	}
	u := math.Float64bits(f)
	if u&(1<<63) != 0 {
		return ^u
	}
	return u ^ (1 << 63)
}

func unorderedFloat64(u uint64) float64 {
	if u == math.MaxUint64 {
		return math.NaN()
	}
	if u&(1<<63) != 0 {
		return math.Float64frombits(u ^ (1 << 63))
	}
	return math.Float64frombits(^u)
}

func orderedFloat32(f float32) uint32 {
	if math.IsNaN(float64(f)) {
		return math.MaxUint32
	}
	if f == 0 {
		f = 0
	}
	u := math.Float32bits(f)
	if u&(1<<31) != 0 {
		return ^u
	}
	return u ^ (1 << 31)
}

func unorderedFloat32(u uint32) float32 {
	if u == math.MaxUint32 {
		return float32(math.NaN())
	}
	if u&(1<<31) != 0 {
		return math.Float32frombits(u ^ (1 << 31))
	}
	return math.Float32frombits(^u)
}

// escape appends s with every 0x00 as 0x00 0xFF and 0x00 0x00 at the end,
// which keeps byte order and lets a cell be found in a key.
func escape(b, s []byte) []byte {
	for _, c := range s {
		if c == 0 {
			b = append(b, 0x00, 0xFF)
		} else {
			b = append(b, c)
		}
	}
	return append(b, 0x00, 0x00)
}

// unescape reads an escaped string from b, returning it and the rest.
func unescape(b []byte) ([]byte, []byte, error) {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] != 0 {
			out = append(out, b[i])
			continue
		}
		if i+1 >= len(b) {
			return nil, nil, fmt.Errorf("%w: a string's escape at its end", chunk.ErrCorrupt)
		}
		switch b[i+1] {
		case 0x00:
			return out, b[i+2:], nil
		case 0xFF:
			out = append(out, 0)
			i++
		default:
			return nil, nil, fmt.Errorf("%w: a string escapes 0x00 %02x", chunk.ErrCorrupt, b[i+1])
		}
	}
	return nil, nil, fmt.Errorf("%w: a string with no end", chunk.ErrCorrupt)
}

// Numeric sign classes.
const (
	numNegative = 0x01
	numZero     = 0x02
	numPositive = 0x03
)

// ParseNumeric reads a decimal in any of its spellings ("1.50", "-0", "01",
// "2e3") into its canonical text. Anything else is ErrValue, and so is a
// value whose canonical text would pass MaxNumericLen, refused from its
// length before the text is built.
func ParseNumeric(s string) (Numeric, error) {
	neg, digits, exp, err := parseDecimal(s)
	if err != nil {
		return "", err
	}
	if n := canonicalLen(neg, len(digits), exp); len(digits) > 0 && n > MaxNumericLen {
		return "", fmt.Errorf("%w: a numeric of %d bytes of text, over %d", ErrValue, n, MaxNumericLen)
	}
	return canonical(neg, digits, exp), nil
}

// parseDecimal splits s into a sign, its significant digits (no leading or
// trailing zeros; none for zero) and the adjusted exponent e such that the
// value is 0.d1d2... × 10^e.
func parseDecimal(s string) (neg bool, digits []byte, exp int, err error) {
	if len(s) == 0 || len(s) > MaxNumericLen {
		return false, nil, 0, fmt.Errorf("%w: a numeric of %d bytes", ErrValue, len(s))
	}
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	}
	var mant []byte
	point := -1
	sawDigit := false
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			mant = append(mant, c-'0')
			sawDigit = true
		case c == '.' && point < 0:
			point = len(mant)
		case (c == 'e' || c == 'E') && sawDigit:
			e, ok := parseExponent(s[i+1:])
			if !ok {
				return false, nil, 0, fmt.Errorf("%w: a numeric's exponent %q", ErrValue, s[i+1:])
			}
			exp = e
			i = len(s)
		default:
			return false, nil, 0, fmt.Errorf("%w: a numeric %q", ErrValue, s)
		}
	}
	if !sawDigit {
		return false, nil, 0, fmt.Errorf("%w: a numeric %q", ErrValue, s)
	}
	if point < 0 {
		point = len(mant)
	}
	// value = 0.mant × 10^(point + exp); strip leading zeros (each moves the point) and trailing zeros.
	lead := 0
	for lead < len(mant) && mant[lead] == 0 {
		lead++
	}
	digits = mant[lead:]
	exp += point - lead
	for len(digits) > 0 && digits[len(digits)-1] == 0 {
		digits = digits[:len(digits)-1]
	}
	if len(digits) == 0 {
		return false, nil, 0, nil
	}
	if len(digits) > maxNumericDigs {
		return false, nil, 0, fmt.Errorf("%w: a numeric of %d digits", ErrValue, len(digits))
	}
	return neg, digits, exp, nil
}

// parseExponent reads a written exponent: an optional '-' then decimal
// digits, whose value is at most maxExponent. It stops at the first digit
// past the bound, so no spelling of an exponent costs more than its bytes.
func parseExponent(s string) (int, bool) {
	neg := len(s) > 0 && s[0] == '-'
	if neg {
		s = s[1:]
	}
	if len(s) == 0 {
		return 0, false
	}
	e := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		if e = e*10 + int(s[i]-'0'); e > maxExponent {
			return 0, false
		}
	}
	if neg {
		e = -e
	}
	return e, true
}

// canonicalLen is the length of canonical's text for a nonzero value of n
// digits, without building it: the exponent comes from the cell, and the
// text is as long as the exponent says (snapshot-engine#2).
func canonicalLen(neg bool, n, exp int) int {
	sign := 0
	if neg {
		sign = 1
	}
	switch {
	case exp <= 0:
		return sign + 2 - exp + n // "0." then -exp zeros, then the digits
	case exp >= n:
		return sign + exp // the digits, then exp-n zeros
	default:
		return sign + n + 1 // the digits with a point among them
	}
}

// canonical writes the decimal text of 0.digits × 10^exp.
func canonical(neg bool, digits []byte, exp int) Numeric {
	if len(digits) == 0 {
		return "0"
	}
	var sb strings.Builder
	if neg {
		sb.WriteByte('-')
	}
	switch {
	case exp <= 0:
		sb.WriteString("0.")
		for range -exp {
			sb.WriteByte('0')
		}
		for _, d := range digits {
			sb.WriteByte('0' + d)
		}
	case exp >= len(digits):
		for _, d := range digits {
			sb.WriteByte('0' + d)
		}
		for range exp - len(digits) {
			sb.WriteByte('0')
		}
	default:
		for _, d := range digits[:exp] {
			sb.WriteByte('0' + d)
		}
		sb.WriteByte('.')
		for _, d := range digits[exp:] {
			sb.WriteByte('0' + d)
		}
	}
	return Numeric(sb.String())
}

// encodeNumeric appends n, which must be canonical: a sign class (0x01
// negative, 0x02 zero, 0x03 positive), then for a nonzero value the adjusted
// exponent as a big-endian int32 with the sign bit flipped and the digits as
// bytes 0x01..0x0A ended by 0x00; a negative value has the exponent's bytes
// and the digits complemented (10-d) and ends with 0xFF, so that byte order
// is numeric order.
func encodeNumeric(b []byte, n Numeric) ([]byte, error) {
	neg, digits, exp, err := parseDecimal(string(n))
	if err != nil {
		return nil, err
	}
	if (len(digits) > 0 && canonicalLen(neg, len(digits), exp) != len(n)) || canonical(neg, digits, exp) != n {
		return nil, fmt.Errorf("%w: a numeric %q is not canonical", ErrValue, string(n))
	}
	if len(digits) == 0 {
		return append(b, numZero), nil
	}
	if exp < math.MinInt32 || exp > math.MaxInt32 {
		return nil, fmt.Errorf("%w: a numeric's exponent %d", ErrValue, exp)
	}
	e := uint32(int32(exp)) ^ (1 << 31)
	if neg {
		b = append(b, numNegative)
		b = binary.BigEndian.AppendUint32(b, ^e)
		for _, d := range digits {
			b = append(b, 10-d)
		}
		return append(b, 0xFF), nil
	}
	b = append(b, numPositive)
	b = binary.BigEndian.AppendUint32(b, e)
	for _, d := range digits {
		b = append(b, d+1)
	}
	return append(b, 0x00), nil
}

// decodeNumeric reads what encodeNumeric wrote.
func decodeNumeric(b []byte) (Numeric, []byte, error) {
	if len(b) == 0 {
		return "", nil, fmt.Errorf("%w: a numeric with no sign", chunk.ErrCorrupt)
	}
	class := b[0]
	b = b[1:]
	if class == numZero {
		return "0", b, nil
	}
	if class != numNegative && class != numPositive {
		return "", nil, fmt.Errorf("%w: a numeric sign class %02x", chunk.ErrCorrupt, class)
	}
	if len(b) < 4 {
		return "", nil, fmt.Errorf("%w: a numeric with no exponent", chunk.ErrCorrupt)
	}
	e := binary.BigEndian.Uint32(b)
	b = b[4:]
	neg := class == numNegative
	if neg {
		e = ^e
	}
	exp := int(int32(e ^ (1 << 31)))
	end := byte(0x00)
	if neg {
		end = 0xFF
	}
	var digits []byte
	for {
		if len(b) == 0 {
			return "", nil, fmt.Errorf("%w: a numeric with no end", chunk.ErrCorrupt)
		}
		c := b[0]
		b = b[1:]
		if c == end {
			break
		}
		var d byte
		if neg {
			if c < 1 || c > 10 {
				return "", nil, fmt.Errorf("%w: a numeric digit %02x", chunk.ErrCorrupt, c)
			}
			d = 10 - c
		} else {
			if c < 1 || c > 10 {
				return "", nil, fmt.Errorf("%w: a numeric digit %02x", chunk.ErrCorrupt, c)
			}
			d = c - 1
		}
		digits = append(digits, d)
		if len(digits) > maxNumericDigs {
			return "", nil, fmt.Errorf("%w: a numeric of over %d digits", chunk.ErrCorrupt, maxNumericDigs)
		}
	}
	if len(digits) == 0 || digits[0] == 0 || digits[len(digits)-1] == 0 {
		return "", nil, fmt.Errorf("%w: a numeric that is not canonical", chunk.ErrCorrupt)
	}
	if n := canonicalLen(neg, len(digits), exp); n > MaxNumericLen {
		return "", nil, fmt.Errorf("%w: a numeric of %d bytes of text, over %d", chunk.ErrCorrupt, n, MaxNumericLen)
	}
	return canonical(neg, digits, exp), b, nil
}

// decodeCell reads one cell of column c from b and returns the value and
// the rest of b.
func decodeCell(b []byte, c Column) (any, []byte, error) {
	if len(b) == 0 {
		return nil, nil, fmt.Errorf("%w: a cell with no marker", chunk.ErrCorrupt)
	}
	switch b[0] {
	case 0x00:
		if !c.Nullable {
			return nil, nil, fmt.Errorf("%w: NULL in column %q, which is not nullable", chunk.ErrCorrupt, c.Name)
		}
		return nil, b[1:], nil
	case 0x01:
	default:
		return nil, nil, fmt.Errorf("%w: a cell marker %02x", chunk.ErrCorrupt, b[0])
	}
	b = b[1:]
	fixed := func(n int) ([]byte, error) {
		if len(b) < n {
			return nil, fmt.Errorf("%w: a %s cell of %d bytes, want %d", chunk.ErrCorrupt, c.Type, len(b), n)
		}
		return b[:n], nil
	}
	var v any
	switch c.Type {
	case TypeBool:
		x, err := fixed(1)
		if err != nil {
			return nil, nil, err
		}
		if x[0] > 1 {
			return nil, nil, fmt.Errorf("%w: a bool of %02x", chunk.ErrCorrupt, x[0])
		}
		v, b = x[0] == 1, b[1:]
	case TypeInt2:
		x, err := fixed(2)
		if err != nil {
			return nil, nil, err
		}
		v, b = int16(binary.BigEndian.Uint16(x)^0x8000), b[2:]
	case TypeInt4:
		x, err := fixed(4)
		if err != nil {
			return nil, nil, err
		}
		v, b = int32(binary.BigEndian.Uint32(x)^(1<<31)), b[4:]
	case TypeInt8:
		x, err := fixed(8)
		if err != nil {
			return nil, nil, err
		}
		v, b = int64(binary.BigEndian.Uint64(x)^(1<<63)), b[8:]
	case TypeFloat4:
		x, err := fixed(4)
		if err != nil {
			return nil, nil, err
		}
		u := binary.BigEndian.Uint32(x)
		f := unorderedFloat32(u)
		if orderedFloat32(f) != u { // -0 or a NaN spelled another way: not what the encoder writes
			return nil, nil, fmt.Errorf("%w: a float4 encoding %08x that is not canonical", chunk.ErrCorrupt, u)
		}
		v, b = f, b[4:]
	case TypeFloat8:
		x, err := fixed(8)
		if err != nil {
			return nil, nil, err
		}
		u := binary.BigEndian.Uint64(x)
		f := unorderedFloat64(u)
		if orderedFloat64(f) != u {
			return nil, nil, fmt.Errorf("%w: a float8 encoding %016x that is not canonical", chunk.ErrCorrupt, u)
		}
		v, b = f, b[8:]
	case TypeNumeric:
		n, rest, err := decodeNumeric(b)
		if err != nil {
			return nil, nil, err
		}
		v, b = n, rest
	case TypeText, TypeVarchar:
		s, rest, err := unescape(b)
		if err != nil {
			return nil, nil, err
		}
		if !utf8.Valid(s) {
			return nil, nil, fmt.Errorf("%w: text in column %q that is not UTF-8", chunk.ErrCorrupt, c.Name)
		}
		if c.Type == TypeVarchar && utf8.RuneCount(s) > int(c.MaxLen) {
			return nil, nil, fmt.Errorf("%w: %d characters in column %q, varchar(%d)", chunk.ErrCorrupt, utf8.RuneCount(s), c.Name, c.MaxLen)
		}
		v, b = string(s), rest
	case TypeBytea:
		s, rest, err := unescape(b)
		if err != nil {
			return nil, nil, err
		}
		v, b = s, rest
	case TypeDate:
		x, err := fixed(4)
		if err != nil {
			return nil, nil, err
		}
		v, b = Date(int32(binary.BigEndian.Uint32(x)^(1<<31))), b[4:]
	case TypeTimestamp:
		x, err := fixed(8)
		if err != nil {
			return nil, nil, err
		}
		v, b = Timestamp(int64(binary.BigEndian.Uint64(x)^(1<<63))), b[8:]
	case TypeTimestampTZ:
		x, err := fixed(8)
		if err != nil {
			return nil, nil, err
		}
		v, b = TimestampTZ(int64(binary.BigEndian.Uint64(x)^(1<<63))), b[8:]
	case TypeUUID:
		x, err := fixed(16)
		if err != nil {
			return nil, nil, err
		}
		var u UUID
		copy(u[:], x)
		v, b = u, b[16:]
	case TypeJSONB:
		s, rest, err := unescape(b)
		if err != nil {
			return nil, nil, err
		}
		if len(s) == 0 || !utf8.Valid(s) {
			return nil, nil, fmt.Errorf("%w: a JSONB in column %q that is empty or not UTF-8", chunk.ErrCorrupt, c.Name)
		}
		v, b = JSONB(s), rest
	default:
		return nil, nil, fmt.Errorf("%w: column %q has type %s", chunk.ErrCorrupt, c.Name, c.Type)
	}
	return v, b, nil
}
