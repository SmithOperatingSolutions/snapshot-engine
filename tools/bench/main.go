// Command bench drives a snapshot-engine database through engine/ alone, as
// an access layer would, under realistic and stressful loads, and reports
// throughput, latency, contention, size on disk and heap. It measures; it
// asserts nothing (docs/PERFORMANCE.md holds the baseline and how to read it).
//
//	go run ./tools/bench -scale full -backend disk -cpuprofile /tmp/prof/cpu
//
// Workloads: W1 bulk load (table, kv, documents), W2 point reads, index
// lookups and scans, W3 OLTP read-modify-write under contention, W4 a
// Redis-shaped kv mix, W5 document partial updates, W6 commits, branches,
// merges and diffs at scale, W7 large objects, W8 a sustained mixed load.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

var ctx = context.Background()

var me = engine.Principal{ID: "user:bench"}

// scale is how much each workload does.
type scale struct {
	rows, rows1k        int // table rows loaded in 10k and 1k batches
	tableBatch, table1k int
	kvKeys, kvBatch     int
	counters            int
	docs, docBatch      int
	readDur             time.Duration
	lookups             int
	phaseDur            time.Duration
	concs, kvConcs      []int
	docConc             int
	vcsSmall, vcsLarge  int
	commitSamples       int
	wideRows, wideSize  int
	bigDocs, bigDocSize int
	bigVals, bigValSize int
	mixDur, mixTick     time.Duration
	mixCommit           time.Duration
	mixConc             int
	maxRetries          int
}

var scales = map[string]scale{
	// tiny is for correctness under the race detector: few rows and keys,
	// so sessions collide, and phases long enough for thousands of
	// transactions.
	"tiny": {
		rows: 1000, rows1k: 200, tableBatch: 500, table1k: 100,
		kvKeys: 1000, kvBatch: 500, counters: 20,
		docs: 200, docBatch: 100,
		readDur: 2 * time.Second, lookups: 20,
		phaseDur: 20 * time.Second, concs: []int{2, 4, 8}, kvConcs: []int{2, 8}, docConc: 8,
		vcsSmall: 10, vcsLarge: 100, commitSamples: 5,
		wideRows: 20, wideSize: 64 << 10, bigDocs: 3, bigDocSize: 1 << 20, bigVals: 10, bigValSize: 256 << 10,
		mixDur: time.Minute, mixTick: 10 * time.Second, mixCommit: 2 * time.Second, mixConc: 8,
		maxRetries: 50,
	},
	"smoke": {
		rows: 2000, rows1k: 500, tableBatch: 500, table1k: 100,
		kvKeys: 2000, kvBatch: 500, counters: 50,
		docs: 300, docBatch: 100,
		readDur: 300 * time.Millisecond, lookups: 5,
		phaseDur: 400 * time.Millisecond, concs: []int{1, 4}, kvConcs: []int{4}, docConc: 4,
		vcsSmall: 10, vcsLarge: 50, commitSamples: 3,
		wideRows: 10, wideSize: 64 << 10, bigDocs: 3, bigDocSize: 1 << 20, bigVals: 5, bigValSize: 256 << 10,
		mixDur: 2 * time.Second, mixTick: 500 * time.Millisecond, mixCommit: time.Second, mixConc: 8,
		maxRetries: 50,
	},
	"full": {
		rows: 1_000_000, rows1k: 200_000, tableBatch: 10_000, table1k: 1_000,
		kvKeys: 1_000_000, kvBatch: 10_000, counters: 10_000,
		docs: 200_000, docBatch: 1_000,
		readDur: 20 * time.Second, lookups: 200,
		phaseDur: 30 * time.Second, concs: []int{1, 4, 16, 64}, kvConcs: []int{16, 64}, docConc: 16,
		vcsSmall: 100, vcsLarge: 10_000, commitSamples: 20,
		wideRows: 2000, wideSize: 64 << 10, bigDocs: 50, bigDocSize: 1 << 20, bigVals: 400, bigValSize: 256 << 10,
		mixDur: 5 * time.Minute, mixTick: 10 * time.Second, mixCommit: 5 * time.Second, mixConc: 64,
		maxRetries: 50,
	},
}

