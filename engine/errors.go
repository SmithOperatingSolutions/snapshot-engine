package engine

import "errors"

// The errors callers see. Each is generic: it names what happened, never a
// key, a value, a row or a file's contents (Engine Spec, security; the
// details go to the database's Logger).
var (
	// ErrPermissionDenied is a call the principal may not make; nothing
	// changed.
	ErrPermissionDenied = errors.New("engine: permission denied")
	// ErrSerialization is a transaction whose commit collided with a
	// concurrent one on the same data (SQLSTATE 40001); none of its changes
	// are in the working set.
	ErrSerialization = errors.New("engine: could not serialize access due to concurrent update")
	// ErrNotFound is a branch, ref, object or row that does not exist.
	ErrNotFound = errors.New("engine: not found")
	// ErrExists is a branch or object that already exists.
	ErrExists = errors.New("engine: already exists")
	// ErrWrongKind is an object asked for as one kind that is another.
	ErrWrongKind = errors.New("engine: object is of another kind")
	// ErrInvalid is input refused at the boundary: a bad name, a value that
	// does not fit its column, a malformed document. Nothing was written.
	ErrInvalid = errors.New("engine: invalid input")
	// ErrMergeInProgress is a call a branch cannot take mid-merge.
	ErrMergeInProgress = errors.New("engine: a merge is in progress")
	// ErrClosed is a call on a closed database, session or finished
	// transaction.
	ErrClosed = errors.New("engine: closed")
)

// errNotImplemented is what a stub returns while E4 is built.
var errNotImplemented = errors.New("engine: not implemented")
