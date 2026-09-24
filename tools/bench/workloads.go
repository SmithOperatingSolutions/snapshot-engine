package main

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// --- W1: bulk load ---

func (s *suite) w1() error {
	if err := s.loadTable("people", s.sc.rows, s.sc.tableBatch, true); err != nil {
		return err
	}
	if err := s.loadTable("people1k", s.sc.rows1k, s.sc.table1k, true); err != nil {
		return err
	}
	if err := s.vcsCommit("W1", "session commit after the loads"); err != nil {
		return err
	}
	if err := s.loadKV(true); err != nil {
		return err
	}
	return s.loadDocs(true)
}

// loadTable creates table name and inserts rows in batches, one transaction
// per batch, reporting when report is set.
func (s *suite) loadTable(name string, rows, batch int, report bool) error {
	if s.loaded[name] {
		return nil
	}
	sess, err := s.session("main")
	if err != nil {
		return err
	}
	defer sess.Close()
	rng := newRand(1, 1)
	var t tally
	start := time.Now()
	for lo := 0; lo < rows; lo += batch {
		hi := min(lo+batch, rows)
		ok := s.txn(sess, &t, rng, false, func(tx *engine.Txn) error {
			var tb *engine.Table
			var err error
			if lo == 0 {
				tb, err = tx.CreateTable(ctx, name, peopleSchema())
			} else {
				tb, err = tx.Table(ctx, name)
			}
			if err != nil {
				return err
			}
			for id := lo; id < hi; id++ {
				if _, err := tb.Insert(ctx, person(int64(id))); err != nil {
					return err
				}
			}
			return nil
		})
		if !ok {
			return fmt.Errorf("loading %s at row %d: %s", name, lo, t.firstErr)
		}
		t.ops += uint64(hi - lo)
		t.bytes += int64(hi-lo) * personBytes
	}
	s.loaded[name] = true
	if report {
		r := t.result("W1", fmt.Sprintf("table %s %d rows @%d/tx", name, rows, batch), "rows", time.Since(start))
		r.note = "lat = per-batch txn; c = its commit"
		s.record(r)
	}
	return nil
}

// vcsCommit records main's working set as a commit and reports its latency.
func (s *suite) vcsCommit(w, phase string) error {
	sess, err := s.session("main")
	if err != nil {
		return err
	}
	defer sess.Close()
	var h hist
	start := time.Now()
	if _, err := sess.Commit(ctx, phase); err != nil {
		return err
	}
	h.add(time.Since(start))
	s.record(result{workload: w, phase: phase, unit: "commit", ops: 1, dur: time.Since(start), lat: &h})
	return nil
}

func (s *suite) loadKV(report bool) error {
	if s.loaded["kv"] {
		return nil
	}
	sess, err := s.session("main")
	if err != nil {
		return err
	}
	defer sess.Close()
	rng := newRand(2, 2)
	var t tally
	start := time.Now()
	for lo := 0; lo < s.sc.kvKeys; lo += s.sc.kvBatch {
		hi := min(lo+s.sc.kvBatch, s.sc.kvKeys)
		ok := s.txn(sess, &t, rng, false, func(tx *engine.Txn) error {
			var m *engine.KV
			var err error
			if lo == 0 {
				if m, err = tx.CreateKV(ctx, "kv"); err != nil {
					return err
				}
				for i := 0; i < s.sc.counters; i++ {
					if err := m.Set(ctx, counterKey(i), engine.Value{Kind: engine.ValueCounter}); err != nil {
						return err
					}
				}
			} else if m, err = tx.KV(ctx, "kv"); err != nil {
				return err
			}
			for i := lo; i < hi; i++ {
				if err := m.Set(ctx, kvKey(i), kvValue(i)); err != nil {
					return err
				}
			}
			return nil
		})
		if !ok {
			return fmt.Errorf("loading kv at %d: %s", lo, t.firstErr)
		}
		t.ops += uint64(hi - lo)
		t.bytes += int64(hi-lo) * (9 + 100)
	}
	s.loaded["kv"] = true
	if report {
		s.record(t.result("W1", fmt.Sprintf("kv %d keys @%d/tx", s.sc.kvKeys, s.sc.kvBatch), "keys", time.Since(start)))
	}
	return nil
}

