# Design

How `docs/specs/engine-layers.md` and the Engine Spec's L4 become code.
Formats and protocols are recorded here as they land; until then this file is
the decisions and the layer table the build enforces.

## 1. Decisions

| # | Question | Decision | Why |
| --- | --- | --- | --- |
| D1 | The Engine Spec's L4 is "tables, schema and sessions"; here the models are one layer and the API another | L4 = the models (`model/*`) and the merge library; L5 = the engine API (`engine/`). The spec's L4 text is read that way: its storage layout, encoding and transaction rules belong to `model/table` and `engine/`, its `Database`/`Session`/`Txn` sketch to `engine/` | A second and third model (kv, document) arrive without a second API; adapters depend on one package |
| D2 | Dependency on the storage core | A pinned tag of `github.com/SmithOperatingSolutions/snapshot-core`, never edited, forked, vendored or `replace`d; its `tools/redcheck` and `tools/mutate` run here as Go tools (`go.mod` `tool` directives) | One test discipline across both repositories; behavior the core lacks is asked for there, in its own PR |
| D3 | Model ids | Table is 3 and the JSON document is 4, as the Storage Core Spec's registry table says (its package name is `model/json`; here the model is `model/document`); 5 is the spec's time series. kv is proposed as 6; the core's table is the registry of record and is amended there before kv ships | One registry across every consumer of the core; two plugins can never collide |
| D4 | Pure Go | `CGO_ENABLED=0` for this module and, by rule, for every adapter. A dependency that needs cgo is refused; the pgwire adapter chooses a pure-Go SQL parser | Same build story as the core: one static binary, no toolchain beyond Go, the race detector the only exception |
| D5 | Where merge semantics live | In `merge/`, a package with no dependency on the core, called by every model per cell, record or value; policies are pure functions with property tests | Three models share one set of semantics; a merge bug is fixed once; the library is testable without a repository |
| D6 | What an adapter may import | `engine/` alone. `engine` re-exports what an adapter needs of the core (`engine.Principal` is `auth.Principal`) so an adapter never imports the core or a model | The API is the boundary the spec's rule 5 asks for; an adapter cannot reach past it by accident |
| D7 | Tooling until the core's `tools/ci` runner is layout-agnostic | `redcheck` and `mutate` as Go tools now; fmt, vet, lint, vuln, race and coverage as explicit steps in `mise.toml` and CI. When the core's `tools/ci` takes its product packages and coverage gate from flags, this module runs it as a tool too | The runner is wired to the core's `core/` and `model/` layout today; the process rules it enforces apply here from the first commit regardless |

| D8 | Set semantics in the merge library | Observed-remove over write tags: a member is `Tagged{Elem, Tag}`, and a removal drops the tags it saw, so a member removed on one side and re-added on the other (a new tag) is present. A model that stores no per-add tag uses one tag per member and loses re-add semantics; kv stores a tag per member | The only set merge that is both deterministic and matches what two writers meant |
| D9 | Sequence conflicts | `Sequence` is a diff3 over element keys aligned by LCS; both-inserted blocks at one position are kept in key order and flagged; two changes to one stretch conflict and the base stretch stays in the value, so the result is a merge only when `Clean()` | Deterministic, side-neutral, and never invents an order the writers did not |
| D10 | Trees | A JSON-like `Node` (null, bool, number as canonical text, string, array, object with sorted fields) merged by path; arrays merge as sequences keyed by the element's canonical form; `TreeOptions.Counter` names the paths whose integers add, and a non-integer there is a conflict | One tree merge serves a document, a JSON cell and a Redis hash |

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
is a frame `kind u8 · payload`: kind 1 is Bytes, its payload at most 256 KiB
(`MaxValueSize`); kind 0 and unknown kinds are refused on encode and decode;
the decoder copies the payload. Further kinds (counter, set, hash, sorted set,
sequence) take further kind bytes (E3).

Still to land: the table's root record, catalog and tuple encoding (E2), the
document record (E5).

## 4. Testing tiers

As the core's: unit, contract (`model/contract` for every model; a contract
suite of our own for every port `engine/` defines), property (`rapid`) for the
merge policies and the encodings, fuzz for every decoder, golden hashes for
determinism, integration in `e2e/` through `engine/` alone, the checked-in
mutants weekly and on demand.
