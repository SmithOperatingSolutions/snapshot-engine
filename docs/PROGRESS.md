# Progress

Where the engine stands against `docs/specs/engine-layers.md` and the Engine
Spec's L4 checklist. Updated at each milestone boundary and whenever a
checklist item turns green; the evidence for each item is the named test, and
the commit that added it carries its red.

**Updated 2026-09-24** · scaffold; no code beyond the API's boundary type

## Milestones

| Milestone | Status | Delivers | Exit criteria |
| --- | --- | --- | --- |
| **E0 Foundations** | 🚧 | Module on snapshot-core v0.1.0, gates (fmt, vet, lint, vuln, race, coverage, redcheck, mutants), `model/kv` at one value kind passing `model/contract` from outside the core | A deliberately failing `test:` commit blocks a PR; a kv object round-trips through a repository and merges cleanly on non-overlapping keys |
| **E1 Merge library** | | `merge/`: scalars by policy, trees by path, sets, counters, sequences; conflicts reported, never repaired | Every policy has a property test: deterministic, one-sided change returns that side, symmetric where the policy is |
| **E2 Tables** | | `model/table`: catalog with tagged columns, order-preserving tuple encoding, primary and secondary indexes, diff per row then cell, merge schema first | The Engine Spec's L4 checklist green (below) |
| **E3 Key-value** | | `model/kv` whole: bytes, counter, set, hash, sorted set, sequence, each with its policy | `model/contract` green; a Redis-shaped workload of every kind branches and merges |
| **E4 Engine API** | | `engine/`: `Database`, `Session`, `Txn`; snapshot isolation; optimistic commit through the models' merge; authorization per call; diff, merge, log | The spec's transaction and authorization items green; an integration suite drives a repository through `engine/` alone |
| **E5 Documents** | | `model/document`: records by id, field-path diff and merge | `model/contract` green; concurrent edits to different fields of one record both land |

Adapters (RESP, Mongo, MySQL, pgwire, filesystems) are separate repositories
and are not tracked here.

## Engine Spec L4 checklists

Carried from `docs/specs/engine-spec.md`, "First failing tests to write",
read per D1: the encoding and index items are `model/table`'s, the
transaction and authorization items `engine/`'s.

### Encoding and tables (E2)
- [ ] **Property:** for random values of each type, `decode(encode(v)) == v`.
- [ ] **Property:** for random pairs, `bytes.Compare(enc(a), enc(b))` matches SQL comparison of a and b.
- [ ] Insert, update, delete, and point lookup by primary key.
- [ ] Secondary index returns the same rows as a full scan with a filter.
- [ ] Writing a 300-char string into `varchar(255)` is rejected; nothing is written.
- [ ] Rename a column on branch A, update a row on branch B, merge: data survives under the new name.

### Transactions and authorization (E4)
- [ ] Two txns update different rows concurrently: both commit.
- [ ] Two txns update the same cell: second commit returns a serialization conflict; its changes are absent.
- [ ] A principal without `write` on `main` gets `ErrPermissionDenied` on insert, and the working set hash is unchanged.
- [ ] Session authz is re-checked per call: revoking a grant mid-session blocks the next call.

## Engine layers checklists

### E0 Foundations
- [ ] The module builds against snapshot-core v0.1.0 with `CGO_ENABLED=0`; lint, vet and vuln clean.
- [ ] `go tool redcheck` blocks a PR whose `test:` commit passes without its change.
- [ ] `go tool mutate` runs the checked-in catalog; a surviving mutant fails the run.
- [ ] `model/kv` with the bytes kind passes `model/contract`.
- [ ] A kv object written through a repository reads back from a fresh open, byte for byte.
- [ ] Two branches setting different keys merge clean; setting one key to two values conflicts on that key alone.

### E1 Merge library
- [ ] Scalars: equal is clean; one side changed is that side; both changed differently is a conflict.
- [ ] Counters: both sides' deltas add.
- [ ] Trees: different fields both land; the same field is a scalar merge at that path; deleted on one side and changed on the other is a conflict.
- [ ] Sets: union of additions minus deletions; removed on one side and re-added on the other is present.
- [ ] Sequences: concurrent inserts at one position land in a deterministic order and are flagged.
- [ ] **Property:** every policy is deterministic and returns either side unchanged when the other made no change.

### E3 Key-value
- [ ] Every value kind round-trips through its decoder; every decoder has a fuzz target.
- [ ] A hash merged per field; a sorted set keyed by score then member keeps its order across a merge.
- [ ] `model/contract` green for every kind.

### E5 Documents
- [ ] A record's fields diff by path; two writers on different fields both land.
- [ ] A document parsed from JSON by the model's own bounded parser; a malformed document is refused with nothing written.
- [ ] `model/contract` green.

## Decisions

- **Structure** (2026-09-24): four layers over the core, models and merge library here, the engine API here, adapters elsewhere; `docs/specs/engine-layers.md` and DESIGN D1–D7.

## What testing found

| Where | What | Resolved |
| --- | --- | --- |
