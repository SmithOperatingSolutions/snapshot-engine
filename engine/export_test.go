package engine

import (
	"context"
	"errors"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"
	"github.com/SmithOperatingSolutions/snapshot-core/core/object"
	"github.com/SmithOperatingSolutions/snapshot-core/core/prolly"
	"github.com/SmithOperatingSolutions/snapshot-core/core/stream"
	"github.com/SmithOperatingSolutions/snapshot-core/core/vcs"
)

// The seams the tests use to set up history without the transaction layer.

// Chunks is the database's chunk store, for writing objects.
func Chunks(d *Database) chunk.ReadWriter { return d.r.Chunks() }

// Prolly is the map geometry the database's models write with.
func Prolly(d *Database) prolly.Config { return d.models.geometry.Prolly() }

// StreamConfig is the stream geometry the database writes files with.
func StreamConfig(d *Database) stream.Config { return d.models.geometry.Stream() }

// PutObject sets path on branch's working and staged namespaces to ref, or
// removes it when ref is nil, acting as p.
func PutObject(ctx context.Context, d *Database, p auth.Principal, branch, path string, ref *object.Ref) error {
	for {
		ws, err := d.r.WorkingSet(ctx, p, branch)
		if err != nil {
			return err
		}
		ns, err := d.r.Namespace(ctx, ws.Working)
		if err != nil {
			return err
		}
		e := ns.Editor()
		if ref == nil {
			err = e.Delete(path)
		} else {
			err = e.Put(path, *ref)
		}
		if err != nil {
			return err
		}
		next, err := e.Flush(ctx)
		if err != nil {
			return err
		}
		n := ws
		n.Working, n.Staged = next.Root(), next.Root()
		if _, err = d.r.UpdateWorkingSet(ctx, p, branch, ws, n); !errors.Is(err, vcs.ErrConflict) {
			return err
		}
	}
}

// ObjectAt is the object at path in commit's namespace, as p.
func ObjectAt(ctx context.Context, d *Database, p auth.Principal, commit Hash, path string) (object.Ref, bool, error) {
	c, err := d.r.ReadCommit(ctx, p, commit)
	if err != nil {
		return object.Ref{}, false, err
	}
	ns, err := d.r.Namespace(ctx, c.Namespace)
	if err != nil {
		return object.Ref{}, false, err
	}
	ref, _, ok, err := ns.Get(ctx, path)
	return ref, ok, err
}

// HeadOf is branch's head commit, as p.
func HeadOf(ctx context.Context, d *Database, p auth.Principal, branch string) (vcs.Commit, error) {
	return d.r.Head(ctx, p, branch)
}

// WorkingSetOf is branch's working set, as p.
func WorkingSetOf(ctx context.Context, d *Database, p auth.Principal, branch string) (vcs.WorkingSet, error) {
	return d.r.WorkingSet(ctx, p, branch)
}

// CreateTag names commit as tag, as p.
func CreateTag(ctx context.Context, d *Database, p auth.Principal, name string, commit Hash) error {
	_, err := d.r.CreateTag(ctx, p, name, commit, "")
	return err
}

// Scratch is a scratch store over the database's chunks.
func Scratch(d *Database) chunk.ReadWriter { return d.scratch() }
