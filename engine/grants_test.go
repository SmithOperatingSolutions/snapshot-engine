package engine_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

var bob = engine.Principal{ID: "user:bob"}

// grantAllows says whether g allows p the action on the resource, failing
// the test on an answer that is neither an allowance nor a denial.
func grantAllows(t *testing.T, g *engine.Grants, p engine.Principal, a engine.Action, resource string) bool {
	t.Helper()
	err := g.Authorize(ctx, p, a, resource)
	if err != nil && !errors.Is(err, auth.ErrDenied) {
		t.Fatalf("Authorize(%v, %q) = %v: neither allowed nor denied", a, resource, err)
	}
	return err == nil
}

type grantCase struct {
	a        engine.Action
	resource string
	want     bool
}

func grantExpect(t *testing.T, name string, g *engine.Grants, p engine.Principal, cases []grantCase) {
	t.Helper()
	for _, c := range cases {
		if got := grantAllows(t, g, p, c.a, c.resource); got != c.want {
			verb := "denied"
			if got {
				verb = "allowed"
			}
			t.Errorf("%s: action %d on %q was %s", name, c.a, c.resource, verb)
		}
	}
}

func grantOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// Every pair the core asks about maps onto one permission at a scope: reads
// of a branch need Read on the database or the branch; the repository's
// reads (log, merge-base, the branch and tag lists) and a tag's read need
// Read somewhere; a write to a branch needs Write on the database, the
// branch or an object on it, and a write to one object needs Write on the
// database, its branch or the object itself; Commit and Merge are the
// branch's; Manage is BranchAdmin, on the branch for a branch and on the
// database for a tag; Admin is the database's. Nothing is implied: Write
// is not Read, a branch is not another branch, an object is its path alone.
func TestGrantsMapTheCoresQuestions(t *testing.T) {
	var g engine.Grants
	grantOK(t, g.Grant("user:bob", engine.DatabaseScope(), engine.PermRead))
	grantExpect(t, "Read on the database", &g, bob, []grantCase{
		{engine.ActionRead, "repo", true},
		{engine.ActionRead, "branch:main", true},
		{engine.ActionRead, "tag:v1", true},
		{engine.ActionWrite, "branch:main", false},
		{engine.ActionCommit, "branch:main", false},
	})

	g = engine.Grants{}
	grantOK(t, g.Grant("user:bob", engine.BranchScope("feature"), engine.PermRead))
	grantExpect(t, "Read on a branch", &g, bob, []grantCase{
		{engine.ActionRead, "branch:feature", true},
		{engine.ActionRead, "branch:main", false},
		{engine.ActionRead, "repo", true},
		{engine.ActionRead, "tag:v1", true},
	})

	g = engine.Grants{}
	grantOK(t, g.Grant("user:bob", engine.BranchScope("main"), engine.PermWrite))
	grantExpect(t, "Write on a branch", &g, bob, []grantCase{
		{engine.ActionWrite, "branch:main", true},
		{engine.ActionWrite, "path:main:people", true},
		{engine.ActionWrite, "path:main:a/b", true},
		{engine.ActionWrite, "branch:feature", false},
		{engine.ActionWrite, "path:feature:people", false},
		{engine.ActionCommit, "branch:main", false},
		{engine.ActionRead, "branch:main", false},
		{engine.ActionRead, "repo", false},
	})

	g = engine.Grants{}
	grantOK(t, g.Grant("user:bob", engine.ObjectScope("main", "people"), engine.PermWrite))
	grantExpect(t, "Write on one object", &g, bob, []grantCase{
		{engine.ActionWrite, "branch:main", true},
		{engine.ActionWrite, "path:main:people", true},
		{engine.ActionWrite, "path:main:pets", false},
		{engine.ActionWrite, "path:main:people/x", false},
		{engine.ActionWrite, "branch:feature", false},
		{engine.ActionWrite, "path:feature:people", false},
	})

	g = engine.Grants{}
	grantOK(t, g.Grant("user:bob", engine.BranchScope("main"), engine.PermCommit, engine.PermMerge))
	grantExpect(t, "Commit and Merge on a branch", &g, bob, []grantCase{
		{engine.ActionCommit, "branch:main", true},
		{engine.ActionMerge, "branch:main", true},
		{engine.ActionCommit, "branch:feature", false},
		{engine.ActionMerge, "branch:feature", false},
		{engine.ActionWrite, "branch:main", false},
	})

	g = engine.Grants{}
	grantOK(t, g.Grant("user:bob", engine.DatabaseScope(), engine.PermMerge))
	grantExpect(t, "Merge on the database", &g, bob, []grantCase{
		{engine.ActionMerge, "branch:main", true},
		{engine.ActionMerge, "branch:anything", true},
	})

	g = engine.Grants{}
	grantOK(t, g.Grant("user:bob", engine.BranchScope("feature"), engine.PermBranchAdmin))
	grantExpect(t, "BranchAdmin on a branch", &g, bob, []grantCase{
		{engine.ActionManage, "branch:feature", true},
		{engine.ActionManage, "branch:main", false},
		{engine.ActionManage, "tag:v1", false},
	})

	g = engine.Grants{}
	grantOK(t, g.Grant("user:bob", engine.DatabaseScope(), engine.PermBranchAdmin, engine.PermAdmin))
	grantExpect(t, "BranchAdmin and Admin on the database", &g, bob, []grantCase{
		{engine.ActionManage, "branch:anything", true},
		{engine.ActionManage, "tag:v1", true},
		{engine.ActionAdmin, "repo", true},
	})
	grantExpect(t, "another principal", &g, alice, []grantCase{
		{engine.ActionManage, "branch:anything", false},
		{engine.ActionAdmin, "repo", false},
	})
}

