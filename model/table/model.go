package table

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

// Model is the table model; Config is how it writes the tables it merges.
type Model struct {
	Config prolly.Config
}

var (
	_ model.Model  = Model{}
	_ model.Walker = Model{}
)

// ID implements model.Model.
func (Model) ID() model.ID { return ID }

// FormatVersion implements model.Model.
func (Model) FormatVersion() uint16 { return Format }

// Validate implements model.Model: the root record, the catalog, every
// map with the count the record claims, every row under the schema, and
// every index as full as the primary map.
func (m Model) Validate(ctx context.Context, root model.Root, r chunk.Reader) error {
	t, err := Open(ctx, readOnly{r}, m.Config, root)
	if err != nil {
		return err
	}
	rows, err := t.Scan(ctx)
	if err != nil {
		return err
	}
	var n uint64
	for {
		_, _, ok, err := rows.Next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		n++
	}
	if n != root.Size {
		return fmt.Errorf("%w: %d rows scanned, the root says %d", chunk.ErrCorrupt, n, root.Size)
	}
	for _, ix := range t.schema.Indexes {
		rows, err := t.IndexLookup(ctx, ix.Tag)
		if err != nil {
			return err
		}
		var seen uint64
		for {
			_, _, ok, err := rows.Next() // each entry decodes and names a row that exists
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			seen++
		}
		if seen != n {
			return fmt.Errorf("%w: index %d walks %d rows of %d", chunk.ErrCorrupt, ix.Tag, seen, n)
		}
	}
	return nil
}

// Walk implements model.Walker: the root chunk, then every map, whose
// long values are streams prolly walks too.
func (m Model) Walk(ctx context.Context, root model.Root, r chunk.Reader, visit func(h hash.Hash, leaf bool) (bool, error)) error {
	if root.Format != Format {
		return fmt.Errorf("%w: table format %d", model.ErrUnknownModel, root.Format)
	}
	if root.Depth != 0 {
		return fmt.Errorf("%w: a table root claims stream depth %d", chunk.ErrCorrupt, root.Depth)
	}
	goOn, err := visit(root.Hash, false)
	if err != nil || !goOn {
		return err
	}
	b, err := r.Get(ctx, root.Hash)
	if err != nil {
		return err
	}
	rec, err := decodeRoot(b)
	if err != nil {
		return err
	}
	if err := prolly.Walk(ctx, r, m.Config, hash.Hash(rec.primary.root), visit, nil); err != nil {
		return err
	}
	for _, ix := range rec.indexes {
		if err := prolly.Walk(ctx, r, m.Config, hash.Hash(ix.root), visit, nil); err != nil {
			return err
		}
	}
	return nil
}

// diffIter turns a primary map's changes into the model's: a row added or
// removed is one change; a row modified is one per cell that differs.
type diffIter struct {
	t      *Table
	d      *prolly.DiffIter
	queued []model.Change
}

var kinds = map[prolly.ChangeKind]model.ChangeKind{prolly.Added: model.Added, prolly.Removed: model.Removed, prolly.Modified: model.Modified}

func (d *diffIter) Next(context.Context) (model.Change, bool, error) {
	for len(d.queued) == 0 {
		c, ok, err := d.d.Next()
		if err != nil || !ok {
			return model.Change{}, false, err
		}
		if c.Kind != prolly.Modified {
			d.queued = append(d.queued, model.Change{Kind: kinds[c.Kind], Location: bytes.Clone(c.Key)})
			continue
		}
		_, from, err := d.t.decodeRow(c.Key, c.From)
		if err != nil {
			return model.Change{}, false, err
		}
		_, to, err := d.t.decodeRow(c.Key, c.To)
		if err != nil {
			return model.Change{}, false, err
		}
		for _, col := range d.t.schema.Columns {
			if d.t.isKeyColumn(col.Tag) || sameCell(from[col.Tag], to[col.Tag]) {
				continue
			}
			d.queued = append(d.queued, model.Change{Kind: model.Modified, Location: cellLocation(c.Key, col.Tag)})
		}
	}
	c := d.queued[0]
	d.queued = d.queued[1:]
	return c, true, nil
}

