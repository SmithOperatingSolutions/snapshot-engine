package engine

import (
	"context"
	"sync"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
)

// Action is what a call would do, as the core asks the Authorizer.
type Action = auth.Action

// The core's actions, for callers writing an Authorizer of their own.
const (
	ActionRead   = auth.Read
	ActionWrite  = auth.Write
	ActionManage = auth.Manage
	ActionAdmin  = auth.Admin
	ActionCommit = auth.Commit
	ActionMerge  = auth.Merge
)

// Permission is what a grant allows (Engine Spec L4, Authorization).
type Permission uint8

// Permissions.
const (
	PermRead        Permission = iota + 1 // read branches, commits, objects
	PermWrite                             // change a branch's working set, or one object on it
	PermCommit                            // record a branch's working set as a commit
	PermMerge                             // merge into a branch, resolve and abandon its merge
	PermBranchAdmin                       // create and delete branches (and, database-wide, tags)
	PermAdmin                             // repository-wide operations: create, GC
)

// Scope is where a grant applies: the whole database (the zero Scope), a
// branch, or one object on a branch.
type Scope struct {
	Branch string // "" with Object "": the whole database
	Object string // an object's path on Branch
}

// DatabaseScope is the whole database.
func DatabaseScope() Scope { return Scope{} }

// BranchScope is one branch and every object on it.
func BranchScope(branch string) Scope { return Scope{Branch: branch} }

// ObjectScope is one object on one branch.
func ObjectScope(branch, path string) Scope { return Scope{Branch: branch, Object: path} }

// Grants is an Authorizer of per-principal grants and protected branches:
// it denies whatever it was not told to allow, and a protected branch
// takes no direct write whatever is granted. It is safe for concurrent
// use; a change takes effect on the next call a session makes.
type Grants struct {
	mu sync.RWMutex
}

var _ Authorizer = (*Grants)(nil)

// Grant gives principal perms at scope. (Stub.)
func (g *Grants) Grant(principal string, s Scope, perms ...Permission) error { return nil }

// Revoke takes perms at scope back from principal. (Stub.)
func (g *Grants) Revoke(principal string, s Scope, perms ...Permission) error { return nil }

// Protect makes branch take merges and no direct writes. (Stub.)
func (g *Grants) Protect(branch string) error { return nil }

// Unprotect lifts Protect. (Stub.)
func (g *Grants) Unprotect(branch string) {}

// Authorize implements Authorizer. (Stub: denies everything.)
func (g *Grants) Authorize(ctx context.Context, p Principal, a Action, resource string) error {
	return auth.ErrDenied
}
