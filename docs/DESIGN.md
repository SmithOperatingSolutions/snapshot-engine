# Design

How `docs/specs/engine-layers.md` and the Engine Spec's L4 become code.
Formats and protocols are recorded here as they land; until then this file is
the decisions and the layer table the build enforces.

## 1. Decisions

| # | Question | Decision | Why |
| --- | --- | --- | --- |
| D1 | The Engine Spec's L4 is "tables, schema and sessions"; here the models are one layer and the API another | L4 = the models (`model/*`) and the merge library; L5 = the engine API (`engine/`). The spec's L4 text is read that way: its storage layout, encoding and transaction rules belong to `model/table` and `engine/`, its `Database`/`Session`/`Txn` sketch to `engine/` | A second and third model (kv, document) arrive without a second API; adapters depend on one package |
| D2 | Dependency on the storage core | A pinned tag of `github.com/SmithOperatingSolutions/snapshot-core` (v0.1.1), never edited, forked, vendored or `replace`d; its `tools/ci`, `tools/redcheck` and `tools/mutate` run here as Go tools (`go.mod` `tool` directives). Work that needs an untagged core is built under a local, uncommitted `go.work` until the core tags it, and the engine moves to the tag in a commit that comes before any commit using it, so every commit builds against a tag | One test discipline across both repositories; behavior the core lacks is asked for there, in its own PR |
| D3 | Model ids | Table is 3 and the JSON document is 4, as the Storage Core Spec's registry table says (its package name is `model/json`; here the model is `model/document`); 5 is the spec's time series. kv is proposed as 6; the core's table is the registry of record and is amended there before kv ships | One registry across every consumer of the core; two plugins can never collide |
| D4 | Pure Go | `CGO_ENABLED=0` for this module and, by rule, for every adapter. A dependency that needs cgo is refused; the pgwire adapter chooses a pure-Go SQL parser | Same build story as the core: one static binary, no toolchain beyond Go, the race detector the only exception |
| D5 | Where merge semantics live | In `merge/`, a package with no dependency on the core, called by every model per cell, record or value; policies are pure functions with property tests | Three models share one set of semantics; a merge bug is fixed once; the library is testable without a repository |
| D6 | What an adapter may import | `engine/` alone. `engine` re-exports what an adapter needs of the core (`engine.Principal` is `auth.Principal`) so an adapter never imports the core or a model | The API is the boundary the spec's rule 5 asks for; an adapter cannot reach past it by accident |
| D7 | The run-all | The storage core's `tools/ci`, a Go tool since core v0.1.1, told the engine's layout (`-product merge/,model/,engine/ -adapters=`): fmt, vet, mod verify, lint, vuln, race, the 90% coverage gate, redcheck, fuzz and the mutant catalog, the same steps and gates as the core's | One test discipline across both repositories, from one runner |

| D8 | Set semantics in the merge library | Observed-remove over write tags: a member is `Tagged{Elem, Tag}`, and a removal drops the tags it saw, so a member removed on one side and re-added on the other (a new tag) is present. A model that stores no per-add tag uses one tag per member and loses re-add semantics; kv stores a tag per member | The only set merge that is both deterministic and matches what two writers meant |
| D9 | Sequence conflicts | `Sequence` is a diff3 over element keys aligned by LCS; both-inserted blocks at one position are kept in key order and flagged; two changes to one stretch conflict and the base stretch stays in the value, so the result is a merge only when `Clean()` | Deterministic, side-neutral, and never invents an order the writers did not |
| D10 | Trees | A JSON-like `Node` (null, bool, number as canonical text, string, array, object with sorted fields) merged by path; arrays merge as sequences keyed by the element's canonical form; `TreeOptions.Counter` names the paths whose integers add, and a non-integer there is a conflict | One tree merge serves a document, a JSON cell and a Redis hash |

| D11 | A sequence both sides inserted into at one position | Clean: both blocks kept in key order, `merge.Result.Flagged` set. The plugin port offers only a conflict, which would stop a merge a writer need not resolve; the engine API (E4) is where a flag reaches a caller | Nothing is lost and the order is deterministic; a conflict would block a merge on something that needs no decision |

