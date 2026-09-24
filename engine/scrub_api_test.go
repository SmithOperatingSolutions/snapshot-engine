package engine_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// Engine Spec, security: errors to callers carry no keys, values or names;
// the details go to the logger under the correlation id the error carries.
// Through the API: a session on a branch named for a customer, a checkout
// of one, a diff of an object named for one and a log from one each fail
// with the engine error and an id, never the name, and the logger holds
// the name under that id.
func TestTheAPIsErrorsCarryNoNames(t *testing.T) {
	log := &scrubLog{}
	o := dbOptions(t)
	o.Logger = log
	db, err := engine.Create(ctx, alice, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := db.Session(ctx, alice, "main")
	if err != nil {
		t.Fatal(err)
	}
	head, err := s.Commit(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "customer-4411"
	for name, call := range map[string]func() error{
		"Session":  func() error { _, err := db.Session(ctx, alice, secret); return err },
		"Checkout": func() error { return s.Checkout(ctx, secret) },
		"Diff": func() error {
			_, err := s.Diff(ctx, engine.Ref(head.String()), engine.Ref(head.String()), "orders/"+secret)
			return err
		},
		"Log": func() error { _, err := s.Log(ctx, engine.Ref(secret), 10); return err },
	} {
		err := call()
		if !errors.Is(err, engine.ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
			continue
		}
		var e *engine.Error
		if !errors.As(err, &e) {
			t.Errorf("%s: the caller's error %q carries no correlation id", name, err)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the caller's error %q names %s", name, err, secret)
		}
		if !strings.Contains(log.entries[e.Correlation], secret) {
			t.Errorf("%s: the log under %s holds %q, want the details", name, e.Correlation, log.entries[e.Correlation])
		}
	}
}
