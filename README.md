# snapshot-engine

The engine of the Versioned DB: the data models that give stored bytes their
shape and merge rules (tables, key-value, documents), the structured-value
merge library they share, and the one Go API every access layer builds on.
It runs on [snapshot-core](https://github.com/SmithOperatingSolutions/snapshot-core),
the storage core, as a pinned module: encrypted content-addressed chunks on
pluggable backends, commits, branches, garbage collection.

The idea in one line: every database flavor is a protocol and a query
language over a small number of data shapes. The shapes live here, once.
The protocols (Postgres, MySQL, Mongo, Redis, filesystems) are adapters, one
repository each, and import only this module's `engine` package.

**Status:** the models (tables, key-value maps, document collections) and
the merge library they share are done, and so is the engine API: sessions,
transactions with snapshot isolation and optimistic commit through the
models' merge, per-table grants and protected branches, errors scrubbed to
a correlation id. The layers, scope and order are in
[`docs/specs/engine-layers.md`](docs/specs/engine-layers.md); the milestones
and every checklist item with its test in [`docs/PROGRESS.md`](docs/PROGRESS.md).

## Layout

```
merge/            three-way merge of typed values, trees, sets, counters, sequences   (no core dependency)
model/table       rows with a schema and indexes; Postgres and MySQL                   (model id 3)
model/kv          an ordered map of typed values with a merge policy each; Redis       (model id 6)
model/document    schemaless records merged by field path; Mongo                       (model id 4)
engine/           Database, Session, Txn: the API adapters and programs use           (L5)
e2e/              a repository driven through engine/ alone
tools/mutate/     the checked-in mutant catalog (run by the core's tool)
docs/             the layers spec, the Engine Spec, design, progress, the testing standard
```

Files and folders are models the core already ships (`model/blob`,
`model/tree`), so filesystem adapters need nothing from here but `engine`.

## Developing

```
mise install          # Go 1.27, golangci-lint, govulncheck, built from source
mise run ci:quick     # fmt, vet, lint, race suite
mise run redcheck     # every test: commit on the branch fails without its feat:/fix:
mise run mutate       # every checked-in mutant is killed
```

Changes land test-first: a `test(pkg):` commit that fails on assertions, then
the `feat(pkg):` or `fix(pkg):` that makes it pass. `redcheck` and `mutate`
are the storage core's tools, run here as Go tools. See
[`CONTRIBUTING.md`](CONTRIBUTING.md) and the testing standard,
[`docs/TESTING.md`](docs/TESTING.md).

Pure Go, `CGO_ENABLED=0`, here and in every adapter.

## Documents

- [`docs/specs/engine-layers.md`](docs/specs/engine-layers.md): what is built here, in which layers, and why
- [`docs/specs/engine-spec.md`](docs/specs/engine-spec.md): the Engine Spec; its L4 is built here, its L0 to L3 are the core's
- [`docs/DESIGN.md`](docs/DESIGN.md): decisions, the layer table the build enforces, formats as they land
- [`docs/PROGRESS.md`](docs/PROGRESS.md): milestones, checklists and their tests, what testing found

## License

Apache License 2.0; see [`LICENSE`](LICENSE).
