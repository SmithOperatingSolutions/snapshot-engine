package document

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
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

// spec is the collection as a map-shaped model: a map from record id to
// record frame under a configuration, its records checked as ids of ours
// holding frames of ours.
func spec(c prolly.Config) mapobject.Spec {
	return mapobject.Spec{Name: "document", Format: Format, Config: c, Check: func(id, frame []byte) error {
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

// Write stores records, by id, as an object.
func Write(ctx context.Context, s chunk.ReadWriter, c prolly.Config, records map[string]merge.Node) (model.Root, error) {
	frames := make(map[string][]byte, len(records))
	for id, n := range records { // every record checked before anything is stored
		if err := checkID([]byte(id)); err != nil {
			return model.Root{}, err
		}
		f, err := EncodeRecord(n)
		if err != nil {
			return model.Root{}, fmt.Errorf("record %q: %w", id, err)
		}
		frames[id] = f
	}
	m, err := prolly.Empty(ctx, s, c)
	if err != nil {
		return model.Root{}, err
	}
	e := m.Editor()
	for id, f := range frames {
		if err := e.Put([]byte(id), f); err != nil {
			return model.Root{}, err
		}
	}
	if m, err = e.Flush(ctx); err != nil {
		return model.Root{}, err
	}
	return spec(c).Root(m), nil
}

// Read returns an object's records by id.
func Read(ctx context.Context, r chunk.Reader, c prolly.Config, root model.Root) (map[string]merge.Node, error) {
	m, err := spec(c).Open(ctx, mapobject.ReadOnly(r), root)
	if err != nil {
		return nil, err
	}
	it, err := m.IterRange(ctx, nil, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[string]merge.Node, m.Count())
	for {
		id, f, ok, err := it.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return out, nil
		}
		n, err := checked(id, f)
		if err != nil {
			return nil, err
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
	return spec(m.Config).Merge(ctx, base, ours, theirs, rw, resolve)
}

// resolve decides a record both sides changed from base: a Tree merge of
// the three records when both sides changed it in place; the default rule
// otherwise (equal, both deleted, deleted against changed, added twice).
func resolve(id []byte, ours, theirs prolly.Change) (value []byte, put bool, reason string) {
	value, put, reason = mapobject.Disagreement(id, ours, theirs)
	if reason == "" || ours.Kind != prolly.Modified || theirs.Kind != prolly.Modified {
		return value, put, reason
	}
	var records [3]merge.Node
	for i, frame := range [][]byte{ours.From, ours.To, theirs.To} {
		var err error
		if records[i], err = DecodeRecord(frame); err != nil {
			return nil, false, "a record that does not decode: " + err.Error()
		}
	}
	r := merge.Tree(records[0], records[1], records[2], merge.TreeOptions{})
	if len(r.Conflicts) > 0 {
		return nil, false, fields(r.Conflicts)
	}
	frame, err := EncodeRecord(r.Value)
	if err != nil {
		return nil, false, "the merged record is not a document: " + err.Error()
	}
	return frame, true, ""
}

// fields is a conflict's reason for a record: each conflicting field's
// path and why.
func fields(cs []merge.Conflict) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, "field "+c.Path.String()+": "+c.Reason)
	}
	return strings.Join(parts, "; ")
}

// Locate is the location of a conflict in a collection: the record's id
// and, for a conflict inside the record, the path of the field, empty for
// the record as a whole. (Stub.)
func Locate(id []byte, path merge.Path) []byte { return nil }

// ParseLocation is Locate's inverse; it refuses what Locate did not write.
// (Stub.)
func ParseLocation(loc []byte) (id []byte, path merge.Path, err error) {
	return nil, nil, fmt.Errorf("%w: ParseLocation is not implemented", ErrID)
}
