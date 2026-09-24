// Package table is the table model (model id 3; Engine Spec L4): rows with a
// schema, stored as one prolly map from the primary key to the row's other
// columns and one prolly map per secondary index, under a root record that
// names the catalog and the maps. Keys are encoded so that byte order is the
// column type's SQL order, NULLs first. Every value is validated against its
// column on write and refused, never coerced, when it does not fit.
package table

import (
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
)

// ID is the table model's id (the Storage Core Spec's registry table).
const ID model.ID = 3

// Format is the format version this package writes.
const Format = 1

// Limits on a catalog and its names.
const (
	MaxColumns    = 1024
	MaxNameLen    = 128
	MaxIndexes    = 64
	MaxVarcharLen = 1 << 20
)

// Tag names a column or an index for its whole life: renames and reorders
// keep the tag, so two branches' changes to one column meet by tag.
type Tag uint16

// Type is a column's type: the v1 types of the Engine Spec.
type Type uint8

// The v1 types.
const (
	TypeBool Type = iota + 1
	TypeInt2
	TypeInt4
	TypeInt8
	TypeFloat4
	TypeFloat8
	TypeNumeric
	TypeText
	TypeVarchar
	TypeBytea
	TypeDate
	TypeTimestamp
	TypeTimestampTZ
	TypeUUID
	TypeJSONB
	typeCount
)

var typeNames = [...]string{"", "bool", "int2", "int4", "int8", "float4", "float8", "numeric", "text", "varchar", "bytea", "date", "timestamp", "timestamptz", "uuid", "jsonb"}

// String is the type's SQL name.
func (t Type) String() string {
	if t == 0 || t >= typeCount {
		return fmt.Sprintf("type(%d)", uint8(t))
	}
	return typeNames[t]
}

// Column is one column of a schema.
type Column struct {
	Tag      Tag
	Name     string
	Type     Type
	Nullable bool
	MaxLen   uint32 // Varchar: the most characters a value may have
}

// Index is a secondary index over columns, in that order.
type Index struct {
	Tag     Tag
	Columns []Tag
}

// Schema is a table's catalog: its columns in order, the primary key's
// columns in order (none: rows get a hidden 16-byte row id), and its indexes.
type Schema struct {
	Columns    []Column
	PrimaryKey []Tag
	Indexes    []Index
}

// ErrSchema is a schema that cannot be a table's.
var ErrSchema = errors.New("table: invalid schema")

// Validate checks the schema: every column has a nonzero tag and a name,
// both unique; every type is a v1 type; a varchar has a length; the primary
// key's columns exist and are not nullable; every index has a unique tag and
// names existing columns, none twice.
func (s Schema) Validate() error {
	if len(s.Columns) == 0 {
		return fmt.Errorf("%w: no columns", ErrSchema)
	}
	if len(s.Columns) > MaxColumns {
		return fmt.Errorf("%w: %d columns, over %d", ErrSchema, len(s.Columns), MaxColumns)
	}
	tags := make(map[Tag]bool, len(s.Columns))
	names := make(map[string]bool, len(s.Columns))
	for _, c := range s.Columns {
		switch {
		case c.Tag == 0:
			return fmt.Errorf("%w: column %q has tag 0", ErrSchema, c.Name)
		case tags[c.Tag]:
			return fmt.Errorf("%w: two columns have tag %d", ErrSchema, c.Tag)
		case c.Name == "" || len(c.Name) > MaxNameLen:
			return fmt.Errorf("%w: column %d has a name of %d bytes, want 1..%d", ErrSchema, c.Tag, len(c.Name), MaxNameLen)
		case names[c.Name]:
			return fmt.Errorf("%w: two columns are named %q", ErrSchema, c.Name)
		case c.Type == 0 || c.Type >= typeCount:
			return fmt.Errorf("%w: column %q has type %d", ErrSchema, c.Name, uint8(c.Type))
		case c.Type == TypeVarchar && (c.MaxLen == 0 || c.MaxLen > MaxVarcharLen):
			return fmt.Errorf("%w: varchar column %q has length %d, want 1..%d", ErrSchema, c.Name, c.MaxLen, MaxVarcharLen)
		case c.Type != TypeVarchar && c.MaxLen != 0:
			return fmt.Errorf("%w: %s column %q has a length", ErrSchema, c.Type, c.Name)
		}
		tags[c.Tag] = true
		names[c.Name] = true
	}
	seen := map[Tag]bool{}
	for _, t := range s.PrimaryKey {
		c, _, ok := s.column(t)
		switch {
		case !ok:
			return fmt.Errorf("%w: the primary key names column %d, which does not exist", ErrSchema, t)
		case c.Nullable:
			return fmt.Errorf("%w: primary key column %q is nullable", ErrSchema, c.Name)
		case seen[t]:
			return fmt.Errorf("%w: the primary key names column %q twice", ErrSchema, c.Name)
		}
		seen[t] = true
	}
	if len(s.Indexes) > MaxIndexes {
		return fmt.Errorf("%w: %d indexes, over %d", ErrSchema, len(s.Indexes), MaxIndexes)
	}
	itags := map[Tag]bool{}
	for _, ix := range s.Indexes {
		switch {
		case ix.Tag == 0:
			return fmt.Errorf("%w: an index has tag 0", ErrSchema)
		case itags[ix.Tag]:
			return fmt.Errorf("%w: two indexes have tag %d", ErrSchema, ix.Tag)
		case len(ix.Columns) == 0:
			return fmt.Errorf("%w: index %d has no columns", ErrSchema, ix.Tag)
		}
		itags[ix.Tag] = true
		cols := map[Tag]bool{}
		for _, t := range ix.Columns {
			if _, _, ok := s.column(t); !ok {
				return fmt.Errorf("%w: index %d names column %d, which does not exist", ErrSchema, ix.Tag, t)
			}
			if cols[t] {
				return fmt.Errorf("%w: index %d names column %d twice", ErrSchema, ix.Tag, t)
			}
			cols[t] = true
		}
	}
	return nil
}