| D12 | What a caller is shown | Every exported call returns through `scrub`: the engine error it matches (`ErrInternal` when none) as an `*Error` with a fresh correlation id, the details logged to the `Logger` under that id; a cancelled context and a scan callback's own error go back as they came | The Engine Spec's security row: errors to callers are generic, details go to logs; no key, value, row or name reaches an adapter |
| D13 | Transactions | Snapshot isolation on the working set as of `Begin`; writes buffered per object and flushed before a read (read-your-writes); `Commit` swaps its namespace in when the working set has not moved, else refuses an item both wrote (D18) and three-way merges base, ours and the current namespace through the core's merge and swaps that in, any conflict `ErrSerialization` with nothing written; a lost swap is retried up to five times. `Staged` follows `Working`: there is no staging area at the engine | The Engine Spec's L4 transaction rules; a merge combines what two writers wrote to different items, the models' policies apply per cell, key and field in branch merges |
| D14 | Authorization | `Grants`, default deny, over the core's six actions: read, write, commit, merge and branch-admin per database, branch or object, admin on the database; a protected branch refuses Write on itself while Merge and Commit take merges in. The core asks about reads per branch, so the engine asks Read per object itself when it opens, diffs or lists one; a reader or writer of one object is let through the branch's question and asked per object | The Engine Spec's L4 grants per branch and per table, and protected branches, enforced where the core updates the ref |
| D15 | Refs and sessions | A ref that spells a full commit hash is that commit first, then a branch's head, then a tag's commit; a denied lookup ends the resolution. A session holds its branch through the core's checkout, so it cannot be deleted from under it (`ErrInUse`). Deleting a missing kv key or record is a no-op (Redis, Mongo); a missing row is `ErrNotFound` (SQL) | Readers of one branch can name commits; each model keeps its flavor's semantics |
| D16 | What a program imports | `engine/` alone: the core's and models' types a program needs are re-exported (`Principal`, `Authorizer`, `Blobs`, `Keyring`, `Hash`, `Schema`, `Row`, `Key`, `Type*`, `Value`, `Value*`, `Node`), with `NewKeyring`, `MemoryBlobs`, `DiskBlobs`, `AllowAll` and `ParseDocument`; depguard holds `e2e/engine_*test.go` and `tools/bench` to it | DESIGN D6 made checkable |
| D17 | Maintenance through the engine | `Database.Collect(ctx, p, grace)` runs the core's GC (`repo.GC` on the raw store under the database's authorizer: Admin on the database) and returns the engine's own `CollectReport`, deleted objects counted as packs or index objects so no core name leaks; a grace of 0 is `DefaultGrace`, the Storage Core Spec's seven days, and a negative grace is `ErrInvalid`. It runs beside open sessions and transactions; a transaction whose unpublished write counted on data a collection deleted gets `ErrSessionLost` at commit (from `vcs.ErrSessionLost`, shown to the caller because it can act on it: the database refuses writes until opened again). The grace window must outlast any open transaction or session. Nothing schedules it: a program collects periodically, and its index compaction keeps a database under the core's 100,000 index-object limit | Performance review (#1), aging |
| D18 | Two transactions writing one item | A commit that finds the working set moved compares, item by item, what it wrote with what landed since its snapshot: a table row, a kv key, a document record. An item both wrote is `ErrSerialization`, whatever the values, before any merge; a changed cell is its row, and a table whose schema changed on either side is every row of it. Counters are items too, for now: the core takes identical object roots as one change before the kv model can sum a counter's deltas, and counters are exempted again once the core asks models about identical changes. Branch merges (`Session.Merge`) keep combining cells and fields | The user's decision, 2026-09-25. The Engine Spec asks for snapshot isolation and for a clean merge to commit; a merge takes the same change on both sides as one change, so two read-modify-writes of one item both committed and one write was lost (tools/bench: 7 of 151 increments at 64 sessions on disk). Snapshot isolation forbids that; the item is the grain PostgreSQL (a row), MongoDB (a document) and Redis `WATCH` (a key) use, and the one adapters will expect |

## 2. Layers (enforced by depguard, `.golangci.yml`)

