package engine

import (
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
	"github.com/SmithOperatingSolutions/snapshot-core/core/blob/local"
	"github.com/SmithOperatingSolutions/snapshot-core/core/blob/mem"
	"github.com/SmithOperatingSolutions/snapshot-core/core/seal"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
)

// What a program needs to open a database without importing the core
// (DESIGN D6): a key and a backend. Other backends (several mount points,
// S3, a root kept apart) are the core's, configured by the host.

// NewKeyring makes a fresh master key.
func NewKeyring() (*Keyring, error) { return seal.NewKeyring() }

// MemoryBlobs is a backend in memory: a database on it lives as long as
// the process.
func MemoryBlobs() Blobs { return mem.New() }

// DiskBlobs is a backend in the directory dir: the store already there, or
// a new one when dir is absent or empty, so a program opens the same
// directory on every start.
func DiskBlobs(dir string) (Blobs, error) {
	st, err := local.Open(dir, local.Options{})
	if errors.Is(err, local.ErrNotAStore) {
		st, err = local.Create(dir, local.Options{})
	}
	if err != nil {
		return nil, fmt.Errorf("%w (%w)", ErrInvalid, err)
	}
	return st, nil
}

// AllowAll is an authorizer that allows every call: for a program with no
// principals to tell apart. A database shared by people takes Grants.
func AllowAll() Authorizer { return auth.AllowAll{} }

// ParseDocument parses JSON text into a document with the document model's
// bounded parser; what is not a document is ErrInvalid.
func ParseDocument(text []byte) (_ Node, err error) {
	n, err := document.Parse(text)
	if err != nil {
		return Node{}, fmt.Errorf("%w (%w)", ErrInvalid, err)
	}
	return n, nil
}