func (s *suite) loadDocs(report bool) error {
	if s.loaded["docs"] {
		return nil
	}
	sess, err := s.session("main")
	if err != nil {
		return err
	}
	defer sess.Close()
	rng := newRand(3, 3)
	var t tally
	start := time.Now()
	for lo := 0; lo < s.sc.docs; lo += s.sc.docBatch {
		hi := min(lo+s.sc.docBatch, s.sc.docs)
		var n int64
		ok := s.txn(sess, &t, rng, false, func(tx *engine.Txn) error {
			n = 0
			var c *engine.Collection
			var err error
			if lo == 0 {
				c, err = tx.CreateCollection(ctx, "docs")
			} else {
				c, err = tx.Collection(ctx, "docs")
			}
			if err != nil {
				return err
			}
			for i := lo; i < hi; i++ {
				j := docJSON(i)
				n += int64(len(j))
				if err := c.PutJSON(ctx, docID(i), j); err != nil {
					return err
				}
			}
			return nil
		})
		if !ok {
			return fmt.Errorf("loading docs at %d: %s", lo, t.firstErr)
		}
		t.ops += uint64(hi - lo)
		t.bytes += n
	}
	s.loaded["docs"] = true
	if report {
		s.record(t.result("W1", fmt.Sprintf("documents %d @%d/tx", s.sc.docs, s.sc.docBatch), "docs", time.Since(start)))
	}
	return nil
}

// --- W2: point reads, index lookups, scans ---

