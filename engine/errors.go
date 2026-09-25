package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
	"github.com/SmithOperatingSolutions/snapshot-core/core/repo"
	"github.com/SmithOperatingSolutions/snapshot-core/core/vcs"
)

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
	// ErrSessionLost is a write a collection reclaimed before it was
	// published: the database refuses every further write until it is
	// opened again, and the write must be done again there.
	ErrSessionLost = errors.New("engine: session lost: garbage collection reclaimed unpublished writes; open the database again")
	// ErrInUse is a branch another session is on.
	ErrInUse = errors.New("engine: in use by another session")
	// ErrInternal is a failure the caller can do nothing about (a store
	// failure, a corrupt object); its details are in the log.
	ErrInternal = errors.New("engine: internal error")
)

// Error is what a caller holds: an engine error and the correlation id its
// details are logged under. It unwraps to the engine error alone.
type Error struct {
	Kind        error // one of the Err values above
	Correlation string
}

func (e *Error) Error() string { return e.Kind.Error() + " [ref " + e.Correlation + "]" }

// Unwrap is the engine error, so errors.Is matches it.
func (e *Error) Unwrap() error { return e.Kind }

// callerErrors are the engine errors a caller may be shown, most specific
// first.
var callerErrors = []error{ErrPermissionDenied, ErrSessionLost, ErrSerialization, ErrNotFound, ErrExists, ErrWrongKind, ErrInvalid, ErrMergeInProgress, ErrInUse, ErrClosed}

// scrub is what the API returns in place of err: the engine error it
// matches (ErrInternal when none) and a fresh correlation id, the details
// logged under the id. A cancelled or expired context is returned as it
// is: it carries nothing of the data.
func scrub(ctx context.Context, l Logger, err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var own callerOwn
	if errors.As(err, &own) { // the caller's own callback's error goes back as it came
		return own.err
	}
	if _, ok := err.(*Error); ok { //nolint:errorlint // only an error scrubbed at the top is scrubbed already; one wrapped inside another is not
		return err
	}
	kind := ErrInternal
	for _, k := range callerErrors {
		if errors.Is(err, k) {
			kind = k
			break
		}
	}
	id := correlation()
	if l != nil {
		l.Log(ctx, id, err.Error())
	}
	return &Error{Kind: kind, Correlation: id}
}

// correlation is a fresh id: 8 random bytes in hex.
func correlation() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil { // crypto/rand does not fail on the platforms Go supports
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// translate turns an error from the core into the engine error a caller
// matches, keeping the core's for errors.Is. What reaches a caller is
// scrubbed at the API's boundary.
func translate(err error) error {
	var to error
	switch {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrDenied):
		to = ErrPermissionDenied
	case errors.Is(err, auth.ErrInvalidPrincipal), errors.Is(err, vcs.ErrInvalidName):
		to = ErrInvalid
	case errors.Is(err, repo.ErrNoRepo), errors.Is(err, vcs.ErrNoRepo), errors.Is(err, vcs.ErrBranchNotFound), errors.Is(err, vcs.ErrTagNotFound):
		to = ErrNotFound
	case errors.Is(err, repo.ErrExists), errors.Is(err, vcs.ErrExists), errors.Is(err, vcs.ErrBranchExists), errors.Is(err, vcs.ErrTagExists):
		to = ErrExists
	case errors.Is(err, vcs.ErrMergeState), errors.Is(err, vcs.ErrUnresolvedConflicts):
		to = ErrMergeInProgress
	case errors.Is(err, vcs.ErrBranchInUse):
		to = ErrInUse
	case errors.Is(err, vcs.ErrSessionLost):
		to = ErrSessionLost
	default:
		return err
	}
	return fmt.Errorf("%w (%w)", to, err)
}

// scrubInto is scrub for a deferred call over a named error result: every
// exported call returns through it, so no path can hand a caller the core's
// error.
func scrubInto(ctx context.Context, l Logger, err *error) { *err = scrub(ctx, l, *err) }

// callerOwn marks an error the caller's own callback returned (a scan's
// each): scrub hands it back as it came, since it is the caller's.
type callerOwn struct{ err error }

func (c callerOwn) Error() string { return c.err.Error() }
func (c callerOwn) Unwrap() error { return c.err }

// callersOwn marks err, when there is one, as the caller's own.
func callersOwn(err error) error {
	if err == nil {
		return nil
	}
	return callerOwn{err}
}
