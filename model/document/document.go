package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"github.com/SmithOperatingSolutions/snapshot-core/core/wire"
	"github.com/SmithOperatingSolutions/snapshot-core/model/mapobject"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

// ID is the document model's id: the Storage Core Spec's JSON document
// (docs/DESIGN.md D3).
const ID model.ID = 4

// MaxIDSize is the longest record id, the prolly map's own key limit; an id
// is never empty.
const MaxIDSize = prolly.MaxKeySize

// ErrID is a record id that is empty or over MaxIDSize.
var ErrID = errors.New("document: a record id must be 1 to 4096 bytes")

// Model is the document model; Config is how it writes the maps it merges.
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

// config is c for a collection's map: no record is longer than MaxRecord,
// so no longer value is read, a stream's claimed length being able to pass
// what it stores many times over (snapshot-core#23).
func config(c prolly.Config) prolly.Config {
	c.MaxValue = MaxRecord
	return c
}

// spec is the collection as a map-shaped model: a map from record id to
// record frame under a configuration, its records checked as ids of ours
// holding frames of ours.
func spec(c prolly.Config) mapobject.Spec {
	return mapobject.Spec{Name: "document", Format: Format, Config: config(c), Check: func(id, frame []byte) error {
		_, err := checked(id, frame)
		return err
	}}
}

// checkID refuses an empty id and one over MaxIDSize.
func checkID(id []byte) error {
	if len(id) == 0 || len(id) > MaxIDSize {
		return fmt.Errorf("%w: %d bytes", ErrID, len(id))
	}
	return nil
}

// checked decodes a stored record and its id.
func checked(id, frame []byte) (merge.Node, error) {
	if err := checkID(id); err != nil {
		return merge.Node{}, fmt.Errorf("%w: a stored id: %w", chunk.ErrCorrupt, err)
	}
	return DecodeRecord(frame)
}

// Write stores records, by id, as an object: every record is checked
// before anything is stored, then a new collection holds them all.
func Write(ctx context.Context, s chunk.ReadWriter, c prolly.Config, records map[string]merge.Node) (model.Root, error) {
	frames := make(map[string][]byte, len(records))
	for id, n := range records {
		if err := checkID([]byte(id)); err != nil {
			return model.Root{}, err
		}
		f, err := EncodeRecord(n)
		if err != nil {
			return model.Root{}, fmt.Errorf("record %q: %w", id, err)
		}
		frames[id] = f
	}
	col, err := Empty(ctx, s, c)
	if err != nil {
		return model.Root{}, err
	}
	e := col.Edit()
	for id, f := range frames { // checked above: put as encoded
		if err := e.ed.Put([]byte(id), f); err != nil {
			return model.Root{}, err
		}
	}
	if col, err = e.Flush(ctx); err != nil {
		return model.Root{}, err
	}
	return col.Root(), nil
}

// Read returns an object's records by id.
func Read(ctx context.Context, r chunk.Reader, c prolly.Config, root model.Root) (map[string]merge.Node, error) {
	col, err := Open(ctx, mapobject.ReadOnly(r), c, root)
	if err != nil {
		return nil, err
	}
	it, err := col.Scan(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]merge.Node{} // grown as records are read, never sized from the root's claim (snapshot-engine#5)
	for {
		id, n, ok, err := it.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		out[string(id)] = n
	}
}

// Validate implements model.Model: the map opens under the root's claims
// and every entry is an id of ours holding a record of ours.
func (m Model) Validate(ctx context.Context, root model.Root, r chunk.Reader) error {
	return spec(m.Config).Validate(ctx, root, r)
}

// Walk implements model.Walker: the object is its map, every record inline.
func (m Model) Walk(ctx context.Context, root model.Root, r chunk.Reader, visit func(h hash.Hash, leaf bool) (bool, error)) error {
	return spec(m.Config).Walk(ctx, root, r, visit, func(id, frame []byte) error {
		_, err := checked(id, frame)
		return err
	})
}