type config struct {
	scale                                  string
	backends                               []string
	dir                                    string
	only                                   map[string]bool
	cpuprof, memprof, mutexprof, blockprof string
	keep                                   bool
	out                                    io.Writer
}

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(args []string, out io.Writer) int {
	fl := flag.NewFlagSet("bench", flag.ContinueOnError)
	fl.SetOutput(out)
	c := config{out: out}
	var backend, only, concs, dur string
	var rows, keys, docs int
	fl.StringVar(&c.scale, "scale", "smoke", "tiny, smoke or full")
	fl.IntVar(&rows, "rows", 0, "table rows, overriding the scale's")
	fl.IntVar(&keys, "keys", 0, "kv keys, overriding the scale's")
	fl.IntVar(&docs, "docs", 0, "documents, overriding the scale's")
	fl.StringVar(&backend, "backend", "disk", "disk, mem or both")
	fl.StringVar(&c.dir, "dir", "", "where the disk backend lives (default: a temp dir, removed afterwards)")
	fl.StringVar(&only, "only", "", "comma-separated workloads to report (W1..W8; default all)")
	fl.StringVar(&concs, "conc", "", "comma-separated session counts: W3 and W4 run each, W5 and W8 the largest (default: the scale's)")
	fl.StringVar(&dur, "duration", "", "each timed phase's length, overriding the scale's")
	fl.StringVar(&c.cpuprof, "cpuprofile", "", "path prefix for a CPU profile per workload")
	fl.StringVar(&c.memprof, "memprofile", "", "path prefix for an allocation profile per workload")
	fl.StringVar(&c.mutexprof, "mutexprofile", "", "path prefix for a mutex profile per workload")
	fl.StringVar(&c.blockprof, "blockprofile", "", "path prefix for a block profile per workload")
	fl.BoolVar(&c.keep, "keep", false, "keep the disk backend's directory")
	if err := fl.Parse(args); err != nil {
		return 2
	}
	sc, ok := scales[c.scale]
	if !ok {
		fmt.Fprintf(out, "bench: unknown scale %q\n", c.scale)
		return 2
	}
	if concs != "" {
		sc.concs = nil
		for _, s := range strings.Split(concs, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || n < 1 {
				fmt.Fprintf(out, "bench: bad -conc %q\n", concs)
				return 2
			}
			sc.concs = append(sc.concs, n)
		}
		sc.kvConcs = sc.concs
		sc.docConc = slices.Max(sc.concs)
		sc.mixConc = sc.docConc
	}
	for _, o := range []struct {
		n   int
		dst *int
	}{{rows, &sc.rows}, {keys, &sc.kvKeys}, {docs, &sc.docs}} {
		if o.n < 0 || (o.n > 0 && o.n < 100) {
			fmt.Fprintf(out, "bench: -rows, -keys and -docs take at least 100 (the hot-record phase needs 100 documents)\n")
			return 2
		}
		if o.n > 0 {
			*o.dst = o.n
		}
	}
	sc.rows1k = min(sc.rows1k, sc.rows)
	sc.counters = min(sc.counters, sc.kvKeys)
	sc.vcsLarge = min(sc.vcsLarge, sc.rows/4)
	sc.vcsSmall = min(sc.vcsSmall, sc.vcsLarge)
	if dur != "" {
		d, err := time.ParseDuration(dur)
		if err != nil {
			fmt.Fprintf(out, "bench: bad -duration %q\n", dur)
			return 2
		}
		sc.phaseDur, sc.readDur = d, d
	}
	switch backend {
	case "disk", "mem":
		c.backends = []string{backend}
	case "both":
		c.backends = []string{"disk", "mem"}
	default:
		fmt.Fprintf(out, "bench: unknown backend %q\n", backend)
		return 2
	}
	if only != "" {
		c.only = map[string]bool{}
		for _, w := range strings.Split(only, ",") {
			c.only[strings.ToUpper(strings.TrimSpace(w))] = true
		}
	}
	if c.mutexprof != "" {
		runtime.SetMutexProfileFraction(5)
	}
	if c.blockprof != "" {
		runtime.SetBlockProfileRate(100_000) // a sample per 100µs blocked
	}
	fmt.Fprintf(out, "bench: scale %s, backends %v, GOMAXPROCS %d, %s/%s, %s\n", c.scale, c.backends, runtime.GOMAXPROCS(0), runtime.GOOS, runtime.GOARCH, runtime.Version())
	rep := &report{}
	start := time.Now()
	for _, b := range c.backends {
		if err := runSuite(c, sc, b, rep); err != nil {
			fmt.Fprintf(out, "bench: backend %s: %v\n", b, err)
			rep.print(func(f string, a ...any) { fmt.Fprintf(out, f, a...) })
			return 1
		}
	}
	rep.print(func(f string, a ...any) { fmt.Fprintf(out, f, a...) })
	fmt.Fprintf(out, "\nbench: done in %s\n", time.Since(start).Round(time.Second))
	return 0
}

