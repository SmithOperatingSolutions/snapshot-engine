package main

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// The data a workload writes: deterministic from an id, with the entropy of
// real text (no two rows share a name), so compression and deduplication
// see what production would give them.

const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 "

func text(seed uint64, n int) string {
	var b strings.Builder
	b.Grow(n)
	x := seed*0x9E3779B97F4A7C15 + 1
	for i := 0; i < n; i++ {
		x ^= x >> 30
		x *= 0xBF58476D1CE4E5B9
		x ^= x >> 27
		b.WriteByte(letters[x%uint64(len(letters))])
	}
	return b.String()
}

// people is the OLTP table: an int8 key, six columns of mixed types (text of
// about 100 bytes among them) and a secondary index on city.
func peopleSchema() engine.Schema {
	return engine.Schema{
		Columns: []engine.Column{
			{Tag: 1, Name: "id", Type: engine.TypeInt8},
			{Tag: 2, Name: "name", Type: engine.TypeText},
			{Tag: 3, Name: "age", Type: engine.TypeInt4},
			{Tag: 4, Name: "score", Type: engine.TypeFloat8},
			{Tag: 5, Name: "active", Type: engine.TypeBool},
			{Tag: 6, Name: "city", Type: engine.TypeText},
			{Tag: 7, Name: "balance", Type: engine.TypeInt8},
		},
		PrimaryKey: []engine.Tag{1},
		Indexes:    []engine.Index{{Tag: 10, Columns: []engine.Tag{6}}},
	}
}

const cities = 1000

func city(i int64) string { return fmt.Sprintf("city-%04d", i%cities) }

func person(id int64) engine.Row {
	return engine.Row{
		1: id,
		2: text(uint64(id), 100),
		3: int32(18 + id%70),
		4: float64(id%1000) / 7,
		5: id%3 == 0,
		6: city(id),
		7: int64(1000),
	}
}

const personBytes = 8 + 100 + 4 + 8 + 1 + 9 + 8

func kvKey(i int) []byte      { return []byte(fmt.Sprintf("k%08d", i)) }
func counterKey(i int) []byte { return []byte(fmt.Sprintf("c%06d", i)) }

func kvValue(i int) engine.Value {
	return engine.Value{Kind: engine.ValueBytes, Bytes: []byte(text(uint64(i)+1<<40, 100))}
}

func docID(i int) []byte { return []byte(fmt.Sprintf("d%07d", i)) }

// docJSON is a record of about 1 KB: a name, an email, an address, tags,
// eight numeric fields the partial updates touch, and a bio.
func docJSON(i int) []byte {
	seed := uint64(i) + 2<<40
	return []byte(fmt.Sprintf(`{"name":%q,"email":"user%d@example.com","address":{"street":%q,"city":%q,"zip":"%05d"},"tags":["t%d","t%d","t%d","t%d","t%d"],"n1":0,"n2":0,"n3":0,"n4":0,"n5":0,"n6":0,"n7":0,"n8":0,"bio":%q}`,
		text(seed, 20), i, text(seed+1, 30), city(int64(i)), i%100000, i%7, i%11, i%13, i%17, i%19, text(seed+2, 700)))
}

// bump returns doc with its numeric field named field increased by one,
// every other field as it was: a partial update, as an adapter would make
// it from a Node (the engine re-exports Node but no helpers to edit one;
// this edits its exported fields).
func bump(doc engine.Node, field string) (engine.Node, bool) {
	out := doc
	out.Fields = append(out.Fields[:0:0], doc.Fields...)
	for i, f := range out.Fields {
		if f.Name != field {
			continue
		}
		n, err := strconv.Atoi(f.Value.Number)
		if err != nil {
			return doc, false
		}
		out.Fields[i].Value.Number = strconv.Itoa(n + 1)
		return out, true
	}
	return doc, false
}

// newRand is a workload's random source: which keys it touches and which
// operation comes next, never a secret.
func newRand(seed1, seed2 uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed1, seed2)) //nolint:gosec // G404: workload key choice, not a secret
}

// tally counts what happened to one goroutine's transactions.
type tally struct {
	ops, retries, serial, gaveUp, errors uint64
	bytes                                int64
	firstErr                             string
	lat, commit                          hist
	live                                 *atomic.Uint64 // when set, counts retries as they happen (W8's tick line)
	incs, docIncs                        int64          // increments committed: table balances, document fields
}

func (t *tally) fail(err error) {
	t.errors++
	if t.firstErr == "" {
		t.firstErr = err.Error()
	}
}

func (t *tally) merge(o *tally) {
	t.ops += o.ops
	t.retries += o.retries
	t.serial += o.serial
	t.gaveUp += o.gaveUp
	t.errors += o.errors
	t.bytes += o.bytes
	t.incs += o.incs
	t.docIncs += o.docIncs
	if t.firstErr == "" {
		t.firstErr = o.firstErr
	}
	t.lat.merge(&o.lat)
	t.commit.merge(&o.commit)
}

func (t *tally) result(w, phase, unit string, dur time.Duration) result {
	return result{workload: w, phase: phase, unit: unit, ops: t.ops, bytes: t.bytes, dur: dur,
		lat: &t.lat, commit: &t.commit, retries: t.retries, serial: t.serial, gaveUp: t.gaveUp,
		errors: t.errors, firstErr: t.firstErr}
}

// txn runs f in a transaction on sess as an application would: a
// serialization failure retries the whole transaction after a short random
// backoff, up to maxRetries. The latency recorded is the application's,
// retries included; the commit latency is the last attempt's commit alone.
// A read-only transaction rolls back instead of committing.
func (s *suite) txn(sess *engine.Session, t *tally, rng *rand.Rand, readOnly bool, f func(tx *engine.Txn) error) bool {
	start := time.Now()
	for attempt := 0; ; attempt++ {
		tx, err := sess.Begin(ctx)
		if err != nil {
			t.fail(err)
			return false
		}
		if err := f(tx); err != nil {
			_ = tx.Rollback(ctx)
			if !errors.Is(err, engine.ErrSerialization) {
				t.fail(err)
				return false
			}
		} else {
			cs := time.Now()
			if readOnly {
				err = tx.Rollback(ctx)
			} else {
				err = tx.Commit(ctx)
			}
			end := time.Now()
			if err == nil {
				t.lat.add(end.Sub(start))
				if !readOnly {
					t.commit.add(end.Sub(cs))
				}
				return true
			}
			if !errors.Is(err, engine.ErrSerialization) {
				t.fail(err)
				return false
			}
		}
		t.serial++
		if attempt+1 >= s.sc.maxRetries {
			t.gaveUp++
			return false
		}
		t.retries++
		if t.live != nil {
			t.live.Add(1)
		}
		time.Sleep(time.Duration(rng.IntN(1+attempt)) * 200 * time.Microsecond)
	}
}

func (s *suite) session(branch string) (*engine.Session, error) {
	return s.db.Session(ctx, me, branch)
}
