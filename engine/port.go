// Package engine is the Versioned DB's one API: a database opened on a branch
// of a snapshot-core repository, sessions carrying a principal, transactions
// with snapshot isolation and optimistic commit through the models' merge,
// and the version operations (commit, branch, merge, diff, log) an access
// layer needs, reached here and never by importing the core
// (docs/specs/engine-layers.md; Engine Spec L4's API sketch, DESIGN D1).
//
// Adapters (a wire protocol and a query language each, in their own
// repositories) import this package alone (DESIGN D6): what they need of the
// core and of the models is re-exported here.
//
// The files, and who owns each while E4 is built:
//
//	port.go      the re-exported types and the result types (shared; change by agreement)
//	errors.go    the errors callers see (shared sentinels; scrubbing in errors.go)
//	database.go  Create, Open, Close, Session
//	session.go   the version operations on a branch
//	txn.go       transactions and the per-model handles
package engine

import (
	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
	"github.com/SmithOperatingSolutions/snapshot-core/core/blob"
	"github.com/SmithOperatingSolutions/snapshot-core/core/hash"
	"github.com/SmithOperatingSolutions/snapshot-core/core/seal"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/table"
)

// Principal is who a session acts as: the core's principal, checked against
// the repository's authorizer on every call. An adapter builds one from its
// protocol's authentication and never reaches into the core for it.
type Principal = auth.Principal

// Authorizer decides every call a session makes (the core's port).
type Authorizer = auth.Authorizer

// Blobs is the backend a database lives on (the core's port).
type Blobs = blob.BlobStore

// BlobVersion is a version of a backend's root, as Blobs.SwapRoot takes
// and returns it: named here so a program can wrap Blobs (to count or
// meter what reaches its backend) without importing the core.
type BlobVersion = blob.Version

// Journaler is a Blobs that keeps a commit journal beside its objects
// (the core's port; DiskBlobs is one, MemoryBlobs is not). The core looks
// for it by type, so a program that wraps Blobs hides the journal, and
// every commit publishes in full, unless the wrapper is a Journaler too
// when the store it wraps is one: wrap as `struct{ wrapper; Journaler }`
// in that case (core v0.3.0, #34).
type Journaler = blob.Journaler

// Journal is an open commit journal, as Journaler.OpenJournal returns it:
// named here for the same wrapper.
type Journal = blob.Journal

// Keyring holds a database's master key (the core's).
type Keyring = seal.Keyring

// Hash names a commit.
type Hash = hash.Hash

// The table model's types, for callers that create and edit tables.
type (
	Schema = table.Schema
	Column = table.Column
	Index  = table.Index
	Tag    = table.Tag
	Type   = table.Type
	Row    = table.Row
	Key    = table.Key
)

// The table model's column types.
const (
	TypeBool        = table.TypeBool
	TypeInt2        = table.TypeInt2
	TypeInt4        = table.TypeInt4
	TypeInt8        = table.TypeInt8
	TypeFloat4      = table.TypeFloat4
	TypeFloat8      = table.TypeFloat8
	TypeNumeric     = table.TypeNumeric
	TypeText        = table.TypeText
	TypeVarchar     = table.TypeVarchar
	TypeBytea       = table.TypeBytea
	TypeDate        = table.TypeDate
	TypeTimestamp   = table.TypeTimestamp
	TypeTimestampTZ = table.TypeTimestampTZ
	TypeUUID        = table.TypeUUID
	TypeJSONB       = table.TypeJSONB
)

// Value is a key-value entry's value (the kv model's).
type Value = kv.Value

// ValueKind is what a key-value entry holds, and how it merges.
type ValueKind = kv.Kind

// The kv model's value kinds.
const (
	ValueBytes     = kv.Bytes
	ValueCounter   = kv.Counter
	ValueSet       = kv.Set
	ValueHash      = kv.Hash
	ValueSortedSet = kv.SortedSet
	ValueSequence  = kv.Sequence
)

// Node is a document, or a field of one (the merge library's JSON-like tree).
type Node = merge.Node

// Ref names a point in history: a branch, a tag, or a commit hash in hex.
type Ref string

// Kind is what an object is: which model it is stored in.
type Kind uint8

// Object kinds.
const (
	KindTable Kind = iota + 1
	KindKV
	KindDocuments
	KindFile
	KindFolder
)

// CommitMeta is one commit as a caller sees it.
type CommitMeta struct {
	Hash    Hash
	Parents []Hash // the first is the branch merged into
	Author  string
	Message string
	Time    int64 // unix nanoseconds, UTC
}

// ChangeKind says how an object, or a part of one, changed.
type ChangeKind uint8

// Change kinds.
const (
	Added ChangeKind = iota + 1
	Removed
	Modified
)

// Change is one part of an object that differs between two points in
// history. Location is the object's model's own address for it (a row key,
// a kv key, a record id); the object's handle decodes it.
type Change struct {
	Kind     ChangeKind
	Location []byte
}

// Conflict is one object a merge could not decide, and where inside it.
type Conflict struct {
	Path  string         // the object's name
	Why   string         // why the object conflicts as a whole
	Parts []ConflictPart // where inside it, in its model's location encoding
}

// ConflictPart is one place inside an object two sides could not combine.
type ConflictPart struct {
	Location []byte
	Reason   string
}

// MergeResult is what merging another branch into a session's branch did:
// with no conflicts the merge was committed as Commit; with conflicts the
// branch is mid-merge until they are resolved or the merge is aborted.
type MergeResult struct {
	Commit    Hash
	Conflicts []Conflict
}
