package table_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// rebaseTable is a table of schema s holding 500 rows, and their keys in
// key order.
func rebaseTable(t *testing.T, s *memstore.Store, schema table.Schema) (*table.Table, []table.Key) {
	t.Helper()
	tb, err := table.Create(ctx, s, cfg(), schema)
	if err != nil {
		t.Fatal(err)
	}
	e := tb.Edit()
	for i := range 500 {
		if _, err := e.Insert(table.Row{1: int64(i), 2: fmt.Sprintf("name %d %0100d", i, i), 3: int32(i % 90), 4: "e"}); err != nil {
			t.Fatal(err)
		}
	}
	if tb, err = e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := tb.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var keys []table.Key
	for {
		k, _, ok, err := rows.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return tb, keys
		}
		keys = append(keys, k)
	}
}

// edited is tb with f's edits flushed.
func edited(t *testing.T, tb *table.Table, f func(e *table.Editor) error) *table.Table {
	t.Helper()
	e := tb.Edit()
	if err := f(e); err != nil {
		t.Fatal(err)
	}
	out, err := e.Flush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func rebaseRow(id int64, name string) table.Row {
	return table.Row{1: id, 2: name, 3: int32(id % 7), 4: "r"}
}

// Rebase is the merge of two sides that changed different rows, reached by
// reading one side's changes and a point read of the other per row:
// Rebase(base, side, onto) is Merge(base, onto, side), byte for byte (the
// root record, the primary map and every index), with rows updated,
// inserted and deleted on each side, for a keyed table and a table without
// a declared key.
func TestRebaseIsTheMergeOfChangesToDifferentRows(t *testing.T) {
	keyless := people()
	keyless.PrimaryKey = nil
	for name, schema := range map[string]table.Schema{"keyed": people(), "keyless": keyless} {
		t.Run(name, func(t *testing.T) {
			s := memstore.New()
			m := table.Model{Config: cfg()}
			base, keys := rebaseTable(t, s, schema)
			change := func(from int) func(e *table.Editor) error {
				return func(e *table.Editor) error {
					for i := from; i < from+40; i += 4 {
						if err := e.Update(keys[i], rebaseRow(int64(i), fmt.Sprintf("edited %d", i))); err != nil {
							return err
						}
					}
					if err := e.Delete(keys[from+41]); err != nil {
						return err
					}
					_, err := e.Insert(rebaseRow(int64(1000+from), "added"))
					return err
				}
			}
			side := edited(t, base, change(0))
			onto := edited(t, base, change(200))
			want, err := m.Merge(ctx, base.Root(), onto.Root(), side.Root(), s)
			if err != nil || len(want.Conflicts) > 0 {
				t.Fatalf("the merge: %v, %v", want.Conflicts, err)
			}
			got, err := m.Rebase(ctx, base.Root(), side.Root(), onto.Root(), s)
			if err != nil || got != want.Root {
				t.Errorf("Rebase = %v, %v; Merge = %v: a transaction rebased onto the batch would land another table than the merge it replaces", got, err, want.Root)
			}
		})
	}
}

// A row the side changed that the target changed too since the base is
// ErrChangedSince, whatever the two wrote; a target whose schema is not the
// base's and the side's is ErrSchema (the caller merges instead). A row
// only one of them changed rebases (the positive control, same fixture).
func TestRebaseRefusesARowBothSidesChangedAndAnotherSchema(t *testing.T) {
	s := memstore.New()
	m := table.Model{Config: cfg()}
	base, keys := rebaseTable(t, s, people())
	update := func(i int, name string) func(e *table.Editor) error {
		return func(e *table.Editor) error { return e.Update(keys[i], rebaseRow(int64(i), name)) }
	}
	side := edited(t, base, update(1, "side"))
	if _, err := m.Rebase(ctx, base.Root(), side.Root(), edited(t, base, update(2, "onto")).Root(), s); err != nil {
		t.Fatalf("positive control: rebasing a change to row 1 onto a change to row 2 = %v", err)
	}
	for name, onto := range map[string]*table.Table{
		"the same value": edited(t, base, update(1, "side")),
		"another cell of the row": edited(t, base, func(e *table.Editor) error {
			return e.Update(keys[1], table.Row{1: int64(1), 2: fmt.Sprintf("name 1 %0100d", 1), 3: int32(55), 4: "e"})
		}),
		"a delete": edited(t, base, func(e *table.Editor) error { return e.Delete(keys[1]) }),
	} {
		if _, err := m.Rebase(ctx, base.Root(), side.Root(), onto.Root(), s); !errors.Is(err, table.ErrChangedSince) {
			t.Errorf("rebasing a row both sides changed (%s) = %v, want ErrChangedSince: two read-modify-writes of one row would both land", name, err)
		}
	}
	added := edited(t, base, func(e *table.Editor) error { _, err := e.Insert(rebaseRow(900, "side")); return err })
	alsoAdded := edited(t, base, func(e *table.Editor) error { _, err := e.Insert(rebaseRow(900, "onto")); return err })
	if _, err := m.Rebase(ctx, base.Root(), added.Root(), alsoAdded.Root(), s); !errors.Is(err, table.ErrChangedSince) {
		t.Errorf("rebasing a row both sides added = %v, want ErrChangedSince", err)
	}
	wider := withColumn(people(), table.Column{Tag: 5, Name: "note", Type: table.TypeText, Nullable: true})
	altered, err := base.WithSchema(ctx, wider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Rebase(ctx, base.Root(), side.Root(), altered.Root(), s); !errors.Is(err, table.ErrSchema) {
		t.Errorf("rebasing onto a table whose schema changed = %v, want ErrSchema: the rows would be written under a schema they were not read under", err)
	}
}
