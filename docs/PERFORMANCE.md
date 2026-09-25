# Performance

What the engine does under realistic and stressful load, where the time
goes, and what to change first. The numbers come from `tools/bench`, a
program that drives a database through `engine/` alone, as an adapter
would (depguard holds it to that). Baseline taken 2026-09-25 on
snapshot-core v0.1.1. This is a measurement: nothing here was optimized.

Status: interim. The full sweep below found the problems. A
correctness-first ladder followed: tiny scale under the race detector
(item 1 below), each failure turned into a test and fixed, then 1k to 1M
rows and 1 to 64 sessions in steps ("Scaling in steps", after the
baseline). The full-scale and sustained runs were repeated last, as
validation ("Validation", after that).

## Broken under load: read this first

1. **Concurrent transactions lost updates. Fixed on this branch.** Two
   transactions that read the same value and wrote it back changed (a
   balance from 1000 to 1001) both committed, and one increment was gone.
   The second commit found the working set moved, merged three ways, and
   the merge took a change both sides made identically as clean. A table
   cell, a kv bytes value, a document field and a kv counter all lost the
   update. The counter lost it because the core takes two identical object
   roots as one change before the kv model can sum the deltas; that
   happens when the counter is the only change on both sides, as in W4's
   one-op INCR. Before the fix, under load:

   | Where | Disk | Memory |
   | --- | ---: | ---: |
   | Two sessions, one value, one increment each (the smallest case: a table cell, a kv bytes value, a document field, a counter) | not run | 1 of 2, each kind |
   | W3 uniform keys over 1M rows, 1 to 64 sessions | 0 | 0 |
   | W3 Zipf 1.1, 4 sessions | 2 of 632 | 32 of 1,584 |
   | W3 Zipf 1.1, 16 sessions | 2 of 222 | 17 of 449 |
   | W3 Zipf 1.1, 64 sessions | 7 of 151 | 12 of 176 |
   | W3 Zipf 1.1 over 1,000 rows, 2 sessions (race detector) | 6 of 568 | 27 of 1,117 |
   | W4 counters, one op per transaction, 2 sessions over 1,000 keys (race detector) | 0 of 93 | 3 of 159 |
   | W5, uniform and 100 hot records, 16 sessions | 0 of 236 | 0 of 501 |
   | W8 table and documents, 64 sessions | 0 of 284 | 0 of 500 |

   The counts are committed increments the authority does not show
   (before, plus what committed, less what is there after; the bench reads
   the sums with a full scan around each phase). Collisions need two
   transactions on one item at once, so they appear where keys are hot and
   commits frequent.

   The Engine Spec promises snapshot isolation and, in the same paragraph,
   that a clean merge commits; snapshot isolation forbids lost updates, so
   the two rules disagreed exactly here. The user decided the grain: a
   transaction is the one writer of an item (a table row, a kv key, a
   document record) until it commits, counters included for now (DESIGN
   D18 on main). `Txn.Commit` now refuses an item both sides wrote before
   it merges, whatever the values; branch merges still combine cells and
   fields. Counters will sum again once the core asks models about
   identical changes. After the fix, the race-detector runs (1,000 and 100
   rows, keys and records, 2 to 8 sessions, every workload, both backends)
   showed no data race, no error and no lost update in several thousand
   checked increments of each kind. Tests:
   `TestTransactionsWritingOneItemSerializeWhateverTheyWrote`,
   `TestTransactionsWritingDifferentItemsAllCommit`,
   `TestATransactionAlteringATableConflictsWithOneWritingItsRows`,
   `TestWhatTheMergeRefusesStillSerializes`.

