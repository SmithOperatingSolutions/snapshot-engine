package table

import (
	"bytes"
	"fmt"
	"slices"
	"sort"

	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

// schemaLocation is the address of a conflict about the schema as a whole;
// a column's is schema/<tag>, an index's schema/index/<tag>.
const schemaLocation = "schema"

func columnLocation(tag Tag) []byte { return []byte(fmt.Sprintf("%s/%d", schemaLocation, tag)) }
func indexLocation(tag Tag) []byte  { return []byte(fmt.Sprintf("%s/index/%d", schemaLocation, tag)) }

// mergeSchemas merges two schemas against their base, by tag. A side that
// did not change the schema yields to the other; otherwise the base's
// columns survive in their order, each attribute merged three-way, a
// column dropped on one side goes unless the other side changed it, a
// column added on one side is appended (the additions by tag), and indexes
// follow the same rules. The primary key may not change. The conflicts are
// at the tag that could not be combined; with any, the schema is nothing.
func mergeSchemas(base, ours, theirs Schema, baseCat, oursCat, theirsCat []byte) (Schema, []model.Conflict) {
	switch {
	case bytes.Equal(oursCat, theirsCat):
		return ours, nil
	case bytes.Equal(oursCat, baseCat):
		return theirs, nil
	case bytes.Equal(theirsCat, baseCat):
		return ours, nil
	}
	if !slices.Equal(ours.PrimaryKey, base.PrimaryKey) || !slices.Equal(theirs.PrimaryKey, base.PrimaryKey) {
		return Schema{}, []model.Conflict{{Location: []byte(schemaLocation), Reason: "the primary key changed; rows cannot be matched across it"}}
	}
	merged := Schema{PrimaryKey: append([]Tag(nil), base.PrimaryKey...)}
	var conflicts []model.Conflict
	for _, b := range base.Columns {
		o, _, inO := ours.column(b.Tag)
		t, _, inT := theirs.column(b.Tag)
		switch {
		case !inO && !inT:
		case !inO || !inT:
			kept := o
			if !inO {
				kept = t
			}
			if kept != b {
				conflicts = append(conflicts, model.Conflict{Location: columnLocation(b.Tag), Reason: "dropped on one side and changed on the other"})
			}
		default:
			c, reason := mergeColumn(b, o, t)
			if reason != "" {
				conflicts = append(conflicts, model.Conflict{Location: columnLocation(b.Tag), Reason: reason})
				continue
			}
			merged.Columns = append(merged.Columns, c)
		}
	}
	added := map[Tag]Column{}
	for _, side := range [2]Schema{ours, theirs} {
		for _, c := range side.Columns {
			if _, _, inBase := base.column(c.Tag); inBase {
				continue
			}
			if prev, twice := added[c.Tag]; twice && prev != c {
				conflicts = append(conflicts, model.Conflict{Location: columnLocation(c.Tag), Reason: "added differently on both sides"})
				continue
			}
			added[c.Tag] = c
		}
	}
	merged.Columns = append(merged.Columns, sortedByTag(added)...)
	present := map[Tag]bool{}
	for _, c := range merged.Columns {
		present[c.Tag] = true
	}
	merged.Indexes, conflicts = mergeIndexes(base, ours, theirs, present, conflicts)
	if len(conflicts) > 0 {
		return Schema{}, conflicts
	}
	if err := merged.Validate(); err != nil {
		return Schema{}, []model.Conflict{{Location: []byte(schemaLocation), Reason: "the merged schema is not a table's: " + err.Error()}}
	}
	return merged, nil
}

// mergeColumn merges one column's attributes three-way; reason names the
// first attribute both sides changed differently.
func mergeColumn(b, o, t Column) (Column, string) {
	c := Column{Tag: b.Tag}
	name := merge.Scalar(b.Name, o.Name, t.Name)
	typ := merge.Scalar(b.Type, o.Type, t.Type)
	null := merge.Scalar(b.Nullable, o.Nullable, t.Nullable)
	length := merge.Scalar(b.MaxLen, o.MaxLen, t.MaxLen)
	for attr, clean := range map[string]bool{"name": name.Clean(), "type": typ.Clean(), "nullability": null.Clean(), "length": length.Clean()} {
		if !clean {
			return Column{}, "the " + attr + " changed differently on both sides"
		}
	}
	c.Name, c.Type, c.Nullable, c.MaxLen = name.Value, typ.Value, null.Value, length.Value
	return c, ""
}

// mergeIndexes merges the indexes by tag: the base's survive in order unless
// dropped on one side and unchanged on the other, or their columns are gone;
// additions are appended by tag.
func mergeIndexes(base, ours, theirs Schema, present map[Tag]bool, conflicts []model.Conflict) ([]Index, []model.Conflict) {
	find := func(s Schema, tag Tag) (Index, bool) {
		for _, ix := range s.Indexes {
			if ix.Tag == tag {
				return ix, true
			}
		}
		return Index{}, false
	}
	same := func(a, b Index) bool { return slices.Equal(a.Columns, b.Columns) }
	covers := func(ix Index) bool {
		for _, tag := range ix.Columns {
			if !present[tag] {
				return false
			}
		}
		return true
	}
	var out []Index
	for _, b := range base.Indexes {
		o, inO := find(ours, b.Tag)
		t, inT := find(theirs, b.Tag)
		switch {
		case !inO && !inT:
		case !inO || !inT:
			kept := o
			if !inO {
				kept = t
			}
			if !same(kept, b) {
				conflicts = append(conflicts, model.Conflict{Location: indexLocation(b.Tag), Reason: "dropped on one side and changed on the other"})
			}
		case same(o, t) || same(t, b):
			if covers(o) {
				out = append(out, o)
			}
		case same(o, b):
			if covers(t) {
				out = append(out, t)
			}
		default:
			conflicts = append(conflicts, model.Conflict{Location: indexLocation(b.Tag), Reason: "changed differently on both sides"})
		}
	}
	added := map[Tag]Index{}
	for _, side := range [2]Schema{ours, theirs} {
		for _, ix := range side.Indexes {
			if _, inBase := find(base, ix.Tag); inBase {
				continue
			}
			if prev, twice := added[ix.Tag]; twice && !same(prev, ix) {
				conflicts = append(conflicts, model.Conflict{Location: indexLocation(ix.Tag), Reason: "added differently on both sides"})
				continue
			}
			added[ix.Tag] = ix
		}
	}
	tags := make([]int, 0, len(added))
	for tag := range added {
		tags = append(tags, int(tag))
	}
	sort.Ints(tags)
	for _, tag := range tags {
		if ix := added[Tag(tag)]; covers(ix) {
			out = append(out, ix)
		}
	}
	return out, conflicts
}

func sortedByTag(cols map[Tag]Column) []Column {
	out := make([]Column, 0, len(cols))
	for _, c := range cols {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return out
}

// droppedBy lists the base's column tags a side's schema lacks.
func droppedBy(base, side Schema) []Tag {
	var out []Tag
	for _, c := range base.Columns {
		if _, _, ok := side.column(c.Tag); !ok {
			out = append(out, c.Tag)
		}
	}
	return out
}