// Diff implements model.Model: a change per record, located by its id.
func (m Model) Diff(ctx context.Context, from, to model.Root, r chunk.Reader) (model.DiffIter, error) {
	return spec(m.Config).Diff(ctx, from, to, r)
}

// Merge implements model.Model, per record: what only one side changed
// lands; a record both sides changed is merged by field path through the
// merge library, so two writers on different fields both land and one
// field changed two ways is a conflict at the record, its reason naming
// the field; a record deleted on one side and changed on the other, or
// added differently on both, is a conflict (mapobject.Disagreement).
func (m Model) Merge(ctx context.Context, base, ours, theirs model.Root, rw chunk.ReadWriter) (model.MergeResult, error) {
	return spec(m.Config).MergeWith(ctx, base, ours, theirs, rw, decide)
}

// decide decides a record both sides changed from base: a Tree merge of
// the three records when both sides changed it in place, each field
// conflict located at the record and the field; the default rule otherwise
// (equal, both deleted, deleted against changed, added twice), a conflict
// at the record as a whole. A record that does not decode aborts the merge.
func decide(id []byte, ours, theirs prolly.Change) (mapobject.Decision, error) {
	value, put, reason := mapobject.Disagreement(id, ours, theirs)
	if reason == "" {
		return mapobject.Decision{Value: value, Put: put}, nil
	}
	if ours.Kind != prolly.Modified || theirs.Kind != prolly.Modified {
		return mapobject.Decision{Conflicts: []model.Conflict{{Location: Locate(id, nil), Reason: reason}}}, nil
	}
	var records [3]merge.Node
	for i, frame := range [][]byte{ours.From, ours.To, theirs.To} {
		var err error
		if records[i], err = DecodeRecord(frame); err != nil {
			return mapobject.Decision{}, fmt.Errorf("record %q: %w", id, err)
		}
	}
	r := merge.Tree(records[0], records[1], records[2], merge.TreeOptions{})
	if len(r.Conflicts) > 0 {
		cs := make([]model.Conflict, 0, len(r.Conflicts))
		for _, c := range r.Conflicts {
			cs = append(cs, model.Conflict{Location: Locate(id, c.Path), Reason: c.Reason})
		}
		return mapobject.Decision{Conflicts: cs}, nil
	}
	frame, err := EncodeRecord(r.Value)
	if err != nil {
		return mapobject.Decision{}, fmt.Errorf("record %q merged to what is not a document: %w", id, err)
	}
	return mapobject.Decision{Value: frame, Put: true}, nil
}

// Locate is the location of a conflict in a collection: the record's id
// and, for a conflict inside the record, the path of the field, empty for
// the record as a whole. Each part is length-prefixed (a uvarint), so an
// id may hold any bytes and a field name any text. A change's location
// (Diff) is the bare id: a change is a record.
func Locate(id []byte, path merge.Path) []byte {
	var w wire.Writer
	w.LenBytes(id)
	for _, seg := range path {
		w.LenBytes([]byte(seg))
	}
	return w.Bytes()
}

// ParseLocation is Locate's inverse; it refuses what Locate did not write:
// an empty or oversized id, a path deeper than MaxDepth, a truncated part,
// a varint that is not minimal.
func ParseLocation(loc []byte) (id []byte, path merge.Path, err error) {
	r := wire.NewReader(loc)
	id = r.LenBytes(MaxIDSize)
	if r.Err() == nil && len(id) == 0 {
		return nil, nil, fmt.Errorf("%w: a location with an empty id", ErrID)
	}
	for r.Err() == nil && r.Remaining() > 0 {
		if len(path) == MaxDepth {
			return nil, nil, fmt.Errorf("%w: a location deeper than %d fields", ErrID, MaxDepth)
		}
		path = append(path, string(r.LenBytes(MaxDocument)))
	}
	if err := r.Done(); err != nil {
		return nil, nil, fmt.Errorf("%w: a location: %w", ErrID, err)
	}
	return bytes.Clone(id), path, nil
}
