package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/auth"
)

// A batch's own calls to the core are made as one member, so the core's
// questions are answered from what the batch's members were each allowed:
// exactly those questions, and nothing else. Any other question, and any
// question asked without the batch's context, goes to the database's
// authorizer, which here allows nothing; with none, everything is denied.
func TestABatchIsAllowedOnlyWhatItsMembersWere(t *testing.T) {
	ctx := context.Background()
	bob := Principal{ID: "user:bob"}
	a := batchAuthorizer{inner: &Grants{}}
	g := &granted{}
	g.allow(auth.Write, "path:main:people")
	if err := a.Authorize(g.on(ctx), bob, auth.Write, "path:main:people"); err != nil {
		t.Fatalf("a question a member of the batch was allowed = %v, want allowed: the batch's swap would fail every member", err)
	}
	for _, q := range []question{
		{auth.Write, "path:main:pets"},  // another object
		{auth.Read, "path:main:people"}, // another action
		{auth.Write, "path:dev:people"}, // another branch
		{auth.Write, "branch:main"},     // the branch as a whole
	} {
		if err := a.Authorize(g.on(ctx), bob, q.a, q.resource); !errors.Is(err, auth.ErrDenied) {
			t.Errorf("under a batch allowed Write on people, action %d on %s = %v, want ErrDenied: the batch would write what no member was allowed to", q.a, q.resource, err)
		}
	}
	if err := a.Authorize(ctx, bob, auth.Write, "path:main:people"); !errors.Is(err, auth.ErrDenied) {
		t.Errorf("the batch's question asked without the batch's context = %v, want ErrDenied: a caller outside the queue is allowed a batch's grants", err)
	}
	if err := (batchAuthorizer{}).Authorize(ctx, bob, auth.Read, "repo"); !errors.Is(err, auth.ErrDenied) {
		t.Errorf("a question with no authorizer = %v, want ErrDenied: no authorizer must mean deny", err)
	}
	if err := (batchAuthorizer{inner: auth.AllowAll{}}).Authorize(ctx, bob, auth.Read, "repo"); err != nil {
		t.Errorf("a question the database's authorizer allows = %v, want allowed", err)
	}
}
