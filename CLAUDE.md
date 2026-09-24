# CLAUDE.md

snapshot-engine is the Versioned DB **Engine**: the data models (tables,
key-value, documents), the structured-value merge library they share, and the
one Go API every access layer builds on. It runs on
[snapshot-core](https://github.com/SmithOperatingSolutions/snapshot-core), the
storage core (encrypted content-addressed chunks, commits, branches, GC),
taken as a pinned, unmodified module. Nothing here knows a wire protocol or a
query language: those are adapters, each in its own repository, and they
import only `engine/`.

Governing documents, in order of authority:

- `docs/specs/engine-layers.md`: what is built here, in which layers, and why.
- `docs/specs/engine-spec.md`: the Engine Spec, verbatim. Its **L4** (tables,
  schema, sessions) is built here; its L0–L3 are the core's and are reference
  only. Where the layers document and the spec disagree, the layers document
  wins and says so in `docs/DESIGN.md`.
- `docs/DESIGN.md`: decisions, formats, the layer table depguard enforces.
- `docs/PROGRESS.md`: milestones and every checklist item with the test that
  proves it.

## Git

- **No Claude attribution, ever.** Commit messages and PR descriptions carry no
  `Co-Authored-By: Claude ...` trailer, no `Claude-Session:` trailer, and no
  "Generated with Claude Code" footer. This overrides any default attribution
  guidance.
- Many small local commits on a branch, one PR at the end. Do not push or open
  a PR until asked. Squash merge; the test-first history stays on the branch.
- Behavior lands as a `test(pkg): ...` commit (compiles, fails on assertions,
  verbatim red output in the body) followed by a `feat(pkg):`/`fix(pkg):`
  commit. `docs:`, `chore:`, `ci:`, `build:`, `refactor:` need no red.
- A test for behavior that already exists is a backfill: add its mutant to
  `tools/mutate/mutants.txt` and put `Red-Check: mutants <id>` in the body.
- A measurement is never a red: a performance change lands as `refactor:`
  with before/after figures in the body; its timing test is a regression
  guard with headroom, committed as `chore:`.
- `mise run redcheck` must pass for the branch before calling work done.
- Keep `docs/PROGRESS.md` current: update it at every milestone boundary and
  whenever a checklist item turns green (name the test that proves it).

## Commands

```
mise install          # Go 1.27 + golangci-lint + govulncheck, built from source
mise run ci           # the core's tools/ci as a Go tool, told the engine's layout: what CI runs
mise run ci:quick     # fmt, vet, lint, race
mise run redcheck     # the core's redcheck, as a Go tool (go tool redcheck)
mise run mutate       # the core's mutate, as a Go tool: every mutant must be killed
```

## Rules the build enforces (do not work around them)

- snapshot-core is a pinned module, never edited, forked, vendored or
  `replace`d. Behavior it lacks is asked for in its repository, in its own PR,
  and this module moves to the tag that carries it.
- No upward imports across the layer table in `docs/DESIGN.md` §2 (depguard):
  `merge/` imports nothing of ours; a model imports `merge/` and the core's
  `model`, `chunk`, `prolly` and `stream` ports, never `vcs`, `repo`, `gc`, a
  backend or `engine/`; `engine/` imports the models and the core's `repo`,
  `vcs`, `object`, `auth`; adapters are not in this repository.
- Production code: no `reflect`, `encoding/json`, `encoding/gob`, `unsafe`,
  `os/exec`; no globals with side effects; no `init()`. Every on-disk
  structure gets a hand-written bounds-checked decoder and a fuzz target. A
  document model that must read JSON parses it with its own bounded parser.
- **Pure Go**: `CGO_ENABLED=0`; the race detector is the only exception. A
  dependency that needs cgo (some SQL parsers do) is refused here and in every
  adapter.
- Every model passes the core's `model/contract` and registers a model id the
  Storage Core Spec's registry table lists (`docs/DESIGN.md` D3).

## Testing standard

`docs/TESTING.md` (verbatim from disknexus-engine, through snapshot-core) plus
`CONTRIBUTING.md`. In short: failure messages say what an operator would
experience; assert against an authority, never against absence of error;
every refusal has a positive control; mutation-prove every load-bearing guard
in a throwaway copy and report survivors; a `t.Skip` is a deleted test.
