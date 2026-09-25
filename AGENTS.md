# AGENTS.md

Instructions for anyone, human or coding agent, changing this repository.
`CLAUDE.md` imports this file. The process in full is
[`CONTRIBUTING.md`](CONTRIBUTING.md). This file is the short version, plus the
rules that most often go wrong.

snapshot-engine is the Versioned DB **Engine**. It holds three things:
- the data models: tables, key-value maps and document collections
- the structured-value merge library they share
- the one Go API every access layer builds on

It runs on
[snapshot-core](https://github.com/SmithOperatingSolutions/snapshot-core),
the storage core, taken as a pinned, unmodified module. The core provides
encrypted content-addressed chunks, commits, branches and GC. Nothing here
knows a wire protocol or a query language. Those are adapters, each in its own
repository, and they import only `engine/`.

## Governing documents, in order of authority

- `docs/specs/engine-layers.md`: what is built here, in which layers, and why.
- `docs/specs/engine-spec.md`: the Engine Spec, verbatim. Its **L4** (tables,
  schema, sessions) is built here. Its L0 to L3 are the core's and are
  reference only. Where the layers document and the spec disagree, the layers
  document wins and says so in `docs/DESIGN.md`.
- `docs/DESIGN.md`: decisions D1 onward, formats, and the layer table depguard
  enforces. A new decision takes the next free D number.
- `docs/PROGRESS.md`: milestones, every checklist item with the test that
  proves it, and what testing found.
- `docs/PERFORMANCE.md`: the load tool, the baseline, and every performance
  change with its before and after figures.

## Where work is tracked

GitHub issues on SmithOperatingSolutions/snapshot-engine. Every open question,
deferred item and decision to revisit is an issue, not a note in a document or
someone's memory. Behavior the core lacks is an issue on snapshot-core.
Issue `#1` is the performance review, and its comments record where it stands.

## Git

- **No tool attribution.** Commit messages and PR descriptions carry no
  `Co-Authored-By:` line for an AI tool, no session link, and no "Generated
  with" footer.
- Many small local commits on a branch, then one PR. It is squash-merged, and
  the test-first history stays on the branch.
- Each behavior lands as a `test(pkg): ...` commit, then the
  `feat(pkg):` or `fix(pkg):` commit that makes it pass. The test commit must
  compile, fail on assertions, and carry its verbatim red output in the body.
  `docs:`, `chore:`, `ci:`, `build:` and `refactor:` need no red.
- A test for behavior that already exists is a backfill. Add its mutant to
  `tools/mutate/mutants.txt`, and put `Red-Check: mutants a, b` in the body.
- A measurement is never a red. A performance change lands as `refactor:`
  with before and after figures in the body. Its timing test is a regression
  guard with headroom, committed as `chore:`.
- A resource bug (memory, time, a loop) is proven by a red that fails a
  **small budget**, such as an allocation delta, a work counter, or growth
  between two sizes. The red must never actually blow up.
- `mise run redcheck` and `mise run mutate` must pass before work is done.
- Keep `docs/PROGRESS.md` current. Update it at every milestone, and whenever
  a checklist item turns green, naming the test that proves it.

## Commands

```
mise install          # Go 1.27, golangci-lint, govulncheck, built from source
mise run ci           # the core's tools/ci as a Go tool, told the engine's layout: what CI runs
mise run ci:quick     # fmt, vet, lint, race
mise run redcheck     # the core's redcheck, as a Go tool (go tool redcheck)
mise run mutate       # the core's mutate, as a Go tool: every mutant must be killed
go run ./tools/bench -h   # the load tool (docs/PERFORMANCE.md)
```

## Rules the build enforces (do not work around them)

- snapshot-core is a pinned module. It is never edited, forked, vendored or
  `replace`d. Behavior it lacks is asked for in its repository, in its own
  PR, and this module moves to the tag that carries it. To build against an
  unreleased core branch, use an **uncommitted** `go.work` (it is
  gitignored). See CONTRIBUTING.
- No upward imports across the layer table in `docs/DESIGN.md` §2 (depguard):
  - `merge/` imports nothing of ours.
  - A model imports `merge/` and the core's `model`, `chunk`, `prolly`,
    `stream` and `wire` ports. It never imports `vcs`, `repo`, `gc`, a
    backend or `engine/`.
  - `engine/` imports the models and the core's `repo`, `vcs`, `object` and
    `auth`.
  - Adapters are not in this repository.
- Production code has no `reflect`, `encoding/json`, `encoding/gob`, `unsafe`
  or `os/exec`, no globals with side effects, and no `init()`. Every on-disk
  structure gets a hand-written, bounds-checked decoder and a fuzz target.
  Every length, count, exponent and depth read from input is checked against
  a limit before anything is sized from it.
- **Pure Go**: `CGO_ENABLED=0`. The race detector is the only exception.
- Every model passes the core's `model/contract` and registers a model id
  listed in the Storage Core Spec's registry table (`docs/DESIGN.md` D3).

## Keep heavy runs from taking the machine down

Uncapped fuzzing once took a 62 GB development host out of memory twice,
killing every terminal and agent session on it. So:
- Run tests, race, fuzz, bench, redcheck and mutate under a memory cap of their
  own, for example
  `systemd-run --user --scope -q -p MemoryMax=6G -p MemorySwapMax=0 <cmd>`.
- Use `GOFLAGS=-p=2` for multi-package race runs.
- Never run `go tool ci` locally without `-only`, unless you are on a machine
  sized for it.
- Fuzz with at most 4 workers and `GOMEMLIMIT=2GiB`.

## Testing standard

`docs/TESTING.md` (verbatim from disknexus-engine, through snapshot-core), plus
`CONTRIBUTING.md`. In short:
- Failure messages say what an operator would experience.
- Assert against an authority, never against the absence of an error.
- Every refusal has a positive control at its limit.
- Mutation-prove every load-bearing guard.
- A `t.Skip` is a deleted test.
