# Contributing

snapshot-engine gives other people's data its shape and its merge rules. A
defect here is a merge that silently drops a change, an encoding whose byte
order disagrees with its type's order, or a transaction that commits half of
itself. The rules below exist for that reason.

The testing standard is `docs/TESTING.md` (a verbatim copy of
disknexus-engine's, through snapshot-core). This file adds the process that
is specific to this repository; it is the storage core's process, so a change
that lands there lands the same way here.

## Run everything locally

```
mise install        # Go 1.27, golangci-lint, govulncheck, all built from source
mise run ci         # everything CI runs: the core's tools/ci, told the engine's layout
mise run ci:quick   # fmt, vet, lint, race suite
mise run redcheck   # every test: commit on the branch fails without its feat:/fix:
mise run mutate     # every checked-in mutant is killed
```

`ci`, `redcheck` and `mutate` are the storage core's tools, run here through
Go's `tool` directive (`go tool ci`, `go tool redcheck`, `go tool mutate`);
there is nothing to install beyond Go. `tools/ci` is told the engine's layout
(DESIGN D7) and runs the core's gates here unchanged.

## The loop for every change

1. **Red.** Write the smallest test that names the next behavior. Run it and
   confirm it fails *for the right reason*: an assertion, not a compile error
   and not a panic in setup. If the API does not exist yet, add a stub that
   compiles and returns the wrong answer.
2. **Green.** Write the minimum code that passes. No extra options, no
   "while I'm here".
3. **Refactor.** With everything green, remove duplication and fix names.
   Behavior does not change in this step.
4. **Mutation-prove** the guard the test protects (`docs/TESTING.md` §5): add
   the mutant to `tools/mutate/mutants.txt` and run `mise run mutate`. A guard
   that survives its own mutation is decoration.

## Commits

Many small commits on a branch, one PR, squash-merged; the test-first history
stays on the branch. Each behavior lands as a pair:

| Prefix | Contains | Must |
| --- | --- | --- |
| `test(pkg): ...` | the new tests, plus stubs so they compile | **fail**, on assertions; the verbatim red output goes in the commit body |
| `feat(pkg): ...` / `fix(pkg): ...` | the implementation | make that red green, and keep everything else green |

Other prefixes (`docs:`, `chore:`, `ci:`, `build:`, `refactor:`) carry no
behavior change and need no red.

`mise run redcheck` checks every `test:` commit between the branch and `main`:
it checks the commit out in a scratch worktree, runs only the tests that
commit added or changed, and requires each of them to FAIL on an assertion. A
build failure, a panic, a skip, or a pass blocks the PR. Every `feat:`/`fix:`
needs a `test:` commit since the previous one, of the same scope. A change to
a port's contract suite (`<pkg>/contract`) counts as a change to every Test
function that calls it.

**Renames are not reds.** redcheck judges every test a `test:` commit
changed, so a rename that touches an existing test file blocks the commit:
those tests pass without the change. A rename goes in a `refactor:` commit
of its own, before the red.

**Backfills.** A test for behavior that already exists cannot fail against
its parent. Its red is a mutant instead: add the mutant to
`tools/mutate/mutants.txt` in the same commit and name it in the body
(`Red-Check: mutants a, b`). redcheck then requires the commit's tests to
pass, and each named mutant to be killed by those tests alone.

**Measurements are never a red.** A performance change lands as `refactor:`
(or `chore:` for its test alone) with before/after figures in the body,
measured on the same machine in the same run; any behavior it carries gets
its own red. Its timing test is a regression guard with headroom.

**Property tests and their red.** A `test:` commit's red run fails its
`rapid` properties on purpose, and rapid saves each failure under
`testdata/rapid/`. Those files record the stub, not a bug: delete them
before committing. A real property failure found later is minimized and
checked in as a regression case. Check that a property's cases reach the
inputs it is about.

**Resource bugs have bounded reds.** A bug that costs memory, time or loop
iterations is proven the same way as any other, with a failing test first.
The red asserts a small budget that the unfixed code measurably exceeds on
a small input, and it never reproduces the blow-up itself. Budgets used here:
- a `runtime.MemStats` `TotalAlloc` delta over a few hundred KiB
- a counter of reads, visits or root swaps
- growth between two input sizes

The red run should allocate tens of MB at most.

## Where work is tracked, and how it lands

- **Issues.** Every piece of work, open question and decision to revisit is
  an issue on SmithOperatingSolutions/snapshot-engine. A PR closes its issues
  with one keyword per issue (`Closes #3`, then `Closes #4` on the next line).
  GitHub closes only the first issue in "Closes #3, #4".
- **Branches.** One branch per batch of issues, started from `origin/main`,
  with one PR. The PR is squash-merged with its description as the commit
  body. The branch is kept afterwards, because it holds the test-first
  history the squash hides.
- **Before calling a branch done:** run `mise run redcheck` over the branch,
  `mise run mutate` over the whole catalog, then `go tool ci -only` for fmt,
  vet, lint, race and cover. Update `docs/PROGRESS.md`, and `docs/DESIGN.md`
  for any decision. Design rows are numbered D1 onward, and a new one takes
  the next free number.

## Working against an unreleased core

This module requires snapshot-core by tag. When a change needs core behavior
that is not tagged yet:

1. Make the change in snapshot-core, in its own branch and PR, following its
   CONTRIBUTING.
2. Build the engine against that core checkout with an **uncommitted**
   workspace. `go.work` is gitignored, and is never committed:
   ```
   go work init . ../snapshot-core
   ```
3. `redcheck` checks each `test:` commit out in a scratch worktree, which has
   no `go.work`. A red that needs the untagged core cannot build there, so
   verify it by hand: in a throwaway worktree with the workspace, it must
   show failing tests and no build failures. Say so in the commit body.
4. Once the core is tagged, move this module to the tag (`go get
   github.com/SmithOperatingSolutions/snapshot-core@vX.Y.Z`, then `go mod
   tidy`) as the **first** commit of the branch. Every later commit then
   builds against the tag, and redcheck judges every red itself.

## Heavy runs

The race suite, fuzzing, the mutant catalog and the load tool can use many
gigabytes. Once, 17 uncapped fuzz workers took a 62 GB host out of memory and
killed every session on it. On a shared or development machine:

- Run each heavy command under a memory ceiling of its own, so a runaway
  kills only itself:
  ```
  systemd-run --user --scope -q -p MemoryMax=6G -p MemorySwapMax=0 go tool mutate -j 2
  ```
- Run the race suite over several packages with `GOFLAGS=-p=2`.
- Fuzz with at most 4 workers under `GOMEMLIMIT=2GiB`. The core's `tools/ci`
  sets both from v0.2.0 on, through `-fuzzparallel` and `-fuzzmemlimit`.

## Performance work

The load tool is `tools/bench`, and `docs/PERFORMANCE.md` explains its
workloads (W1 to W8), scales and flags. It drives a database through
`engine/` alone, as an adapter would, and checks every increment against an
authority, so a lost update fails the run.

- **Measure on a quiet machine.** Check that the load average is under 2 and
  that no other tests or benches are running. Record the load in the report.
  Before and after runs use the same machine, scale and flags.
- **Serialize timed runs.** When several people or agents share a machine,
  take one lock per timed run:
  ```
  flock /path/to/measure.lock go run ./tools/bench ...
  ```
  Take it for one run at a time, not a chain of runs, so others can
  interleave.
- **Where the figures go.** They go in the `refactor:` commit body and in
  `docs/PERFORMANCE.md`, and as a comment on the performance issue (#1). The
  ranked list of candidate changes is at the end of `docs/PERFORMANCE.md`.

## The mutant catalog

`tools/mutate/mutants.txt` is a list of stanzas separated by exactly one
blank line:
- `id` is unique.
- `file` is the source file to mutate.
- `find` is the exact text to replace, with `\n` and `\t` escapes, and must
  occur exactly once in its file.
- `replace` is the mutation.
- `pkg` and `run` name the tests that must kill it.

When code moves, re-anchor its mutants in a `chore(mutate):` commit. When two
branches both append stanzas, a rebase can drop the blank line between them,
and the tool then fails with "id given twice". Check the separators after
every merge. A mutant that cannot be killed is equivalent: remove it and say
why in the commit body.

## Regressions

Every bug gets a test named after its ticket, written before the fix:
`TestRegression_SE12_MergeDropsDeletedField`, in `<pkg>/regress_test.go`.
Regression tests are never deleted. A fuzz or property failure is minimized
and checked in as a permanent seed (`testdata/fuzz/...`) or regression case.

## Boundaries the build enforces

- **snapshot-core is a pinned module.** Never edit, fork, patch, vendor or
  `replace` it. Behavior it lacks is asked for in its repository.
- **No upward imports.** The layer table is `docs/DESIGN.md` §2, enforced by
  `depguard`: `merge` imports nothing of ours; a model imports `merge` and the
  core's ports; `engine` imports the models and the core; adapters, in their
  own repositories, import `engine` alone.
- **No `unsafe`, no `os/exec`, no `reflect`, no `encoding/json` or `gob` on
  stored bytes** in production code. Every on-disk structure has a
  hand-written, bounds-checked decoder and a fuzz target; a model that reads
  JSON does so with its own bounded parser.
- **No globals, no `init()` side effects.** Registries, authorizers and
  clocks are passed in.
- **Pure Go.** `CGO_ENABLED=0` everywhere except the race detector, here and
  in every adapter.

## Security checklist for a change

- Inputs validated with allowlists at the boundary; invalid input rejected,
  never repaired or coerced.
- On any error, nothing partial reaches the working set.
- Error messages never carry keys, values, rows or file contents.
- New on-disk structure: hand-written decoder, limits on every length, fuzz
  target, sealed by the core under its own domain tag.
