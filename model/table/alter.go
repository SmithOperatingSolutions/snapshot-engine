package table

import (
	"context"
	"fmt"
	"slices"
)

// convertible says whether a column's values can follow a change of type:
// the same type, an integer widened, a float widened, a varchar turned text.
// Lengths are the values' business (encodeCell checks them).
func convertible(from, to Column) error {
	switch {
	case from.Type == to.Type:
		return nil
	case from.Type == TypeInt2 && (to.Type == TypeInt4 || to.Type == TypeInt8):
		return nil
	case from.Type == TypeInt4 && to.Type == TypeInt8:
		return nil
	case from.Type == TypeFloat4 && to.Type == TypeFloat8:
		return nil
	case from.Type == TypeVarchar && to.Type == TypeText:
		return nil
	}
	return fmt.Errorf("%w: column %q cannot change from %s to %s", ErrSchema, from.Name, from.Type, to.Type)
}

// convertCell carries a value from column from to column to.
func convertCell(v any, from, to Column) (any, error) {
	if err := convertible(from, to); err != nil {
		return nil, err
	}
	if v == nil || from.Type == to.Type {
		return v, nil
	}
	switch x := v.(type) {
	case int16:
		if to.Type == TypeInt4 {
			return int32(x), nil
		}
		return int64(x), nil
	case int32:
		return int64(x), nil
	case float32:
		return float64(x), nil
	}
	return v, nil // varchar to text: the same string
}

// convertRow returns row under next, columns matched by tag: a column next
// lacks is dropped, one t lacks is NULL.
func (t *Table) convertRow(row Row, next Schema) (Row, error) {
	out := make(Row, len(row))
	for _, c := range next.Columns {
		v, ok := row[c.Tag]
		if !ok {
			continue
		}
		from, _, had := t.schema.column(c.Tag)
		if !had {
			continue
		}
		cv, err := convertCell(v, from, c)
		if err != nil {
			return nil, err
		}
		if cv != nil {
			out[c.Tag] = cv
		}
	}
	return out, nil
}

// alterable says whether next can be this table's schema: it validates, the
// primary key is the same columns with the same types, every column added
// is nullable, and every column kept can take its values.
func (t *Table) alterable(next Schema) error {
	if err := next.Validate(); err != nil {
		return err
	}
	if !slices.Equal(next.PrimaryKey, t.schema.PrimaryKey) {
		return fmt.Errorf("%w: the primary key changed from %v to %v", ErrSchema, t.schema.PrimaryKey, next.PrimaryKey)
	}
	for _, c := range next.Columns {
		from, _, had := t.schema.column(c.Tag)
		if !had {
			if !c.Nullable {
				return fmt.Errorf("%w: column %q is added NOT NULL to a table whose rows have no value for it", ErrSchema, c.Name)
			}
			continue
		}
		if t.isKeyColumn(c.Tag) && (from.Type != c.Type || from.MaxLen != c.MaxLen) {
			return fmt.Errorf("%w: key column %q changed type", ErrSchema, from.Name)
		}
		if err := convertible(from, c); err != nil {
			return err
		}
	}
	return nil
}

// WithSchema rewrites the table under a new schema, matching columns by tag
// (a rename costs the catalog alone), and returns it; the table itself is
// unchanged. A column added is NULL in every row and must be nullable; a
// column dropped takes its cells; an index added is built; a type may widen
// (int2 to int4 to int8, float4 to float8, varchar to a longer varchar or to
// text) and the values follow. A value that does not fit the new schema, a
// type that cannot take the values, and any change to the primary key are
// refused.
func (t *Table) WithSchema(ctx context.Context, next Schema) (*Table, error) {
	if err := t.alterable(next); err != nil {
		return nil, err
	}
	out, err := empty(ctx, t.store, t.cfg, next)
	if err != nil {
		return nil, err
	}
	e := out.Edit()
	rows, err := t.Scan(ctx)
	if err != nil {
		return nil, err
	}
	for {
		key, row, ok, err := rows.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		conv, err := t.convertRow(row, next)
		if err != nil {
			return nil, err
		}
		kb, err := out.encodeKey(key)
		if err != nil {
			return nil, err
		}
		if err := e.put(kb, key, conv); err != nil {
			return nil, err
		}
	}
	return e.Flush(ctx)
}