// column returns the column with tag t.
func (s Schema) column(t Tag) (Column, int, bool) {
	for i, c := range s.Columns {
		if c.Tag == t {
			return c, i, true
		}
	}
	return Column{}, -1, false
}

// catalogMagic starts every catalog record.
const catalogMagic = "VDTC"

// EncodeCatalog returns the schema's record: magic "VDTC" · version u16 ·
// column count uvarint · per column (tag u16 · name (uvarint length, bytes) ·
// type u8 · nullable u8 · max length u32) · primary key count uvarint · tags
// u16 · index count uvarint · per index (tag u16 · column count uvarint ·
// tags u16), little-endian. It refuses a schema that does not validate.
func EncodeCatalog(s Schema) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	var w writer
	w.raw([]byte(catalogMagic))
	w.u16(Format)
	w.uvarint(uint64(len(s.Columns)))
	for _, c := range s.Columns {
		w.u16(uint16(c.Tag))
		w.bytes([]byte(c.Name))
		w.u8(uint8(c.Type))
		if c.Nullable {
			w.u8(1)
		} else {
			w.u8(0)
		}
		w.u32(c.MaxLen)
	}
	w.uvarint(uint64(len(s.PrimaryKey)))
	for _, t := range s.PrimaryKey {
		w.u16(uint16(t))
	}
	w.uvarint(uint64(len(s.Indexes)))
	for _, ix := range s.Indexes {
		w.u16(uint16(ix.Tag))
		w.uvarint(uint64(len(ix.Columns)))
		for _, t := range ix.Columns {
			w.u16(uint16(t))
		}
	}
	return w.b, nil
}

// DecodeCatalog parses a record (chunk.ErrCorrupt for anything else) into a
// schema that validates.
func DecodeCatalog(b []byte) (Schema, error) {
	r := &reader{b: b}
	if magic := r.take(len(catalogMagic)); r.err == nil && string(magic) != catalogMagic {
		r.fail("not a catalog record")
	}
	if v := r.u16(); r.err == nil && v != Format {
		r.fail("catalog format %d, this package reads %d", v, Format)
	}
	var s Schema
	n := r.uvarint(MaxColumns)
	for i := uint64(0); i < n && r.err == nil; i++ {
		c := Column{Tag: Tag(r.u16())}
		c.Name = string(r.bytes(MaxNameLen))
		c.Type = Type(r.u8())
		switch nullable := r.u8(); nullable {
		case 0:
		case 1:
			c.Nullable = true
		default:
			r.fail("nullable byte %d", nullable)
		}
		c.MaxLen = r.u32()
		s.Columns = append(s.Columns, c)
	}
	n = r.uvarint(MaxColumns)
	for i := uint64(0); i < n && r.err == nil; i++ {
		s.PrimaryKey = append(s.PrimaryKey, Tag(r.u16()))
	}
	n = r.uvarint(MaxIndexes)
	for i := uint64(0); i < n && r.err == nil; i++ {
		ix := Index{Tag: Tag(r.u16())}
		m := r.uvarint(MaxColumns)
		for j := uint64(0); j < m && r.err == nil; j++ {
			ix.Columns = append(ix.Columns, Tag(r.u16()))
		}
		s.Indexes = append(s.Indexes, ix)
	}
	if err := r.done(); err != nil {
		return Schema{}, err
	}
	if err := s.Validate(); err != nil {
		return Schema{}, fmt.Errorf("%w: %w", chunk.ErrCorrupt, err)
	}
	return s, nil
}
