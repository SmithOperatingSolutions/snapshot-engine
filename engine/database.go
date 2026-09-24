package engine

import (
	"context"
	"sync"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/repo"
	blobmodel "github.com/SmithOperatingSolutions/snapshot-core/model/blob"
	"github.com/SmithOperatingSolutions/snapshot-core/model/tree"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// Logger receives what callers are not shown: an error's details, keyed by
// the correlation id the caller's error carries. Nil discards.
type Logger interface {
	Log(ctx context.Context, correlation string, detail string)
}

// Options configures Create and Open.
type Options struct {
	Blobs      Blobs
	Keys       *Keyring
	Authorizer Authorizer       // nil denies everything
	Logger     Logger           // nil discards
	Clock      func() time.Time // nil: time.Now
}

// Database is a snapshot-core repository opened with every model the engine
// knows: tables, key-value maps, document collections, files and folders.
type Database struct {
	o      Options
	r      *repo.Repo
	models models

	mu     sync.Mutex
	closed bool
}

// models is every model the engine registers, configured for one geometry.
type models struct {
	geometry repo.Geometry
	registry *model.Registry
	table    table.Model
	kv       kv.Model
	document document.Model
}

func modelsFor(g repo.Geometry) (models, error) {
	c := g.Prolly()
	m := models{geometry: g, table: table.Model{Config: c}, kv: kv.Model{Config: c}, document: document.Model{Config: c}}
	reg, err := model.NewRegistry(m.table, m.kv, m.document, blobmodel.Model{}, tree.Model{Config: c})
	if err != nil {
		return models{}, err
	}
	m.registry = reg
	return m, nil
}

func (o Options) repo(m models) repo.Options {
	return repo.Options{Blobs: o.Blobs, Keys: o.Keys, Registry: m.registry, Authorizer: o.Authorizer, Clock: o.Clock, Geometry: m.geometry}
}

// Create makes a new database on o.Blobs, owned by p, with main as its
// first branch, written at the default geometry.
func Create(ctx context.Context, p Principal, o Options) (*Database, error) {
	m, err := modelsFor(repo.DefaultGeometry())
	if err != nil {
		return nil, err
	}
	r, err := repo.Init(ctx, p, o.repo(m))
	if err != nil {
		return nil, translate(err)
	}
	return &Database{o: o, r: r, models: m}, nil
}

// Open opens the database on o.Blobs, with the models configured for the
// geometry it was created with.
func Open(ctx context.Context, o Options) (*Database, error) {
	m, err := modelsFor(repo.DefaultGeometry())
	if err != nil {
		return nil, err
	}
	r, err := repo.Open(ctx, o.repo(m))
	if err != nil {
		return nil, translate(err)
	}
	if r.Config.Geometry != m.geometry { // written at another geometry: the models must write as it does
		if err := r.Close(); err != nil {
			return nil, err
		}
		if m, err = modelsFor(r.Config.Geometry); err != nil {
			return nil, err
		}
		if r, err = repo.Open(ctx, o.repo(m)); err != nil {
			return nil, translate(err)
		}
	}
	return &Database{o: o, r: r, models: m}, nil
}

// Close closes the database; its sessions refuse every call after it.
func (d *Database) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return d.r.Close()
}

// isClosed says whether Close has been called.
func (d *Database) isClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

// Session opens a session for p on branch; every call it makes is checked
// against the authorizer when it is made.
func (d *Database) Session(ctx context.Context, p Principal, branch string) (*Session, error) {
	if d.isClosed() {
		return nil, ErrClosed
	}
	co, err := d.r.Checkout(ctx, p, branch) // checks the principal, the branch and read access, and holds the branch
	if err != nil {
		return nil, translate(err)
	}
	return &Session{db: d, p: p, branch: branch, co: co}, nil
}
