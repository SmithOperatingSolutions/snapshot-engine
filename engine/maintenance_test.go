package engine_test

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-core/core/blob"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

const collectGrace = time.Hour

// collectDB is a database whose clock a test moves forward, and the clock.
func collectDB(t *testing.T, o engine.Options) (*engine.Database, *atomic.Int64) {
	t.Helper()
	var ahead atomic.Int64
	o.Clock = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
	db, err := engine.Create(ctx, alice, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, &ahead
}

// collectPacks is how many packs the backend holds.
func collectPacks(t *testing.T, bs engine.Blobs) int {
	t.Helper()
	infos, err := bs.List(ctx, "packs/", "", blob.MaxListPage)
	if err != nil {
		t.Fatal(err)
	}
	return len(infos)
}

// collectRows makes table name with rows 1..n in a transaction of s.
func collectRows(t *testing.T, tx *engine.Txn, name string, n int) {
	t.Helper()
	tb, err := tx.CreateTable(ctx, name, txnPeople())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if _, err := tb.Insert(ctx, txnPerson(int64(i), "n", int64(i))); err != nil {
			t.Fatal(err)
		}
	}
}

// Collect reclaims what nothing reaches, a grace window after it first found
// it unreachable: a table made and then dropped in the working set, never in
// a commit, is condemned by one collection and deleted by the next a grace
// window later, while every live object reads as before, from this database
// and from one opened again.
func TestCollectReclaimsWhatNothingReaches(t *testing.T) {
	o := dbOptions(t)
	db, ahead := collectDB(t, o)
	s := txnSession(t, db, "main")
	tx := txnBegin(t, s)
	collectRows(t, tx, "gone", 200)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx = txnBegin(t, s)
	collectRows(t, tx, "kept", 50)
	if err := tx.Drop(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	before := collectPacks(t, o.Blobs)

	first, err := db.Collect(ctx, alice, collectGrace)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if first.Condemned == 0 || first.DeletedPacks != 0 {
		t.Fatalf("the first collection condemned %d packs and deleted %d, want the dropped table's pack condemned and nothing deleted within the grace window", first.Condemned, first.DeletedPacks)
	}
	if first.Live == 0 || first.Rounds == 0 {
		t.Errorf("the first collection reports %d live chunks in %d rounds", first.Live, first.Rounds)
	}
	if mid := collectPacks(t, o.Blobs); mid != before+first.Repacked {
		t.Errorf("the backend holds %d packs after the first collection, want %d: %d, plus one for each pack it repacked, nothing deleted", mid, before+first.Repacked, before)
	}
	ahead.Store(int64(collectGrace + time.Minute))
	second, err := db.Collect(ctx, alice, collectGrace)
	if err != nil {
		t.Fatalf("Collect a grace window later: %v", err)
	}
	if second.DeletedPacks == 0 {
		t.Fatalf("a grace window later the collection deleted %d packs, want the dropped table's", second.DeletedPacks)
	}
	if second.DeletedIndexes == 0 {
		t.Errorf("the collection deleted %d packs and no index object: the index still lists what is gone", second.DeletedPacks)
	}
	if after := collectPacks(t, o.Blobs); after != before+first.Repacked-second.DeletedPacks || after >= before {
		t.Errorf("the backend holds %d packs after deleting %d of %d (%d repacked): the space is not reclaimed", after, second.DeletedPacks, before+first.Repacked, first.Repacked)
	}

	for _, d := range []*engine.Database{db, collectReopen(t, o, ahead)} {
		tx := txnBegin(t, txnSession(t, d, "main"))
		kept, err := tx.Table(ctx, "kept")
		if err != nil {
			t.Fatalf("the live table after the collection: %v", err)
		}
		n := 0
		if err := kept.Scan(ctx, func(engine.Key, engine.Row) (bool, error) { n++; return true, nil }); err != nil {
			t.Fatalf("scanning the live table after the collection: %v", err)
		}
		if n != 50 {
			t.Errorf("the live table holds %d rows after the collection, want 50", n)
		}
		if _, err := tx.Table(ctx, "gone"); !errors.Is(err, engine.ErrNotFound) {
			t.Errorf("the dropped table after the collection = %v, want ErrNotFound", err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

// collectReopen opens the database on o's backend again, on the same clock.
func collectReopen(t *testing.T, o engine.Options, ahead *atomic.Int64) *engine.Database {
	t.Helper()
	o.Clock = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
	db, err := engine.Open(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// Collecting is the one operation that deletes: it takes Admin. A
// principal without it is refused, and nothing is condemned or deleted; a
// negative grace window is refused; a closed database collects nothing;
// every refusal reaches the caller scrubbed.
func TestCollectIsRefusedWhereItMayNotRun(t *testing.T) {
	o := dbOptions(t)
	g := &engine.Grants{}
	grantOK(t, g.Grant(alice.ID, engine.DatabaseScope(), engine.PermRead, engine.PermWrite, engine.PermCommit, engine.PermMerge, engine.PermBranchAdmin, engine.PermAdmin))
	o.Authorizer = g
	var ahead atomic.Int64
	o.Clock = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }
	db, err := engine.Create(ctx, alice, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := txnSession(t, db, "main")
	tx := txnBegin(t, s)
	collectRows(t, tx, "gone", 20)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx = txnBegin(t, s)
	if err := tx.Drop(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	grantOK(t, g.Revoke(alice.ID, engine.DatabaseScope(), engine.PermAdmin)) // creating took Admin; collecting now may not
	packs := collectPacks(t, o.Blobs)
	for range 2 {
		_, err := db.Collect(ctx, alice, collectGrace)
		var e *engine.Error
		if !errors.Is(err, engine.ErrPermissionDenied) || !errors.As(err, &e) {
			t.Fatalf("Collect without Admin = %v, want ErrPermissionDenied, scrubbed", err)
		}
		ahead.Add(int64(collectGrace + time.Minute))
	}
	if got := collectPacks(t, o.Blobs); got != packs {
		t.Fatalf("refused collections changed the backend's packs from %d to %d", packs, got)
	}
	grantOK(t, g.Grant(alice.ID, engine.DatabaseScope(), engine.PermAdmin))
	if rep, err := db.Collect(ctx, alice, collectGrace); err != nil || rep.Condemned == 0 || rep.DeletedPacks != 0 {
		t.Errorf("positive control, the first collection with Admin = %+v, %v: the refused ones must have condemned nothing", rep, err)
	}
	if _, err := db.Collect(ctx, alice, -time.Second); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("Collect with a negative grace window = %v, want ErrInvalid", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collect(ctx, alice, collectGrace); !errors.Is(err, engine.ErrClosed) {
		t.Errorf("Collect on a closed database = %v, want ErrClosed", err)
	}
}

// A collection runs beside open sessions and transactions. A transaction
// whose write counted on data the database had stopped reaching, which a
// collection then deleted a grace window later, cannot publish: its commit
// is ErrSessionLost, nothing of it is written, and the database refuses
// every further write until it is opened again, when the same write goes
// through. (A transaction that lives longer than the grace window is the
// one that can meet this.)
func TestAWriteACollectionReclaimedLosesTheSession(t *testing.T) {
	o := dbOptions(t)
	db, ahead := collectDB(t, o)
	s := txnSession(t, db, "main")
	tx := txnBegin(t, s)
	collectRows(t, tx, "t", 30)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	drop := txnBegin(t, s)
	if err := drop.Drop(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	if err := drop.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	slow := txnBegin(t, s) // writes what t held, counting on the chunks already stored
	collectRows(t, slow, "u", 30)
	u, err := slow.Table(ctx, "u")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := u.Get(ctx, engine.Key{int64(1)}); err != nil { // the write reaches the store
		t.Fatal(err)
	}
	if _, err := db.Collect(ctx, alice, collectGrace); err != nil {
		t.Fatal(err)
	}
	ahead.Store(int64(collectGrace + time.Minute))
	if rep, err := db.Collect(ctx, alice, collectGrace); err != nil || rep.DeletedPacks == 0 {
		t.Fatalf("fixture: the second collection = %+v, %v, want t's pack deleted", rep, err)
	}
	err = slow.Commit(ctx)
	var e *engine.Error
	if !errors.Is(err, engine.ErrSessionLost) || !errors.As(err, &e) || strings.Contains(err.Error(), "packs/") {
		t.Fatalf("committing a write a collection reclaimed = %v, want ErrSessionLost, scrubbed", err)
	}
	again := txnBegin(t, txnSession(t, collectReopen(t, o, ahead), "main"))
	if _, err := again.Table(ctx, "u"); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("the lost transaction's table after reopening = %v, want ErrNotFound: nothing of it may be written", err)
	}
	collectRows(t, again, "u", 30)
	if err := again.Commit(ctx); err != nil {
		t.Errorf("the same write on the database opened again: %v", err)
	}
}

// A collection given no grace window keeps what it found unreachable for
// DefaultGrace, the Storage Core Spec's seven days: a day later it deletes
// nothing, seven days later it deletes what nothing reaches.
func TestCollectKeepsUnreachableDataForTheDefaultGraceWindow(t *testing.T) {
	if engine.DefaultGrace != 7*24*time.Hour {
		t.Fatalf("DefaultGrace = %v, want the Storage Core Spec's seven days", engine.DefaultGrace)
	}
	o := dbOptions(t)
	db, ahead := collectDB(t, o)
	s := txnSession(t, db, "main")
	tx := txnBegin(t, s)
	collectRows(t, tx, "gone", 20)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx = txnBegin(t, s)
	if err := tx.Drop(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if rep, err := db.Collect(ctx, alice, 0); err != nil || rep.Condemned == 0 {
		t.Fatalf("the first collection = %+v, %v, want the dropped table's pack condemned", rep, err)
	}
	ahead.Store(int64(2 * 24 * time.Hour))
	if rep, err := db.Collect(ctx, alice, 0); err != nil || rep.DeletedPacks != 0 {
		t.Fatalf("two days later a collection with no grace window = %+v, %v: it deleted what it must keep for seven days", rep, err)
	}
	ahead.Store(int64(engine.DefaultGrace + time.Minute))
	if rep, err := db.Collect(ctx, alice, 0); err != nil || rep.DeletedPacks == 0 {
		t.Errorf("seven days later a collection with no grace window = %+v, %v, want the dropped table's pack deleted", rep, err)
	}
}
