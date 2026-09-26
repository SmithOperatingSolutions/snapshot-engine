# Design

How `docs/specs/engine-layers.md` and the Engine Spec's L4 become code.
Formats and protocols are recorded here as they land; until then this file is
the decisions and the layer table the build enforces.

## 1. Decisions

| # | Question | Decision | Why |
| --- | --- | --- | --- |
| D1 | The Engine Spec's L4 is "tables, schema and sessions"; here the models are one layer and the API another | L4 = the models (`model/*`) and the merge library; L5 = the engine API (`engine/`). The spec's L4 text is read that way: its storage layout, encoding and transaction rules belong to `model/table` and `engine/`, its `Database`/`Session`/`Txn` sketch to `engine/` | A second and third model (kv, document) arrive without a second API; adapters depend on one package |
| D2 | Dependency on the storage core | A pinned tag of `github.com/SmithOperatingSolutions/snapshot-core` (v0.3.0), never edited, forked, vendored or `replace`d; its `tools/ci`, `tools/redcheck` and `tools/mutate` run here as Go tools (`go.mod` `tool` directives). Work that needs an untagged core is built under a local, uncommitted `go.work` until the core tags it, and the engine moves to the tag in a commit that comes before any commit using it, so every commit builds against a tag | One test discipline across both repositories; behavior the core lacks is asked for there, in its own PR |
| D3 | Model ids | Table is 3 and the JSON document is 4, as the Storage Core Spec's registry table says (its package name is `model/json`; here the model is `model/document`); 5 is the spec's time series. kv is proposed as 6; the core's table is the registry of record and is amended there before kv ships | One registry across every consumer of the core; two plugins can never collide |
| D4 | Pure Go | `CGO_ENABLED=0` for this module and, by rule, for every adapter. A dependency that needs cgo is refused; the pgwire adapter chooses a pure-Go SQL parser | Same build story as the core: one static binary, no toolchain beyond Go, the race detector the only exception |
| D5 | Where merge semantics live | In `merge/`, a package with no dependency on the core, called by every model per cell, record or value; policies are pure functions with property tests | Three models share one set of semantics; a merge bug is fixed once; the library is testable without a repository |
| D6 | What an adapter may import | `engine/` alone. `engine` re-exports what an adapter needs of the core (`engine.Principal` is `auth.Principal`) so an adapter never imports the core or a model | The API is the boundary the spec's rule 5 asks for; an adapter cannot reach past it by accident |
| D7 | The run-all | The storage core's `tools/ci`, a Go tool since core v0.1.1, told the engine's layout (`-product merge/,model/,engine/ -adapters=`): fmt, vet, mod verify, lint, vuln, race, the 90% coverage gate, redcheck, fuzz and the mutant catalog, the same steps and gates as the core's | One test discipline across both repositories, from one runner |

| D8 | Set semantics in the merge library | Observed-remove over write tags: a member is `Tagged{Elem, Tag}`, and a removal drops the tags it saw, so a member removed on one side and re-added on the other (a new tag) is present. A model that stores no per-add tag uses one tag per member and loses re-add semantics; kv stores a tag per member | The only set merge that is both deterministic and matches what two writers meant |
| D9 | Sequence conflicts | `Sequence` is a diff3 over element keys aligned by a longest common subsequence (found as D19 says); both-inserted blocks at one position are kept in key order and flagged; two changes to one stretch conflict and the base stretch stays in the value, so the result is a merge only when `Clean()` | Deterministic, side-neutral, and never invents an order the writers did not |
| D10 | Trees | A JSON-like `Node` (null, bool, number as canonical text, string, array, object with sorted fields) merged by path; arrays merge as sequences keyed by the element's canonical form; `TreeOptions.Counter` names the paths whose integers add, and a non-integer there is a conflict. Values nested past `merge.MaxDepth` (64 arrays and objects, the document model's limit) are a conflict at the root, found before any recursion into them; each node is compared once | One tree merge serves a document, a JSON cell and a Redis hash |

| D11 | A sequence both sides inserted into at one position | Clean: both blocks kept in key order, `merge.Result.Flagged` set. The plugin port offers only a conflict, which would stop a merge a writer need not resolve; the engine API (E4) is where a flag reaches a caller | Nothing is lost and the order is deterministic; a conflict would block a merge on something that needs no decision |

