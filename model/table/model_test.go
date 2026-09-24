package table_test

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/model/contract"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// seeded is a people table holding rows 1..n, each a function of its id.
func seeded(t *testing.T, s chunk.ReadWriter, n int) *table.Table {
	t.Helper()
	e := create(t, s, people()).Edit()
	for i := 1; i <= n; i++ {
		if _, err := e.Insert(person(int64(i), fmt.Sprintf("name%d", i), int32(20+i), fmt.Sprintf("p%d@x", i))); err != nil {
			t.Fatal(err)
		}
	}
	return flush(t, e)
}

func edit(t *testing.T, tb *table.Table, f func(e *table.Editor) error) *table.Table {
	t.Helper()
	e := tb.Edit()
	if err := f(e); err != nil {
		t.Fatal(err)
	}
	return flush(t, e)
}

func locate(t *testing.T, tb *table.Table, id int64, column table.Tag) string {
	t.Helper()
	loc, err := tb.Locate(table.Key{id}, column)
	if err != nil {
		t.Fatal(err)
	}
	return string(loc)
}

func diff(t *testing.T, m table.Model, s chunk.Reader, from, to model.Root) map[string]model.ChangeKind {
	t.Helper()
	d, err := m.Diff(ctx, from, to, s)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	out := map[string]model.ChangeKind{}
	for {
		c, ok, err := d.Next(ctx)
		if err != nil {
			t.Fatalf("Diff: %v", err)
		}
		if !ok {
			return out
		}
		if _, dup := out[string(c.Location)]; dup {
			t.Fatalf("Diff reported location %x twice", c.Location)
		}
		out[string(c.Location)] = c.Kind
	}
}

// A diff is per row, then per cell: a row added or removed is one change at
// its key; a row changed is a change per cell that differs, at the key then
// the column's tag, and none for the cells that did not. The locations parse
// back to the key and column.
func TestDiffIsPerRowThenPerCell(t *testing.T) {
	s := memstore.New()
	m := table.Model{Config: cfg()}
	base := seeded(t, s, 4)
	next := edit(t, base, func(e *table.Editor) error {
		if _, err := e.Insert(person(5, "name5", int32(25), "p5@x")); err != nil {
			return err
		}
		if err := e.Delete(table.Key{int64(2)}); err != nil {
			return err
		}
		if err := e.Update(table.Key{int64(3)}, person(3, "renamed", int32(23), "p3@x")); err != nil {
			return err
		}
		return e.Update(table.Key{int64(4)}, person(4, "name4", nil, "new4@x"))
	})
	got := diff(t, m, s, base.Root(), next.Root())
	want := map[string]model.ChangeKind{
		locate(t, base, 5, 0): model.Added,
		locate(t, base, 2, 0): model.Removed,
		locate(t, base, 3, 2): model.Modified,
		locate(t, base, 4, 3): model.Modified,
		locate(t, base, 4, 4): model.Modified,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Diff gave %d changes %v, want %d: an added row, a removed row, one cell of row 3 and two of row 4", len(got), got, len(want))
	}
	if key, col, err := base.ParseLocation([]byte(locate(t, base, 4, 3))); err != nil || len(key) != 1 || key[0] != int64(4) || col != 3 {
		t.Fatalf("ParseLocation = %v, %d, %v; want key 4, column 3", key, col, err)
	}
	if key, col, err := base.ParseLocation([]byte(locate(t, base, 5, 0))); err != nil || key[0] != int64(5) || col != 0 {
		t.Fatalf("ParseLocation of a row = %v, %d, %v; want key 5, column 0", key, col, err)
	}
	if _, _, err := base.ParseLocation([]byte("garbage")); err == nil {
		t.Fatal("ParseLocation accepted garbage")
	}
	if len(diff(t, m, s, base.Root(), base.Root())) != 0 {
		t.Fatal("a table diffed with itself has changes")
	}
}

