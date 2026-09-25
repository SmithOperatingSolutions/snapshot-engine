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
