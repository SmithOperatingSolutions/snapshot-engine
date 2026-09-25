package table_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"

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

// allocated is how many bytes f allocated, by the runtime's own count.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A fixed-width column's cells all have one length, which is the longest
// it holds; any other column's longest is MaxCellLen. A row record's limit
// is built from these, so one too short would refuse a row the table holds
// (snapshot-engine#7).
func TestRegression_SE7_ACellsLongestIsItsTypesWidth(t *testing.T) {
	fixed := map[table.Type][]any{
		table.TypeBool:        {false, true},
		table.TypeInt2:        {int16(0), int16(-1 << 15), int16(1<<15 - 1)},
		table.TypeInt4:        {int32(0), int32(-1 << 31), int32(1<<31 - 1)},
		table.TypeInt8:        {int64(0), int64(-1 << 63), int64(1<<63 - 1)},
		table.TypeFloat4:      {float32(0), float32(-1.5), float32(3e38)},
		table.TypeFloat8:      {float64(0), -1.5, 1e308},
		table.TypeDate:        {table.Date(0), table.Date(-1 << 31)},
		table.TypeTimestamp:   {table.Timestamp(0), table.Timestamp(1<<63 - 1)},
		table.TypeTimestampTZ: {table.TimestampTZ(0), table.TimestampTZ(-1 << 63)},
		table.TypeUUID:        {table.UUID{}, table.UUID{0xFF}},
	}
	for typ, vals := range fixed {
		c := table.Column{Tag: 1, Name: "c", Type: typ, Nullable: true}
		for _, v := range append(vals, nil) {
			cell, err := table.EncodeCell(c, v)
			if err != nil {
				t.Fatalf("a %s cell %v: %v", typ, v, err)
			}
			if v != nil && len(cell) != table.MaxCell(c) || len(cell) > table.MaxCell(c) {
				t.Errorf("a %s cell holding %v is %d bytes, but the longest %s cell is said to be %d: a row holding it would be refused, or a longer forged row read", typ, v, len(cell), typ, table.MaxCell(c))
				break
			}
		}
	}
	for _, typ := range []table.Type{table.TypeNumeric, table.TypeText, table.TypeBytea, table.TypeJSONB} {
		if got := table.MaxCell(table.Column{Tag: 1, Name: "c", Type: typ}); got != table.MaxCellLen {
			t.Errorf("the longest %s cell is said to be %d bytes, want MaxCellLen, %d", typ, got, table.MaxCellLen)
		}
	}
	if got := table.MaxCell(table.Column{Tag: 1, Name: "c", Type: table.TypeVarchar, MaxLen: 10}); got != table.MaxCellLen {
		t.Errorf("the longest varchar(10) cell is said to be %d bytes, want MaxCellLen, %d", got, table.MaxCellLen)
	}
}

// forgedTable is good's table with its primary map or its index map (the
// one index) replaced by the map at root.
func forgedTable(t *testing.T, s *memstore.Store, good model.Root, primary bool, root hash.Hash) model.Root {
	t.Helper()
	b, err := s.Get(ctx, good.Hash)
	if err != nil {
		t.Fatal(err)
	}
	catalog, p, rows, ixs, err := table.DecodeRoot(b)
	if err != nil {
		t.Fatal(err)
	}
	if primary {
		p = root
	} else {
		ixs[0].Root = root
	}
	h, err := s.Put(ctx, table.EncodeRoot(catalog, p, rows, ixs))
	if err != nil {
		t.Fatal(err)
	}
	return model.Root{Hash: h, Size: rows, Format: table.Format}
}

// withValue is the map at root with key set to val, written under a
// configuration that stores values of any length.
func withValue(t *testing.T, s *memstore.Store, root [32]byte, key, val []byte) hash.Hash {
	t.Helper()
	wide := cfg()
	wide.MaxValue = 8 << 20
	m, err := prolly.Open(ctx, s, wide, hash.Hash(root))
	if err != nil {
		t.Fatal(err)
	}
	e := m.Editor()
	if err := e.Put(key, val); err != nil {
		t.Fatal(err)
	}
	if m, err = e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	return m.Root()
}

