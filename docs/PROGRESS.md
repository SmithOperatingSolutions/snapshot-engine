# Progress

Where the engine stands against `docs/specs/engine-layers.md`: the merge
library, the models, and the engine API built in this repository. Updated at
each milestone boundary and whenever a checklist item turns green; the
evidence for each item is the named test, and the commit that added it
carries its red.

Everything below the engine is the storage core's and is tracked in its own
repository: chunks, packs and backends, encryption, prolly maps and streams,
namespaces, commits, branches, merges of the version graph, garbage
collection, and the Engine Spec's L0 to L3 checklists and security rows.
See [snapshot-core's PROGRESS](https://github.com/SmithOperatingSolutions/snapshot-core/blob/main/docs/PROGRESS.md).
The engine takes the core by tag (`go.mod`) and never tracks its items here.

**Updated 2026-09-24** · E1 done: the merge library; kv and table in progress

## Milestones

| Milestone | Status | Delivers | Exit criteria |
| --- | --- | --- | --- |
| **E0 Foundations** | 🚧 | The module on snapshot-core v0.1.0; the gates (fmt, vet, lint with the layer table, vuln, race, coverage, redcheck, mutants); `model/kv` at one value kind, passing the core's `model/contract` from outside the core | A deliberately failing `test:` commit blocks a PR; a kv object round-trips through a repository and two branches setting different keys merge clean |
| **E1 Merge library** | ✅ Done | `merge/`: `Scalar`, `Counter`, `Set` (observed-remove over write tags), `Sequence` (diff3 over element keys), `Tree` (a JSON-like `Node` merged by path, arrays as sequences, counter paths by option); conflicts as values with a path and a reason, never repaired; 97% covered, 11 mutants | Every policy has a property test: deterministic, one-sided change returns that side, symmetric (`TestScalarAndCounterProperties`, `TestSetProperties`, `TestSequenceProperties`, `TestTreeProperties`) |
| **E2 Tables** | | `model/table`: catalog with tagged columns, order-preserving tuple encoding, primary and secondary indexes, diff per row then cell, merge schema first | Every E2 item below green |
| **E3 Key-value** | | `model/kv` whole: bytes, counter, set, hash, sorted set, sequence, each with its policy | Every E3 item below green |
| **E4 Engine API** | | `engine/`: `Database`, `Session`, `Txn`; snapshot isolation; optimistic commit through the models' merge; authorization on every call; commit, branch, merge, diff, log | Every E4 item below green; `e2e/` drives a repository through `engine/` alone |
| **E5 Documents** | | `model/document`: records by id, field-path diff and merge, a bounded parser for JSON input | Every E5 item below green |

Adapters (RESP, Mongo, MySQL, pgwire, filesystems) are separate repositories
with their own progress; they import `engine/` alone.

## Checklists

### E0 Foundations
- [x] The module builds against snapshot-core v0.1.0 with `CGO_ENABLED=0`; gofmt, vet, lint and vuln clean (`mise run ci:quick`, 2026-09-24).
- [x] The core's `redcheck` and `mutate` run here as Go tools (`go tool redcheck`, `go tool mutate`; `go.mod`).
- [ ] `go tool redcheck` blocks a PR whose `test:` commit passes without its change (the first PR proves it, on purpose).
- [ ] `model/kv` with the bytes kind passes the core's `model/contract`.
- [ ] A kv object written through a repository reads back from a fresh open, byte for byte.
- [ ] Two branches setting different keys merge clean; setting one key to two values conflicts on that key alone.
- [ ] Every kv structure on disk has a bounds-checked decoder and a fuzz target.

### E1 Merge library (`merge/`)
- [x] Scalars: equal is clean; one side changed is that side; both changed differently is a conflict (`TestScalarsMergeByWhoChanged`).
- [x] Counters: both sides' deltas add (`TestCountersAddTheirDeltas`); a counter path holding a non-integer is a conflict, not a repair (`TestTreesMergeByPath`, mutant `merge-tree-counter-refuses-a-non-integer`).
- [x] Trees: different fields both land; the same field is a scalar merge at that path; deleted on one side and changed on the other is a conflict; arrays merge as sequences keyed by the element's canonical form (`TestTreesMergeByPath`).
- [x] Sets: union of additions minus deletions; removed on one side and re-added on the other is present, by write tags (`TestSetsKeepAdditionsDropRemovalsAndKeepReAdds`).
- [x] Sequences: concurrent inserts at one position land in a deterministic order and are flagged; two changes to one stretch conflict and the base stretch is kept (`TestSequencesMergeByIdentityAndPosition`).
- [x] **Property:** every policy is deterministic and returns either side unchanged when the other made no change (`TestScalarAndCounterProperties`, `TestSetProperties`, `TestSequenceProperties`, `TestTreeProperties`).
- [x] **Property:** every policy is symmetric in ours and theirs, and documented so; conflict reasons are worded side-neutral so symmetry holds (the same tests).

### E2 Tables (`model/table`; the Engine Spec's L4 storage layout and encoding)
- [ ] **Property:** for random values of each v1 type, `decode(encode(v)) == v`.
- [ ] **Property:** for random pairs, `bytes.Compare(enc(a), enc(b))` matches the type's SQL comparison, NULLs first.
- [ ] Insert, update, delete, and point lookup by primary key.
- [ ] A table without a declared primary key gets a hidden 16-byte row id; two inserts of equal rows are two rows.
- [ ] Secondary index returns the same rows as a full scan with a filter; an index is updated with its primary map, never apart from it.
- [ ] Writing a 300-char string into `varchar(255)` is rejected; nothing is written. Every value is validated against its column type; invalid input is refused, never coerced.
- [ ] The catalog is a versioned record with a stable numeric tag per column; a forged or truncated catalog is refused (fuzz target).
- [ ] Rename a column on branch A, update a row on branch B, merge: data survives under the new name.
- [ ] Two branches change the same cell differently: a conflict on that cell alone; the rest of the table merges.
- [ ] Two branches change the schema incompatibly (drop a column and write to it): a schema conflict, no rows lost.
- [ ] `model/contract` green.

### E3 Key-value (`model/kv`)
- [ ] Every value kind round-trips through its decoder; every decoder has a fuzz target.
- [ ] A counter incremented on two branches merges to the sum.
- [ ] A set added to on one branch and removed from on the other keeps the observed-remove rule.
- [ ] A hash merged per field; a sorted set keyed by score then member keeps its order across a merge.
- [ ] A sequence with concurrent inserts merges deterministically and flags the position.
- [ ] `model/contract` green for every kind.

### E4 Engine API (`engine/`; the Engine Spec's L4 transactions and authorization)
- [ ] `Open(ctx, principal)` on a branch; `Checkout`, `Branch`, `Log`.
- [ ] A transaction reads the working set as of `Begin` and sees none of another transaction's writes (snapshot isolation).
- [ ] Two txns update different rows concurrently: both commit.
- [ ] Two txns update the same cell: the second commit fails with a serialization conflict; its changes are absent; nothing partial reached the working set.
- [ ] Session `Commit` is one VCS commit; `Merge(from)` merges branches through the models and reports conflicts per object.
- [ ] `Diff(from, to, object)` yields a model's change iterator for a table, a kv map, a document collection.
- [ ] A principal without `write` on `main` gets `ErrPermissionDenied` on insert, and the working set hash is unchanged.
- [ ] Session authorization is re-checked per call: revoking a grant mid-session blocks the next call.
- [ ] A protected branch refuses direct writes and requires `merge`.
- [ ] Errors to callers carry no keys, values or rows (log-scrubbing test).
- [ ] `e2e/`: a repository created, written, branched, merged and read back through `engine/` alone, with the models registered by the caller.

### E5 Documents (`model/document`)
- [ ] A record's fields diff by path; two writers on different fields of one record both land.
- [ ] A field deleted on one side and changed on the other is a conflict on that field alone.
- [ ] A document parsed from JSON by the model's own bounded parser; a malformed or oversized document is refused with nothing written (fuzz target).
- [ ] `model/contract` green.

## Decisions

- **Structure** (2026-09-24): four layers over the core; models and the merge
  library here, the engine API here, adapters elsewhere.
  `docs/specs/engine-layers.md`, DESIGN D1 to D7.
- **Pure Go** (2026-09-24): `CGO_ENABLED=0` here and in every adapter; a cgo
  SQL parser is refused. DESIGN D4.
- **Model ids** (2026-09-24): table 3 and document 4 per the Storage Core
  Spec's registry table (5 is its time series); kv 6 proposed, to be added
  there before it ships. DESIGN D3.

## What testing found

| Where | What | Resolved |
| --- | --- | --- |
