package table_test

import (
	"bytes"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

func col(t table.Type, nullable bool) table.Column {
	c := table.Column{Tag: 1, Name: "c", Type: t, Nullable: nullable}
	if t == table.TypeVarchar {
		c.MaxLen = 255
	}
	return c
}

func enc(t *testing.T, c table.Column, v any) []byte {
	t.Helper()
	b, err := table.EncodeCell(c, v)
	if err != nil {
		t.Fatalf("EncodeCell(%s, %v): %v", c.Type, v, err)
	}
	return b
}

// The encodings, by hand: NULL is 0x00; a value is 0x01 then the type's
// bytes: integers big-endian with the sign bit flipped, floats as their
// IEEE bits with the sign flipped when positive and every bit flipped when
// negative, strings and bytes with 0x00 escaped to 0x00 0xFF and 0x00 0x00
// at the end, a numeric as a sign class, an adjusted exponent and digits,
// dates and timestamps as their integers, a UUID raw.
func TestCellsAreTheDocumentedEncodings(t *testing.T) {
	for name, tc := range map[string]struct {
		c    table.Column
		v    any
		want []byte
	}{
		"NULL":          {col(table.TypeInt4, true), nil, []byte{0x00}},
		"false":         {col(table.TypeBool, false), false, []byte{0x01, 0x00}},
		"true":          {col(table.TypeBool, false), true, []byte{0x01, 0x01}},
		"int2 -1":       {col(table.TypeInt2, false), int16(-1), []byte{0x01, 0x7F, 0xFF}},
		"int4 0":        {col(table.TypeInt4, false), int32(0), []byte{0x01, 0x80, 0, 0, 0}},
		"int8 1":        {col(table.TypeInt8, false), int64(1), []byte{0x01, 0x80, 0, 0, 0, 0, 0, 0, 1}},
		"float4 1.0":    {col(table.TypeFloat4, false), float32(1), []byte{0x01, 0xBF, 0x80, 0, 0}},
		"float8 -2.0":   {col(table.TypeFloat8, false), float64(-2), []byte{0x01, 0x3F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		"text a\\x00b":  {col(table.TypeText, false), "a\x00b", []byte{0x01, 'a', 0x00, 0xFF, 'b', 0x00, 0x00}},
		"varchar empty": {col(table.TypeVarchar, false), "", []byte{0x01, 0x00, 0x00}},
		"bytea":         {col(table.TypeBytea, false), []byte{0xFF, 0x00}, []byte{0x01, 0xFF, 0x00, 0xFF, 0x00, 0x00}},
		"numeric 25":    {col(table.TypeNumeric, false), table.Numeric("25"), []byte{0x01, 0x03, 0x80, 0, 0, 2, 0x03, 0x06, 0x00}},
		"numeric -25":   {col(table.TypeNumeric, false), table.Numeric("-25"), []byte{0x01, 0x01, 0x7F, 0xFF, 0xFF, 0xFD, 0x08, 0x05, 0xFF}},
		"numeric 0":     {col(table.TypeNumeric, false), table.Numeric("0"), []byte{0x01, 0x02}},
		"numeric 0.5":   {col(table.TypeNumeric, false), table.Numeric("0.5"), []byte{0x01, 0x03, 0x80, 0, 0, 0, 0x06, 0x00}},
		"date 1":        {col(table.TypeDate, false), table.Date(1), []byte{0x01, 0x80, 0, 0, 1}},
		"timestamp -1":  {col(table.TypeTimestamp, false), table.Timestamp(-1), []byte{0x01, 0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		"timestamptz 0": {col(table.TypeTimestampTZ, false), table.TimestampTZ(0), []byte{0x01, 0x80, 0, 0, 0, 0, 0, 0, 0}},
		"uuid":          {col(table.TypeUUID, false), table.UUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, append([]byte{0x01}, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16)},
		"jsonb":         {col(table.TypeJSONB, false), table.JSONB(`{}`), []byte{0x01, '{', '}', 0x00, 0x00}},
	} {
		got := enc(t, tc.c, tc.v)
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s: encoded %x, want %x", name, got, tc.want)
			continue
		}
		back, rest, err := table.DecodeCell(append(bytes.Clone(got), 0xAB), tc.c)
		if err != nil || len(rest) != 1 || rest[0] != 0xAB {
			t.Errorf("%s: DecodeCell = %v, rest %x, %v; want the value and the byte after it", name, back, rest, err)
			continue
		}
		if !sameValue(back, tc.v) {
			t.Errorf("%s: decoded %#v, want %#v", name, back, tc.v)
		}
	}
}

func sameValue(a, b any) bool {
	switch x := a.(type) {
	case []byte:
		y, ok := b.([]byte)
		return ok && bytes.Equal(x, y)
	case table.JSONB:
		y, ok := b.(table.JSONB)
		return ok && bytes.Equal(x, y)
	case float32:
		y, ok := b.(float32)
		return ok && (x == y || (math.IsNaN(float64(x)) && math.IsNaN(float64(y))))
	case float64:
		y, ok := b.(float64)
		return ok && (x == y || (math.IsNaN(x) && math.IsNaN(y)))
	}
	return a == b
}

// every type's values: a generator, and the type's own order, the authority
// the encoding is held to.
type typeCase struct {
	c   table.Column
	gen func(*rapid.T) any
	cmp func(a, b any) int
}

func sign(x int) int {
	if x < 0 {
		return -1
	}
	if x > 0 {
		return 1
	}
	return 0
}

func floatCmp(a, b float64) int {
	an, bn := math.IsNaN(a), math.IsNaN(b)
	switch {
	case an && bn:
		return 0
	case an: // SQL: NaN sorts above everything
		return 1
	case bn:
		return -1
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func numericCmp(a, b any) int {
	x, _ := new(big.Rat).SetString(string(a.(table.Numeric)))
	y, _ := new(big.Rat).SetString(string(b.(table.Numeric)))
	return x.Cmp(y)
}

func genNumeric(t *rapid.T) any {
	neg := rapid.Bool().Draw(t, "neg")
	digits := rapid.SliceOfN(rapid.IntRange(0, 9), 1, 40).Draw(t, "digits")
	point := rapid.IntRange(0, len(digits)).Draw(t, "point")
	var sb strings.Builder
	if neg {
		sb.WriteByte('-')
	}
	for i, d := range digits {
		if i == point {
			sb.WriteByte('.')
		}
		sb.WriteByte(byte('0' + d))
	}
	s := sb.String()
	if rapid.Bool().Draw(t, "exp") {
		s += "e" + rapid.SampledFrom([]string{"-30", "-3", "0", "2", "25"}).Draw(t, "e")
	}
	n, err := table.ParseNumeric(s)
	if err != nil {
		t.Fatalf("ParseNumeric(%q): %v", s, err)
	}
	return n
}

func specials64(t *rapid.T) any {
	if rapid.IntRange(0, 9).Draw(t, "special") == 0 {
		return rapid.SampledFrom([]float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, math.Copysign(0, -1), math.MaxFloat64, math.SmallestNonzeroFloat64}).Draw(t, "s")
	}
	return rapid.Float64().Draw(t, "f")
}

func typeCases() []typeCase {
	str := func(a, b any) int { return strings.Compare(a.(string), b.(string)) }
	return []typeCase{
		{col(table.TypeBool, false), func(t *rapid.T) any { return rapid.Bool().Draw(t, "v") }, func(a, b any) int {
			x, y := 0, 0
			if a.(bool) {
				x = 1
			}
			if b.(bool) {
				y = 1
			}
			return sign(x - y)
		}},
		{col(table.TypeInt2, false), func(t *rapid.T) any { return rapid.Int16().Draw(t, "v") }, func(a, b any) int { return sign(int(a.(int16)) - int(b.(int16))) }},
		{col(table.TypeInt4, false), func(t *rapid.T) any { return rapid.Int32().Draw(t, "v") }, func(a, b any) int { return sign(int(a.(int32) - b.(int32))) }},
		{col(table.TypeInt8, false), func(t *rapid.T) any { return rapid.Int64().Draw(t, "v") }, func(a, b any) int {
			x, y := a.(int64), b.(int64)
			if x < y {
				return -1
			}
			if x > y {
				return 1
			}
			return 0
		}},
		{col(table.TypeFloat4, false), func(t *rapid.T) any { return float32(specials64(t).(float64)) }, func(a, b any) int { return floatCmp(float64(a.(float32)), float64(b.(float32))) }},
		{col(table.TypeFloat8, false), specials64, func(a, b any) int { return floatCmp(a.(float64), b.(float64)) }},
		{col(table.TypeNumeric, false), genNumeric, numericCmp},
		{col(table.TypeText, false), func(t *rapid.T) any { return rapid.String().Draw(t, "v") }, str},
		{col(table.TypeVarchar, false), func(t *rapid.T) any { return rapid.StringN(0, 255, -1).Draw(t, "v") }, str},
		{col(table.TypeBytea, false), func(t *rapid.T) any { return rapid.SliceOf(rapid.Byte()).Draw(t, "v") }, func(a, b any) int { return bytes.Compare(a.([]byte), b.([]byte)) }},
		{col(table.TypeDate, false), func(t *rapid.T) any { return table.Date(rapid.Int32().Draw(t, "v")) }, func(a, b any) int { return sign(int(a.(table.Date) - b.(table.Date))) }},
		{col(table.TypeTimestamp, false), func(t *rapid.T) any { return table.Timestamp(rapid.Int64().Draw(t, "v")) }, func(a, b any) int {
			return sign(int(min(max(a.(table.Timestamp)-b.(table.Timestamp), -1), 1)))
		}},
		{col(table.TypeTimestampTZ, false), func(t *rapid.T) any { return table.TimestampTZ(rapid.Int64().Draw(t, "v")) }, func(a, b any) int {
			return sign(int(min(max(a.(table.TimestampTZ)-b.(table.TimestampTZ), -1), 1)))
		}},
		{col(table.TypeUUID, false), func(t *rapid.T) any {
			var u table.UUID
			copy(u[:], rapid.SliceOfN(rapid.Byte(), 16, 16).Draw(t, "v"))
			return u
		}, func(a, b any) int { x, y := a.(table.UUID), b.(table.UUID); return bytes.Compare(x[:], y[:]) }},
		{col(table.TypeJSONB, false), func(t *rapid.T) any { return table.JSONB(rapid.StringN(1, 100, -1).Draw(t, "v")) }, func(a, b any) int { return bytes.Compare(a.(table.JSONB), b.(table.JSONB)) }},
	}
}

// Property: for every type, decode(encode(v)) == v.
func TestEveryTypeRoundTrips(t *testing.T) {
	for _, tc := range typeCases() {
		t.Run(tc.c.Type.String(), func(t *testing.T) {
			rapid.Check(t, func(rt *rapid.T) {
				v := tc.gen(rt)
				b, err := table.EncodeCell(tc.c, v)
				if err != nil {
					rt.Fatalf("EncodeCell(%v): %v", v, err)
				}
				back, rest, err := table.DecodeCell(b, tc.c)
				if err != nil || len(rest) != 0 {
					rt.Fatalf("DecodeCell(%x) = %v, %d rest, %v", b, back, len(rest), err)
				}
				if !sameValue(back, v) {
					rt.Fatalf("decode(encode(%#v)) = %#v", v, back)
				}
			})
		})
	}
}

// Property: for every type, byte order of the encodings is the type's order,
// and NULL sorts before every value.
func TestByteOrderIsTheTypesOrder(t *testing.T) {
	for _, tc := range typeCases() {
		t.Run(tc.c.Type.String(), func(t *testing.T) {
			nullable := tc.c
			nullable.Nullable = true
			null := enc(t, nullable, nil)
			rapid.Check(t, func(rt *rapid.T) {
				a, b := tc.gen(rt), tc.gen(rt)
				ea, err := table.EncodeCell(tc.c, a)
				if err != nil {
					rt.Fatal(err)
				}
				eb, err := table.EncodeCell(tc.c, b)
				if err != nil {
					rt.Fatal(err)
				}
				if got, want := bytes.Compare(ea, eb), tc.cmp(a, b); got != want {
					rt.Fatalf("compare(enc(%#v), enc(%#v)) = %d, the type says %d", a, b, got, want)
				}
				if bytes.Compare(null, ea) >= 0 {
					rt.Fatalf("NULL (%x) does not sort before %#v (%x)", null, a, ea)
				}
			})
		})
	}
}

// A value that does not fit its column is refused with ErrValue and no
// bytes: the wrong Go type, NULL in a column that forbids it, a varchar of
// 300 characters in a varchar(255) (bytes are not characters: 255 three-byte
// characters fit), text that is not UTF-8, a numeric that is not canonical,
// an empty or non-UTF-8 JSONB. ParseNumeric turns any spelling canonical.
func TestValuesThatDoNotFitAreRefused(t *testing.T) {
	long := strings.Repeat("x", 300)
	wide := strings.Repeat("ẋ", 255) // 255 characters, 765 bytes
	if got := enc(t, col(table.TypeVarchar, false), wide); len(got) < 766 {
		t.Fatalf("positive control: 255 three-byte characters encode to %d bytes", len(got))
	}
	for name, tc := range map[string]struct {
		c table.Column
		v any
	}{
		"int4 given an int64":       {col(table.TypeInt4, false), int64(1)},
		"text given bytes":          {col(table.TypeText, false), []byte("x")},
		"NULL in a non-null column": {col(table.TypeInt8, false), nil},
		"300 chars in varchar(255)": {col(table.TypeVarchar, false), long},
		"text that is not UTF-8":    {col(table.TypeText, false), "a\xffb"},
		"varchar that is not UTF-8": {col(table.TypeVarchar, false), "\xc3"},
		"numeric 01":                {col(table.TypeNumeric, false), table.Numeric("01")},
		"numeric 1.50":              {col(table.TypeNumeric, false), table.Numeric("1.50")},
		"numeric -0":                {col(table.TypeNumeric, false), table.Numeric("-0")},
		"numeric 1.":                {col(table.TypeNumeric, false), table.Numeric("1.")},
		"numeric .5":                {col(table.TypeNumeric, false), table.Numeric(".5")},
		"numeric empty":             {col(table.TypeNumeric, false), table.Numeric("")},
		"numeric words":             {col(table.TypeNumeric, false), table.Numeric("abc")},
		"numeric with exponent":     {col(table.TypeNumeric, false), table.Numeric("1e5")},
		"jsonb empty":               {col(table.TypeJSONB, false), table.JSONB("")},
		"jsonb not UTF-8":           {col(table.TypeJSONB, false), table.JSONB("\xff")},
		"uuid given bytes":          {col(table.TypeUUID, false), []byte("0123456789abcdef")},
	} {
		if b, err := table.EncodeCell(tc.c, tc.v); !errors.Is(err, table.ErrValue) || b != nil {
			t.Errorf("%s: EncodeCell = %x, %v; want no bytes and ErrValue", name, b, err)
		}
	}
	for in, want := range map[string]string{"1.50": "1.5", "-0": "0", "01": "1", "1.": "1", ".5": "0.5", "1e5": "100000", "1.5e3": "1500", "-0.0010": "-0.001", "12345678901234567890": "12345678901234567890", "1E-2": "0.01"} {
		got, err := table.ParseNumeric(in)
		if err != nil || string(got) != want {
			t.Errorf("ParseNumeric(%q) = %q, %v; want %q", in, got, err, want)
		}
		if _, err := table.EncodeCell(col(table.TypeNumeric, false), got); err != nil {
			t.Errorf("a parsed numeric %q is refused: %v", got, err)
		}
	}
	for _, in := range []string{"", "-", ".", "e5", "1e", "1.2.3", "abc", "1 2", "+1"} {
		if got, err := table.ParseNumeric(in); !errors.Is(err, table.ErrValue) {
			t.Errorf("ParseNumeric(%q) = %q, %v; want ErrValue", in, got, err)
		}
	}
}

// A cell that is not what the encoder writes is refused: a marker other
// than 0x00 or 0x01, a value cut short, a string with no end, a numeric with
// an unknown sign class or a digit out of range, a NULL for a non-null
// column; and the fuzz target holds the decoder to that.
func TestForgedCellsAreRefused(t *testing.T) {
	good := enc(t, col(table.TypeInt4, true), int32(7)) // the positive control: what the encoder writes decodes
	if v, rest, err := table.DecodeCell(good, col(table.TypeInt4, true)); err != nil || v != int32(7) || len(rest) != 0 {
		t.Fatalf("positive control: DecodeCell(%x) = %v, %x, %v", good, v, rest, err)
	}
	for name, tc := range map[string]struct {
		c table.Column
		b []byte
	}{
		"marker 2":                {col(table.TypeInt4, true), []byte{0x02, 0, 0, 0, 0}},
		"int4 cut short":          {col(table.TypeInt4, false), []byte{0x01, 0x80, 0}},
		"text with no end":        {col(table.TypeText, false), []byte{0x01, 'a', 'b'}},
		"text escape at the end":  {col(table.TypeText, false), []byte{0x01, 'a', 0x00}},
		"text bad escape":         {col(table.TypeText, false), []byte{0x01, 0x00, 0x7F, 0x00, 0x00}},
		"text not UTF-8":          {col(table.TypeText, false), []byte{0x01, 0xff, 0x00, 0x00}},
		"numeric sign class 9":    {col(table.TypeNumeric, false), []byte{0x01, 0x09}},
		"numeric digit 0x0B":      {col(table.TypeNumeric, false), []byte{0x01, 0x03, 0x80, 0, 0, 1, 0x0B, 0x00}},
		"numeric no digits":       {col(table.TypeNumeric, false), []byte{0x01, 0x03, 0x80, 0, 0, 1, 0x00}},
		"numeric trailing zero":   {col(table.TypeNumeric, false), []byte{0x01, 0x03, 0x80, 0, 0, 1, 0x03, 0x01, 0x00}},
		"NULL where none allowed": {col(table.TypeInt4, false), []byte{0x00}},
		"empty":                   {col(table.TypeInt4, true), []byte{}},
		"varchar over its length": {col(table.TypeVarchar, false), append(append([]byte{0x01}, bytes.Repeat([]byte("x"), 256)...), 0x00, 0x00)},
	} {
		if v, _, err := table.DecodeCell(tc.b, tc.c); err == nil {
			t.Errorf("%s: DecodeCell accepted %x as %#v", name, tc.b, v)
		}
	}
}

func FuzzDecodeCell(f *testing.F) {
	for _, tc := range typeCases() {
		f.Add(uint8(tc.c.Type), []byte{0x01, 0x80, 0, 0, 0, 0, 0, 0, 0, 0x00, 0x00})
	}
	f.Fuzz(func(t *testing.T, typ uint8, b []byte) {
		c := table.Column{Tag: 1, Name: "c", Type: table.Type(typ), Nullable: true}
		if c.Type == table.TypeVarchar {
			c.MaxLen = 8
		}
		if c.Type == 0 || c.Type > table.TypeJSONB {
			return
		}
		v, rest, err := table.DecodeCell(b, c)
		if err != nil {
			return
		}
		back, err := table.EncodeCell(c, v)
		if err != nil {
			t.Fatalf("a decoded %s does not encode: %#v: %v", c.Type, v, err)
		}
		if !bytes.Equal(back, b[:len(b)-len(rest)]) {
			t.Fatalf("a %s cell that decoded re-encodes differently: %x vs %x", c.Type, back, b[:len(b)-len(rest)])
		}
	})
}