// A row record is its non-key cells, each at most its column's longest, so
// a stream longer than their sum is not one of ours, and a stream's
// claimed length can be many times what it stores (snapshot-core#23). An
// index entry's value is empty. A table holding a row one byte longer than
// its schema's longest, or an index entry with a long value, must be
// refused by every read with prolly.ErrValueTooLarge before the stream is
// read; the longest row there is reads back by every read
// (snapshot-engine#7).
func TestRegression_SE7_ATableReadsNoRowLongerThanItsSchemaHolds(t *testing.T) {
	s := memstore.New()
	id := table.Column{Tag: 1, Name: "id", Type: table.TypeInt8}
	schema := table.Schema{
		Columns: []table.Column{
			id,
			{Tag: 2, Name: "a", Type: table.TypeBytea},
			{Tag: 3, Name: "b", Type: table.TypeBytea, Nullable: true},
			{Tag: 4, Name: "n", Type: table.TypeInt4},
		},
		PrimaryKey: []table.Tag{1},
		Indexes:    []table.Index{{Tag: 1, Columns: []table.Tag{4}}},
	}
	const longest = 2*table.MaxCellLen + 5 // two bytea cells at the cell limit, and an int4
	empty, err := table.Create(ctx, s, cfg(), schema)
	if err != nil {
		t.Fatal(err)
	}
	full := bytes.Repeat([]byte("m"), table.MaxCellLen-3) // 0x01, the bytes, 0x00 0x00
	row := table.Row{1: int64(1), 2: full, 3: full, 4: int32(7)}
	e := empty.Edit()
	if _, err := e.Insert(row); err != nil {
		t.Fatalf("positive control: inserting a row of %d bytes, its schema's longest: %v", longest, err)
	}
	tb, err := e.Flush(ctx)
	if err != nil {
		t.Fatalf("positive control: storing a row of %d bytes, its schema's longest: %v", longest, err)
	}
	good := tb.Root()
	kb, err := table.EncodeCell(id, int64(1))
	if err != nil {
		t.Fatal(err)
	}
	maps := func(r model.Root) (primary [32]byte, index [32]byte) {
		b, err := s.Get(ctx, r.Hash)
		if err != nil {
			t.Fatal(err)
		}
		_, primary, _, ixs, err := table.DecodeRoot(b)
		if err != nil {
			t.Fatal(err)
		}
		return primary, ixs[0].Root
	}
	primary, _ := maps(good)
	overRow := forgedTable(t, s, good, true, withValue(t, s, primary, kb, bytes.Repeat([]byte{0x01}, longest+1)))
	// The index entry is forged on a table whose row is short, so reading the
	// row costs nothing a budget would notice.
	e = empty.Edit()
	if _, err := e.Insert(table.Row{1: int64(1), 2: []byte("x"), 4: int32(7)}); err != nil {
		t.Fatal(err)
	}
	short, err := e.Flush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, index := maps(short.Root())
	ik := binary.BigEndian.AppendUint32([]byte{0x01}, uint32(7)^(1<<31))
	overIndex := forgedTable(t, s, short.Root(), false, withValue(t, s, index, append(ik, kb...), bytes.Repeat([]byte{0x01}, cfg().InlineLimit+1)))

	mdl := table.Model{Config: cfg()}
	drainRows := func(rows *table.Rows, err error) error {
		for err == nil {
			var ok bool
			if _, _, ok, err = rows.Next(); !ok {
				break
			}
		}
		return err
	}
	reads := map[string]func(model.Root) error{
		"Validate": func(r model.Root) error { return mdl.Validate(ctx, r, s) },
		"Diff": func(r model.Root) error {
			d, err := mdl.Diff(ctx, empty.Root(), r, s)
			for err == nil {
				var ok bool
				if _, ok, err = d.Next(ctx); !ok {
					break
				}
			}
			return err
		},
		"Get": func(r model.Root) error {
			tb, err := table.Open(ctx, s, cfg(), r)
			if err == nil {
				_, _, err = tb.Get(ctx, table.Key{int64(1)})
			}
			return err
		},
		"Scan": func(r model.Root) error {
			tb, err := table.Open(ctx, s, cfg(), r)
			if err != nil {
				return err
			}
			return drainRows(tb.Scan(ctx))
		},
		"IndexLookup": func(r model.Root) error {
			tb, err := table.Open(ctx, s, cfg(), r)
			if err != nil {
				return err
			}
			return drainRows(tb.IndexLookup(ctx, 1))
		},
	}
	if got, ok, err := tb.Get(ctx, table.Key{int64(1)}); err != nil || !ok || !bytes.Equal(got[2].([]byte), full) || !bytes.Equal(got[3].([]byte), full) {
		t.Fatalf("positive control: the row of %d bytes, its schema's longest, read back as %v, %v", longest, ok, err)
	}
	for how, read := range reads {
		if err := read(good); err != nil {
			t.Fatalf("positive control: %s of the table holding a row of %d bytes, its schema's longest: %v", how, longest, err)
		}
		if err := read(short.Root()); err != nil {
			t.Fatalf("positive control: %s of the table holding a short row: %v", how, err)
		}
		forged := map[string]model.Root{"a row one byte longer than its schema's longest": overRow}
		if how == "Validate" || how == "IndexLookup" {
			forged[fmt.Sprintf("an index entry whose value is a %d-byte stream", cfg().InlineLimit+1)] = overIndex
		}
		for what, r := range forged {
			var err error
			used := allocated(func() { err = read(r) })
			if !errors.Is(err, prolly.ErrValueTooLarge) || used > 64<<10 {
				t.Errorf("%s of a table holding %s allocated %d bytes and returned %v; want prolly.ErrValueTooLarge within 64 KiB: a forged stream is read before it is refused",
					how, what, used, err)
			}
		}
	}
}