// cellLocation is a cell's address: the encoded key then the tag.
func cellLocation(kb []byte, tag Tag) []byte {
	return binary.BigEndian.AppendUint16(bytes.Clone(kb), uint16(tag))
}

// Locate is the model's address of a row (column 0) or of one cell of it:
// the encoded key, then for a cell the column's tag, big-endian.
func (t *Table) Locate(key Key, column Tag) ([]byte, error) {
	kb, err := t.encodeKey(key)
	if err != nil {
		return nil, err
	}
	if column == 0 {
		return kb, nil
	}
	if _, _, ok := t.schema.column(column); !ok {
		return nil, fmt.Errorf("%w: the schema has no column %d", ErrValue, column)
	}
	return cellLocation(kb, column), nil
}

// ParseLocation reads what Locate wrote: the key, and the column (0 for the
// row itself).
func (t *Table) ParseLocation(loc []byte) (Key, Tag, error) {
	if key, err := t.decodeKey(loc); err == nil {
		return key, 0, nil
	}
	if len(loc) < 2 {
		return nil, 0, fmt.Errorf("%w: a location of %d bytes", chunk.ErrCorrupt, len(loc))
	}
	key, err := t.decodeKey(loc[:len(loc)-2])
	if err != nil {
		return nil, 0, err
	}
	tag := Tag(binary.BigEndian.Uint16(loc[len(loc)-2:]))
	if _, _, ok := t.schema.column(tag); !ok {
		return nil, 0, fmt.Errorf("%w: a location naming column %d, which the schema lacks", chunk.ErrCorrupt, tag)
	}
	return key, tag, nil
}

// Diff implements model.Model: a change per row added or removed, located
// by its key, and per cell changed, located by the key then the column tag.
func (m Model) Diff(ctx context.Context, from, to model.Root, r chunk.Reader) (model.DiffIter, error) {
	ft, err := Open(ctx, readOnly{r}, m.Config, from)
	if err != nil {
		return nil, err
	}
	tt, err := Open(ctx, readOnly{r}, m.Config, to)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(ft.catalog, tt.catalog) {
		return nil, fmt.Errorf("%w: diffing tables of two schemas", ErrSchema)
	}
	d, err := prolly.Diff(ctx, ft.primary, tt.primary)
	if err != nil {
		return nil, err
	}
	return &diffIter{t: tt, d: d}, nil
}

// mergeCell is the three-way rule for one cell, the merge library's Scalar
// over the cell's encoding under its column: the side that changed wins over
// one that did not, the same change on both is that change, and two
// different changes are a conflict. A value that does not encode under the
// column's type is an error, never a silent choice; NULL is a value here.
func mergeCell(c Column, base, ours, theirs any) (merged any, conflict bool, err error) {
	c.Nullable = true // a row added on one side has no base cell: NULL, compared as such
	var enc [3]string
	for i, v := range []any{base, ours, theirs} {
		b, err := encodeCell(nil, c, v)
		if err != nil {
			return nil, false, err
		}
		enc[i] = string(b)
	}
	r := merge.Scalar(enc[0], enc[1], enc[2])
	switch {
	case !r.Clean():
		return nil, true, nil
	case r.Value == enc[1]:
		return ours, false, nil
	}
	return theirs, false, nil
}