| Layer | Packages | May import (ours) | May import (core) |
| --- | --- | --- | --- |
| merge library | `merge` | nothing | nothing |
| models | `model/table`, `model/kv`, `model/document` | `merge` | `core/model`, `core/chunk`, `core/prolly`, `core/stream`, `core/hash`, `model/contract` (tests) |
| engine API | `engine` | the models, `merge` | `core/repo`, `core/vcs`, `core/object`, `core/auth`, `core/model`, `core/seal`, backends under `core/blob/` (to open a repository for a caller that hands over a backend) |
| adapters | not here | `engine` | nothing |

No upward imports: a model never imports `engine`; `merge` never imports a
model. A model never imports a backend, `core/vcs`, `core/repo` or `core/gc`:
it reads and writes chunks through the port it is handed and knows nothing of
history. The rule covers a model's tests too, so a test that drives a model
through a repository lives in the root-level `e2e/` package.

## 3. Formats (a compatibility contract)

Every on-disk structure a model writes carries a version, has a hand-written
bounds-checked decoder and a fuzz target, and is sealed by the core under a
domain tag of its own. The structures land with their models and are listed
here as they do.

**kv, format 1** (`model/kv`, id 6). An object is a prolly map under
`Model.Config`; its `model.Root` claims `Format 1`, `Size` = the entry count,
`Depth 0`. A key is 1 to 4096 bytes (`prolly.MaxKeySize`), any bytes. A value
is a frame `kind u8 · payload`, the payload at most 256 KiB (`MaxValueSize`),
counts bounded, every kind canonical (members in order, no repeats, no
trailing bytes); kind 0 and unknown kinds are refused on encode and decode:

| Kind | Payload | Merge of a key both sides changed |
| --- | --- | --- |
| 1 Bytes | raw bytes | equal is clean; different is a conflict at the key |
| 2 Counter | i64 as u64 little-endian | both sides' deltas from base add (`merge.Counter`) |
| 3 Set | count uvarint, then per member `tag u64 · elem`, sorted, no repeats | observed-remove over write tags (`merge.Set`, D8), always clean |
| 4 Hash | count, then `name · value`, both length-prefixed, sorted by name | per field; one field changed differently is a conflict at that field |
| 5 SortedSet | count, then `score f64 · tag u64 · member`, ordered by score then member; NaN refused, −0 written as 0 | per member by tag; a score changed differently is a conflict at that member |
| 6 Sequence | count, then length-prefixed elements | `merge.Sequence` (D9); concurrent inserts at one position are both kept, clean (D11) |

A kind changed on one side and the value on the other, or two different
kinds, is a conflict at the key; a frame that does not decode mid-merge
aborts the merge with `ErrValue`. *Location*: a change (Diff) is at the bare
key; a conflict at `uvarint(len(key)) · key · sub`, `sub` the hash field or
sorted-set member at fault, empty for the key as a whole (`kv.Location`,
`kv.ParseLocation`).

**table, format 1** (`model/table`, id 3). Little-endian unless said. The
object's root chunk is the *root record*: `"VDTR"` · version u16 (1) ·
catalog (uvarint length ≤ 1 MiB, bytes) · primary map root [32] · row count
u64 · index count uvarint (≤ 64) · per index: tag u16 · map root [32] ·
entry count u64. The decoder refuses trailing bytes, a wrong magic or
version, a catalog that does not validate, indexes that are not the
catalog's in order, or an index whose count differs from the row count.
`model.Root{Hash, Size: rows, Depth: 0, Format: 1}`.

The *catalog*: `"VDTC"` · version u16 · column count uvarint (≤ 1024) · per
column: tag u16 · name (uvarint ≤ 128, bytes) · type u8 · nullable u8 · max
length u32 · primary key count uvarint · tags u16 · index count uvarint
(≤ 64) · per index: tag u16 · column count uvarint · tags u16. Varints are
minimal.

The *primary map*: key = the key columns' cells concatenated (prefix-free),
or the 16-byte row id of a keyless table; value = the non-key columns' cells
in schema order. An *index map*: key = the index columns' cells then the
encoded primary key; value empty.

