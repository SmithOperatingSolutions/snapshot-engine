package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
	"github.com/SmithOperatingSolutions/snapshot-core/core/object"
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
// use; a change takes effect on the next call a session makes. The zero
// Grants allows nothing.
//
// How the questions map onto grants (every other question is denied). The
// core asks all but one; the engine asks Read on path:<b>:<p> itself when a
// transaction opens an object, a diff reads one, or Objects lists one.
//
//	Read   repo, tag:<t>       Read at any scope
//	Read   branch:<b>          Read on the database, on b, or on an object of b
//	Read   path:<b>:<p>        Read on the database, on b, or on the object p of b
//	Write  branch:<b>          Write on the database, on b or on an object of b; never when b is protected
//	Write  path:<b>:<p>        Write on the database, on b, or on the object p of b
//	Commit branch:<b>          Commit on the database or on b
//	Merge  branch:<b>          Merge on the database or on b
//	Manage branch:<b>          BranchAdmin on the database or on b
//	Manage tag:<t>             BranchAdmin on the database
//	Admin  repo                Admin on the database
//
// An object scope takes Read and Write. A reader or writer of one object
// is let through the branch's question (the working set is read and
// written as a whole) and then asked per object: a transaction's commit
// asks Write on every object it changed, and the engine asks Read on every
// object it opens, so a grant on one table is a grant on that table only.
// Reads of the repository as a whole (the log, the branch and tag lists)
// are asked about the repository, not a branch, and are open to anyone who
// may read anything.
type Grants struct {
	mu        sync.RWMutex
	grants    map[string]map[Scope]permSet // principal id -> scope -> permissions
	protected map[string]bool
}

// permSet is a set of permissions, a bit each.
type permSet uint8

func (ps permSet) has(p Permission) bool { return ps&(1<<p) != 0 }

var _ Authorizer = (*Grants)(nil)

// validate refuses a scope no question could ever name.
func (s Scope) validate() error {
	switch {
	case s.Branch == "" && s.Object != "":
		return fmt.Errorf("%w: an object scope needs its branch", ErrInvalid)
	case strings.Contains(s.Branch, ":"):
		return fmt.Errorf("%w: a branch name cannot hold a colon", ErrInvalid)
	case s.Object != "":
		if err := object.ValidPath(s.Object); err != nil {
			return fmt.Errorf("%w: an object path: %w", ErrInvalid, err)
		}
	}
	return nil
}

// checkGrant refuses what Grant and Revoke cannot store: no principal, no
// permission or an unknown one, a scope that names nothing, Admin anywhere
// but the database, and on one object anything but Read or Write.
func checkGrant(principal string, s Scope, perms []Permission) error {
	if principal == "" {
		return fmt.Errorf("%w: a grant needs a principal", ErrInvalid)
	}
	if len(perms) == 0 {
		return fmt.Errorf("%w: a grant needs a permission", ErrInvalid)
	}
	if err := s.validate(); err != nil {
		return err
	}
	for _, p := range perms {
		switch {
		case p < PermRead || p > PermAdmin:
			return fmt.Errorf("%w: permission %d", ErrInvalid, p)
		case s.Object != "" && p != PermRead && p != PermWrite:
			return fmt.Errorf("%w: an object scope takes Read and Write alone", ErrInvalid)
		case p == PermAdmin && s != (Scope{}):
			return fmt.Errorf("%w: Admin is the database's alone", ErrInvalid)
		}
	}
	return nil
}

// Grant gives principal perms at scope.
func (g *Grants) Grant(principal string, s Scope, perms ...Permission) error {
	if err := checkGrant(principal, s, perms); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.grants == nil {
		g.grants = map[string]map[Scope]permSet{}
	}
	if g.grants[principal] == nil {
		g.grants[principal] = map[Scope]permSet{}
	}
	for _, p := range perms {
		g.grants[principal][s] |= 1 << p
	}
	return nil
}