// A table whose columns are all its key stores an empty row record, and
// its limit is one byte, not the core's default that a limit of 0 means:
// a forged stream there is refused unread too (snapshot-engine#7).
func TestRegression_SE7_AKeyOnlyTableReadsNoRowValue(t *testing.T) {
	s := memstore.New()
	id := table.Column{Tag: 1, Name: "id", Type: table.TypeInt8}
	tb, err := table.Create(ctx, s, cfg(), table.Schema{Columns: []table.Column{id}, PrimaryKey: []table.Tag{1}})
	if err != nil {
		t.Fatal(err)
	}
	e := tb.Edit()
	if _, err := e.Insert(table.Row{1: int64(1)}); err != nil {
		t.Fatal(err)
	}
	if tb, err = e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	kb, err := table.EncodeCell(id, int64(1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Get(ctx, tb.Root().Hash)
	if err != nil {
		t.Fatal(err)
	}
	_, primary, _, _, err := table.DecodeRoot(b)
	if err != nil {
		t.Fatal(err)
	}
	forged := forgedTable(t, s, tb.Root(), true, withValue(t, s, primary, kb, bytes.Repeat([]byte{0x01}, cfg().InlineLimit+1)))
	get := func(r model.Root) (table.Row, bool, error) {
		tb, err := table.Open(ctx, s, cfg(), r)
		if err != nil {
			return nil, false, err
		}
		return tb.Get(ctx, table.Key{int64(1)})
	}
	if row, ok, err := get(tb.Root()); err != nil || !ok || row[1] != int64(1) {
		t.Fatalf("positive control: the key-only row read back as %v, %v, %v", row, ok, err)
	}
	var gerr error
	used := allocated(func() { _, _, gerr = get(forged) })
	if !errors.Is(gerr, prolly.ErrValueTooLarge) || used > 64<<10 {
		t.Errorf("reading a key-only table whose row value is a %d-byte stream allocated %d bytes and returned %v; want prolly.ErrValueTooLarge within 64 KiB: a forged stream is read before it is refused",
			cfg().InlineLimit+1, used, gerr)
	}
}