A *cell*: `0x00` is NULL; else `0x01` then the value: bool one byte;
int2/4/8, date (i32 days), timestamp and timestamptz (i64 µs) big-endian
with the sign bit flipped; float4/8 IEEE bits big-endian, sign flipped when
positive and every bit when negative, −0 written as +0 and every NaN as the
one NaN above everything, the decoder refusing any other spelling;
text, varchar, bytea and jsonb as bytes with `0x00` escaped to `0x00 0xFF`
and a `0x00 0x00` terminator (varchar's length in characters; text and
jsonb UTF-8, jsonb non-empty); numeric as a sign class (`01` negative, `02`
zero, `03` positive), then the adjusted exponent i32 big-endian sign-flipped
and the digits as `0x01`..`0x0A` terminated by `0x00`, exponent and digits
complemented with terminator `0xFF` for negatives, canonical text only;
uuid 16 raw bytes. A cell is at most 1 MiB, a numeric at most 1000 digits.

A *location* (diff and merge): the encoded key for a row; the encoded key
then the column tag u16 big-endian for a cell; `schema` for the schema as
a whole, `schema/<tag>` for one column and `schema/index/<tag>` for one
index. Every model invents its own location encoding; this is table's.

*Merge.* The primary key may not change on either side against the base
(rows are matched by keys encoded under the base's key): a conflict at
`schema` before anything else. Equal catalogs take ours; a side whose
catalog is the base's takes the other side's schema whole, so the merge
identities hold and column order is the writer's. Otherwise columns merge
by tag: the base's columns in the base's order, each attribute (name,
type, nullability, length) a three-way scalar merge, a disagreement a
conflict at `schema/<tag>`; a column dropped on one side goes unless the
other side changed it or wrote to it (any row's cell for it changed, added
rows included), a conflict at `schema/<tag>`; additions are appended in
tag order, the same addition on both sides lands once, two different
additions of one tag conflict. Indexes merge by tag the same way at
`schema/index/<tag>`; an index whose columns are no longer all present is
dropped. The merged schema must validate. Then ours is rewritten under it
(`WithSchema`: renames are catalog-only, added columns must be nullable,
dropped columns take their cells, indexes are rebuilt, types may widen
int2→int4→int8, float4→float8, varchar→longer or text), theirs' rows are
carried across (a value that does not fit is a conflict at the row), and
rows merge per row then per cell, a cell a `merge.Scalar` over its
encoding, a row added on one side read under a nullable copy of the
column. With any conflict the result is ours, untouched.

**document, format 1** (`model/document`, id 4). An object is a prolly map
through `mapobject.Spec{Name: "document", Format: 1}` from a record id (1 to
4096 bytes) to a *record frame*: `format u8` (1) then the record's canonical
JSON text. The decoder refuses an empty frame, another format, text that is
not a document, text that is not the canonical spelling (equal values are
equal bytes), and bytes after the text. `model.Root{Size: records, Depth: 0,
Format: 1}`.

*Canonical text*: strict JSON, no whitespace, object fields sorted by name
and unique, numbers in one decimal spelling (no exponent, no leading or
trailing zeros, zero as `0`, `-0` as `0`), strings escaped as `\" \\ \b
\f \n \r \t`, other control characters as `\u00XX`, everything else raw
UTF-8. The model owns it: `merge.Node.Canonical()` is Go-quoted, not JSON.

*The parser*: RFC 8259 only (no leading zeros, `.5`, `1.`, `+1`, hex, NaN,
comments, single quotes or trailing commas); at most 1 MiB of text, 64
nesting levels, 1000 digits either side of a number's point, a 6-digit
exponent; surrogate pairs as one rune, lone surrogates refused, a field
twice in one object refused.

*Merge*: per record through the helper's `MergeWith`; a record both sides
changed in place merges by field path through `merge.Tree`, each field
conflict its own `model.Conflict`; deleted against changed, or added
twice, is one conflict at the record as a whole; a stored record that does
not decode aborts the merge with the error. *Location*: a change (Diff) is
at the bare record id; a conflict at `Locate(id, path)`: the id and each
path segment, each prefixed by its uvarint length, so an id may hold any
bytes and a field name any text; the path is empty for the record as a
whole. `ParseLocation` reads it back and refuses an empty or oversized
id, more than 64 segments, a truncated part or a varint that is not
minimal.

## 4. Testing tiers

As the core's: unit, contract (`model/contract` for every model; a contract
suite of our own for every port `engine/` defines), property (`rapid`) for the
merge policies and the encodings, fuzz for every decoder, golden hashes for
determinism, integration in `e2e/` through `engine/` alone, the checked-in
mutants weekly and on demand.
