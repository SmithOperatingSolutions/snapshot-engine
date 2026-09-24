# The Engine: layers, scope and order

The Versioned DB is one storage core and many faces. The core
([snapshot-core](https://github.com/SmithOperatingSolutions/snapshot-core))
stores namespaces of typed objects with history and knows nothing about what
they mean. The faces are databases people already speak to: Postgres, MySQL,
Mongo, Redis, and filesystems. This document says how the two meet without
one model per database, and what is built in this repository.

The rule that shapes everything: **every database flavor is a protocol and a
query language over a small number of data shapes.** The shapes and their
merge semantics live here, once. The protocols and languages live in
adapters, one repository each, and translate to a single Go API.

## The layers

```
  L6  adapters        pgwire · mysql · mongo wire · RESP · FUSE / S3 API      (one repository each)
  L5  engine API      Database · Session · Txn · Diff · Merge · Log            (engine/)
  L4  data models     model/table · model/kv · model/document                  (this repository)
      merge library   three-way merge of typed values, trees, sets, counters  (merge/)
  L0–L3 storage core  chunks · prolly maps · objects · commits · GC          (snapshot-core)
```

| Layer | Path | Owns | May import |
| --- | --- | --- | --- |
| merge library | `merge/` | three-way merge of typed scalars, of JSON-like trees by path, of sets by union, of counters by delta; conflict reporting | the standard library |
| L4 models | `model/table`, `model/kv`, `model/document` | one data shape each: its encoding, `Validate`, `Diff`, `Merge`, the walk GC needs; a model id | `merge/`; the core's `model` (the plugin port), `chunk` (the read/write port a model is handed), `prolly`, `stream`, `hash` |
| L5 engine API | `engine/` | opening a database on a branch, sessions, transactions, commits, branches, diff between commits, merge of branches, the log; authorization on every call; conflict handling | the models; the core's `repo`, `vcs`, `object`, `auth`, `model`, `seal` |
| L6 adapters | separate modules | a wire protocol and a query language; a planner where the language needs one | `engine/` only, never a model, never the core |

The Engine Spec names one layer, L4, for tables and the API over them. Here
that is two: the models are L4 and the API is L5, so that a second and third
model can arrive without a second API. Where the spec's L4 text is used
below, it is read that way (DESIGN D1).

## What the core already gives

A database is a subtree of one object namespace (`core/object`): paths to
typed objects, each object a `model.Root` naming its model id, format version
and root hash. From that the core supplies, to every model and every adapter
alike:

- **Cross-object atomic commits.** A commit spanning ten tables or ten
  collections is one publish. No flavor builds transactions across its own
  objects; they come from the namespace.
- **Branches and merges of the whole database**, with the three-way merge
  delegated per object to its model.
- **The plugin port**, `core/model.Model`: `ID`, `FormatVersion`, `Validate`,
  `Diff`, `Merge`, and the optional `Walker` GC uses; a `Registry`; the
  contract suite every model passes (`model/contract`).
- **Ordered maps** (`core/prolly`) and **byte streams** (`core/stream`) as the
  two building blocks under every shape, content-defined so that history
  independence holds: the same content gives the same hashes whatever the
  path taken.
- **Encryption, backends, GC, authorization principals.**

## The models

Four shapes cover the flavors in view. Two exist in the core already
(`model/blob` for files, `model/tree` for folders). Three are built here.

**`model/table`** (Engine Spec L4, id 3). A schema (the catalog) with columns
carrying stable numeric tags, rows by primary key in one prolly map, one
prolly map per secondary index, a hidden 16-byte row id where no key is
declared. Order-preserving key encoding so byte order is SQL order, NULLs
first. Diff is per row then per cell; merge is schema first, then rows, then
cells, through the merge library. Serves Postgres and MySQL.

**`model/kv`** (id 6). An ordered map from key to value, where each value has
a kind and each kind a merge policy: opaque bytes (last writer wins, conflict
on concurrent change), counter (sum of deltas), set (union with deletions),
hash (a map merged per field), sorted set (a map keyed by score then member),
sequence (an ordered list with its own merge). Serves Redis, and is the
smallest real model: the first one built, because it proves the plugin port
from outside the core at low cost.

**`model/document`** (id 4, the spec's "JSON document", `model/json` in its table). Records by id, each a tree of fields with no
schema; diff and merge by field path through the merge library. Serves Mongo.
Most of it is the merge library and the key encoding table already has.

The ids come from the Storage Core Spec's registry table, which is the
registry of record: table 3 and JSON document 4 are listed there (5 is
reserved for time series); kv 6 is proposed here and is added there before
the model ships (DESIGN D3).

## The merge library

The piece that makes three models cheap instead of expensive. One package,
`merge/`, with no dependency on the core, that answers: given a base, ours and
theirs of a typed value, what is the merged value, or what is the conflict?

- Scalars by type: equal is clean; one side changed is that side; both changed
  to different values is a conflict, except where the type's policy says
  otherwise (counters add their deltas).
- Trees (a document, a JSON cell, a Redis hash): merge by path, so two writers
  changing different fields of one record both land; a field changed on both
  sides is a scalar merge at that path; a field deleted on one side and
  changed on the other is a conflict.
- Sets: union of additions, minus deletions, with the observed-remove rule so
  an element removed on one side and re-added on the other is present.
- Sequences: the one hard shape; an ordered list merged by element identity
  and position, with concurrent inserts at the same position kept in a
  deterministic order and flagged.

Every policy is a pure function with a property test: merging is
deterministic, symmetric in ours and theirs where the policy is, and returns
either side unchanged when the other side made no change.

## The engine API (L5)

The spec's `Database`, `Session` and `Txn`, with the VCS operations reached
through `Session` and never by an adapter reaching into the core:

- `Open(ctx, principal)` gives a session on a branch; every method re-checks
  the principal against the core's authorizer.
- `Begin` opens a transaction with snapshot isolation on the working set as
  of `Begin`; `Commit` on the transaction is optimistic: re-read the working
  set, merge the two through the models if it moved, publish on a clean
  merge, fail closed with a serialization conflict otherwise. No partial
  commits.
- `Commit(msg)` on the session is a VCS commit; `Merge(from)`, `Diff(from,
  to, object)`, `Log`, `Checkout`, `Branch`.
- The API is Go, usable without any protocol, and is what the adapters and the
  integration tests call. It is frozen at v1 of this module; until then a
  minor version may change it and says so.

## What is pluggable, and where

Three things differ across flavors. They are decided in the models, never
hidden in an adapter:

- **Identity of a record**: a primary key, an `_id`, a Redis key. The model
  owns it, because diff and merge are defined by it.
- **Merge policy**: last writer wins, structural by field, set union, counter.
  A Redis counter and a Postgres integer column are the same bytes with
  different merge meaning; the policy is declared in the catalog or the key's
  kind, not guessed by an adapter.
- **Schema evolution**: a Postgres `ALTER TABLE` and a Mongo document that
  simply has a new field are the ends of one axis. Table merges schema first
  with explicit rules for concurrent schema changes; document has no schema.
  That difference is why they are two models.

Query processing stays out of the models. A model exposes point lookup, range
scan, and index maintenance; a planner in an adapter turns a language into
those calls.

## What is not here

- **Adapters.** Each in its own repository, importing `engine/` alone:
  `snapshot-pgwire`, `snapshot-mysql`, `snapshot-mongo`, `snapshot-resp`,
  `snapshot-fs`. They carry the heavy protocol dependencies and their own
  release cadence.
- **SQL parsing and planning.** An adapter's. One constraint is decided now:
  every adapter is pure Go like the core and the engine, so the common
  Postgres parser (cgo) is out; a pure-Go parser is chosen in the pgwire
  repository.
- **The storage core itself.** Behavior it lacks is asked for in its
  repository; this module moves to the tag that carries it.

## Order

1. **E0 Foundations.** This module, its gates, and `model/kv` at its first
   value kind, passing `model/contract` from outside the core: the first real
   test of the plugin port from a consumer, on the smallest model.
2. **E1 The merge library**, built with table in view and document in mind.
3. **E2 `model/table`**: the Engine Spec's L4 checklist.
4. **E3 `model/kv`** whole: every value kind and policy, sequences last.
5. **E4 The engine API**: sessions, transactions, optimistic commit,
   authorization per call. A Redis adapter can reach end to end here, in its
   own repository, before any SQL work leans on the API.
6. **E5 `model/document`.**
7. Adapters: RESP first (smallest, proves L5 end to end), then Mongo, then
   MySQL and Postgres (largest, and they benefit most from an API two other
   adapters have already bent into shape). Filesystem adapters can start any
   time; their models exist.