// Revoke takes perms at scope back from principal; the rest of its grants
// stand.
func (g *Grants) Revoke(principal string, s Scope, perms ...Permission) error {
	if err := checkGrant(principal, s, perms); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	scopes := g.grants[principal]
	if scopes == nil { // nothing to take back
		return nil
	}
	for _, p := range perms {
		scopes[s] &^= 1 << p
	}
	if scopes[s] == 0 {
		delete(scopes, s)
	}
	return nil
}

// Protect makes branch take merges and their commits and no direct writes.
func (g *Grants) Protect(branch string) error {
	if branch == "" || strings.Contains(branch, ":") {
		return fmt.Errorf("%w: a branch to protect", ErrInvalid)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.protected == nil {
		g.protected = map[string]bool{}
	}
	g.protected[branch] = true
	return nil
}

// Unprotect lifts Protect.
func (g *Grants) Unprotect(branch string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.protected, branch)
}

// Authorize implements Authorizer: nil when a grant allows the question,
// auth.ErrDenied otherwise.
func (g *Grants) Authorize(ctx context.Context, p Principal, a Action, resource string) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.allows(g.grants[p.ID], a, resource) { // no grant is ever stored for an empty id
		return nil
	}
	return auth.ErrDenied
}

// allows answers one question from one principal's grants.
func (g *Grants) allows(scopes map[Scope]permSet, a Action, resource string) bool {
	db := scopes[Scope{}]
	switch kind, branch, path := split(resource); kind {
	case "repo":
		switch a {
		case auth.Read:
			return anywhere(scopes, PermRead)
		case auth.Admin:
			return db.has(PermAdmin)
		}
	case "tag":
		switch a {
		case auth.Read:
			return anywhere(scopes, PermRead)
		case auth.Manage:
			return db.has(PermBranchAdmin)
		}
	case "branch":
		onBranch := func(p Permission) bool { return db.has(p) || scopes[Scope{Branch: branch}].has(p) }
		switch a {
		case auth.Read:
			return onBranch(PermRead) || onAnObject(scopes, branch, PermRead)
		case auth.Write:
			return !g.protected[branch] && (onBranch(PermWrite) || onAnObject(scopes, branch, PermWrite))
		case auth.Commit:
			return onBranch(PermCommit)
		case auth.Merge:
			return onBranch(PermMerge)
		case auth.Manage:
			return onBranch(PermBranchAdmin)
		}
	case "path":
		if a == auth.Write || a == auth.Read {
			p := PermWrite
			if a == auth.Read {
				p = PermRead
			}
			return db.has(p) || scopes[Scope{Branch: branch}].has(p) || scopes[Scope{Branch: branch, Object: path}].has(p)
		}
	}
	return false
}

// split reads a resource the core names: "repo", "tag:<t>", "branch:<b>"
// or "path:<b>:<p>" (a branch name holds no colon); anything else, or a
// part left empty, is kind "".
func split(resource string) (kind, branch, path string) {
	if resource == "repo" {
		return "repo", "", ""
	}
	k, rest, ok := strings.Cut(resource, ":")
	if !ok || rest == "" {
		return "", "", ""
	}
	switch k {
	case "tag":
		return "tag", "", ""
	case "branch":
		return "branch", rest, ""
	case "path":
		b, p, ok := strings.Cut(rest, ":")
		if !ok || b == "" || p == "" {
			return "", "", ""
		}
		return "path", b, p
	}
	return "", "", ""
}

// anywhere says whether any of the scopes holds p.
func anywhere(scopes map[Scope]permSet, p Permission) bool {
	for _, ps := range scopes {
		if ps.has(p) {
			return true
		}
	}
	return false
}

// onAnObject says whether Write is granted on some object of branch.
func onAnObject(scopes map[Scope]permSet, branch string, p Permission) bool {
	for s, ps := range scopes {
		if s.Branch == branch && s.Object != "" && ps.has(p) {
			return true
		}
	}
	return false
}