// With every permission on the whole database, what the core never asks,
// or a resource of a shape it never writes, is denied: Write, Commit,
// Merge or Manage on the repository; Admin on a branch or a tag; Read, Commit
// or Merge on one object; Write, Commit or Merge on a tag; an empty branch
// or path; a resource of an unknown kind; an unknown action; a principal
// with no id. And nothing at all is allowed to a principal nothing was
// granted to.
func TestGrantsDenyWhatTheyDoNotKnow(t *testing.T) {
	var g engine.Grants
	all := []engine.Permission{engine.PermRead, engine.PermWrite, engine.PermCommit, engine.PermMerge, engine.PermBranchAdmin, engine.PermAdmin}
	grantOK(t, g.Grant("user:bob", engine.DatabaseScope(), all...))
	grantExpect(t, "positive control", &g, bob, []grantCase{
		{engine.ActionWrite, "branch:main", true},
		{engine.ActionWrite, "path:main:people", true},
	})
	grantExpect(t, "shapes the core never asks", &g, bob, []grantCase{
		{engine.ActionWrite, "repo", false},
		{engine.ActionCommit, "repo", false},
		{engine.ActionMerge, "repo", false},
		{engine.ActionManage, "repo", false},
		{engine.ActionAdmin, "branch:main", false},
		{engine.ActionAdmin, "tag:v1", false},
		{engine.ActionRead, "path:main:people", false},
		{engine.ActionCommit, "path:main:people", false},
		{engine.ActionMerge, "path:main:people", false},
		{engine.ActionWrite, "tag:v1", false},
		{engine.ActionCommit, "tag:v1", false},
		{engine.ActionMerge, "tag:v1", false},
		{engine.ActionWrite, "branch:", false},
		{engine.ActionWrite, "path:main", false},
		{engine.ActionWrite, "path::people", false},
		{engine.ActionWrite, "path:main:", false},
		{engine.ActionRead, "tag:", false},
		{engine.ActionRead, "", false},
		{engine.ActionRead, "table:people", false},
		{engine.Action(99), "branch:main", false},
		{engine.Action(0), "branch:main", false},
	})
	grantExpect(t, "a principal with no id", &g, engine.Principal{}, []grantCase{
		{engine.ActionRead, "repo", false},
	})
	var none engine.Grants
	grantExpect(t, "nothing granted", &none, bob, []grantCase{
		{engine.ActionRead, "repo", false},
		{engine.ActionRead, "branch:main", false},
		{engine.ActionWrite, "path:main:people", false},
	})
}