2. **Write throughput falls as sessions are added, and transactions
   starve. Fixed by group commit ("Group commit (#1)" below): 1.7 to
   138 tx/s at 64 sessions on disk, no transaction gives up.** Read-modify-write on uniform keys over a million rows (W3),
   where two transactions almost never touch the same row:

   | Sessions | Disk tx/s | Disk p99 | Memory tx/s | Memory p99 |
   | ---: | ---: | ---: | ---: | ---: |
   | 1 | 26.5 | 50 ms | 213 | 8.9 ms |
   | 4 | 12.5 | 14.5 s | 93.6 | 503 ms |
   | 16 | 4.1 | 31 s | 24.5 | 4.2 s |
   | 64 | 1.7 | 56 s | 3.1 | 43 s |

   Every serialization failure in those rows is a lost swap ("the working
   set moved under 5 attempts in a row"), not a conflict. In the sustained
   64-session mix (W8) 37 transactions on disk and 15 in memory gave up
   after 50 retries. The cause is below (W3): every commit is a
   compare-and-swap on the whole repository's root, the core uploads a
   losing committer's pack before it checks the root, and the losers queue
   on the same lock as the winner.

3. **A session commit can wait minutes. Fixed by group commit: W8's
   session commit p99 is 419 ms on disk.** In W8 on disk a `Session.Commit`
   every 5 s landed 7 times in 5.5 minutes, the first after about four
   minutes, p99 249 s (memory: p99 34 s). A version-control commit swaps
   the same root as every transaction, and the core retries a lost swap up
   to 1,000 times with no backoff.

4. **Every transaction gets slower as the database ages.** Each publish
   adds an index object to the packstore's manifest, and every root read
   (every `Begin`, every commit attempt) reads and decrypts the whole
   manifest and walks its index list. One session committing one-key
   transactions on the memory backend:

   | Commits so far | tx/s | `Begin` | `Commit` |
   | ---: | ---: | ---: | ---: |
   | 5,000 | 233 | 0.16 ms | 4.1 ms |
   | 20,000 | 93 | 1.0 ms | 9.8 ms |
   | 50,000 | 36 | 3.3 ms | 24 ms |
   | 70,000 | 22 | 5.7 ms | 39 ms |

   (Stopped at 70,000 commits after 25 minutes, short of the cap; from
   about 20,000 on it shared the machine with other runs, which moves the
   timings but not the trend.)

   In the baseline this shows as the one-session memory rate falling from
   213 tx/s (W3 uniform) to 86.9 tx/s (W3 Zipf) minutes later, a session
   commit going from 3.0 ms after the load to 16.8 ms in W6, and the
   manifest read taking 70% of all CPU in W8 on memory (55% on disk). The
   core refuses a publish once the manifest lists 100,000 index objects
   ("run GC to compact"), and the engine offers no GC.

Not seen: no crash, deadlock or panic in either run; every point read,
index lookup, scan count and disjoint merge returned what the bench wrote.
On disk the heap stayed between 277 and 541 MiB through the sustained mix.
On memory it grows with the data, as it must (the store is the heap).

## The machine

| | |
| --- | --- |
| CPU | 13th Gen Intel Core i7-1360P: 12 cores (4 performance, 8 efficient), 16 threads, up to 5.0 GHz, SHA-NI, AES-NI, AVX2 |
| Memory | 62 GiB |
| Disk | Samsung SSD 990 PRO 2 TB (NVMe), ext4 on LVM, no disk encryption |
| OS | Linux 6.8.0 (Ubuntu), `GOMAXPROCS` 16 |
| Go | 1.27.1, `CGO_ENABLED=0` |

A durable put on this disk (write, fsync, link, fsync the directory) takes
10.2 ms at p50 and a root swap (write, fsync, rename, fsync the directory)
10.2 ms, measured apart from the engine; a commit on disk chains the two.

## How to run

```
go run ./tools/bench -scale smoke                 # every workload in seconds, disk in a temp dir
go run ./tools/bench -scale full -backend both    # the baseline: about 17 min on disk, 16 on memory
go run ./tools/bench -scale full -only W3 -conc 1,64 -duration 10s
go build -o /tmp/bench ./tools/bench && /tmp/bench -scale full -cpuprofile /tmp/p/cpu -memprofile /tmp/p/mem
```

| Flag | Meaning |
| --- | --- |
| `-scale smoke\|full` | how much each workload does (`scales` in `tools/bench/main.go`) |
| `-backend disk\|mem\|both` | `engine.DiskBlobs` in a temp directory (removed afterwards) or `engine.MemoryBlobs` |
| `-dir` | where the disk backend's directory goes; `-keep` keeps it |
| `-only W3,W8` | the workloads to run; a later workload loads what it needs quietly |
| `-conc 1,4,16,64` | W3's session counts |
| `-duration 30s` | each timed phase's length |
| `-cpuprofile`, `-memprofile`, `-mutexprofile`, `-blockprofile` | a path prefix: one profile per workload, `<prefix>-<backend>-<W>.pprof`. The allocation, mutex and block profiles are cumulative, so diff a workload against the one before it (`go tool pprof -base mem-disk-W2.pprof mem-disk-W3.pprof`) |

`go test ./tools/bench` runs every workload at smoke scale on both backends
(memory only with `-short`) and checks each one reports and nothing fails.
It makes no timing assertions.

Reading a row: ops/s is throughput over the phase, including the tail of
transactions still retrying when it ends; latency is the application's,
retries and backoff included (a retry waits a random 0 to 200 µs per
attempt so far); commit p50 is the last attempt's `Commit` alone;
"serialization failures (lost swaps)" counts every `ErrSerialization`,
and in brackets how many the engine logged as a lost swap rather than a
conflict (the bench reads the details from the database's `Logger`);
"gave up" is a transaction that failed 50 attempts; "swaps" counts the
root swaps that landed during the phase (the bench wraps the backend and
counts `SwapRoot`), and "sw/op" is swaps per operation: 1 when every
transaction publishes alone; disk and heap are measured after the phase,
the heap after a GC.

### The workloads at full scale

| | What it does |
| --- | --- |
| W1 bulk load | `people`: 1M rows (int8 key; name text of 100 bytes, age int4, score float8, active bool, city text, balance int8; a secondary index on city, 1,000 cities) in 10k-row transactions, and 200k more rows in 1k-row transactions; 1M kv keys of 100-byte values in 10k batches, plus 10k counters; 200k documents of about 1 KB (eight numeric fields, a 700-character bio) in 1k batches |
| W2 reads | point `Get`s from 1 and 16 sessions, 20 s each; 200 index lookups of about 1,000 rows; one full scan |
| W3 OLTP | read 2 to 4 rows, add one to the balance of 1 or 2 of them, commit; 1, 4, 16 and 64 sessions, uniform keys then Zipf 1.1; 30 s each |
| W4 Redis-shaped | GET 70% (rolled back), SET 20%, INCR 10% (a counter); one op per transaction and 100 per transaction; 16 and 64 sessions |
| W5 documents | read a record and add one to one of its numeric fields (the engine has no field-level update, and no helpers to edit a `Node`: the bench edits its exported fields); 16 sessions, uniform and over 100 hot records |
| W6 version control | a session commit after a one-row transaction (20 samples); then a branch, 100 rows changed on each side, merge, and a diff of main across the merge; then the same with 10,000 rows a side |
| W7 large objects | 2,000 rows with a 64 KiB text column; 50 documents of 1 MiB less 64 bytes of text; 400 kv values of 256 KiB; each written and read back |
| W8 sustained mix | 64 sessions for 5 minutes: 50% W3, 30% W4 (one op), 20% W5; a session commit every 5 s; a line every 10 s |

## Baseline

Full scale, profiles on (CPU at 100 Hz, allocations at Go's default rate,
mutex 1 in 5, block 1 per 100 µs blocked). The disk run took 16 min 57 s
(peak RSS 0.8 GB, 1.5 GB on disk at the end, deleted afterwards); the
memory run 15 min 32 s (peak RSS 6.7 GB). Produced by the tool as
committed in `e8c1168`; the memory run also counted W8's retries as they
happen (added in `15885bb`), which changes only W8's tick line. The
lost-update counts above come from a rerun of W3, W5 and W8 with the tool
as committed in `15885bb`.

### Disk

| W | Phase | Ops | Rate /s | MB/s | p50 ms | p99 ms | Max ms | Commit p50 ms | Serialization failures (lost swaps) | Gave up | Disk MiB | Heap MiB |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| W1 | table people 1000000 rows @10000/tx | 1,000,000 rows | 40,335 | 5.6 | 243 | 369 | 377 | 210 | 0 | 0 | 161 | 117 |
| W1 | table people1k 200000 rows @1000/tx | 200,000 rows | 9,256 | 1.3 | 113 | 168 | 171 | 105 | 0 | 0 | 217 | 130 |
| W1 | session commit after the loads | 1 commit | 28.4 | - | 33.5 | 33.5 | 35.2 | - | 0 | 0 | 217 | 131 |
| W1 | kv 1000000 keys @10000/tx | 1,000,000 keys | 121,724 | 13.3 | 79.7 | 96.5 | 98.4 | 65.0 | 0 | 0 | 299 | 130 |
| W1 | documents 200000 @1000/tx | 200,000 docs | 13,514 | 12.8 | 71.3 | 92.3 | 94.7 | 46.1 | 0 | 0 | 424 | 131 |
| W2 | point get, 1 session(s) | 660,296 gets | 33,015 | 4.6 | 0.033 | 0.066 | 0.800 | - | 0 | 0 | 424 | 131 |
| W2 | point get, 16 session(s) | 3,592,354 gets | 179,616 | 24.8 | 0.063 | 0.475 | 5.90 | - | 0 | 0 | 424 | 132 |
| W2 | index lookup by city | 200 lookups | 33.8 | - | 31.5 | 44.0 | 50.4 | - | 0 | 0 | 424 | 132 |
| W2 | full scan | 1,000,000 rows | 736,734 | 101.7 | - | - | - | - | 0 | 0 | 424 | 132 |
| W3 | rmw uniform, 1 session(s) | 795 tx | 26.5 | - | 37.8 | 50.3 | 56.3 | 31.5 | 0 | 0 | 437 | 134 |
| W3 | rmw uniform, 4 session(s) | 377 tx | 12.5 | - | 79.7 | 14,496 | 15,354 | 71.3 | 222 (222) | 0 | 449 | 137 |
| W3 | rmw uniform, 16 session(s) | 130 tx | 4.1 | - | 285 | 31,139 | 31,343 | 268 | 359 (359) | 0 | 461 | 139 |
| W3 | rmw uniform, 64 session(s) | 97 tx | 1.7 | - | 40,802 | 55,835 | 57,638 | 973 | 779 (779) | 0 | 484 | 136 |
| W3 | rmw zipf1.1, 1 session(s) | 665 tx | 22.1 | - | 46.1 | 54.5 | 60.0 | 37.8 | 0 | 0 | 495 | 138 |
| W3 | rmw zipf1.1, 4 session(s) | 363 tx | 12.0 | - | 83.9 | 9,664 | 20,445 | 75.5 | 216 (211) | 1 | 511 | 141 |
| W3 | rmw zipf1.1, 16 session(s) | 127 tx | 4.0 | - | 285 | 31,139 | 31,795 | 260 | 352 (344) | 0 | 529 | 139 |
| W3 | rmw zipf1.1, 64 session(s) | 101 tx | 1.7 | - | 40,802 | 57,982 | 59,740 | 805 | 849 (824) | 0 | 565 | 144 |
| W4 | kv 70/20/10 GET/SET/INCR, 1/tx, 16 sess | 378 ops | 11.7 | - | 4.06 | 31,139 | 32,185 | 302 | 310 (310) | 0 | 573 | 148 |
| W4 | kv 70/20/10 GET/SET/INCR, 1/tx, 64 sess | 335 ops | 5.1 | - | 35.6 | 64,425 | 65,847 | 1,141 | 704 (704) | 0 | 590 | 161 |
| W4 | kv 70/20/10 GET/SET/INCR, 100/tx, 16 sess | 11,800 ops | 358 | - | 319 | 32,212 | 32,915 | 268 | 314 (314) | 0 | 746 | 169 |
| W4 | kv 70/20/10 GET/SET/INCR, 100/tx, 64 sess | 13,800 ops | 295 | - | 17,180 | 45,097 | 46,156 | 96.5 | 663 (660) | 0 | 1067 | 172 |
| W5 | doc field update, uniform, 16 sess | 110 tx | 3.4 | - | 319 | 32,212 | 32,483 | 319 | 297 (297) | 0 | 1072 | 169 |
| W5 | doc field update, hot 100 records, 16 sess | 107 tx | 3.3 | - | 319 | 32,212 | 32,630 | 319 | 290 (290) | 0 | 1085 | 173 |
| W6 | session commit after a 1-row txn | 20 commit | 19.4 | - | 50.3 | 67.1 | 69.1 | - | 0 | 0 | 1085 | 171 |
| W6 | txn changing 100 rows (x2 branches) | 2 tx | 8.3 | 0.1 | 126 | 126 | 128 | 58.7 | 0 | 0 | 1087 | 169 |
| W6 | merge 100+100 changed rows | 1 merge | 8.5 | - | 117 | 117 | 118 | - | 0 | 0 | 1087 | 171 |
| W6 | diff across the 100-row merge | 100 changes | 270,243 | - | 0.360 | 0.360 | 0.370 | - | 0 | 0 | 1087 | 171 |
| W6 | txn changing 10000 rows (x2 branches) | 2 tx | 0.2 | 0.2 | 5,906 | 5,906 | 5,980 | 58.7 | 0 | 0 | 1291 | 204 |
| W6 | merge 10000+10000 changed rows | 1 merge | 2.1 | - | 470 | 470 | 482 | - | 0 | 0 | 1291 | 207 |
| W6 | diff across the 10000-row merge | 10,000 changes | 480,617 | - | 19.9 | 19.9 | 20.8 | - | 0 | 0 | 1291 | 207 |
| W7 | write 64-KiB rows @100/tx | 2,000 rows | 1,095 | 71.8 | 67.1 | 83.9 | 86.8 | 60.8 | 0 | 0 | 1416 | 209 |
| W7 | read 64-KiB rows (scan) | 2,000 items | 5,531 | 362.5 | - | - | - | - | 0 | 0 | 1416 | 211 |
| W7 | write 1023-KiB documents @5/tx | 50 docs | 41.1 | 43.1 | 117 | 134 | 142 | 62.9 | 0 | 0 | 1418 | 222 |
| W7 | read 1023-KiB documents | 50 items | 146 | 153.3 | - | - | - | - | 0 | 0 | 1418 | 217 |
| W7 | write 256-KiB kv values @10/tx | 400 values | 146 | 38.2 | 67.1 | 83.9 | 86.8 | 60.8 | 0 | 0 | 1428 | 208 |
| W7 | read 256-KiB kv values (scan) | 400 items | 2,816 | 738.1 | - | - | - | - | 0 | 0 | 1428 | 207 |
| W8 | mixed 50/30/20 table/kv/doc, 64 sess | 325 tx | 1.0 | - | 1,275 | 326,418 | 328,748 | 1,275 | 3082 (3082) | 37 | 1500 | 218 |

### Memory

| W | Phase | Ops | Rate /s | MB/s | p50 ms | p99 ms | Max ms | Commit p50 ms | Serialization failures (lost swaps) | Gave up | Disk MiB | Heap MiB |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| W1 | table people 1000000 rows @10000/tx | 1,000,000 rows | 48,690 | 6.7 | 193 | 336 | 339 | 168 | 0 | 0 | - | 319 |
| W1 | table people1k 200000 rows @1000/tx | 200,000 rows | 16,411 | 2.3 | 60.8 | 105 | 110 | 56.6 | 0 | 0 | - | 413 |
| W1 | session commit after the loads | 1 commit | 324 | - | 3.01 | 3.01 | 3.08 | - | 0 | 0 | - | 414 |
| W1 | kv 1000000 keys @10000/tx | 1,000,000 keys | 254,529 | 27.7 | 35.6 | 54.5 | 56.1 | 30.4 | 0 | 0 | - | 515 |
| W1 | documents 200000 @1000/tx | 200,000 docs | 31,820 | 30.2 | 30.4 | 39.9 | 50.6 | 15.2 | 0 | 0 | - | 718 |
| W2 | point get, 1 session(s) | 763,790 gets | 38,190 | 5.3 | 0.027 | 0.043 | 0.402 | - | 0 | 0 | - | 718 |
| W2 | point get, 16 session(s) | 3,384,548 gets | 169,226 | 23.4 | 0.061 | 0.475 | 4.20 | - | 0 | 0 | - | 719 |
| W2 | index lookup by city | 200 lookups | 38.6 | - | 26.2 | 37.8 | 40.0 | - | 0 | 0 | - | 719 |
| W2 | full scan | 1,000,000 rows | 826,013 | 114.0 | - | - | - | - | 0 | 0 | - | 719 |
| W3 | rmw uniform, 1 session(s) | 6,398 tx | 213 | - | 4.46 | 8.91 | 14.5 | 1.38 | 0 | 0 | - | 883 |
| W3 | rmw uniform, 4 session(s) | 2,813 tx | 93.6 | - | 11.0 | 503 | 2,300 | 6.03 | 1223 (1223) | 0 | - | 1024 |
| W3 | rmw uniform, 16 session(s) | 748 tx | 24.5 | - | 185 | 4,161 | 6,809 | 25.2 | 1384 (1384) | 0 | - | 1125 |
| W3 | rmw uniform, 64 session(s) | 137 tx | 3.1 | - | 15,032 | 42,950 | 43,772 | 386 | 1222 (1222) | 0 | - | 1184 |
| W3 | rmw zipf1.1, 1 session(s) | 2,607 tx | 86.9 | - | 11.0 | 18.9 | 27.7 | 7.08 | 0 | 0 | - | 1249 |
| W3 | rmw zipf1.1, 4 session(s) | 1,227 tx | 40.8 | - | 25.2 | 872 | 1,535 | 13.6 | 633 (616) | 0 | - | 1337 |
| W3 | rmw zipf1.1, 16 session(s) | 321 tx | 10.4 | - | 872 | 7,785 | 9,326 | 96.5 | 793 (776) | 0 | - | 1412 |
| W3 | rmw zipf1.1, 64 session(s) | 120 tx | 2.6 | - | 22,549 | 45,097 | 46,670 | 537 | 1034 (999) | 0 | - | 1487 |
| W4 | kv 70/20/10 GET/SET/INCR, 1/tx, 16 sess | 799 ops | 25.8 | - | 11.5 | 8,590 | 19,214 | 122 | 698 (698) | 0 | - | 1515 |
| W4 | kv 70/20/10 GET/SET/INCR, 1/tx, 64 sess | 353 ops | 7.2 | - | 83.9 | 47,245 | 48,915 | 570 | 1000 (1000) | 0 | - | 1557 |
| W4 | kv 70/20/10 GET/SET/INCR, 100/tx, 16 sess | 20,700 ops | 656 | - | 168 | 21,475 | 28,016 | 48.2 | 363 (361) | 0 | - | 1865 |
| W4 | kv 70/20/10 GET/SET/INCR, 100/tx, 64 sess | 13,700 ops | 297 | - | 15,032 | 45,097 | 46,105 | 79.7 | 553 (550) | 0 | - | 2301 |
| W5 | doc field update, uniform, 16 sess | 244 tx | 7.8 | - | 143 | 16,106 | 30,812 | 126 | 669 (669) | 0 | - | 2319 |
| W5 | doc field update, hot 100 records, 16 sess | 226 tx | 7.3 | - | 151 | 20,401 | 24,130 | 143 | 614 (614) | 0 | - | 2371 |
| W6 | session commit after a 1-row txn | 20 commit | 56.4 | - | 16.8 | 21.0 | 21.3 | - | 0 | 0 | - | 2364 |
| W6 | txn changing 100 rows (x2 branches) | 2 tx | 11.8 | 0.2 | 83.9 | 83.9 | 87.7 | 21.0 | 0 | 0 | - | 2367 |
| W6 | merge 100+100 changed rows | 1 merge | 18.9 | - | 52.4 | 52.4 | 53.0 | - | 0 | 0 | - | 2368 |
| W6 | diff across the 100-row merge | 100 changes | 218,660 | - | 0.442 | 0.442 | 0.457 | - | 0 | 0 | - | 2368 |
| W6 | txn changing 10000 rows (x2 branches) | 2 tx | 0.2 | 0.2 | 6,174 | 6,174 | 6,381 | 30.4 | 0 | 0 | - | 2624 |
| W6 | merge 10000+10000 changed rows | 1 merge | 2.7 | - | 369 | 369 | 371 | - | 0 | 0 | - | 2620 |
| W6 | diff across the 10000-row merge | 10,000 changes | 482,703 | - | 19.9 | 19.9 | 20.7 | - | 0 | 0 | - | 2619 |
| W7 | write 64-KiB rows @100/tx | 2,000 rows | 1,540 | 100.9 | 37.8 | 54.5 | 55.1 | 35.6 | 0 | 0 | - | 2780 |
| W7 | read 64-KiB rows (scan) | 2,000 items | 6,895 | 451.9 | - | - | - | - | 0 | 0 | - | 2782 |
| W7 | write 1023-KiB documents @5/tx | 50 docs | 54.8 | 57.4 | 88.1 | 101 | 103 | 37.8 | 0 | 0 | - | 2786 |
| W7 | read 1023-KiB documents | 50 items | 160 | 167.8 | - | - | - | - | 0 | 0 | - | 2786 |
| W7 | write 256-KiB kv values @10/tx | 400 values | 254 | 66.7 | 37.8 | 54.5 | 55.1 | 35.6 | 0 | 0 | - | 2797 |
| W7 | read 256-KiB kv values (scan) | 400 items | 3,762 | 986.3 | - | - | - | - | 0 | 0 | - | 2793 |
| W8 | mixed 50/30/20 table/kv/doc, 64 sess | 537 tx | 1.6 | - | 1,745 | 197,568 | 219,658 | 872 | 4677 (4677) | 15 | - | 3023 |

## Scaling in steps

W3 on disk after the item rule, 10 s a phase, each run under the shared
measurement lock. Transactions per second:

| Rows | 1 session | 4 | 16 | 64 | Zipf: 1 | 4 | 16 | 64 |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1,000 | 22.8 | 12.1 | 4.6 | 2.0 | 28.5 | 12.8 | 4.6 | 2.3 |
| 10,000 | 20.5 | 10.5 | 3.9 | 1.6 | 21.3 | 9.8 | 4.1 | 1.6 |
| 100,000 | 16.2 | 10.7 | 4.5 | 2.2 | 27.1 | 12.1 | 4.8 | 2.3 |
| 1,000,000 | 25.7 | 13.1 | 5.1 | 2.4 | 25.0 | 12.3 | 4.7 | 2.3 |

Throughput stops scaling at the second session, whatever the table's
size. The p99 at 4 sessions is 8.6 to 10.2 s, and 31 to 45 s at 64. Table
size does not matter, from 1,000 rows to a million. The 1-session rate
varies with what else the disk was doing, from 16 to 29 tx/s. On uniform
keys almost every serialization failure is still a lost swap: 515 of 515
at 64 sessions over a million rows. On Zipf keys the item rule adds real
conflicts, 18 to 32% of failures at 4 to 64 sessions (117 of 619 at 64
sessions over a million rows), all of which retried and committed. No
transaction gave up, and no increment was lost at any step. At a million
rows, 96% of blocked time is on the packstore's commit lock and the item
check itself costs 4.2% of CPU. The ceiling is the single root swap
(item 2 above and changes 1, 3 and 4 below), not the data or the rule.

## Validation

The full sweep again, both backends, with the item rule, each run under
the shared measurement lock. Every phase's authority check came back
clean: no lost update of a balance, a document field or a counter, and no
error. Throughput matches the baseline within noise. W3 runs at 26.5,
12.9, 4.3 and 1.7 tx/s on disk at 1, 4, 16 and 64 sessions, and at 213,
112, 30.3 and 3.2 in memory. On Zipf keys, 12 to 17% of serialization
failures are now real conflicts, retried and committed. The sustained mix
still collapses: 1.0 tx/s on disk and 1.7 in memory at 64 sessions, with
42 and 14 transactions giving up after 50 retries. That is the root swap,
which the ranked changes address.

## Group commit (#1)

DESIGN D20: commits to a branch of one database take turns in its queue,
and the transactions waiting are published with one root swap, each
checked against its own snapshot. Full scale, W3 and W8 only
(`-only W3,W8`), 30 s a W3 phase and W8's 5 minutes, each run under the
shared measurement lock, started once the load average was under 2 with
no test or bench process running. Other agents' test runs shared the
machine during some runs; the load average during each (sampled every
30 s) is given. "Before" is main's engine (47e7817) with the bench's
swap counter added (f00b85f); "after" is this branch as it ends
(8737891); both on core v0.1.1. "Next core" is the same engine built
under an uncommitted `go.work` against snapshot-core's unreleased
`core-next` branch (53bcbde, the v0.1.2 batch: a loser learns before it
uploads, backoff between lost swaps, among others).

W3 read-modify-write on uniform keys over a million rows, disk:

| Sessions | Before tx/s | p99 | swaps/tx | After tx/s | p99 | swaps/tx | Next core tx/s | p99 |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 26.6 | 50 ms | 1.000 | 26.4 | 48 ms | 1.000 | 28.2 | 57 ms |
| 4 | 12.2 | 9.7 s | 1.000 | 45.3 | 109 ms | 0.500 | 49.4 | 109 ms |
| 16 | 4.1 | 31 s | 1.000 | 115.0 | 159 ms | 0.125 | 122.8 | 168 ms |
| 64 | 1.7 | 56 s | 1.000 | 137.8 | 487 ms | 0.031 | 143.2 | 487 ms |

Memory:

| Sessions | Before tx/s | p99 | swaps/tx | After tx/s | p99 | swaps/tx | Next core tx/s | p99 |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | 213.1 | 9.4 ms | 1.000 | 214.3 | 8.9 ms | 1.000 | 922.0 | 2.2 ms |
| 4 | 103.6 | 570 ms | 1.000 | 167.9 | 36 ms | 0.500 | 452.6 | 15 ms |
| 16 | 31.0 | 4.6 s | 1.000 | 244.1 | 76 ms | 0.125 | 295.2 | 76 ms |
| 64 | 3.1 | 43 s | 1.000 | 168.0 | 436 ms | 0.031 | 184.9 | 403 ms |

Zipf 1.1 keys, tx/s at 1, 4, 16 and 64 sessions: disk before 22.3, 11.4,
3.8, 1.6; after 26.0, 40.3, 104.3, 156.5; next core 29.3, 48.2, 108.3,
185.1. Memory before 89.4, 42.2, 11.1, 2.5; after 122.5, 130.6, 196.5,
202.1; next core 897.1, 509.6, 368.8, 285.2. Before, every uniform-key
failure was a lost swap (768 at 64 sessions on disk) and three
transactions gave up on disk; after, no uniform-key transaction failed at all,
and on Zipf keys every failure is a real collision of two writers on one
row under the item rule (2,823 at 64 sessions on disk), each retried and
committed. No lost update in any phase.

W8, the sustained mix, 64 sessions for 5 minutes:

| | Disk before | Disk after | Disk next core | Memory before | Memory after |
| --- | ---: | ---: | ---: | ---: | ---: |
| tx/s | 1.2 | 237.0 | 272.1 | 1.8 | 260.1 |
| p99 | 51.5 s | 419 ms | 352 ms | 163 s | 386 ms |
| gave up (50 attempts) | 63 | 0 | 0 | 21 | 0 |
| serialization failures | 3,632 | 2 | 5 | 5,510 | 5 |
| root swaps per transaction | 0.80 | 0.025 | 0.025 | 0.87 | 0.026 |
| session commits (every 5 s) | 7 | 60 | 60 | 39 | 60 |
| session commit p99 | 292 s | 419 ms | 369 ms | 34 s | 319 ms |

(Swaps per transaction counts W4's read-only GETs, which roll back and
swap nothing, as transactions: 0.8 before is every writer swapping
alone.) Memory on the next core ran 3 minutes at 310 to 335 tx/s, then the
run's 12 GiB memory cap stopped it: the memory backend's store is the heap,
and at that rate it passed 11.8 GiB. Load average during the runs, median
(max): disk before 4.95 (8.63), after 2.39 (3.35), next core 2.54 (4.16);
memory before 2.48 (3.26), after 1.80 (2.43), next core 1.46 (1.88).

**Where the time goes now.** A batch's cost is its leader's: one swap
(two rounds of fsync on disk, about 31 ms) and, per member, the item rule
and the merge, both reading the difference between the member's snapshot
and the batch's working set. That difference grows with what landed
since the snapshot, so a member's check costs more the more sessions
there are: at 64 sessions a batch carries about 30 transactions and takes
about 0.4 s, nearly all of it reading prolly nodes and hashing each chunk
read (the spec's verify-on-every-read; ranked change 7). The first queue
also merged each member as base, ours, the batch, so the table model
rewrote the member with every earlier member's rows: W3 at 64 sessions
ran at 41 tx/s in memory, below 16 sessions' 146. Taking the batch as the
merge's first side (the item rule leaves the sides disjoint, so the
result is the same) made it 163 and 265 (`8737891`'s figures). The next
core lifts one session fourfold in memory (a pending pack sized to what
it holds, ranked change 3) and adds about 5% on disk; above one session
the leader's reading, not the swap, is the ceiling.

## Where the time goes

**W1 bulk load.** One goroutine does everything, CPU-bound (82% of a core on
disk): building the prolly trees (60% of CPU, in `prolly.(*Editor).Flush`),
and inside that compressing each chunk with zstd (23 to 29%) and hashing it
with SHA-256 for its address (17 to 20%). The table loads at 40k rows/s on
disk and 49k in memory; 1k-row batches are 3 to 4.4 times slower per row
because each carries a commit (about 100 ms on disk, fsyncs included, for
1k rows). kv loads at 122k keys/s (255k in memory), documents at 13.5k/s
(12.8 MB/s; 31.8k/s in memory), bounded by the same single-threaded chunk
preparation plus the document model's JSON parse. It scales with the batch
until the commit is amortized; it does not use a second core.

**W2 reads.** A point read takes 33 µs on disk and 27 µs in memory: a
walk from the root that, for each node, takes the packstore's lock, copies
the chunk out of its cache and re-hashes it (the spec verifies every read,
cached or not), or on a miss (the cache holds 64 MiB of a 424 MiB database)
opens the pack file, reads, decrypts and decompresses; then decodes the
node (21 to 23% of CPU) into fresh slices. A point read allocates about
44 KB. Sixteen sessions reach 180k reads/s on disk and 169k in memory,
5.4 and 4.4 times one session on 16 threads: 79% (disk) and 93% (memory)
of lock wait is the packstore cache's single mutex. An index lookup returns 1,000 rows in 26
to 31 ms, a primary-key `Get` per index entry, about 30 µs a row. A full
scan runs at 737k rows/s (826k in memory).

**W3 OLTP.** One session on disk commits every 31 ms: two rounds of fsync
(the pack and its index object in parallel, then the root), 21 ms of the
disk's time, plus about 3 ms zeroing a fresh 32 MiB pack buffer and the
merge and manifest work. Adding sessions makes it slower, and a simple
model accounts for it. Every commit ends in one compare-and-swap of the
repository's single root (`vcs.update`, then
`packstore.CompareAndSetRoot`), serialized by the packstore's commit lock.
The core finishes and uploads a committer's pending pack and index object,
with their fsyncs, and only then compares the root. With N committers
queued, one wins (31 ms) and the other N-1 each spend about 11 ms uploading
and fail, re-read the working set, merge again and queue again. That
predicts 16, 5.1 and 1.4 tx/s at 4, 16 and 64 sessions; the disk measured
12.5, 4.1 and 1.7. The block profile agrees: 97% of blocked time is on
that lock. In CPU terms (memory backend, all of W3), the manifest read
takes 39% of CPU in W3, zeroing pack buffers 18%, the models' merges after
lost swaps 24%, GC 10%; W3 allocated 594 GB in five minutes on disk, 430
GB of it pack buffers. Zipf 1.1 on a million rows adds few real conflicts
(25 of 849 failures at 64 sessions); the collapse is contention on the
root, not on rows.

**W4 Redis-shaped.** One op per transaction behaves like W3: 11.7 and 5.1
ops/s on disk at 16 and 64 sessions. A hundred ops per transaction carries
30 times the ops for the same number of commits (359 ops/s on disk, 656 in
memory at 16 sessions). CPU is the models' merges after lost swaps (55%),
chunk preparation (35%) and the manifest read (18 to 26%). INCR on a
counter never lost an increment (counters merge by summing).

**W5 documents.** The same collapse: 3.4 tx/s on disk and 7.8 in memory
at 16 sessions, hot records no worse than uniform ones, because the
contention is on the root, not the records. The manifest read is 49%
(disk) and 64% (memory) of CPU here, more than in W3 because the manifest
is longer by now.

**W6 version control.** Merge and diff cost scales with the change, not
the table. A 100+100-row merge takes 117 ms on disk (52 in memory), most of
it the two publishes a merge commit makes; 10,000+10,000 rows take 470 ms
(369 ms), about 20 µs a changed row. A diff walks the changes at 2 µs each:
0.36 ms for 100, 20 ms for 10,000. What does not scale is the transaction
that makes the changes: updating 10,000 rows (read, then write, each) took
5.9 s, 0.59 ms a row, against 0.025 ms a row to insert in W1, because every
read after a write flushes the transaction's pending edits into new chunks
(`engine.(*Table).flush`, 61% of CPU) so the read can see them. A session
commit took 50 ms on disk here against 34 ms after the load (16.8 ms
against 3.0 ms in memory): the manifest again.

**W7 large objects.** Nothing breaks: 1 MiB documents store and read back,
64 KiB rows and 256 KiB kv values too. Writes run at 72 MB/s for wide rows,
43 MB/s for 1 MiB documents and 38 MB/s for 256 KiB values on disk (101,
57 and 67 MB/s in memory), bounded by single-threaded chunk preparation
and the commit. Reads run at 362 MB/s for wide rows, 153 MB/s for documents
(the model parses each back into a `Node`) and 738 MB/s for kv values (452,
168 and 986 MB/s in memory).

**W8 sustained mix.** 1.0 tx/s on disk and 1.6 in memory with 64 sessions,
steady across the five minutes (each 10 s line between 0.5 and 2.3 tx/s on
disk, 1.0 and 4.4 in memory). The manifest read is 55% of CPU on disk and
70% in memory, the commit lock 97% of blocked time. On disk the heap stayed
between 277 and 541 MiB and the directory grew 67 MiB over the phase, most
of it packs that lost swaps uploaded and nothing references (no GC runs).
In memory the heap between GCs swung from 3.0 to 6.0 GiB, the in-memory
store plus the 32 MiB pack buffers of up to 64 committers.

## What to change, ranked

The lost update (above) is a correctness decision and comes before any of
these. Estimates are from the profiles and the models above; each change
lands as a `refactor:` with the bench's before and after figures.

| # | Change | Where | Estimated effect |
| ---: | --- | --- | --- |
| 1 | **Done (DESIGN D20; "Group commit" above).** **Commit transactions through a per-branch queue in the engine: group commit.** A committer that finds others waiting merges their transactions onto the working set one after another, in memory, and publishes once for the group; each transaction still succeeds or fails alone. Lost swaps between this process's own transactions disappear; the optimistic swap stays for other processes. Session commits go through the same queue | engine (`engine/txn.go`, `Session.Commit`, `Session.Merge`) | Disk at 64 sessions from 1.7 tx/s to roughly 200 to 500 (one 31 ms publish for a group, plus 1 to 4.5 ms of merge per transaction); at 16 from 4.1 to 150 to 300; no more lost-swap failures or give-ups in one process; session commits wait one group, not minutes |
| 2 | **Stop re-reading the manifest on every root read.** Compare the stored root's version with the one in hand before decrypting and decoding; the store's own swaps already update it | core (`core/chunk/packstore`: `Root`, `refresh`) | Removes the growth in item 4 of the list above: `Begin` and commit stop slowing with age. 28 to 70% of CPU under contention (W3, W5, W8), 39% in W3 on memory |
| 3 | **Size the pending pack to what it holds.** `newWriter` asks `pack.NewWriter` for the full 32 MiB pack size as its starting buffer; `NewWriterSized` with a small expectation grows only as needed | core (`core/chunk/packstore`) | A one-row commit on memory from about 3 ms to well under 1 ms (a commit that publishes nothing takes 0.1 ms); 3 ms of 31 ms on disk; 430 of 594 GB allocated in W3; 14 to 22% of CPU under contention, and GC load |
| 4 | **Check the root before uploading, and back off between lost swaps.** `CompareAndSetRoot` can refuse a stale expected root before it finishes and uploads the pending pack; `vcs.update` retries 1,000 times without waiting | core (`core/chunk/packstore`, `core/vcs`) | Without #1, N sessions get about one session's throughput (26 tx/s on disk) instead of collapsing; with it, it protects writers in other processes; ends the four-minute session commit |
| 5 | **Read your writes without flushing.** A read after a write consults the transaction's pending edits instead of writing them into new chunks | engine (`engine/txn.go` handles; the models' editors) | Large read-modify-write transactions 10 to 20 times faster (W6: 10k rows in 5.9 s, against 0.25 s to insert 10k); fewer garbage chunks per transaction |
| 6 | **Shard the chunk cache and size it to the machine.** One mutex guards the LRU; 64 MiB default | core (`core/chunk/packstore/cache.go`) | 16-session point reads from 170 to 180k/s toward 300k/s; fewer misses on a working set over 64 MiB |
| 7 | **Cache decoded nodes, and skip the copy on a cache hit.** A point read decodes every node into fresh slices and clones each cached chunk before re-hashing it: 44 KB allocated a read | core (`core/prolly`, `core/chunk/packstore`) | Point reads 2 to 3 times faster. Needs a reading of the spec's "SHA-256 verified on every read, from every backend, cached or not" for a cache above verification |
| 8 | **Prepare chunks in parallel on a large flush.** `Prepare` (compress, seal, hash) is already apart from `PutPrepared`; a flush calls them one chunk at a time | core (`core/prolly` store path) | Bulk load and large objects 1.5 to 2 times faster: compression and hashing are 40 to 50% of W1's single core |
| 9 | **Fetch an index lookup's rows in key order.** Each index entry does a full primary-key `Get` from the root | engine (`model/table`) | A 1,000-row lookup from 26 to 31 ms to 5 to 10 ms |
| 10 | **Expose GC through the engine.** The core has `repo.GC`; a program on the engine cannot reach it, so the manifest only grows (until 100,000 index objects stop publishes) and packs that lost swaps and flushes wrote are never reclaimed (1.5 GB on disk after the run, 424 MiB of it data) | engine (`engine/database.go`), using `core/repo` | Bounded manifest and disk; with #2, flat per-transaction cost for the life of a database |
| 11 | **Keep pack files open.** `blob/local` opens the file for every chunk read | core (`core/blob/local`) | 5 to 7% of read CPU on disk |

Items 2, 3, 4, 6, 7, 8 and 11 are core changes: they are asked for in
snapshot-core, land in its own PR and tag, and the engine moves to that tag
(DESIGN D2). Items 1, 5, 9 and 10 are the engine's.

## Core v0.2.0 (#7)

The move to snapshot-core v0.2.0, with kv's counters summing in the
merge and a counter's INCR and DECR leaving the item rule (DESIGN D18,
#7). W4 and W8 only (`-only W4,W8`), full scale, both backends, each
run under the shared measurement lock on a quiet machine (load under 2
and no test or bench process before each run started). "Before" is
main's engine (4689848) on core v0.1.1; "after" is this branch as it
ends (8e828cd) on v0.2.0. Every phase checks each counter against the
sum of its increments, the table balances against theirs and the
document fields against theirs. No phase of any run lost an update:
W8's counters, 2,233 and 2,552 increments on disk, 2,957 and 3,031 in
memory, all accounted for. Load average during each run, median (max):
disk before 2.04 (3.84), after 1.89 (3.54); memory before 1.70 (2.11),
after 1.72 (2.87). The disk maxima include a few short builds and
single-package test runs of the author's during the disk runs.

W4, kv 70% GET, 20% SET, 10% INCR; W8, the sustained mix, 64 sessions
for 5 minutes. W4 in ops/s, W8 in tx/s; retries are the phase's
serialization failures, every one retried and committed:

| Phase | Disk before | p99 | retries | Disk after | p99 | retries | Memory before | p99 | retries | Memory after | p99 | retries |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| W4, 1 op/tx, 16 sessions | 427.9 | 143 ms | 1 | 423.6 | 143 ms | 0 | 1,077.4 | 61 ms | 0 | 1,288.7 | 50 ms | 0 |
| W4, 1 op/tx, 64 sessions | 601.9 | 352 ms | 2 | 604.2 | 369 ms | 0 | 645.4 | 336 ms | 5 | 700.1 | 319 ms | 0 |
| W4, 100 ops/tx, 16 sessions | 2,399.2 | 1.6 s | 76 | 2,401.4 | 705 ms | 2 | 3,072.2 | 1.0 s | 77 | 2,962.6 | 570 ms | 2 |
| W4, 100 ops/tx, 64 sessions | 1,777.2 | 10.7 s | 189 | 1,574.2 | 4.2 s | 5 | 1,845.9 | 8.3 s | 202 | 1,743.7 | 7.0 s | 6 |
| W8, 64 sessions | 252.5 | 386 ms | 5 | 274.2 | 352 ms | 2 | 324.7 | 302 ms | 4 | 336.9 | 285 ms | 6 |

**What changed.** At 100 ops per transaction, where a transaction's ten
INCRs collide with other transactions' on the same counters, the
retries fall from 76 to 2 at 16 sessions and from 189 to 5 at 64 on
disk, and from 77 to 2 and 202 to 6 in memory: those were the item rule
refusing two INCRs of one counter, and they now sum. The tail latencies
fall with them, p99 from 1.6 s to 705 ms at 16 sessions and from 10.7 s
to 4.2 s at 64 on disk. Throughput at 100 ops per transaction and 64
sessions is 11% lower on disk and 6% lower in memory; at 16 sessions
and 100 ops it is level on disk and 4% lower in memory. The phases that
rarely collided (1 op per transaction) and W8 are level or better: W8
252.5 to 274.2 tx/s on disk, 324.7 to 336.9 in memory, and one op per
transaction at 16 sessions in memory 1,077 to 1,289. Whether the drop at
100 ops and 64 sessions is the merge summing counters where the item
rule refused before, or noise between two runs, one run each cannot
say; the phase's p99 says the work per transaction is not larger.

**Not confirmed here.** The group-commit section's "next core" column
took one session of W3 in memory from 214 to 922 tx/s on the unreleased
core. W3 was not part of these runs; it is measured on v0.2.0 by #8.