// suite is one backend's run: one database, the workloads in order.
type suite struct {
	c       config
	sc      scale
	backend string
	dir     string
	o       engine.Options
	db      *engine.Database
	rep     *report
	loaded  map[string]bool
	log     *causes
	seen    causeCounts // the causes counted up to the last result
}

func runSuite(c config, sc scale, backend string, rep *report) (err error) {
	s := &suite{c: c, sc: sc, backend: backend, rep: rep, loaded: map[string]bool{}, log: &causes{}}
	keys, err := engine.NewKeyring()
	if err != nil {
		return err
	}
	s.o = engine.Options{Keys: keys, Authorizer: engine.AllowAll(), Logger: s.log}
	switch backend {
	case "disk":
		dir := c.dir
		if dir == "" {
			if dir, err = os.MkdirTemp("", "snapshot-bench-*"); err != nil {
				return err
			}
			if !c.keep {
				defer os.RemoveAll(dir)
			}
		} else {
			dir = filepath.Join(dir, fmt.Sprintf("bench-%d", time.Now().UnixNano()))
			if !c.keep {
				defer os.RemoveAll(dir)
			}
		}
		s.dir = dir
		if s.o.Blobs, err = engine.DiskBlobs(dir); err != nil {
			return err
		}
	case "mem":
		s.o.Blobs = engine.MemoryBlobs()
	}
	if s.db, err = engine.Create(ctx, me, s.o); err != nil {
		return err
	}
	defer func() {
		if cerr := s.db.Close(); err == nil {
			err = cerr
		}
	}()
	for _, w := range []struct {
		name string
		f    func() error
	}{
		{"W1", s.w1}, {"W2", s.w2}, {"W3", s.w3}, {"W4", s.w4},
		{"W5", s.w5}, {"W6", s.w6}, {"W7", s.w7}, {"W8", s.w8},
	} {
		if !s.wants(w.name) {
			continue
		}
		fmt.Fprintf(c.out, "bench: %s %s ...\n", backend, w.name)
		if err := s.profiled(w.name, w.f); err != nil {
			return fmt.Errorf("%s: %w", w.name, err)
		}
	}
	return nil
}

func (s *suite) wants(w string) bool { return s.c.only == nil || s.c.only[w] }

// profiled runs f under the profiles asked for, one set per workload.
func (s *suite) profiled(name string, f func() error) error {
	suffix := "-" + s.backend + "-" + name
	if s.c.cpuprof != "" {
		fh, err := os.Create(s.c.cpuprof + suffix + ".pprof") //nolint:gosec // G304: the operator's own profile path
		if err != nil {
			return err
		}
		defer fh.Close()
		if err := pprof.StartCPUProfile(fh); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}
	err := f()
	for prefix, name := range map[string]string{s.c.memprof: "allocs", s.c.mutexprof: "mutex", s.c.blockprof: "block"} {
		if prefix == "" {
			continue
		}
		fh, ferr := os.Create(prefix + suffix + ".pprof") //nolint:gosec // G304: the operator's own profile path
		if ferr != nil {
			return ferr
		}
		werr := pprof.Lookup(name).WriteTo(fh, 0)
		if cerr := fh.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
	}
	return err
}

// record finishes a result with the size on disk and the heap, and prints it.
func (s *suite) record(r result) {
	r.backend = s.backend
	now := s.log.snapshot()
	r.lost = now.lost - s.seen.lost
	if other := now.other - s.seen.other; other > 0 {
		r.note += fmt.Sprintf(" [%d other failures logged, first: %s]", other, s.log.firstOther)
	}
	s.seen = now
	r.disk = s.diskBytes()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	r.heap = m.HeapAlloc
	s.rep.add(r)
	fmt.Fprintf(s.c.out, "%s\n", r.line())
	if r.firstErr != "" {
		fmt.Fprintf(s.c.out, "     first error: %s\n", r.firstErr)
	}
}

func (s *suite) note(format string, args ...any) {
	n := fmt.Sprintf("[%s] ", s.backend) + fmt.Sprintf(format, args...)
	s.rep.notes = append(s.rep.notes, n)
	fmt.Fprintf(s.c.out, "%s\n", n)
}

func (s *suite) diskBytes() int64 {
	if s.dir == "" {
		return -1
	}
	var n int64
	_ = filepath.WalkDir(s.dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