// A grant that could never be asked about, or that names nothing, is
// refused, never stored: no principal, no permission or an unknown one, a
// scope with an object and no branch, a branch name holding a colon, an
// object path the namespace would refuse, Admin anywhere but the database,
// and on one object anything but Write (the core asks per object only for
// writes; Read, Commit and Merge there would be enforced nowhere).
func TestAGrantThatNamesNothingIsRefused(t *testing.T) {
	var g engine.Grants
	for name, grant := range map[string]func() error{
		"no principal":          func() error { return g.Grant("", engine.DatabaseScope(), engine.PermRead) },
		"no permission":         func() error { return g.Grant("user:bob", engine.DatabaseScope()) },
		"permission 0":          func() error { return g.Grant("user:bob", engine.DatabaseScope(), engine.Permission(0)) },
		"an unknown permission": func() error { return g.Grant("user:bob", engine.DatabaseScope(), engine.Permission(99)) },
		"an object with no branch": func() error {
			return g.Grant("user:bob", engine.Scope{Object: "people"}, engine.PermWrite)
		},
		"a branch with a colon":      func() error { return g.Grant("user:bob", engine.BranchScope("a:b"), engine.PermRead) },
		"a bad object path":          func() error { return g.Grant("user:bob", engine.ObjectScope("main", "a/../b"), engine.PermWrite) },
		"Admin on a branch":          func() error { return g.Grant("user:bob", engine.BranchScope("main"), engine.PermAdmin) },
		"Read on an object":          func() error { return g.Grant("user:bob", engine.ObjectScope("main", "people"), engine.PermRead) },
		"Commit on an object":        func() error { return g.Grant("user:bob", engine.ObjectScope("main", "people"), engine.PermCommit) },
		"Merge on an object":         func() error { return g.Grant("user:bob", engine.ObjectScope("main", "people"), engine.PermMerge) },
		"a revoke of nothing":        func() error { return g.Revoke("user:bob", engine.DatabaseScope()) },
		"a revoke with no principal": func() error { return g.Revoke("", engine.DatabaseScope(), engine.PermRead) },
		"protecting no branch":       func() error { return g.Protect("") },
		"protecting a bad name":      func() error { return g.Protect("a:b") },
	} {
		if err := grant(); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	grantExpect(t, "after the refusals", &g, bob, []grantCase{
		{engine.ActionRead, "repo", false},
		{engine.ActionWrite, "branch:main", false},
	})
}

// Revoke takes back exactly what it names: the rest of the grant stands.
// Protect makes a branch refuse writes to the branch itself whatever is
// granted, while its objects' writes, Commit and Merge stand, so a merge
// and its commit go in; another branch is untouched; Unprotect lifts it.
func TestRevokeAndProtect(t *testing.T) {
	var g engine.Grants
	grantOK(t, g.Grant("user:bob", engine.DatabaseScope(), engine.PermRead, engine.PermWrite, engine.PermCommit, engine.PermMerge))
	grantOK(t, g.Revoke("user:bob", engine.DatabaseScope(), engine.PermWrite))
	grantExpect(t, "after revoking Write", &g, bob, []grantCase{
		{engine.ActionWrite, "branch:main", false},
		{engine.ActionRead, "branch:main", true},
		{engine.ActionCommit, "branch:main", true},
	})
	grantOK(t, g.Revoke("user:bob", engine.BranchScope("main"), engine.PermRead)) // a scope it was never granted at
	grantExpect(t, "a revoke at another scope", &g, bob, []grantCase{
		{engine.ActionRead, "branch:main", true},
	})

	grantOK(t, g.Grant("user:bob", engine.DatabaseScope(), engine.PermWrite))
	grantOK(t, g.Grant("user:bob", engine.ObjectScope("main", "people"), engine.PermWrite))
	grantOK(t, g.Protect("main"))
	grantExpect(t, "a protected main", &g, bob, []grantCase{
		{engine.ActionWrite, "branch:main", false},
		{engine.ActionWrite, "path:main:people", true},
		{engine.ActionWrite, "path:main:pets", true},
		{engine.ActionCommit, "branch:main", true},
		{engine.ActionMerge, "branch:main", true},
		{engine.ActionWrite, "branch:feature", true},
	})
	g.Unprotect("main")
	grantExpect(t, "main unprotected", &g, bob, []grantCase{
		{engine.ActionWrite, "branch:main", true},
	})
}

// Grants change while sessions ask: concurrent grants, revokes and
// questions neither race nor lose a grant (run under -race).
func TestGrantsAreSafeForConcurrentUse(t *testing.T) {
	var g engine.Grants
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := range 50 {
				id := fmt.Sprintf("user:%d", i)
				_ = g.Grant(id, engine.BranchScope(fmt.Sprintf("b%d", j)), engine.PermRead)
				_ = g.Revoke(id, engine.BranchScope(fmt.Sprintf("b%d", j)), engine.PermRead)
				_ = g.Protect(fmt.Sprintf("b%d", j))
				g.Unprotect(fmt.Sprintf("b%d", j))
			}
			_ = g.Grant(fmt.Sprintf("user:%d", i), engine.DatabaseScope(), engine.PermRead)
		}()
		go func() {
			defer wg.Done()
			for j := range 50 {
				_ = g.Authorize(ctx, engine.Principal{ID: fmt.Sprintf("user:%d", i)}, engine.ActionRead, fmt.Sprintf("branch:b%d", j))
			}
		}()
	}
	wg.Wait()
	for i := range 8 {
		if !grantAllows(t, &g, engine.Principal{ID: fmt.Sprintf("user:%d", i)}, engine.ActionRead, "repo") {
			t.Errorf("user:%d lost the grant it was given last", i)
		}
	}
}