| D12 | What a caller is shown | Every exported call returns through `scrub`: the engine error it matches (`ErrInternal` when none) as an `*Error` with a fresh correlation id, the details logged to the `Logger` under that id; a cancelled context and a scan callback's own error go back as they came | The Engine Spec's security row: errors to callers are generic, details go to logs; no key, value, row or name reaches an adapter |
| D13 | Transactions | Snapshot isolation on the working set as of `Begin`; writes buffered per object and read back from the buffer laid over the snapshot, never flushed for a read (read-your-writes; #9: a handle's Get, Scan and Lookup go through its editor, which merges its pending edits into the stored walk, in key or index order), flushed once when the commit makes the namespace; `Commit` takes its turn in the branch's commit queue (D20) and lands its namespace as is when the working set it is applied to is its snapshot, else refuses an item both wrote (D18) and three-way merges base, ours and that working set through the core's merge, any conflict `ErrSerialization` with nothing written; the working set it is applied to is the one its batch has made so far, and a swap lost to another process rebuilds the batch, up to five times. `Staged` follows `Working`: there is no staging area at the engine | The Engine Spec's L4 transaction rules; a merge combines what two writers wrote to different items, the models' policies apply per cell, key and field in branch merges |
| D14 | Authorization | `Grants`, default deny, over the core's six actions: read, write, commit, merge and branch-admin per database, branch or object, admin on the database; a protected branch refuses Write on itself while Merge and Commit take merges in. The core asks about reads per branch, so the engine asks Read per object itself when it opens, diffs or lists one; a reader or writer of one object is let through the branch's question and asked per object | The Engine Spec's L4 grants per branch and per table, and protected branches, enforced where the core updates the ref |
| D15 | Refs and sessions | A ref that spells a full commit hash is that commit first, then a branch's head, then a tag's commit; a denied lookup ends the resolution. A session holds its branch through the core's checkout, so it cannot be deleted from under it (`ErrInUse`). Deleting a missing kv key or record is a no-op (Redis, Mongo); a missing row is `ErrNotFound` (SQL) | Readers of one branch can name commits; each model keeps its flavor's semantics |
| D16 | What a program imports | `engine/` alone: the core's and models' types a program needs are re-exported (`Principal`, `Authorizer`, `Blobs`, `BlobVersion`, `Keyring`, `Hash`, `Schema`, `Row`, `Key`, `Type*`, `Value`, `Value*`, `Node`), with `NewKeyring`, `MemoryBlobs`, `DiskBlobs`, `AllowAll` and `ParseDocument`; depguard holds `e2e/engine_*test.go` and `tools/bench` to it | DESIGN D6 made checkable |
| D17 | Maintenance through the engine | `Database.Collect(ctx, p, grace)` runs the core's GC (`repo.GC` on the raw store under the database's authorizer: Admin on the database) and returns the engine's own `CollectReport`, deleted objects counted as packs or index objects so no core name leaks; a grace of 0 is `DefaultGrace`, the Storage Core Spec's seven days, and a negative grace is `ErrInvalid`. It runs beside open sessions and transactions; a transaction whose unpublished write counted on data a collection deleted gets `ErrSessionLost` at commit (from `vcs.ErrSessionLost`, shown to the caller because it can act on it: the database refuses writes until opened again). The grace window must outlast any open transaction or session. Nothing schedules it: a program collects periodically, and its index compaction keeps a database under the core's 100,000 index-object limit | Performance review (#1), aging |
| D18 | Two transactions writing one item | A commit that finds the working set moved compares, item by item, what it wrote with what landed since its snapshot: a table row, a kv key, a document record. An item both wrote is `ErrSerialization`, whatever the values, before any merge; a changed cell is its row, and a table whose schema changed on either side is every row of it. **A kv counter's delta is not an item** (#7, from snapshot-core v0.2.0): a key the committing transaction wrote from a counter to a counter, as an INCR or DECR is run (read the counter, write it back by more), is left out of the comparison (`Txn.counterDeltas`, called from `Txn.writeWrite`) and goes to the merge, where kv, a model that accumulates (`model.Accumulator`), adds both sides' deltas, two identical INCRs included. What the other side did to that key is then the merge's to decide: a counter written again sums; another kind, or a delete, is a conflict at the key, so the commit still serializes. A key added as a counter, or turned into one from another kind, stays an item. Every write of a counter over a counter is a delta, as it is in branch merges: an absolute SET of a counter's value racing an INCR sums with it rather than replacing it. Branch merges (`Session.Merge`) keep combining cells and fields | The user's decision, 2026-09-25, was (a), counters included until the core lets counters sum; snapshot-core v0.2.0 does (the namespace merge asks a model that accumulates about identical changes), so they left the rule in #7. The Engine Spec asks for snapshot isolation and for a clean merge to commit; a merge takes the same change on both sides as one change, so two read-modify-writes of one item both committed and one write was lost (tools/bench: 7 of 151 increments at 64 sessions on disk). Snapshot isolation forbids that; the item is the grain PostgreSQL (a row), MongoDB (a document) and Redis `WATCH` (a key) use, and the one adapters will expect |
| D19 | What a sequence merge may cost | Each side is aligned with the base by Myers' difference algorithm in linear space (middle snake, divide and conquer) over interned keys, after the common prefix and suffix: memory in proportion to the lists, work at most about (n+m)·`MaxSequenceEdits`. `merge.MaxSequenceEdits` is 1,000 insertions and deletions (a replaced element is one of each) between the base and either side; a side past it, when the other side changed the list too, makes the merge a conflict at the list (an empty path; kv at the key, a document at the array's path) with the base kept. A side that left the list as it was yields to the other before any alignment, however much that one changed, so merge(b, o, b) = o still holds | The LCS table was (n+1)×(m+1): 1.15 GB for three versions of an 8 KB kv list, 69 GB at kv's `MaxMembers`, 4.4 TB for a 1 MiB document array, reachable by two sessions editing one list (#4). A conflict is the port's one honest answer when the sides cannot be aligned cheaply: nothing is lost, the writer picks a side, and two versions a thousand edits apart were rarely going to merge cleanly by position anyway. 1,000 keeps a 1 MiB array of 524,288 elements with 1,000 edits spread across it under half a second (0.42 s measured) |
| D20 | Commits to one branch in one process (group commit) | Every commit to a branch of one `Database` takes its turn in that branch's queue, oldest first: `Txn.Commit`, `Session.Commit`, `Session.Merge`. The call at the head leads. **Transactions**: the leader takes the transactions in a row at the head, at most 64 (`maxBatch`), reads the working set once, applies each in order to what the ones before it made, each checked against its own snapshot as a commit alone would be (the item rule, D18, then the models' merge, D13, with the batch's working set as the merge's first side, so a model rewrites it with the member's few items rather than rewriting the member with everything that landed since its snapshot: the item rule has left the sides disjoint, so the order changes nothing in the result), and swaps the result in with one `UpdateWorkingSet`. A member that collides, is denied or fails, fails alone, with nothing of it applied; every member learns its own outcome after the swap, so success is reported once the publish is durable, and each error is scrubbed at its own caller's boundary with its own correlation id. **Authorization**: each member's principal is asked what the core would ask it (Read on the branch, then Write on the branch and on every object it changes, against the working set it is applied to); the batch's own calls to the core, made as one member, are allowed exactly the questions its members were allowed (`batchAuthorizer`, keyed by a context value only the engine makes), anything else asked of the database's authorizer. **Session calls**: a session's commit or merge is a batch of one, run by its own caller on its own context in its turn: it records exactly the transactions queued before it and none queued after. **Bounds**: 64 transactions a publish; no linger (a leader never waits for more: a batch is what queued while the publish before it ran); a caller waits for the publish in flight and at most ceil(ahead/64) more; a caller's context ending while it waits takes it out of the queue with nothing written, and once taken into a publish it learns that publish's outcome; a batch's shared work runs detached from any one member's cancellation, until the latest of its members' deadlines (none when one member has none). **Lost swap**: another process (or anything else writing the working set outside the queue) moved it; the whole batch is rebuilt from the new root, every member checked again, at most 5 times (`maxCommitAttempts`), after which the members that would have committed get `ErrSerialization`. GC swaps the root but never a working set, so the core's own swap loop absorbs it. **Per process**: the queue orders one `Database`'s calls; two processes on one store still meet only at the swap, optimistically, as before. A queue exists while it holds a call | Issue #1: every commit was its own swap of the repository's one root, so a branch committed at one disk's swap rate (26.5 tx/s at one session) and lost ground as sessions were added (1.7 tx/s at 64), almost every failure a lost swap. One swap for a batch pays the fsyncs once for all of it; merging in memory, in order, keeps each transaction's checks what they were alone. 64 is the bench's largest session count and keeps a publish's merges (1 to 4.5 ms each) near the swap's own cost |
| D21 | How long a stored value may be | Each model opens and writes its prolly maps under a `prolly.Config.MaxValue` of the longest value it can legitimately store, derived from its own constants, so a stream value longer than that is refused with `prolly.ErrValueTooLarge` before it is read: kv `1 + MaxValueSize` (a frame's kind byte and longest payload); document `MaxRecord` (the frame byte and `MaxDocument` bytes of text); a table's primary map the sum of its non-key columns' longest cells (`maxCell`: a fixed-width type's width with its `0x01`, `MaxCellLen` for numeric, text, varchar, bytea and jsonb), at least one byte, each table under its own schema's; a table's index maps one byte, their values being empty (0 is the core's default). It is not geometry: nothing stored depends on it | A stream's claimed length can be many times what it stores, and the core's default limit is 64 MiB (snapshot-core#23, #7). A wide table's limit can pass the default (1,024 text columns: a GiB), which the core's editor had refused at write; varchar takes `MaxCellLen` rather than its tighter 3 + 4n |
| D22 | What a batch member costs its leader (superseded by D23 for the leader; the models' `Rebase` stays as the batch's authority in their tests) | A transaction committing onto a working set that moved is applied by **rebase** before the merge: its own changes against its snapshot, object by object; an object only it changed is its own, one the working set changed too goes through its model's `Rebase` (table, kv, document), which reads the transaction's items and one item of the working set per change, never what landed since the snapshot. A row or record both wrote is `ErrSerialization` there, as the item rule (D18) has it. What the rebase does not take is declined, with no error, to the merge of D20, which decides it as before: an object added, dropped or changed in kind on either side, a table whose schema changed, a kv key both wrote (a counter both incremented sums there, D18). D18 and D20 are unchanged in what commits: the rebase makes the merge's namespace root for root wherever it takes a change | Issue #8: with group commit each member's item check and merge diffed its snapshot against the batch's working set, so the leader's work grew with batch size times staleness, and 64 sessions barely beat 16 (138 against 115 tx/s on disk). A one-row commit onto 3,000 moved rows read 51 objects through the merge and 12 through the rebase, 12 with 300 |
| D23 | How a batch's members are applied (#22) | In **runs**: the leader takes members one after another into a run; each member's changes, by its own diff against its snapshot, are checked object by object against the working set the run began on and the members taken before it (the models' `Batch`: `Check` reads the member's items and one item of the target per change, refusing an item the target holds otherwise than the snapshot did or one a member taken changed; `Take` records them), its writes are authorized by the paths its diff names, and an object nobody else changed is taken as the member left it, without a check. When the run closes, every object it touched is flushed **once** (`Batch.Flush`) and the namespace once. A member the run cannot check closes it and is merged onto what the run made, on its own (`onto`: its own namespace when the working set is its snapshot, else the merge of D13 behind the item rule of D18) (an object added, dropped or changed in kind, a schema change, a kv key both wrote, a model without a batch); the next run begins there. D20's contract holds: each member checked against its own snapshot, order decides collisions between members (a row or record both wrote is the later member's `ErrSerialization`, alone, nothing of it applied), a member denied fails alone. A store fault in a run's flush is every taken member's error. An object nobody else changed costs the leader nothing but a namespace edit: one session in memory runs at 980 tx/s with that, 540 with every object checked and flushed. | With D22 a batch of 64 was 64 rebases and 64 flushes of the same maps in a row, each reading the nodes the flush before it wrote: 54% of the leader's CPU under the chunker's node writes at 64 sessions in memory (zstd 27%, SHA-256 17%). 32 members renaming 32 rows of one table published 24,394 bytes; one member alone 1,308; in a run 1,305 (`TestABatchWritesEachObjectOnce`) |

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
