package table_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// numericCell is a numeric cell as the encoding lays it out: a sign class,
// the adjusted exponent, the digits and an end byte, a negative value's
// exponent and digits complemented.
func numericCell(neg bool, exp int32, digits ...byte) []byte {
	e := uint32(exp) ^ (1 << 31)
	if neg {
		b := binary.BigEndian.AppendUint32([]byte{0x01, 0x01}, ^e)
		for _, d := range digits {
			b = append(b, 10-d)
		}
		return append(b, 0xFF)
	}
	b := binary.BigEndian.AppendUint32([]byte{0x01, 0x03}, e)
	for _, d := range digits {
		b = append(b, d+1)
	}
	return append(b, 0x00)
}

// A numeric cell is an exponent and digits, and its value is written out as
// text. A cell asking for more text than a numeric may have is refused, and
// refused before the text is built: an eight-byte cell with the largest
// exponent asked for two billion zeros, and one fuzz worker grew to 6 GB in
// three seconds (snapshot-engine#2).
func TestRegression_SE2_ANumericCellCannotAskForUnboundedText(t *testing.T) {
	col := table.Column{Tag: 1, Name: "amount", Type: table.TypeNumeric}

	// Positive controls: the longest numerics there are, a thousand bytes of
	// text, decode and re-encode.
	for _, c := range []struct {
		cell []byte
		want table.Numeric
	}{
		{numericCell(false, table.MaxNumericLen, 1), table.Numeric("1" + strings.Repeat("0", table.MaxNumericLen-1))},
		{numericCell(true, -(table.MaxNumericLen - 4), 1), table.Numeric("-0." + strings.Repeat("0", table.MaxNumericLen-4) + "1")},
	} {
		v, rest, err := table.DecodeCell(c.cell, col)
		if err != nil || len(rest) != 0 || v != c.want {
			t.Fatalf("a numeric of %d bytes of text, the most a numeric may have, decoded as %.20q… (%d bytes), %v: want it back whole", len(c.want), v, len(asNumeric(v)), err)
		}
		back, err := table.EncodeCell(col, v)
		if err != nil || !bytes.Equal(back, c.cell) {
			t.Fatalf("a numeric of %d bytes of text decoded but does not re-encode: %v", len(c.want), err)
		}
	}

	for _, c := range []struct {
		what string
		cell []byte
	}{
		{"one byte of text too many, a large value", numericCell(false, table.MaxNumericLen+1, 1)},
		{"one byte of text too many, a small negative value", numericCell(true, -(table.MaxNumericLen - 3), 1)},
		{"sixteen million zeros", numericCell(false, 1<<24, 1)},
		{"sixteen million zeros after the point", numericCell(false, -(1 << 24), 1)},
	} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		v, _, err := table.DecodeCell(c.cell, col)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, chunk.ErrCorrupt) {
			t.Errorf("a numeric cell asking for %s decoded as %d bytes of text (%v): a reader would take a corrupt chunk for a value no writer could store", c.what, len(asNumeric(v)), err)
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
			t.Errorf("decoding a %d-byte numeric cell asking for %s allocated %d bytes: a corrupt or hostile chunk costs memory without bound", len(c.cell), c.what, grew)
		}
	}
}

func asNumeric(v any) table.Numeric {
	n, _ := v.(table.Numeric)
	return n
}

// A numeric a client writes is text, and its exponent says how long its
// canonical text is. One asking for more text than a numeric may have is
// refused, from the arithmetic, before any text is built: "1e1000000000",
// twelve bytes, built a gigabyte of zeros and allocated 5.4 GB before the
// write refused it, and an exponent near the largest integer wrapped
// around to a different value (snapshot-engine#3).
func TestRegression_SE3_AWrittenNumericCannotAskForUnboundedText(t *testing.T) {
	col := table.Column{Tag: 1, Name: "amount", Type: table.TypeNumeric}
	big := "1" + strings.Repeat("0", table.MaxNumericLen-1)
	small := "-0." + strings.Repeat("0", table.MaxNumericLen-4) + "1"

	// Positive controls: the longest numerics there are, spelled with an
	// exponent, parse to their thousand bytes of text and are written.
	for _, c := range []struct{ in, want string }{
		{"1e999", big},
		{"0.001e1002", big},
		{"-1e-997", small},
		{small, small},
		{"0e999999", "0"}, // the largest exponent read
	} {
		got, err := table.ParseNumeric(c.in)
		if err != nil || string(got) != c.want {
			t.Fatalf("ParseNumeric(%q) = %.20q… (%d bytes), %v: want the %d-byte numeric it spells, the most a numeric may have", c.in, got, len(got), err, len(c.want))
		}
		if _, err := table.EncodeCell(col, got); err != nil {
			t.Fatalf("writing the %d-byte numeric %q… is refused: %v", len(got), got[:10], err)
		}
	}

	for _, in := range []string{
		"1e1000",                // one byte of text too many
		"-1e-998",               // one byte too many, after the point
		"1e999999",              // the largest exponent: a million zeros
		"1e-999999",             // a million zeros after the point
		"1e1000000",             // an exponent past the largest
		"0e1000000",             // past the largest, even for zero
		"1e9223372036854775807", // an exponent that wraps around
	} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got, err := table.ParseNumeric(in)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, table.ErrValue) {
			t.Errorf("ParseNumeric(%q) = %.20q… (%d bytes), %v: a client's numeric past a numeric's limits was taken for a value, where the write must refuse it", in, got, len(got), err)
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
			t.Errorf("parsing the %d-byte numeric %q allocated %d bytes: one written value costs memory without bound", len(in), in, grew)
		}
	}

	// Writing a numeric that is not canonical is refused, and refused as
	// cheaply: the text it would take is worked out, not built.
	for _, in := range []string{"1e999999", "-1e-999999", "1e1000000", "1e9223372036854775807"} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := table.EncodeCell(col, table.Numeric(in))
		runtime.ReadMemStats(&after)
		if !errors.Is(err, table.ErrValue) {
			t.Errorf("writing the numeric %q: %v, want ErrValue", in, err)
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
			t.Errorf("refusing to write the %d-byte numeric %q allocated %d bytes: one insert costs memory without bound", len(in), in, grew)
		}
	}
}
