package table_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// people is the schema most tests use: an int8 key, a text name, a nullable
// int4 age, a varchar(255) email, and an index on age.
func people() table.Schema {
	return table.Schema{
		Columns: []table.Column{
			{Tag: 1, Name: "id", Type: table.TypeInt8},
			{Tag: 2, Name: "name", Type: table.TypeText},
			{Tag: 3, Name: "age", Type: table.TypeInt4, Nullable: true},
			{Tag: 4, Name: "email", Type: table.TypeVarchar, MaxLen: 255},
		},
		PrimaryKey: []table.Tag{1},
		Indexes:    []table.Index{{Tag: 10, Columns: []table.Tag{3}}},
	}
}

// The catalog is the documented record, laid out by hand here, and decodes
// back to the schema it was made from.
func TestTheCatalogIsTheDocumentedRecord(t *testing.T) {
	s := people()
	got, err := table.EncodeCatalog(s)
	if err != nil {
		t.Fatalf("EncodeCatalog: %v", err)
	}
	var want []byte
	want = append(want, "VDTC"...)
	want = binary.LittleEndian.AppendUint16(want, 1) // version
	want = append(want, 4)                           // columns
	col := func(tag uint16, name string, typ byte, nullable byte, maxLen uint32) {
		want = binary.LittleEndian.AppendUint16(want, tag)
		want = append(want, byte(len(name)))
		want = append(want, name...)
		want = append(want, typ, nullable)
		want = binary.LittleEndian.AppendUint32(want, maxLen)
	}
	col(1, "id", byte(table.TypeInt8), 0, 0)
	col(2, "name", byte(table.TypeText), 0, 0)
	col(3, "age", byte(table.TypeInt4), 1, 0)
	col(4, "email", byte(table.TypeVarchar), 0, 255)
	want = append(want, 1) // primary key columns
	want = binary.LittleEndian.AppendUint16(want, 1)
	want = append(want, 1) // indexes
	want = binary.LittleEndian.AppendUint16(want, 10)
	want = append(want, 1)
	want = binary.LittleEndian.AppendUint16(want, 3)
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeCatalog =\n%x\nwant\n%x", got, want)
	}
	back, err := table.DecodeCatalog(want)
	if err != nil {
		t.Fatalf("DecodeCatalog: %v", err)
	}
	if err := sameSchema(back, s); err != nil {
		t.Fatal(err)
	}
}

func sameSchema(got, want table.Schema) error {
	a, err := table.EncodeCatalog(got)
	if err != nil {
		return err
	}
	b, err := table.EncodeCatalog(want)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return errors.New("the decoded schema differs from the one encoded")
	}
	return nil
}

// A schema that cannot be a table's is refused by Validate and by
// EncodeCatalog, with ErrSchema; the schema the tests use is the positive
// control.
func TestSchemasThatCannotBeATablesAreRefused(t *testing.T) {
	if err := people().Validate(); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	for name, mutate := range map[string]func(s *table.Schema){
		"no columns":                      func(s *table.Schema) { s.Columns = nil; s.PrimaryKey = nil; s.Indexes = nil },
		"a tag of 0":                      func(s *table.Schema) { s.Columns[1].Tag = 0 },
		"two columns with one tag":        func(s *table.Schema) { s.Columns[1].Tag = 1 },
		"two columns with one name":       func(s *table.Schema) { s.Columns[1].Name = "id" },
		"an empty name":                   func(s *table.Schema) { s.Columns[1].Name = "" },
		"a name over the limit":           func(s *table.Schema) { s.Columns[1].Name = string(bytes.Repeat([]byte("n"), table.MaxNameLen+1)) },
		"an unknown type":                 func(s *table.Schema) { s.Columns[1].Type = 99 },
		"a type of 0":                     func(s *table.Schema) { s.Columns[1].Type = 0 },
		"a varchar without a length":      func(s *table.Schema) { s.Columns[3].MaxLen = 0 },
		"a length on a text column":       func(s *table.Schema) { s.Columns[1].MaxLen = 10 },
		"a key column that is unknown":    func(s *table.Schema) { s.PrimaryKey = []table.Tag{9} },
		"a key column that is nullable":   func(s *table.Schema) { s.PrimaryKey = []table.Tag{3} },
		"a key column twice":              func(s *table.Schema) { s.PrimaryKey = []table.Tag{1, 1} },
		"an index of no columns":          func(s *table.Schema) { s.Indexes[0].Columns = nil },
		"an index over an unknown column": func(s *table.Schema) { s.Indexes[0].Columns = []table.Tag{9} },
		"an index naming a column twice":  func(s *table.Schema) { s.Indexes[0].Columns = []table.Tag{3, 3} },
		"an index tag of 0":               func(s *table.Schema) { s.Indexes[0].Tag = 0 },
		"two indexes with one tag":        func(s *table.Schema) { s.Indexes = append(s.Indexes, table.Index{Tag: 10, Columns: []table.Tag{2}}) },
	} {
		s := people()
		mutate(&s)
		if err := s.Validate(); !errors.Is(err, table.ErrSchema) {
			t.Errorf("%s: Validate = %v, want ErrSchema", name, err)
		}
		if _, err := table.EncodeCatalog(s); !errors.Is(err, table.ErrSchema) {
			t.Errorf("%s: EncodeCatalog = %v, want ErrSchema", name, err)
		}
	}
}

// A catalog record that is not what the encoder writes is refused with
// ErrCorrupt: truncations, trailing bytes, a wrong magic or version, and a
// record whose schema does not validate.
func TestForgedCatalogsAreRefused(t *testing.T) {
	good, err := table.EncodeCatalog(people())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.DecodeCatalog(good); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	forged := map[string][]byte{
		"empty":          {},
		"wrong magic":    append([]byte("VDTX"), good[4:]...),
		"version 2":      append(append([]byte("VDTC"), 2, 0), good[6:]...),
		"trailing byte":  append(bytes.Clone(good), 0),
		"a nullable key": bytes.Replace(good, []byte{byte(table.TypeInt8), 0, 0, 0, 0, 0}, []byte{byte(table.TypeInt8), 1, 0, 0, 0, 0}, 1),
	}
	for i := 1; i < len(good); i += 3 {
		forged["truncated to "+string(rune('0'+i%10))+" bytes, at "+string(rune('a'+i%26))] = good[:i]
	}
	for name, b := range forged {
		if _, err := table.DecodeCatalog(b); !errors.Is(err, chunk.ErrCorrupt) {
			t.Errorf("%s: DecodeCatalog = %v, want ErrCorrupt", name, err)
		}
	}
}

// FuzzDecodeCatalog: a catalog decoder never panics, and whatever it accepts
// re-encodes to the same bytes (the record is canonical).
func FuzzDecodeCatalog(f *testing.F) {
	good, _ := table.EncodeCatalog(people())
	f.Add(good)
	f.Add([]byte("VDTC"))
	f.Fuzz(func(t *testing.T, b []byte) {
		s, err := table.DecodeCatalog(b)
		if err != nil {
			return
		}
		back, err := table.EncodeCatalog(s)
		if err != nil {
			t.Fatalf("a decoded catalog does not encode: %v", err)
		}
		if !bytes.Equal(back, b) {
			t.Fatalf("a catalog that decoded re-encodes differently")
		}
	})
}
