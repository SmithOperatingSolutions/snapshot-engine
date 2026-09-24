package engine_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// E4: errors to callers carry no keys, values or rows. A row whose value
// does not fit its column, a kv key too long to be a key, a document that
// is not JSON and a row that is not there, each holding a customer's data,
// fail with the engine error and a correlation id; the caller's error
// holds none of the data, the log holds it under the id.
func TestTransactionErrorsCarryNoData(t *testing.T) {
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
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	people, err := tx.CreateTable(ctx, "people", txnPeople())
	if err != nil {
		t.Fatal(err)
	}
	cache, err := tx.CreateKV(ctx, "cache")
	if err != nil {
		t.Fatal(err)
	}
	users, err := tx.CreateCollection(ctx, "users")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "4242-4242-4242-4242"
	for name, tc := range map[string]struct {
		err  error
		want error
	}{
		"a value that does not fit": {func() error { _, err := people.Insert(ctx, txnPerson(1, strings.Repeat(secret, 20), 30)); return err }(), engine.ErrInvalid},
		"a key too long":            {cache.Set(ctx, []byte(strings.Repeat(secret, 300)), engine.Value{Kind: 1, Bytes: []byte("x")}), engine.ErrInvalid},
		"a document not JSON":       {users.PutJSON(ctx, []byte("u1"), []byte(`{"card": "`+secret+`"`)), engine.ErrInvalid},
		"a row that is not there":   {people.Update(ctx, engine.Key{int64(4242424242424242)}, txnPerson(4242424242424242, "x", 1)), engine.ErrNotFound},
	} {
		if !errors.Is(tc.err, tc.want) {
			t.Errorf("%s: %v, want %v", name, tc.err, tc.want)
			continue
		}
		var e *engine.Error
		if !errors.As(tc.err, &e) {
			t.Errorf("%s: the caller's error %q carries no correlation id", name, tc.err)
			continue
		}
		if strings.Contains(tc.err.Error(), secret) || strings.Contains(tc.err.Error(), "4242424242424242") {
			t.Errorf("%s: the caller's error %q holds the customer's data", name, tc.err)
		}
		if log.entries[e.Correlation] == "" {
			t.Errorf("%s: nothing was logged under %s", name, e.Correlation)
		}
	}
}
