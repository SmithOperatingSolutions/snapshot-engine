package engine

import (
	"context"
	"time"
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
	o Options
}

// Create makes a new database on o.Blobs, owned by p, with main as its
// first branch. (Stub.)
func Create(ctx context.Context, p Principal, o Options) (*Database, error) {
	return nil, errNotImplemented
}

// Open opens the database on o.Blobs. (Stub.)
func Open(ctx context.Context, o Options) (*Database, error) {
	return nil, errNotImplemented
}

// Close closes the database; its sessions refuse every call after it.
// (Stub.)
func (d *Database) Close() error { return errNotImplemented }

// Session opens a session for p on branch; every call it makes is checked
// against the authorizer when it is made. (Stub.)
func (d *Database) Session(ctx context.Context, p Principal, branch string) (*Session, error) {
	return nil, errNotImplemented
}
