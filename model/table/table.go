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
	return fmt.Errorf("%w: not implemented", ErrSchema)
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
	return nil, fmt.Errorf("%w: not implemented", ErrSchema)
}

// DecodeCatalog parses a record (chunk.ErrCorrupt for anything else) into a
// schema that validates.
func DecodeCatalog(b []byte) (Schema, error) {
	return Schema{}, fmt.Errorf("%w: not implemented", chunk.ErrCorrupt)
}