func mergeTables(t *testing.T, m table.Model, s chunk.ReadWriter, base, ours, theirs *table.Table) model.MergeResult {
	t.Helper()
	r, err := m.Merge(ctx, base.Root(), ours.Root(), theirs.Root(), s)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return r
}

func conflictLocations(r model.MergeResult) []string {
	var out []string
	for _, c := range r.Conflicts {
		out = append(out, string(c.Location))
	}
	sort.Strings(out)
	return out
}

// Rows merge independently and cells within a row too: two branches that
// change different cells of one row both land, a row deleted on one side
// and untouched on the other is gone (row 6, deleted by theirs alone; row 2,
// by ours alone), rows added on each side are both
// there, and identical changes are taken once. A cell changed differently
// on both sides is a conflict at that cell alone, a row deleted on one side
// and changed on the other a conflict at that row; a conflicted merge
// returns ours and every conflict.
func TestMergeCombinesRowsAndCells(t *testing.T) {
	s := memstore.New()
	m := table.Model{Config: cfg()}
	base := seeded(t, s, 6)
	ours := edit(t, base, func(e *table.Editor) error {
		if err := e.Update(table.Key{int64(1)}, person(1, "ours", int32(21), "p1@x")); err != nil {
			return err
		}
		if err := e.Delete(table.Key{int64(2)}); err != nil {
			return err
		}
		if err := e.Update(table.Key{int64(5)}, person(5, "same", int32(25), "p5@x")); err != nil {
			return err
		}
		_, err := e.Insert(person(7, "seven", int32(27), "p7@x"))
		return err
	})
	theirs := edit(t, base, func(e *table.Editor) error {
		if err := e.Update(table.Key{int64(1)}, person(1, "name1", int32(99), "p1@x")); err != nil {
			return err
		}
		if err := e.Update(table.Key{int64(3)}, person(3, "name3", int32(23), "t3@x")); err != nil {
			return err
		}
		if err := e.Update(table.Key{int64(5)}, person(5, "same", int32(25), "p5@x")); err != nil {
			return err
		}
		if err := e.Delete(table.Key{int64(6)}); err != nil {
			return err
		}
		_, err := e.Insert(person(8, "eight", int32(28), "p8@x"))
		return err
	})
	r := mergeTables(t, m, s, base, ours, theirs)
	if len(r.Conflicts) != 0 {
		t.Fatalf("a merge of changes to different cells and rows conflicts: %v", r.Conflicts)
	}
	merged, err := table.Open(ctx, s, cfg(), r.Root)
	if err != nil {
		t.Fatal(err)
	}
	_, rows := scanAll(t, merged)
	var got []string
	for _, row := range rows {
		got = append(got, fmt.Sprintf("%d:%s:%v:%s", row[1], row[2], row[3], row[4]))
	}
	want := []string{"1:ours:99:p1@x", "3:name3:23:t3@x", "4:name4:24:p4@x", "5:same:25:p5@x", "7:seven:27:p7@x", "8:eight:28:p8@x"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the merged table holds\n  %v\nwant\n  %v", got, want)
	}
	if err := m.Validate(ctx, r.Root, s); err != nil {
		t.Fatalf("the merged table does not validate (are its indexes maintained?): %v", err)
	}
	// Conflicts: cell 3 of row 1 changed differently; row 4 deleted on one side, changed on the other.
	ours2 := edit(t, base, func(e *table.Editor) error {
		if err := e.Update(table.Key{int64(1)}, person(1, "name1", int32(50), "p1@x")); err != nil {
			return err
		}
		return e.Delete(table.Key{int64(4)})
	})
	theirs2 := edit(t, base, func(e *table.Editor) error {
		if err := e.Update(table.Key{int64(1)}, person(1, "theirs", int32(60), "p1@x")); err != nil {
			return err
		}
		return e.Update(table.Key{int64(4)}, person(4, "name4", int32(40), "p4@x"))
	})
	r = mergeTables(t, m, s, base, ours2, theirs2)
	wantConflicts := []string{locate(t, base, 1, 3), locate(t, base, 4, 0)}
	sort.Strings(wantConflicts)
	if got := conflictLocations(r); fmt.Sprint(got) != fmt.Sprint(wantConflicts) {
		t.Fatalf("conflicts at %x, want row 1's age cell and row 4", got)
	}
	if r.Root != ours2.Root() {
		t.Fatalf("a conflicted merge returned root %+v, want ours %+v", r.Root, ours2.Root())
	}
	for _, c := range r.Conflicts {
		if c.Reason == "" {
			t.Fatalf("conflict at %x has no reason", c.Location)
		}
	}
}

