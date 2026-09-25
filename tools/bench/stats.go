package main

import (
	"context"
	"fmt"
	"math/bits"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// hist is a log-linear latency histogram: each power of two split into 16
// buckets, so a quantile is within about 6% of the true value, recording
// costs a few instructions and no allocation. One per goroutine, merged.
type hist struct {
	counts [64 * 16]uint64
	n      uint64
	sum    time.Duration
	max    time.Duration
}

func bucket(d time.Duration) int {
	ns := uint64(d)
	if ns < 16 {
		return int(ns)
	}
	exp := bits.Len64(ns) - 1 // ns in [2^exp, 2^(exp+1))
	sub := (ns >> (exp - 4)) & 15
	return exp*16 + int(sub)
}

// lower is the smallest duration bucket i holds.
func lower(i int) time.Duration {
	if i < 16 {
		return time.Duration(i)
	}
	exp, sub := i/16, uint64(i%16)
	return time.Duration((16 + sub) << (exp - 4))
}

func (h *hist) add(d time.Duration) {
	if d < 0 {
		d = 0
	}
	h.counts[bucket(d)]++
	h.n++
	h.sum += d
	if d > h.max {
		h.max = d
	}
}

func (h *hist) merge(o *hist) {
	for i, c := range o.counts {
		h.counts[i] += c
	}
	h.n += o.n
	h.sum += o.sum
	if o.max > h.max {
		h.max = o.max
	}
}

// quantile is the latency at q (0..1).
func (h *hist) quantile(q float64) time.Duration {
	if h.n == 0 {
		return 0
	}
	target := uint64(q * float64(h.n))
	if target >= h.n {
		target = h.n - 1
	}
	var seen uint64
	for i, c := range h.counts {
		seen += c
		if seen > target {
			return lower(i)
		}
	}
	return h.max
}

// result is one measured phase.
type result struct {
	backend, workload, phase string
	ops                      uint64 // operations (rows, reads, transactions, ...)
	unit                     string // what an op is
	bytes                    int64  // payload bytes moved, for MB/s; 0 when not meaningful
	dur                      time.Duration
	lat                      *hist // per-op latency; nil when not meaningful
	commit                   *hist // commit-only latency, for transactions
	retries, serial, gaveUp  uint64
	lost                     uint64 // serialization failures that were lost swaps, not conflicts
	errors                   uint64
	swaps                    uint64 // root swaps that landed during the phase
	firstErr                 string
	disk                     int64 // bytes on disk after the phase; -1 when not on disk
	heap                     uint64
	note                     string
}

func (r result) rate() float64 {
	if r.dur <= 0 {
		return 0
	}
	return float64(r.ops) / r.dur.Seconds()
}

// swapsPerOp is root swaps per operation: 1 when every transaction
// publishes alone, less when transactions share a publish.
func (r result) swapsPerOp() string {
	if r.ops == 0 {
		return "-"
	}
	return fmt.Sprintf("%.3f", float64(r.swaps)/float64(r.ops))
}

func (r result) mbps() string {
	if r.bytes <= 0 || r.dur <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", float64(r.bytes)/r.dur.Seconds()/1e6)
}

func ms(d time.Duration) string {
	switch {
	case d == 0:
		return "-"
	case d < time.Millisecond:
		return fmt.Sprintf("%.3f", float64(d)/1e6)
	case d < 10*time.Second:
		return fmt.Sprintf("%.2f", float64(d)/1e6)
	}
	return fmt.Sprintf("%.0f", float64(d)/1e6)
}

func mib(b int64) string {
	if b < 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f", float64(b)/(1<<20))
}

func (r result) line() string {
	var p50, p95, p99, max, c50, c99 time.Duration
	if r.lat != nil {
		p50, p95, p99, max = r.lat.quantile(.50), r.lat.quantile(.95), r.lat.quantile(.99), r.lat.max
	}
	if r.commit != nil {
		c50, c99 = r.commit.quantile(.50), r.commit.quantile(.99)
	}
	return fmt.Sprintf("%-4s %-40s %10d %-7s %10.1f %8s | %8s %8s %8s %9s | %8s %8s | %6d %6d %6d %4d %5d | %7d %6s | %7s %7s %s",
		r.workload, r.phase, r.ops, r.unit, r.rate(), r.mbps(),
		ms(p50), ms(p95), ms(p99), ms(max), ms(c50), ms(c99),
		r.retries, r.serial, r.lost, r.gaveUp, r.errors,
		r.swaps, r.swapsPerOp(),
		mib(r.disk), fmt.Sprintf("%.0f", float64(r.heap)/(1<<20)), r.note)
}

const header = "W    phase                                           ops unit          op/s     MB/s |   p50 ms   p95 ms   p99 ms    max ms |   c50 ms   c99 ms |  retry serial   lost gave   err |   swaps  sw/op | diskMiB heapMiB note"

// report collects results and prints them.
type report struct {
	results []result
	notes   []string
}

func (r *report) add(res result) { r.results = append(r.results, res) }

func (r *report) byBackend() map[string][]result {
	out := map[string][]result{}
	for _, res := range r.results {
		out[res.backend] = append(out[res.backend], res)
	}
	return out
}

func (r *report) print(w func(format string, args ...any)) {
	groups := r.byBackend()
	backends := make([]string, 0, len(groups))
	for b := range groups {
		backends = append(backends, b)
	}
	sort.Strings(backends)
	for _, b := range backends {
		w("\n== backend %s\n%s\n", b, header)
		for _, res := range groups[b] {
			w("%s\n", res.line())
			if res.firstErr != "" {
				w("     first error: %s\n", res.firstErr)
			}
		}
	}
	if len(r.notes) > 0 {
		w("\n== notes\n")
		for _, n := range r.notes {
			w("%s\n", n)
		}
	}
}

// causes is the database's Logger: the engine shows a caller only an
// error's kind, and logs the details, so this is where the bench learns why
// a transaction failed. A serialization failure is either a lost swap (the
// working set moved under every one of Commit's attempts: contention, no
// data conflict) or a conflict (the changes collided in a model).
type causes struct {
	lost, conflict, other atomic.Uint64
	mu                    sync.Mutex
	firstOther            string
}

type causeCounts struct{ lost, conflict, other uint64 }

func (c *causes) Log(_ context.Context, _ string, detail string) {
	switch {
	case strings.Contains(detail, "moved under"):
		c.lost.Add(1)
	case strings.HasPrefix(detail, engine.ErrSerialization.Error()):
		c.conflict.Add(1)
	default:
		c.other.Add(1)
		c.mu.Lock()
		if c.firstOther == "" {
			c.firstOther = detail
		}
		c.mu.Unlock()
	}
}

func (c *causes) snapshot() causeCounts {
	return causeCounts{c.lost.Load(), c.conflict.Load(), c.other.Load()}
}