// Merge implements model.Model: the schemas merge by tag (mergeSchemas),
// ours is rewritten under the merged schema, and the rows merge
// independently, cells by mergeCell, theirs' rows carried across the
// schema change. A column one side dropped and the other wrote to is a
// conflict at the column; a row that does not fit the merged schema is a
// conflict at the row. With any conflict the result is ours, untouched.
func (m Model) Merge(ctx context.Context, base, ours, theirs model.Root, rw chunk.ReadWriter) (model.MergeResult, error) {
	var ts [3]*Table
	for i, r := range []model.Root{base, ours, theirs} {
		var err error
		if ts[i], err = Open(ctx, rw, m.Config, r); err != nil {
			return model.MergeResult{}, err
		}
	}
	schema, conflicts := mergeSchemas(ts[0].schema, ts[1].schema, ts[2].schema, ts[0].catalog, ts[1].catalog, ts[2].catalog)
	if len(conflicts) > 0 {
		return model.MergeResult{Root: ours, Conflicts: conflicts}, nil
	}
	mt := ts[1]
	if catalog, err := EncodeCatalog(schema); err != nil {
		return model.MergeResult{}, err
	} else if !bytes.Equal(catalog, ts[1].catalog) {
		if err := ts[1].alterable(schema); err != nil {
			return model.MergeResult{Root: ours, Conflicts: []model.Conflict{{Location: []byte(schemaLocation), Reason: "ours does not fit the merged schema: " + err.Error()}}}, nil
		}
		if err := ts[2].alterable(schema); err != nil {
			return model.MergeResult{Root: ours, Conflicts: []model.Conflict{{Location: []byte(schemaLocation), Reason: "theirs does not fit the merged schema: " + err.Error()}}}, nil
		}
		if mt, err = ts[1].WithSchema(ctx, schema); err != nil {
			if errors.Is(err, ErrValue) {
				return model.MergeResult{Root: ours, Conflicts: []model.Conflict{{Location: []byte(schemaLocation), Reason: "ours' rows do not fit the merged schema: " + err.Error()}}}, nil
			}
			return model.MergeResult{}, err
		}
	}
	mg := &merger{ts: ts, mt: mt, ed: mt.Edit(), droppedByOurs: droppedBy(ts[0].schema, ts[1].schema), droppedByTheirs: droppedBy(ts[0].schema, ts[2].schema), seen: map[string]bool{}}
	dOurs, err := prolly.Diff(ctx, ts[0].primary, ts[1].primary)
	if err != nil {
		return model.MergeResult{}, err
	}
	dTheirs, err := prolly.Diff(ctx, ts[0].primary, ts[2].primary)
	if err != nil {
		return model.MergeResult{}, err
	}
	co, okO, err := dOurs.Next()
	if err != nil {
		return model.MergeResult{}, err
	}
	ct, okT, err := dTheirs.Next()
	if err != nil {
		return model.MergeResult{}, err
	}
	for (okO || okT) && err == nil {
		switch {
		case !okT || (okO && bytes.Compare(co.Key, ct.Key) < 0): // only ours changed it: already in mt
			if err = mg.wroteDropped(ts[1], co, mg.droppedByTheirs); err == nil {
				co, okO, err = dOurs.Next()
			}
		case !okO || bytes.Compare(ct.Key, co.Key) < 0: // only theirs changed it
			if err = mg.wroteDropped(ts[2], ct, mg.droppedByOurs); err == nil {
				if err = mg.apply(ct); err == nil {
					ct, okT, err = dTheirs.Next()
				}
			}
		default: // both changed it
			if err = mg.wroteDropped(ts[1], co, mg.droppedByTheirs); err == nil {
				if err = mg.wroteDropped(ts[2], ct, mg.droppedByOurs); err == nil {
					if err = mg.reconcile(co, ct); err == nil {
						if co, okO, err = dOurs.Next(); err == nil {
							ct, okT, err = dTheirs.Next()
						}
					}
				}
			}
		}
	}
	if err != nil {
		return model.MergeResult{}, err
	}
	if len(mg.conflicts) > 0 {
		return model.MergeResult{Root: ours, Conflicts: mg.conflicts}, nil
	}
	merged, err := mg.ed.Flush(ctx)
	if err != nil {
		return model.MergeResult{}, err
	}
	return model.MergeResult{Root: merged.Root()}, nil
}

// merger is one merge's state: the three tables, the merged table under
// edit, the columns each side dropped, and the conflicts so far.
type merger struct {
	ts                             [3]*Table
	mt                             *Table
	ed                             *Editor
	droppedByOurs, droppedByTheirs []Tag
	conflicts                      []model.Conflict
	seen                           map[string]bool // conflict locations reported once
}

