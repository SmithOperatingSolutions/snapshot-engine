# MEMORY.md

Working knowledge for this repository: what was learned the hard way and
what the owner expects. The code, `docs/`, GitHub issues and git history
don't record these. Read it before your first change. When you learn
something the next person would otherwise relearn, add it here, one fact per
bullet, with the date and why. Put open questions and pending work in GitHub
issues, not here.

## Who owns what

As of 2026-09-25 this repository belongs to the new engine team. The storage
core's maintainers built it, handed it over, and now work on snapshot-core
only:
- Behaviour the engine needs from the core is an issue on snapshot-core.
- The core's own open items are snapshot-core#29 to #33.

The practices below are how the repository was run until the handover. The
new team may keep or revise them, and should record any change here.

## How the owner works

- **Surface every scope call, don't make it.** Nothing is deferred or cut
  on your own: no "not in v1", no "a follow-up", no quiet shrinking of what
  was asked. When something looks like it should wait, or a choice has real
  trade-offs, give the options with a recommendation and let the owner
  decide. Anything done in a smaller form is named as such. (2026-09-23,
  after a merge that left six follow-ups the owner hadn't chosen.)
- **State lives in GitHub, not in notes.** Open work, questions and
  decisions to revisit are issues. Documents describe what is built, and
  memory (this file) describes how to work.
- **Nothing is pushed, merged or released without the owner's word.** Local
  commits are fine. Pushes, PRs, merges and tags wait for an explicit
  go-ahead, and each approval covers only what was approved.
- **Findings are fixed test-first as they are found.** When an audit or a
  fuzzer turns something up, each bug is filed as an issue, proven by a
  failing test, then fixed. For a resource bug, the failing test asserts a
  small budget and never blows up the machine. (2026-09-25, the owner's
  words: "Prove they are a bug first with failing bounded test".)
- **A measurement is never a red.** Performance changes carry before and
  after figures, taken on a quiet machine, with the load recorded.

## Pitfalls already paid for

- **Uncapped fuzzing took the host down.** On 2026-09-24 and 25, 17 fuzz
  workers (Go's default is one per core) took a 62 GB host out of memory
  twice. One of them was the table numeric decoder, #2, asking for 2 GB of
  text from 8 bytes. systemd then killed every terminal and agent session
  beside it. Run heavy commands under their own memory cap, as AGENTS.md
  says.
- **Every length, count, exponent and depth read from input is a promise to
  check.** The audit after that crash found the same class of bug in six
  more places: a claimed count sizing a map, a list merge whose alignment
  grew with the square of its length, and a stream repeating one chunk to
  stand for gigabytes (#3 to #6, snapshot-core#22 to #27). A fuzzer that
  decodes one chunk at a time cannot reach multi-chunk bugs. Structure-level
  fuzz targets can.
- **A rebase can drop the blank line between mutant stanzas.** When two
  branches both append to `tools/mutate/mutants.txt`, the rebase can merge
  their stanzas, and the tool then fails with "id given twice". Check the
  separators after every merge.
- **Renames block redcheck.** A `test:` commit that renames an existing test
  file fails redcheck, because those tests pass without the change. Rename
  in a `refactor:` commit first.
- **rapid failure files.** rapid writes `testdata/rapid/<Test>/*.fail` on
  every property failure, including a red's intended failure. Delete them
  before committing.
- **The coverage gate counts only product packages' own test binaries.** A
  test in `e2e/` proves nothing to it.
- **Scrubbed errors hide which failure happened.** Once errors are scrubbed
  to a correlation id, a fault test asserting only "an error" can't tell two
  failures apart. Fault tests require `ErrInternal` and the injected fault in
  the log.
- **GitHub closes only the first issue** in "Closes #a, #b". Write one
  keyword per issue.
- **GitHub only dispatches workflows that already exist on main.** A new
  workflow can't be run by hand from a branch.
- **`pkill -f` with a pattern that also appears in your own shell command**
  kills your shell. Match by exact process name instead.
- **The measurement lock is not first-in-first-out.** Timed runs share one
  `flock`, which does not serve waiters in order, so a chain of long runs
  starves everyone else. Take the lock for one run at a time.
- **The in-memory backend is the heap.** A long load run on
  `engine.MemoryBlobs` grows until it hits its cap; the 5-minute W8 reached
  12 GiB. Size memory runs, or use disk.

## The core, from here

- snapshot-core is a separate repository with its own owner process: a
  branch and PR per batch, squash merges, branches kept afterwards, and
  annotated `vX.Y.Z` tags on main after CI is green, each with a GitHub
  release. Until v1, a minor version may change Go APIs and says so; v0.2.0
  did (`stream.ReadAll` gained a limit).
- The engine reaches the core's tools (`ci`, `redcheck`, `mutate`) through
  go.mod `tool` directives. They run at the pinned tag's version, so a fix
  to a core tool reaches the engine only when the engine moves tags.
- To build against untagged core work, use an uncommitted `go.work`, as
  CONTRIBUTING describes. A red that needs it is verified by hand, because
  redcheck's scratch worktree has no workspace.
- The core's `redcheck` needs a local branch named `main` to compare
  against.
- To prove a new tag resolves, use a throwaway module: `go get
  …@vX.Y.Z`, `go mod tidy`, `go build`. Check the compiler's exit code, not a
  pipe's.

## The development machine

The engine was built on one machine, and the next developer works on the
same one. It is an i7-1360P with 4 performance and 8 efficient cores, 62 GB of
RAM, and an NVMe disk.

- **Checkouts.** `/home/adamsmith/git/snapshot-engine` (main) and
  `/home/adamsmith/git/snapshot-core`. A `go.work` over the two is made by
  hand when needed, and never committed.
- **Worktrees as of 2026-09-25:**
  - `../snapshot-engine-bd` holds branch `batch-diff`, a partial start on #8.
    It has seven commits plus an uncommitted engine change, none verified; #8
    describes it.
  - `../snapshot-engine-v020` holds branch `core-v0.2.0`, the move to core
    v0.2.0 (#7). It was stopped part-way, with uncommitted doc edits and no
    gates run; #7 lists what is done and what is not.
  
  Every other branch is already in main, and branches are kept after merging.
- **Memory caps.** `systemd-run --user --scope` works here and is how heavy
  runs are capped.
- **Measurement lock.** Timed runs have shared a lock file under the Claude
  scratchpad (`/tmp/claude-1000/.../scratchpad/measure.lock`). Any path works,
  as long as everyone measuring uses the same one.
- **Other services share the machine.** Docker, a Postgres, logflare and
  MinIO all run here, so check the load before timing anything.

## Tooling quirks seen on the development machine

- The machine's `gh` (2.45.0) cannot run `gh pr edit` or `gh issue view`,
  which fail on a removed GraphQL field. It also lacks `gh pr checks --json`.
  Use the REST API instead: `gh api repos/<owner>/<repo>/issues/<n> --jq
  .body`, and `gh api -X PATCH …/pulls/<n> -F body=@file`.
- `gh run view --log-failed` refuses while a run is in progress. Fetch a
  finished job's log with `gh api …/actions/jobs/<id>/logs`.
