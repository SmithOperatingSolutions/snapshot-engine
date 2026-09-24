package table_test

import (
	"fmt"
	"sort"
	"testing"

	"pgregory.net/rapid"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

func alter(t *testing.T, tb *table.Table, s table.Schema) *table.Table {
	t.Helper()
	out, err := tb.WithSchema(ctx, s)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	return out
}

func renamed(s table.Schema, tag table.Tag, name string) table.Schema {
	s.Columns = append([]table.Column(nil), s.Columns...)
	for i := range s.Columns {
		if s.Columns[i].Tag == tag {
			s.Columns[i].Name = name
		}
	}
	return s
}

func dropped(s table.Schema, tag table.Tag) table.Schema {
	var cols []table.Column
	for _, c := range s.Columns {
		if c.Tag != tag {
			cols = append(cols, c)
		}
	}
	s.Columns = cols
	return s
}

func nullable(s table.Schema, tag table.Tag, n bool) table.Schema {
	s.Columns = append([]table.Column(nil), s.Columns...)
	for i := range s.Columns {
		if s.Columns[i].Tag == tag {
			s.Columns[i].Nullable = n
		}
	}
	return s
}

func withIndex(s table.Schema, ix table.Index) table.Schema {
	var out []table.Index
	for _, x := range s.Indexes {
		if x.Tag != ix.Tag {
			out = append(out, x)
		}
	}
	out = append(out, ix)
	s.Indexes = out
	return s
}

// withRows inserts people rows 1..n into tb.
func withRows(t *testing.T, tb *table.Table, n int) *table.Table {
	t.Helper()
	e := tb.Edit()
	for i := 1; i <= n; i++ {
		if _, err := e.Insert(person(int64(i), fmt.Sprintf("name%d", i), int32(20+i), fmt.Sprintf("p%d@x", i))); err != nil {
			t.Fatal(err)
		}
	}
	return flush(t, e)
}

func open(t *testing.T, s chunk.ReadWriter, root model.Root) *table.Table {
	t.Helper()
	tb, err := table.Open(ctx, s, cfg(), root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return tb
}

// The Engine Spec's item: rename a column on branch A, update a row on
// branch B, merge: the data survives under the new name. Columns are matched
// by tag, so the rename is a catalog change and the row edit lands in it;
// the merge is the same whichever side is ours.
func TestAColumnRenamedOnOneSideKeepsTheOthersEdits(t *testing.T) {
	s := memstore.New()
	m := table.Model{Config: cfg()}
	base := seeded(t, s, 3)
	renamer := alter(t, base, renamed(people(), 2, "full_name"))
	editor := edit(t, base, func(e *table.Editor) error {
		return e.Update(table.Key{int64(1)}, person(1, "edited", int32(21), "p1@x"))
	})
	var roots []model.Root
	for _, sides := range [][2]*table.Table{{renamer, editor}, {editor, renamer}} {
		r := mergeTables(t, m, s, base, sides[0], sides[1])
		if len(r.Conflicts) != 0 {
			t.Fatalf("a rename against a row edit conflicts: %+v", r.Conflicts)
		}
		merged := open(t, s, r.Root)
		if got := merged.Schema().Columns[1].Name; got != "full_name" {
			t.Errorf("the merged table names column 2 %q, want the rename full_name", got)
		}
		_, rows := scanAll(t, merged)
		if len(rows) != 3 || rows[0][2] != "edited" || rows[1][2] != "name2" || rows[2][2] != "name3" {
			t.Errorf("the merged rows are %v: want row 1 edited under the renamed column and the rest as they were", rows)
		}
		roots = append(roots, r.Root)
	}
	if roots[0] != roots[1] {
		t.Errorf("the merge depends on which side is ours: %+v against %+v", roots[0], roots[1])
	}
}

// Schemas merge by column and index tag. A change on one side lands: a
// column added (nullable, since the other side's rows have no value), a
// column renamed, its type widened, its nullability or length changed, an
// index added or dropped, a column dropped whose cells the other side left
// alone. The same tag changed differently on both sides is a conflict at
// that tag; a column dropped on one side and written to on the other is a
// conflict at the column and no row is lost; the primary key cannot change.
// With any conflict the result is ours.
func TestSchemasMergeByTag(t *testing.T) {
	m := table.Model{Config: cfg()}
	phone := table.Column{Tag: 5, Name: "phone", Type: table.TypeText, Nullable: true}
	for name, tc := range map[string]struct {
		ours, theirs  func(s table.Schema) table.Schema
		fresh         func(t *testing.T, s chunk.ReadWriter) *table.Table // theirs built anew, where WithSchema would refuse the change
		freshOurs     bool                                                // ours is the same fresh table
		edit          func(e *table.Editor) error                         // theirs' row edit, if any
		wantConflicts []string
		check         func(t *testing.T, merged *table.Table)
	}{
		"the primary key changed the same way on both sides": { // rows cannot be matched to the base's
			fresh: func(t *testing.T, s chunk.ReadWriter) *table.Table {
				return withRows(t, create(t, s, withKey(people(), []table.Tag{2})), 3)
			},
			freshOurs:     true,
			wantConflicts: []string{"schema"},
		},
		"the primary key changed on one side": {
			fresh: func(t *testing.T, s chunk.ReadWriter) *table.Table {
				return withRows(t, create(t, s, withKey(people(), []table.Tag{2})), 3)
			},
			wantConflicts: []string{"schema"},
		},
		"a NOT NULL column added on one side": {
			fresh: func(t *testing.T, s chunk.ReadWriter) *table.Table {
				tb := create(t, s, withColumn(people(), table.Column{Tag: 6, Name: "must", Type: table.TypeBool}))
				e := tb.Edit()
				for i := int64(1); i <= 3; i++ {
					row := person(i, fmt.Sprintf("name%d", i), int32(20+i), fmt.Sprintf("p%d@x", i))
					row[6] = true
					if _, err := e.Insert(row); err != nil {
						t.Fatal(err)
					}
				}
				return flush(t, e)
			},
			wantConflicts: []string{"schema"},
		},
		"a column dropped on one side and changed on the other": {
			ours:          func(s table.Schema) table.Schema { return dropped(s, 4) },
			theirs:        func(s table.Schema) table.Schema { return withType(s, 4, table.TypeVarchar, 300) },
			wantConflicts: []string{"schema/4"},
		},
		"a type widened on one side, a row edited and one added on the other": {
			ours: func(s table.Schema) table.Schema { return withType(s, 3, table.TypeInt8, 0) },
			edit: func(e *table.Editor) error {
				if err := e.Update(table.Key{int64(2)}, person(2, "name2", int32(99), "p2@x")); err != nil {
					return err
				}
				_, err := e.Insert(person(4, "name4", int32(24), "p4@x"))
				return err
			},
			check: func(t *testing.T, merged *table.Table) {
				_, rows := scanAll(t, merged)
				if len(rows) != 4 || rows[1][3] != int64(99) || rows[3][3] != int64(24) {
					t.Errorf("rows after the widening, the edit and the insert: %v; want four, row 2's age int64 99 and row 4's int64 24", rows)
				}
			},
		},
		"a column added on one side": {
			theirs: func(s table.Schema) table.Schema { return withColumn(s, phone) },
			check: func(t *testing.T, merged *table.Table) {
				if len(merged.Schema().Columns) != 5 {
					t.Errorf("merged schema has %d columns, want people's four and phone", len(merged.Schema().Columns))
				}
				if _, rows := scanAll(t, merged); len(rows) != 3 || rows[0][5] != nil || rows[0][2] != "name1" {
					t.Errorf("merged rows %v: want three, phone NULL, the rest as before", rows)
				}
			},
		},
		"the same column added on both sides": {
			ours:   func(s table.Schema) table.Schema { return withColumn(s, phone) },
			theirs: func(s table.Schema) table.Schema { return withColumn(s, phone) },
			check: func(t *testing.T, merged *table.Table) {
				if n := len(merged.Schema().Columns); n != 5 {
					t.Errorf("merged schema has %d columns, want phone once", n)
				}
			},
		},
		"a column added differently on both sides": {
			ours: func(s table.Schema) table.Schema { return withColumn(s, phone) },
			theirs: func(s table.Schema) table.Schema {
				return withColumn(s, table.Column{Tag: 5, Name: "phone", Type: table.TypeInt8, Nullable: true})
			},
			wantConflicts: []string{"schema/5"},
		},
		"a column renamed differently on both sides": {
			ours:          func(s table.Schema) table.Schema { return renamed(s, 2, "full_name") },
			theirs:        func(s table.Schema) table.Schema { return renamed(s, 2, "display_name") },
			wantConflicts: []string{"schema/2"},
		},
		"a type widened on one side": {
			theirs: func(s table.Schema) table.Schema { return withType(s, 3, table.TypeInt8, 0) },
			check: func(t *testing.T, merged *table.Table) {
				if _, rows := scanAll(t, merged); rows[0][3] != int64(21) {
					t.Errorf("age after the widening is %T %v, want int64 21", rows[0][3], rows[0][3])
				}
			},
		},
		"a length changed on one side, nullability on the other": {
			ours:   func(s table.Schema) table.Schema { return withType(s, 4, table.TypeVarchar, 300) },
			theirs: func(s table.Schema) table.Schema { return nullable(s, 4, true) },
			check: func(t *testing.T, merged *table.Table) {
				c := merged.Schema().Columns[3]
				if c.MaxLen != 300 || !c.Nullable {
					t.Errorf("email merged as %+v, want varchar(300) nullable", c)
				}
			},
		},
		"a length changed differently on both sides": {
			ours:          func(s table.Schema) table.Schema { return withType(s, 4, table.TypeVarchar, 300) },
			theirs:        func(s table.Schema) table.Schema { return withType(s, 4, table.TypeVarchar, 400) },
			wantConflicts: []string{"schema/4"},
		},
		"a column dropped on one side, untouched on the other": {
			ours: func(s table.Schema) table.Schema { return dropped(s, 4) },
			edit: func(e *table.Editor) error {
				return e.Update(table.Key{int64(2)}, person(2, "edited", int32(22), "p2@x"))
			},
			check: func(t *testing.T, merged *table.Table) {
				if _, has := merged.Schema().Columns[len(merged.Schema().Columns)-1], len(merged.Schema().Columns) == 4; has {
					t.Errorf("the dropped column is still in the merged schema: %+v", merged.Schema().Columns)
				}
				if _, rows := scanAll(t, merged); rows[1][2] != "edited" || rows[1][4] != nil {
					t.Errorf("row 2 merged as %v: want the edit landed and no email", rows[1])
				}
			},
		},
		"a column dropped on one side and written to on the other": {
			ours: func(s table.Schema) table.Schema { return dropped(s, 4) },
			edit: func(e *table.Editor) error {
				return e.Update(table.Key{int64(2)}, person(2, "name2", int32(22), "new@x"))
			},
			wantConflicts: []string{"schema/4"},
		},
		"an index added on one side": {
			theirs: func(s table.Schema) table.Schema { return withIndex(s, table.Index{Tag: 11, Columns: []table.Tag{2}}) },
			check: func(t *testing.T, merged *table.Table) {
				if _, rows := lookupAll(t, merged, 11, "name3"); len(rows) != 1 || rows[0][1] != int64(3) {
					t.Errorf("the merged index finds %v for name3, want row 3", rows)
				}
			},
		},
		"an index dropped on one side and changed on the other": {
			ours:          func(s table.Schema) table.Schema { s.Indexes = nil; return s },
			theirs:        func(s table.Schema) table.Schema { return withIndex(s, table.Index{Tag: 10, Columns: []table.Tag{2}}) },
			wantConflicts: []string{"schema/index/10"},
		},
		"a row deleted on one side across a schema change on the other": {
			ours: func(s table.Schema) table.Schema { return renamed(s, 2, "full_name") },
			edit: func(e *table.Editor) error { return e.Delete(table.Key{int64(3)}) },
			check: func(t *testing.T, merged *table.Table) {
				if keys, _ := scanAll(t, merged); len(keys) != 2 {
					t.Errorf("the merged table holds %d rows, want the two left after the delete", len(keys))
				}
			},
		},
		"an index changed differently on both sides": {
			ours:          func(s table.Schema) table.Schema { return withIndex(s, table.Index{Tag: 10, Columns: []table.Tag{2}}) },
			theirs:        func(s table.Schema) table.Schema { return withIndex(s, table.Index{Tag: 10, Columns: []table.Tag{4}}) },
			wantConflicts: []string{"schema/index/10"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := memstore.New()
			base := seeded(t, s, 3)
			ours, theirs := base, base
			if tc.ours != nil {
				ours = alter(t, base, tc.ours(people()))
			}
			if tc.theirs != nil {
				theirs = alter(t, base, tc.theirs(people()))
			}
			if tc.fresh != nil {
				theirs = tc.fresh(t, s)
				if tc.freshOurs {
					ours = theirs
				}
			}
			if tc.edit != nil {
				theirs = edit(t, theirs, tc.edit)
			}
			r := mergeTables(t, m, s, base, ours, theirs)
			if tc.wantConflicts != nil {
				if got := conflictLocations(r); fmt.Sprint(got) != fmt.Sprint(tc.wantConflicts) {
					t.Fatalf("conflicts %v (%+v), want %v", got, r.Conflicts, tc.wantConflicts)
				}
				for _, c := range r.Conflicts {
					if c.Reason == "" {
						t.Errorf("a conflict at %s has no reason", c.Location)
					}
				}
				if r.Root != ours.Root() {
					t.Errorf("with conflicts the result is %+v, want ours untouched", r.Root)
				}
				return
			}
			if len(r.Conflicts) != 0 {
				t.Fatalf("conflicts: %+v", r.Conflicts)
			}
			merged := open(t, s, r.Root)
			if err := m.Validate(ctx, r.Root, s); err != nil {
				t.Fatalf("the merged table does not validate: %v", err)
			}
			tc.check(t, merged)
		})
	}
}

// Independent, compatible schema edits on both sides (each side touches
// columns of its own, adds columns and indexes with tags of its own) merge
// clean, to the same table whichever side is ours, and the same table
// twice; the merged table validates.
func TestIndependentSchemaEditsMergeCleanAndSymmetric(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := memstore.New()
		m := table.Model{Config: cfg()}
		n := rapid.IntRange(2, 6).Draw(rt, "columns")
		schema := table.Schema{Columns: []table.Column{{Tag: 1, Name: "id", Type: table.TypeInt8}}, PrimaryKey: []table.Tag{1}}
		for i := 2; i <= n; i++ {
			schema.Columns = append(schema.Columns, table.Column{Tag: table.Tag(i), Name: fmt.Sprintf("c%d", i), Type: table.TypeVarchar, MaxLen: 8, Nullable: true})
		}
		tb := create(t, s, schema)
		e := tb.Edit()
		for id := 1; id <= rapid.IntRange(0, 4).Draw(rt, "rows"); id++ {
			row := table.Row{1: int64(id)}
			for i := 2; i <= n; i++ {
				if rapid.Bool().Draw(rt, "set") {
					row[table.Tag(i)] = fmt.Sprintf("v%d", id)
				}
			}
			if _, err := e.Insert(row); err != nil {
				rt.Fatal(err)
			}
		}
		base := flush(t, e)
		sides := [2]*table.Table{}
		for side := range 2 {
			next := schema
			next.Columns = append([]table.Column(nil), schema.Columns...)
			for i := 2; i <= n; i++ { // this side edits the columns whose tag has its parity
				if i%2 != side {
					continue
				}
				switch rapid.IntRange(0, 3).Draw(rt, fmt.Sprintf("edit%d", i)) {
				case 0: // leave
				case 1: // rename
					next = renamed(next, table.Tag(i), fmt.Sprintf("r%d_%d", side, i))
				case 2: // widen
					next = withType(next, table.Tag(i), table.TypeText, 0)
				case 3: // drop
					next = dropped(next, table.Tag(i))
				}
			}
			for k := range rapid.IntRange(0, 2).Draw(rt, fmt.Sprintf("adds%d", side)) {
				next = withColumn(next, table.Column{Tag: table.Tag(100 + 50*side + k), Name: fmt.Sprintf("a%d_%d", side, 9-k), Type: table.TypeText, Nullable: true}) // names run against the tags
			}
			if rapid.Bool().Draw(rt, fmt.Sprintf("index%d", side)) {
				next = withIndex(next, table.Index{Tag: table.Tag(200 + side), Columns: []table.Tag{1}})
			}
			sides[side] = alter(t, base, next)
		}
		var roots []model.Root
		for _, order := range [][2]*table.Table{{sides[0], sides[1]}, {sides[1], sides[0]}, {sides[0], sides[1]}} {
			r := mergeTables(t, m, s, base, order[0], order[1])
			if len(r.Conflicts) != 0 {
				rt.Fatalf("independent edits conflicted: %+v", r.Conflicts)
			}
			if err := m.Validate(ctx, r.Root, s); err != nil {
				rt.Fatalf("the merged table does not validate: %v", err)
			}
			roots = append(roots, r.Root)
		}
		if roots[0] != roots[1] || roots[0] != roots[2] {
			rt.Fatalf("the merge is not symmetric or not deterministic: %+v %+v %+v", roots[0], roots[1], roots[2])
		}
		merged := open(t, s, roots[0])
		var tags []int
		for _, c := range merged.Schema().Columns {
			tags = append(tags, int(c.Tag))
		}
		kept := tags[:0:0]
		var added []int
		for _, tag := range tags {
			if tag < 100 {
				kept = append(kept, tag)
			} else {
				added = append(added, tag)
			}
		}
		if !sort.IntsAreSorted(kept) || !sort.IntsAreSorted(added) || fmt.Sprint(tags) != fmt.Sprint(append(kept, added...)) {
			rt.Fatalf("merged columns %v: want the base's kept columns in their order, then the additions by tag", tags)
		}
	})
}
