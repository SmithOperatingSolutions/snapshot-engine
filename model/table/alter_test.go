package table_test

import (
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// A table takes a new schema and keeps its rows: columns match by tag, so a
// rename costs nothing but the catalog; a column added is NULL in every row
// and so must be nullable; a column dropped takes its cells with it; an
// index added is built; a type may widen (int4 to int8, varchar to a longer
// varchar or to text) and its values follow. What the rows hold must fit the
// new schema: a varchar narrowed under a value in it, a type that cannot
// take the values, and any change to the primary key are refused, and the
// table is as it was.
func TestATableTakesANewSchema(t *testing.T) {
	s := memstore.New()
	base := seeded(t, s, 3)
	next := table.Schema{
		Columns: []table.Column{
			{Tag: 1, Name: "id", Type: table.TypeInt8},
			{Tag: 2, Name: "full_name", Type: table.TypeText},
			{Tag: 3, Name: "age", Type: table.TypeInt8, Nullable: true},
			{Tag: 5, Name: "phone", Type: table.TypeText, Nullable: true},
		},
		PrimaryKey: []table.Tag{1},
		Indexes:    []table.Index{{Tag: 10, Columns: []table.Tag{3}}, {Tag: 11, Columns: []table.Tag{2}}},
	}
	altered, err := base.WithSchema(ctx, next)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	if got := altered.Schema(); len(got.Columns) != 4 || got.Columns[1].Name != "full_name" || got.Columns[2].Type != table.TypeInt8 || got.Columns[3].Tag != 5 {
		t.Fatalf("the altered table's schema is %+v", got)
	}
	keys, rows := scanAll(t, altered)
	if len(keys) != 3 {
		t.Fatalf("the altered table holds %d rows, want the 3 it had", len(keys))
	}
	for i, row := range rows {
		id := int64(i + 1)
		if row[2] != "name"+itoa(id) || row[3] != int64(20+id) || row[4] != nil || row[5] != nil {
			t.Errorf("row %d after the new schema = %v: want the name under tag 2, age widened to int64, email gone, phone NULL", id, row)
		}
	}
	if _, byName := lookupAll(t, altered, 11, "name2"); len(byName) != 1 || byName[0][1] != int64(2) {
		t.Errorf("the index added with the new schema finds %v for name2, want row 2", byName)
	}
	if _, byAge := lookupAll(t, altered, 10, int64(23)); len(byAge) != 1 || byAge[0][1] != int64(3) {
		t.Errorf("the index over the widened column finds %v for age 23, want row 3", byAge)
	}
	if base.Root() == altered.Root() {
		t.Error("a table under a new schema has the root it had")
	}

	for name, bad := range map[string]struct {
		schema table.Schema
		want   error
	}{
		"a NOT NULL column added":             {withColumn(people(), table.Column{Tag: 6, Name: "must", Type: table.TypeBool}), table.ErrSchema},
		"a varchar narrowed under its values": {withType(people(), 4, table.TypeVarchar, 3), table.ErrValue},
		"a type that cannot take the values":  {withType(people(), 2, table.TypeInt4, 0), table.ErrSchema},
		"the primary key changed":             {withKey(people(), []table.Tag{2}), table.ErrSchema},
	} {
		if _, err := base.WithSchema(ctx, bad.schema); !errors.Is(err, bad.want) {
			t.Errorf("%s: WithSchema = %v, want %v", name, err, bad.want)
		}
	}
	if _, rows := scanAll(t, base); len(rows) != 3 || rows[0][4] != "p1@x" {
		t.Error("a refused schema changed the table")
	}
}

func itoa(i int64) string { return string(rune('0' + i)) }

func withColumn(s table.Schema, c table.Column) table.Schema {
	s.Columns = append(append([]table.Column(nil), s.Columns...), c)
	return s
}

func withType(s table.Schema, tag table.Tag, ty table.Type, maxLen uint32) table.Schema {
	s.Columns = append([]table.Column(nil), s.Columns...)
	for i := range s.Columns {
		if s.Columns[i].Tag == tag {
			s.Columns[i].Type, s.Columns[i].MaxLen = ty, maxLen
		}
	}
	return s
}

func withKey(s table.Schema, pk []table.Tag) table.Schema {
	s.PrimaryKey = pk
	return s
}
