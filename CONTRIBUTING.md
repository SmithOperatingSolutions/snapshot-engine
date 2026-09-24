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