// Two tables of different schemas do not merge: one conflict, at the
// schema, and ours comes back untouched; the same schema written on both
// sides is no conflict (the positive control above).
func TestTablesOfDifferentSchemasAreOneConflict(t *testing.T) {
	s := memstore.New()
	m := table.Model{Config: cfg()}
	base := seeded(t, s, 2)
	ours := edit(t, base, func(e *table.Editor) error {
		return e.Update(table.Key{int64(1)}, person(1, "ours", int32(21), "p1@x"))
	})
	wider := people()
	wider.Columns = append(wider.Columns, table.Column{Tag: 5, Name: "phone", Type: table.TypeText, Nullable: true})
	theirsTable := create(t, s, wider)
	theirs := edit(t, theirsTable, func(e *table.Editor) error {
		for i := int64(1); i <= 2; i++ {
			if _, err := e.Insert(person(i, fmt.Sprintf("name%d", i), int32(20+i), fmt.Sprintf("p%d@x", i))); err != nil {
				return err
			}
		}
		return nil
	})
	r := mergeTables(t, m, s, base, ours, theirs)
	if len(r.Conflicts) != 1 || string(r.Conflicts[0].Location) != "schema" || r.Root != ours.Root() {
		t.Fatalf("merging tables of two schemas = root %+v, conflicts %v; want ours and one conflict at \"schema\"", r.Root, r.Conflicts)
	}
}

// The content the contract writes and reads: the catalog, then one line per
// row in key order, cells separated by '|', NULL as "NULL".
func serialize(schema table.Schema, rows []table.Row) []byte {
	catalog, _ := table.EncodeCatalog(schema)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%x\n", catalog)
	for _, r := range rows {
		var cells []string
		for _, c := range schema.Columns {
			v, ok := r[c.Tag]
			if !ok {
				cells = append(cells, "NULL")
				continue
			}
			cells = append(cells, fmt.Sprint(v))
		}
		sb.WriteString(strings.Join(cells, "|") + "\n")
	}
	return []byte(sb.String())
}

func parse(t *testing.T, content []byte) (table.Schema, []table.Row) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	var catalog []byte
	if _, err := fmt.Sscanf(lines[0], "%x", &catalog); err != nil {
		t.Fatal(err)
	}
	schema, err := table.DecodeCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	var rows []table.Row
	for _, line := range lines[1:] {
		cells := strings.Split(line, "|")
		row := table.Row{}
		for i, c := range schema.Columns {
			if cells[i] == "NULL" {
				continue
			}
			switch c.Type {
			case table.TypeInt8:
				n, _ := strconv.ParseInt(cells[i], 10, 64)
				row[c.Tag] = n
			case table.TypeInt4:
				n, _ := strconv.ParseInt(cells[i], 10, 32)
				row[c.Tag] = int32(n)
			default:
				row[c.Tag] = cells[i]
			}
		}
		rows = append(rows, row)
	}
	return schema, rows
}

func generate(seed uint64) (table.Schema, []table.Row) {
	n := 1 + int(seed%5)
	var rows []table.Row
	for i := 0; i < n; i++ {
		var age any
		if (seed+uint64(i))%3 != 0 {
			age = int32(20 + int(seed) + i)
		}
		rows = append(rows, person(int64(seed*10+uint64(i)), fmt.Sprintf("n%d-%d", seed, i), age, fmt.Sprintf("s%d.%d@x", seed, i)))
	}
	return people(), rows
}