func (mg *merger) conflict(loc []byte, reason string) {
	if mg.seen[string(loc)] {
		return
	}
	mg.seen[string(loc)] = true
	mg.conflicts = append(mg.conflicts, model.Conflict{Location: loc, Reason: reason})
}

// wroteDropped reports a conflict at each column the other side dropped
// that this side's change wrote to.
func (mg *merger) wroteDropped(side *Table, c prolly.Change, dropped []Tag) error {
	if len(dropped) == 0 || c.Kind == prolly.Removed {
		return nil
	}
	_, to, err := side.decodeRow(c.Key, c.To)
	if err != nil {
		return err
	}
	var from Row
	if c.Kind == prolly.Modified {
		if _, from, err = mg.ts[0].decodeRow(c.Key, c.From); err != nil {
			return err
		}
	}
	for _, tag := range dropped {
		if !sameCell(from[tag], to[tag]) {
			mg.conflict(columnLocation(tag), "dropped on one side and written to on the other")
		}
	}
	return nil
}

// apply makes theirs' change to a row in the merged table, carried across
// the schema change; a row that does not fit is a conflict at the row.
func (mg *merger) apply(c prolly.Change) error {
	if c.Kind == prolly.Removed {
		return mg.ed.remove(c.Key)
	}
	key, row, err := mg.ts[2].decodeRow(c.Key, c.To)
	if err != nil {
		return err
	}
	return mg.putConverted(c.Key, key, row, mg.ts[2])
}

// putConverted stores row, decoded under from's schema, into the merged
// table; what does not fit the merged schema is a conflict at the row.
func (mg *merger) putConverted(kb []byte, key Key, row Row, from *Table) error {
	conv, err := from.convertRow(row, mg.mt.schema)
	if err == nil {
		err = mg.ed.put(kb, key, conv)
	}
	if errors.Is(err, ErrValue) || errors.Is(err, ErrSchema) {
		mg.conflict(bytes.Clone(kb), "the row does not fit the merged schema: "+err.Error())
		return nil
	}
	return err
}

// reconcile merges both sides' changes to one row: both removed is nothing;
// one removed is a conflict at the row; otherwise every cell of the merged
// schema by mergeCell, each side's cell carried across its schema change.
func (mg *merger) reconcile(co, ct prolly.Change) error {
	switch {
	case co.Kind == prolly.Removed && ct.Kind == prolly.Removed:
		return nil
	case co.Kind == prolly.Removed || ct.Kind == prolly.Removed:
		mg.conflict(bytes.Clone(co.Key), "deleted on one side and changed on the other")
		return nil
	}
	rows := [3]Row{}
	var key Key
	for i, c := range []prolly.Change{co, co, ct} {
		vb := c.To
		if i == 0 {
			if co.Kind != prolly.Modified {
				continue // added on both sides: no base row
			}
			vb = co.From
		}
		k, row, err := mg.ts[i].decodeRow(c.Key, vb)
		if err != nil {
			return err
		}
		if rows[i], err = mg.ts[i].convertRow(row, mg.mt.schema); err != nil {
			mg.conflict(bytes.Clone(co.Key), "the row does not fit the merged schema: "+err.Error())
			return nil
		}
		key = k
	}
	merged := Row{}
	before := len(mg.conflicts)
	for _, col := range mg.mt.schema.Columns {
		if mg.mt.isKeyColumn(col.Tag) {
			merged[col.Tag] = rows[1][col.Tag]
			continue
		}
		v, conflict, err := mergeCell(col, rows[0][col.Tag], rows[1][col.Tag], rows[2][col.Tag])
		if err != nil {
			mg.conflict(cellLocation(co.Key, col.Tag), "the cell does not fit the merged schema: "+err.Error())
			continue
		}
		if conflict {
			reason := "changed differently on both sides"
			if co.Kind == prolly.Added {
				reason = "added differently on both sides"
			}
			mg.conflict(cellLocation(co.Key, col.Tag), reason)
			continue
		}
		if v != nil {
			merged[col.Tag] = v
		}
	}
	if len(mg.conflicts) > before {
		return nil
	}
	return mg.putConverted(co.Key, key, merged, mg.mt)
}