func (s *suite) w2() error {
	if err := s.loadTable("people", s.sc.rows, s.sc.tableBatch, false); err != nil {
		return err
	}
	for _, conc := range []int{1, 16} {
		t, dur, err := s.parallel(conc, s.sc.readDur, func(sess *engine.Session, rng *rand.Rand, t *tally, deadline time.Time) error {
			tx, err := sess.Begin(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback(ctx) }()
			tb, err := tx.Table(ctx, "people")
			if err != nil {
				return err
			}
			for time.Now().Before(deadline) {
				key := engine.Key{int64(rng.IntN(s.sc.rows))}
				st := time.Now()
				row, ok, err := tb.Get(ctx, key)
				if err != nil {
					return err
				}
				if !ok || row[1] != key[0] {
					return fmt.Errorf("point read of %v: found %v, row %v", key, ok, row[1])
				}
				t.lat.add(time.Since(st))
				t.ops++
				t.bytes += personBytes
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.record(t.result("W2", fmt.Sprintf("point get, %d session(s)", conc), "gets", dur))
	}
	// Index lookups: a random city, about rows/1000 rows each.
	{
		sess, err := s.session("main")
		if err != nil {
			return err
		}
		tx, err := sess.Begin(ctx)
		if err != nil {
			return err
		}
		tb, err := tx.Table(ctx, "people")
		if err != nil {
			return err
		}
		rng := newRand(4, 4)
		var t tally
		var rows uint64
		start := time.Now()
		for i := 0; i < s.sc.lookups; i++ {
			c := city(int64(rng.IntN(cities)))
			st := time.Now()
			err := tb.Lookup(ctx, 10, []any{c}, func(k engine.Key, r engine.Row) (bool, error) {
				if r[6] != c {
					return false, fmt.Errorf("index lookup of %s returned a row of %v", c, r[6])
				}
				rows++
				return true, nil
			})
			if err != nil {
				return err
			}
			t.lat.add(time.Since(st))
			t.ops++
		}
		r := t.result("W2", "index lookup by city", "lookups", time.Since(start))
		r.note = fmt.Sprintf("%.0f rows/lookup, %.0f rows/s", float64(rows)/float64(max(t.ops, 1)), float64(rows)/time.Since(start).Seconds())
		s.record(r)
		// A full scan.
		var n uint64
		start = time.Now()
		err = tb.Scan(ctx, func(k engine.Key, r engine.Row) (bool, error) { n++; return true, nil })
		if err != nil {
			return err
		}
		sd := time.Since(start)
		_ = tx.Rollback(ctx)
		_ = sess.Close()
		if n != uint64(s.sc.rows) {
			return fmt.Errorf("a full scan of people saw %d rows, want %d", n, s.sc.rows)
		}
		s.record(result{workload: "W2", phase: "full scan", unit: "rows", ops: n, bytes: int64(n) * personBytes, dur: sd})
	}
	return nil
}

// parallel runs f on conc goroutines, each with its own session and random
// source, until deadline, and merges their tallies.
func (s *suite) parallel(conc int, dur time.Duration, f func(sess *engine.Session, rng *rand.Rand, t *tally, deadline time.Time) error) (tally, time.Duration, error) {
	tallies := make([]tally, conc)
	errs := make([]error, conc)
	sessions := make([]*engine.Session, conc)
	for i := range sessions {
		var err error
		if sessions[i], err = s.session("main"); err != nil {
			return tally{}, 0, err
		}
	}
	var wg sync.WaitGroup
	start := time.Now()
	deadline := start.Add(dur)
	for i := 0; i < conc; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rng := newRand(uint64(i)+100, uint64(time.Now().UnixNano()))
			errs[i] = f(sessions[i], rng, &tallies[i], deadline)
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	var all tally
	for i := range tallies {
		all.merge(&tallies[i])
		_ = sessions[i].Close()
	}
	return all, elapsed, errors.Join(errs...)
}

// --- W3: OLTP read-modify-write under contention ---

func (s *suite) w3() error {
	if err := s.loadTable("people", s.sc.rows, s.sc.tableBatch, false); err != nil {
		return err
	}
	for _, dist := range []string{"uniform", "zipf1.1"} {
		for _, conc := range s.sc.concs {
			t, dur, err := s.parallel(conc, s.sc.phaseDur, func(sess *engine.Session, rng *rand.Rand, t *tally, deadline time.Time) error {
				pick := s.picker(dist, rng)
				for time.Now().Before(deadline) {
					if s.rmw(sess, rng, pick, t) {
						t.ops++
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			r := t.result("W3", fmt.Sprintf("rmw %s, %d session(s)", dist, conc), "tx", dur)
			r.note = "2-4 reads, 1-2 updates per tx"
			s.record(r)
		}
	}
	return nil
}

// picker chooses row ids: uniform over the table, or Zipf-skewed so a few
// rows take most of the traffic.
func (s *suite) picker(dist string, rng *rand.Rand) func() int64 {
	if dist == "uniform" {
		return func() int64 { return int64(rng.IntN(s.sc.rows)) }
	}
	z := rand.NewZipf(rng, 1.1, 1, uint64(s.sc.rows-1))
	return func() int64 { return int64(z.Uint64()) }
}

// rmw is one OLTP transaction: read 2 to 4 rows, add one to the balance of
// 1 or 2 of them, commit (retrying a serialization failure).
func (s *suite) rmw(sess *engine.Session, rng *rand.Rand, pick func() int64, t *tally) bool {
	return s.txn(sess, t, rng, false, func(tx *engine.Txn) error {
		tb, err := tx.Table(ctx, "people")
		if err != nil {
			return err
		}
		n := 2 + rng.IntN(3)
		upd := 1 + rng.IntN(2)
		for i := 0; i < n; i++ {
			key := engine.Key{pick()}
			row, ok, err := tb.Get(ctx, key)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("row %v is missing", key)
			}
			if i < upd {
				row[7] = row[7].(int64) + 1
				if err := tb.Update(ctx, key, row); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// --- W4: a Redis-shaped kv mix ---

func (s *suite) w4() error {
	if err := s.loadKV(false); err != nil {
		return err
	}
	for _, batch := range []int{1, 100} {
		for _, conc := range s.sc.kvConcs {
			t, dur, err := s.parallel(conc, s.sc.phaseDur, func(sess *engine.Session, rng *rand.Rand, t *tally, deadline time.Time) error {
				for time.Now().Before(deadline) {
					s.kvOps(sess, rng, t, batch)
				}
				return nil
			})
			if err != nil {
				return err
			}
			r := t.result("W4", fmt.Sprintf("kv 70/20/10 GET/SET/INCR, %d/tx, %d sess", batch, conc), "ops", dur)
			r.note = "lat = per tx"
			s.record(r)
		}
	}
	return nil
}

// kvOps is one transaction of batch operations: GET 70%, SET 20%, INCR 10%
// (a GET-only transaction rolls back).
func (s *suite) kvOps(sess *engine.Session, rng *rand.Rand, t *tally, batch int) {
	kinds := make([]int, batch)
	readOnly := true
	for i := range kinds {
		kinds[i] = rng.IntN(100)
		if kinds[i] >= 70 {
			readOnly = false
		}
	}
	ok := s.txn(sess, t, rng, readOnly, func(tx *engine.Txn) error {
		m, err := tx.KV(ctx, "kv")
		if err != nil {
			return err
		}
		for _, k := range kinds {
			switch {
			case k < 70:
				i := rng.IntN(s.sc.kvKeys)
				v, ok, err := m.Get(ctx, kvKey(i))
				if err != nil {
					return err
				}
				if !ok || v.Kind != engine.ValueBytes {
					return fmt.Errorf("GET %s: found %v, kind %d", kvKey(i), ok, v.Kind)
				}
			case k < 90:
				i := rng.IntN(s.sc.kvKeys)
				if err := m.Set(ctx, kvKey(i), kvValue(rng.IntN(1<<30))); err != nil {
					return err
				}
			default:
				key := counterKey(rng.IntN(s.sc.counters))
				v, ok, err := m.Get(ctx, key)
				if err != nil {
					return err
				}
				if !ok || v.Kind != engine.ValueCounter {
					return fmt.Errorf("INCR %s: found %v, kind %d", key, ok, v.Kind)
				}
				v.Counter++
				if err := m.Set(ctx, key, v); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if ok {
		t.ops += uint64(batch)
	}
}

// --- W5: document partial updates ---

func (s *suite) w5() error {
	if err := s.loadDocs(false); err != nil {
		return err
	}
	for _, hot := range []int{s.sc.docs, 100} {
		label := "uniform"
		if hot < s.sc.docs {
			label = fmt.Sprintf("hot %d records", hot)
		}
		t, dur, err := s.parallel(s.sc.docConc, s.sc.phaseDur, func(sess *engine.Session, rng *rand.Rand, t *tally, deadline time.Time) error {
			for time.Now().Before(deadline) {
				if s.docBump(sess, rng, t, hot) {
					t.ops++
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		r := t.result("W5", fmt.Sprintf("doc field update, %s, %d sess", label, s.sc.docConc), "tx", dur)
		s.record(r)
	}
	return nil
}

// docBump reads a record and adds one to one of its eight numeric fields.
func (s *suite) docBump(sess *engine.Session, rng *rand.Rand, t *tally, among int) bool {
	id := docID(rng.IntN(among))
	field := fmt.Sprintf("n%d", 1+rng.IntN(8))
	return s.txn(sess, t, rng, false, func(tx *engine.Txn) error {
		c, err := tx.Collection(ctx, "docs")
		if err != nil {
			return err
		}
		doc, ok, err := c.Get(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("record %s is missing", id)
		}
		next, ok := bump(doc, field)
		if !ok {
			return fmt.Errorf("record %s has no field %s", id, field)
		}
		return c.Put(ctx, id, next)
	})
}

// --- W6: version control at scale ---

func (s *suite) w6() error {
	if err := s.loadTable("people", s.sc.rows, s.sc.tableBatch, false); err != nil {
		return err
	}
	sess, err := s.session("main")
	if err != nil {
		return err
	}
	defer sess.Close()
	rng := newRand(6, 6)
	if _, err := sess.Commit(ctx, "before W6"); err != nil {
		return err
	}
	// A session commit after one small transaction.
	var ch, th hist
	for i := 0; i < s.sc.commitSamples; i++ {
		var t tally
		if !s.updateRange(sess, rng, &t, int64(i), 1) {
			return fmt.Errorf("a one-row update: %s", t.firstErr)
		}
		th.merge(&t.lat)
		st := time.Now()
		if _, err := sess.Commit(ctx, fmt.Sprintf("sample %d", i)); err != nil {
			return err
		}
		ch.add(time.Since(st))
	}
	s.record(result{workload: "W6", phase: "session commit after a 1-row txn", unit: "commit", ops: uint64(s.sc.commitSamples), dur: ch.sum, lat: &ch, note: fmt.Sprintf("the 1-row txn itself p50 %s ms", ms(th.quantile(.5)))})
	for _, n := range []int{s.sc.vcsSmall, s.sc.vcsLarge} {
		if err := s.mergeAtScale(sess, rng, n); err != nil {
			return err
		}
	}
	return nil
}

// updateRange adds one to the balance of n rows from lo in one transaction.
func (s *suite) updateRange(sess *engine.Session, rng *rand.Rand, t *tally, lo int64, n int) bool {
	return s.txn(sess, t, rng, false, func(tx *engine.Txn) error {
		tb, err := tx.Table(ctx, "people")
		if err != nil {
			return err
		}
		for id := lo; id < lo+int64(n); id++ {
			row, ok, err := tb.Get(ctx, engine.Key{id})
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("row %d is missing", id)
			}
			row[7] = row[7].(int64) + 1
			if err := tb.Update(ctx, engine.Key{id}, row); err != nil {
				return err
			}
		}
		return nil
	})
}

// mergeAtScale branches, changes n rows on each side (disjoint), commits
// both, merges the branch into main and diffs main across the merge.
func (s *suite) mergeAtScale(sess *engine.Session, rng *rand.Rand, n int) error {
	branch := fmt.Sprintf("vcs-%d", n)
	if err := sess.CreateBranch(ctx, branch, ""); err != nil {
		return err
	}
	other, err := s.session(branch)
	if err != nil {
		return err
	}
	defer other.Close()
	var t tally
	start := time.Now()
	if !s.updateRange(sess, rng, &t, 10, n) || !s.updateRange(other, rng, &t, int64(s.sc.rows/2), n) {
		return fmt.Errorf("changing %d rows on each side: %s", n, t.firstErr)
	}
	t.ops, t.bytes = 2, int64(2*n)*personBytes
	s.record(t.result("W6", fmt.Sprintf("txn changing %d rows (x2 branches)", n), "tx", time.Since(start)))
	if _, err := sess.Commit(ctx, "main side"); err != nil {
		return err
	}
	if _, err := other.Commit(ctx, "branch side"); err != nil {
		return err
	}
	before, err := sess.Log(ctx, "main", 1)
	if err != nil {
		return err
	}
	var mh hist
	st := time.Now()
	res, err := sess.Merge(ctx, engine.Ref(branch), "merge "+branch)
	if err != nil {
		return err
	}
	mh.add(time.Since(st))
	if len(res.Conflicts) != 0 {
		return fmt.Errorf("merging %d disjoint changes a side conflicted: %+v", n, res.Conflicts)
	}
	s.record(result{workload: "W6", phase: fmt.Sprintf("merge %d+%d changed rows", n, n), unit: "merge", ops: 1, dur: mh.sum, lat: &mh})
	after, err := sess.Log(ctx, "main", 1)
	if err != nil {
		return err
	}
	var dh hist
	st = time.Now()
	it, err := sess.Diff(ctx, engine.Ref(before[0].Hash.String()), engine.Ref(after[0].Hash.String()), "people")
	if err != nil {
		return err
	}
	var changes uint64
	for {
		_, ok, err := it.Next(ctx)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		changes++
	}
	dh.add(time.Since(st))
	r := result{workload: "W6", phase: fmt.Sprintf("diff across the %d-row merge", n), unit: "changes", ops: changes, dur: dh.sum, lat: &dh}
	if changes == 0 {
		r.note = "no changes seen"
	}
	s.record(r)
	return nil
}

// --- W7: large objects ---

func (s *suite) w7() error {
	sess, err := s.session("main")
	if err != nil {
		return err
	}
	defer sess.Close()
	rng := newRand(7, 7)
	// Wide rows: a 64 KiB text column.
	wide := engine.Schema{Columns: []engine.Column{{Tag: 1, Name: "id", Type: engine.TypeInt8}, {Tag: 2, Name: "body", Type: engine.TypeText}}, PrimaryKey: []engine.Tag{1}}
	body := text(77, s.sc.wideSize)
	var t tally
	start := time.Now()
	const wideBatch = 100
	for lo := 0; lo < s.sc.wideRows; lo += wideBatch {
		hi := min(lo+wideBatch, s.sc.wideRows)
		ok := s.txn(sess, &t, rng, false, func(tx *engine.Txn) error {
			var tb *engine.Table
			var err error
			if lo == 0 {
				tb, err = tx.CreateTable(ctx, "wide", wide)
			} else {
				tb, err = tx.Table(ctx, "wide")
			}
			if err != nil {
				return err
			}
			for id := lo; id < hi; id++ {
				if _, err := tb.Insert(ctx, engine.Row{1: int64(id), 2: fmt.Sprintf("%08d", id) + body[8:]}); err != nil {
					return err
				}
			}
			return nil
		})
		if !ok {
			t.ops = uint64(lo)
			r := t.result("W7", fmt.Sprintf("write %d-KiB rows", s.sc.wideSize>>10), "rows", time.Since(start))
			s.record(r)
			return nil
		}
		t.ops += uint64(hi - lo)
		t.bytes += int64(hi-lo) * int64(s.sc.wideSize)
	}
	s.record(t.result("W7", fmt.Sprintf("write %d-KiB rows @%d/tx", s.sc.wideSize>>10, wideBatch), "rows", time.Since(start)))
	if err := s.readAll(sess, "W7", fmt.Sprintf("read %d-KiB rows (scan)", s.sc.wideSize>>10), func(tx *engine.Txn) (uint64, int64, error) {
		tb, err := tx.Table(ctx, "wide")
		if err != nil {
			return 0, 0, err
		}
		var n uint64
		var b int64
		err = tb.Scan(ctx, func(k engine.Key, r engine.Row) (bool, error) {
			n++
			b += int64(len(r[2].(string)))
			return true, nil
		})
		return n, b, err
	}); err != nil {
		return err
	}
	// Large documents: the largest size that stores, trying 1 MiB first.
	for _, size := range []int{s.sc.bigDocSize - 64, 768 << 10, 512 << 10, 256 << 10} {
		name := fmt.Sprintf("bigdocs%d", size>>10)
		blob := text(78, size)
		var t tally
		start := time.Now()
		ok := true
		for lo := 0; lo < s.sc.bigDocs && ok; lo += 5 {
			hi := min(lo+5, s.sc.bigDocs)
			ok = s.txn(sess, &t, rng, false, func(tx *engine.Txn) error {
				var c *engine.Collection
				var err error
				if lo == 0 {
					c, err = tx.CreateCollection(ctx, name)
				} else {
					c, err = tx.Collection(ctx, name)
				}
				if err != nil {
					return err
				}
				for i := lo; i < hi; i++ {
					if err := c.PutJSON(ctx, docID(i), []byte(fmt.Sprintf(`{"id":%d,"blob":%q}`, i, blob))); err != nil {
						return err
					}
				}
				return nil
			})
			if ok {
				t.ops += uint64(hi - lo)
				t.bytes += int64(hi-lo) * int64(size)
			}
		}
		r := t.result("W7", fmt.Sprintf("write %d-KiB documents @5/tx", size>>10), "docs", time.Since(start))
		s.record(r)
		if ok {
			if err := s.readAll(sess, "W7", fmt.Sprintf("read %d-KiB documents", size>>10), func(tx *engine.Txn) (uint64, int64, error) {
				c, err := tx.Collection(ctx, name)
				if err != nil {
					return 0, 0, err
				}
				var n uint64
				var b int64
				for i := 0; i < s.sc.bigDocs; i++ {
					doc, found, err := c.Get(ctx, docID(i))
					if err != nil {
						return n, b, err
					}
					if !found {
						return n, b, fmt.Errorf("document %d is missing", i)
					}
					n++
					for _, f := range doc.Fields {
						b += int64(len(f.Value.Text))
					}
				}
				return n, b, nil
			}); err != nil {
				return err
			}
			break
		}
		s.note("W7: %d-KiB documents do not store: %s", size>>10, strings.SplitN(t.firstErr, "\n", 2)[0])
	}
	// Large kv values: 256 KiB.
	val := engine.Value{Kind: engine.ValueBytes, Bytes: []byte(text(79, s.sc.bigValSize))}
	t = tally{}
	start = time.Now()
	for lo := 0; lo < s.sc.bigVals; lo += 10 {
		hi := min(lo+10, s.sc.bigVals)
		ok := s.txn(sess, &t, rng, false, func(tx *engine.Txn) error {
			var m *engine.KV
			var err error
			if lo == 0 {
				m, err = tx.CreateKV(ctx, "bigkv")
			} else {
				m, err = tx.KV(ctx, "bigkv")
			}
			if err != nil {
				return err
			}
			for i := lo; i < hi; i++ {
				v := val
				v.Bytes = append([]byte(fmt.Sprintf("%08d", i)), val.Bytes[8:]...)
				if err := m.Set(ctx, kvKey(i), v); err != nil {
					return err
				}
			}
			return nil
		})
		if !ok {
			break
		}
		t.ops += uint64(hi - lo)
		t.bytes += int64(hi-lo) * int64(s.sc.bigValSize)
	}
	s.record(t.result("W7", fmt.Sprintf("write %d-KiB kv values @10/tx", s.sc.bigValSize>>10), "values", time.Since(start)))
	return s.readAll(sess, "W7", fmt.Sprintf("read %d-KiB kv values (scan)", s.sc.bigValSize>>10), func(tx *engine.Txn) (uint64, int64, error) {
		m, err := tx.KV(ctx, "bigkv")
		if err != nil {
			return 0, 0, err
		}
		var n uint64
		var b int64
		err = m.Scan(ctx, nil, func(k []byte, v engine.Value) (bool, error) {
			n++
			b += int64(len(v.Bytes))
			return true, nil
		})
		return n, b, err
	})
}

// readAll times one read-only pass.
func (s *suite) readAll(sess *engine.Session, w, phase string, f func(tx *engine.Txn) (uint64, int64, error)) error {
	tx, err := sess.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	start := time.Now()
	n, b, err := f(tx)
	if err != nil {
		return err
	}
	s.record(result{workload: w, phase: phase, unit: "items", ops: n, bytes: b, dur: time.Since(start)})
	return nil
}

// --- W8: sustained mixed load ---

func (s *suite) w8() error {
	for _, f := range []func() error{
		func() error { return s.loadTable("people", s.sc.rows, s.sc.tableBatch, false) },
		func() error { return s.loadKV(false) },
		func() error { return s.loadDocs(false) },
	} {
		if err := f(); err != nil {
			return err
		}
	}
	var done, retried, failed atomic.Uint64
	var commits, commitErrs atomic.Uint64
	var commitLat hist
	var clMu sync.Mutex
	stop := make(chan struct{})
	var bg sync.WaitGroup
	// The committer: a session commit every mixCommit.
	bg.Add(1)
	go func() {
		defer bg.Done()
		sess, err := s.session("main")
		if err != nil {
			commitErrs.Add(1)
			return
		}
		defer sess.Close()
		tick := time.NewTicker(s.sc.mixCommit)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				st := time.Now()
				if _, err := sess.Commit(ctx, "mixed load"); err != nil {
					commitErrs.Add(1)
					continue
				}
				clMu.Lock()
				commitLat.add(time.Since(st))
				clMu.Unlock()
				commits.Add(1)
			}
		}
	}()
	// The reporter: a line every mixTick.
	bg.Add(1)
	go func() {
		defer bg.Done()
		tick := time.NewTicker(s.sc.mixTick)
		defer tick.Stop()
		start := time.Now()
		var lastDone, lastRetry uint64
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				d, r := done.Load(), retried.Load()
				secs := s.sc.mixTick.Seconds()
				s.note("W8 t=%-5s %8.1f tx/s  %7.1f retries/s  failed %d  commits %d (err %d)  heap %d MiB  disk %s MiB",
					time.Since(start).Round(100*time.Millisecond), float64(d-lastDone)/secs, float64(r-lastRetry)/secs,
					failed.Load(), commits.Load(), commitErrs.Load(), m.HeapAlloc>>20, mib(s.diskBytes()))
				lastDone, lastRetry = d, r
			}
		}
	}()
	t, dur, err := s.parallel(s.sc.mixConc, s.sc.mixDur, func(sess *engine.Session, rng *rand.Rand, t *tally, deadline time.Time) error {
		pick := s.picker("uniform", rng)
		for time.Now().Before(deadline) {
			before := t.retries
			var ok bool
			switch r := rng.IntN(100); {
			case r < 50:
				ok = s.rmw(sess, rng, pick, t)
			case r < 80:
				n := t.ops
				s.kvOps(sess, rng, t, 1)
				ok = t.ops > n
				t.ops = n
			default:
				ok = s.docBump(sess, rng, t, s.sc.docs)
			}
			retried.Add(t.retries - before)
			if ok {
				t.ops++
				done.Add(1)
			} else {
				failed.Add(1)
			}
		}
		return nil
	})
	close(stop)
	bg.Wait()
	if err != nil {
		return err
	}
	r := t.result("W8", fmt.Sprintf("mixed 50/30/20 table/kv/doc, %d sess", s.sc.mixConc), "tx", dur)
	r.note = fmt.Sprintf("session commits %d (err %d), commit p50 %s ms p99 %s ms", commits.Load(), commitErrs.Load(), ms(commitLat.quantile(.5)), ms(commitLat.quantile(.99)))
	s.record(r)
	return nil
}