func subject(t *testing.T) contract.Subject {
	s := memstore.New()
	m := table.Model{Config: cfg()}
	return contract.Subject{
		Model: m,
		Store: s,
		Generate: func(seed uint64) []byte {
			schema, rows := generate(seed)
			return serialize(schema, rows)
		},
		Mutate: func(c []byte, seed uint64) []byte {
			schema, rows := parse(t, c)
			rows[0][2] = fmt.Sprintf("%s+%d", rows[0][2], seed)
			rows = append(rows, person(int64(1000+seed), "added", int32(seed), "a@x"))
			sort.Slice(rows, func(i, j int) bool { return rows[i][1].(int64) < rows[j][1].(int64) })
			return serialize(schema, rows)
		},
		Write: func(t *testing.T, c []byte) model.Root {
			t.Helper()
			schema, rows := parse(t, c)
			e := create(t, s, schema).Edit()
			for _, r := range rows {
				if _, err := e.Insert(r); err != nil {
					t.Fatal(err)
				}
			}
			return flush(t, e).Root()
		},
		Read: func(t *testing.T, r model.Root) []byte {
			t.Helper()
			tb, err := table.Open(ctx, s, cfg(), r)
			if err != nil {
				t.Fatal(err)
			}
			_, rows := scanAll(t, tb)
			return serialize(tb.Schema(), rows)
		},
	}
}

func TestContract(t *testing.T) { contract.Run(t, subject) }

// A table with a long text cell (past the map's inline limit, so the value
// is a stream of its own) still walks whole: Walk names the stream's chunks.
func TestWalkNamesALongCellsChunks(t *testing.T) {
	s := memstore.New()
	m := table.Model{Config: cfg()}
	e := create(t, s, people()).Edit()
	long := strings.Repeat("long ", 60000) // 300 KB, over the 256 KiB inline limit
	if _, err := e.Insert(person(1, long, int32(1), "l@x")); err != nil {
		t.Fatal(err)
	}
	tb := flush(t, e)
	kept := memstore.New()
	err := m.Walk(ctx, tb.Root(), s, func(h hash.Hash, _ bool) (bool, error) {
		b, err := s.Get(ctx, h)
		if err != nil {
			return false, err
		}
		_, err = kept.Put(ctx, b)
		return true, err
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if err := m.Validate(ctx, tb.Root(), kept); err != nil {
		t.Fatalf("from the chunks Walk names alone, a table with a long cell does not validate: %v", err)
	}
}

// Cells of the byte types compare by content: a bytea changed is a change
// at that cell, a jsonb rewritten with the same bytes is none.
func TestByteCellsDiffByContent(t *testing.T) {
	s := memstore.New()
	m := table.Model{Config: cfg()}
	schema := table.Schema{Columns: []table.Column{
		{Tag: 1, Name: "id", Type: table.TypeInt4},
		{Tag: 2, Name: "blob", Type: table.TypeBytea},
		{Tag: 3, Name: "doc", Type: table.TypeJSONB},
	}, PrimaryKey: []table.Tag{1}}
	base := edit(t, create(t, s, schema), func(e *table.Editor) error {
		_, err := e.Insert(table.Row{1: int32(1), 2: []byte{1, 2}, 3: table.JSONB(`{"a":1}`)})
		return err
	})
	next := edit(t, base, func(e *table.Editor) error {
		return e.Update(table.Key{int32(1)}, table.Row{1: int32(1), 2: []byte{1, 3}, 3: table.JSONB(`{"a":1}`)})
	})
	got := diff(t, m, s, base.Root(), next.Root())
	loc, err := base.Locate(table.Key{int32(1)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[string(loc)] != model.Modified {
		t.Fatalf("Diff = %v, want one change at the blob cell", got)
	}
	other := create(t, s, people())
	if _, err := m.Diff(ctx, base.Root(), other.Root(), s); err == nil {
		t.Fatal("Diff of two tables with different schemas did not fail")
	}
}