// grantDB is a database whose main holds tables people (1 ada 36, 2 bob
// 40) and pets (1 rex 3), committed, and a branch feature at that commit;
// built by alice allowed everything, then opened again under g, where
// alice holds every permission on the database and bob nothing yet.
func grantDB(t *testing.T) (*engine.Database, *engine.Grants) {
	t.Helper()
	o := dbOptions(t)
	db, err := engine.Create(ctx, alice, o)
	if err != nil {
		t.Fatal(err)
	}
	s := txnSession(t, db, "main")
	tx := txnBegin(t, s)
	people, err := tx.CreateTable(ctx, "people", txnPeople())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []engine.Row{txnPerson(1, "ada", 36), txnPerson(2, "bob", 40)} {
		if _, err := people.Insert(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	pets, err := tx.CreateTable(ctx, "pets", txnPeople())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pets.Insert(ctx, txnPerson(1, "rex", 3)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.CommitWorkingSet(ctx, s, "the fixture"); err != nil {
		t.Fatal(err)
	}
	if err := engine.CreateBranchHere(ctx, s, "feature"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	g := &engine.Grants{}
	grantOK(t, g.Grant("user:alice", engine.DatabaseScope(), engine.PermRead, engine.PermWrite, engine.PermCommit, engine.PermMerge, engine.PermBranchAdmin, engine.PermAdmin))
	o.Authorizer = g
	if db, err = engine.Open(ctx, o); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, g
}

// grantSession is a session for p on branch.
func grantSession(t *testing.T, db *engine.Database, p engine.Principal, branch string) *engine.Session {
	t.Helper()
	s, err := db.Session(ctx, p, branch)
	if err != nil {
		t.Fatalf("a session for %s on %s: %v", p.ID, branch, err)
	}
	return s
}

// grantUpdate begins a transaction on s, renames row id of table to name,
// and commits.
func grantUpdate(t *testing.T, s *engine.Session, tableName string, id int64, name string) error {
	t.Helper()
	tx := txnBegin(t, s)
	if err := txnTable(t, tx, tableName).Update(ctx, engine.Key{id}, txnPerson(id, name, 36)); err != nil {
		t.Fatal(err)
	}
	return tx.Commit(ctx)
}

// grantRow reads the name of row id in table through alice on branch.
func grantRow(t *testing.T, db *engine.Database, branch, tableName string, id int64) string {
	t.Helper()
	tx := txnBegin(t, grantSession(t, db, alice, branch))
	defer func() { _ = tx.Rollback(ctx) }()
	row, ok, err := txnTable(t, tx, tableName).Get(ctx, engine.Key{id})
	if err != nil || !ok {
		t.Fatalf("row %d of %s on %s: %v, %v", id, tableName, branch, ok, err)
	}
	return row[2].(string)
}

// E4: a principal without Write on main gets ErrPermissionDenied on a
// transaction's commit, and the working set is unchanged.
func TestACommitWithoutWriteOnMainIsDenied(t *testing.T) {
	db, g := grantDB(t)
	grantOK(t, g.Grant("user:bob", engine.BranchScope("main"), engine.PermRead, engine.PermCommit))
	s := grantSession(t, db, bob, "main")
	before := txnWS(t, s)
	if err := grantUpdate(t, s, "people", 1, "mallory"); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Fatalf("a commit by bob, without Write on main, = %v, want ErrPermissionDenied", err)
	}
	if got := txnWS(t, s); got != before {
		t.Errorf("a denied commit changed the working set (%s, want %s)", got.Short(), before.Short())
	}
	if got := grantRow(t, db, "main", "people", 1); got != "ada" {
		t.Errorf("row 1 is %q after a denied commit, want ada", got)
	}
	grantOK(t, g.Grant("user:bob", engine.BranchScope("main"), engine.PermWrite))
	if err := grantUpdate(t, s, "people", 1, "mallory"); err != nil {
		t.Fatalf("positive control: with Write on main the commit = %v", err)
	}
}

// E4: authorization is asked on every call: a grant revoked mid-session
// blocks the session's next call, a transaction's commit and then its
// begin, and nothing is written.
func TestAGrantRevokedMidSessionBlocksTheNextCall(t *testing.T) {
	db, g := grantDB(t)
	grantOK(t, g.Grant("user:bob", engine.BranchScope("main"), engine.PermRead, engine.PermWrite))
	s := grantSession(t, db, bob, "main")
	if err := grantUpdate(t, s, "people", 1, "one"); err != nil {
		t.Fatalf("positive control: bob's first commit = %v", err)
	}
	grantOK(t, g.Revoke("user:bob", engine.BranchScope("main"), engine.PermWrite))
	before := txnWS(t, s)
	if err := grantUpdate(t, s, "people", 1, "two"); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Fatalf("a commit after Write was revoked mid-session = %v, want ErrPermissionDenied", err)
	}
	if got := txnWS(t, s); got != before {
		t.Error("a commit after the revocation changed the working set")
	}
	grantOK(t, g.Revoke("user:bob", engine.BranchScope("main"), engine.PermRead))
	if _, err := s.Begin(ctx); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Errorf("Begin after Read was revoked mid-session = %v, want ErrPermissionDenied", err)
	}
	if got := grantRow(t, db, "main", "people", 1); got != "one" {
		t.Errorf("row 1 is %q, want one: only the commit before the revocation", got)
	}
}

// A protected main takes no direct write, whatever is granted, while a
// merge from a feature branch and its commit go in (the core's Merge and
// CommitWorkingSet, which the session's Merge will call).
func TestAProtectedMainTakesMergesNotDirectWrites(t *testing.T) {
	db, g := grantDB(t)
	feature := grantSession(t, db, alice, "feature")
	tx := txnBegin(t, feature)
	if _, err := txnTable(t, tx, "people").Insert(ctx, txnPerson(3, "cyd", 30)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.CommitWorkingSet(ctx, feature, "cyd"); err != nil {
		t.Fatal(err)
	}
	grantOK(t, g.Grant("user:bob", engine.DatabaseScope(), engine.PermRead, engine.PermWrite, engine.PermCommit, engine.PermMerge))
	grantOK(t, g.Protect("main"))
	s := grantSession(t, db, bob, "main")
	before := txnWS(t, s)
	if err := grantUpdate(t, s, "people", 1, "mallory"); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Fatalf("a direct write to a protected main = %v, want ErrPermissionDenied", err)
	}
	if got := txnWS(t, s); got != before {
		t.Error("a direct write to a protected main changed its working set")
	}
	n, err := engine.MergeBranch(ctx, s, "feature")
	if err != nil || n != 0 {
		t.Fatalf("merging feature into a protected main = %d conflicts, %v; want a clean merge", n, err)
	}
	if err := engine.CommitWorkingSet(ctx, s, "merge feature"); err != nil {
		t.Fatalf("committing the merge into a protected main: %v", err)
	}
	if got := grantRow(t, db, "main", "people", 3); got != "cyd" {
		t.Errorf("row 3 on main is %q after the merge, want cyd", got)
	}
	g.Unprotect("main")
	if err := grantUpdate(t, s, "people", 1, "mallory"); err != nil {
		t.Errorf("positive control: a direct write to main unprotected = %v", err)
	}
}

// Grants per table: Write on one table of main lets a transaction change
// that table; a transaction that also changes another table is refused
// whole, nothing written, and so is one that changes only the other.
func TestWriteOnOneTableWritesThatTableOnly(t *testing.T) {
	db, g := grantDB(t)
	grantOK(t, g.Grant("user:bob", engine.BranchScope("main"), engine.PermRead))
	grantOK(t, g.Grant("user:bob", engine.ObjectScope("main", "people"), engine.PermWrite))
	s := grantSession(t, db, bob, "main")
	if err := grantUpdate(t, s, "people", 1, "ann"); err != nil {
		t.Fatalf("a commit to the table bob may write = %v", err)
	}
	before := txnWS(t, s)
	tx := txnBegin(t, s)
	if err := txnTable(t, tx, "people").Update(ctx, engine.Key{int64(2)}, txnPerson(2, "bea", 40)); err != nil {
		t.Fatal(err)
	}
	if err := txnTable(t, tx, "pets").Update(ctx, engine.Key{int64(1)}, txnPerson(1, "max", 3)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Fatalf("a commit that also changes pets = %v, want ErrPermissionDenied", err)
	}
	if err := grantUpdate(t, s, "pets", 1, "max"); !errors.Is(err, engine.ErrPermissionDenied) {
		t.Fatalf("a commit that changes only pets = %v, want ErrPermissionDenied", err)
	}
	if got := txnWS(t, s); got != before {
		t.Error("a refused commit changed the working set")
	}
	if got := grantRow(t, db, "main", "people", 2); got != "bob" {
		t.Errorf("row 2 of people is %q, want bob: the refused commit wrote nothing", got)
	}
	if got := grantRow(t, db, "main", "pets", 1); got != "rex" {
		t.Errorf("row 1 of pets is %q, want rex", got)
	}
}

// Revoking from a principal that was never granted anything takes nothing
// back and breaks nothing: a later grant to it works as any other.
func TestRevokingFromAStrangerIsANoOp(t *testing.T) {
	var g engine.Grants
	grantOK(t, g.Revoke("user:nobody", engine.DatabaseScope(), engine.PermRead))
	grantOK(t, g.Grant("user:nobody", engine.DatabaseScope(), engine.PermRead))
	if !grantAllows(t, &g, engine.Principal{ID: "user:nobody"}, engine.ActionRead, "repo") {
		t.Error("a grant after a revoke of nothing does not hold")
	}
}
