# perf

Inputs of the performance and end-to-end tests that are data rather than code (R-ARCH-027). Load scripts and the P-12
driver join this directory with the work packages that need them.

```
perf/
├── README.md                    this file
└── npm/
    ├── reference.sh             writes or checks the reference project below (make npm-reference, make npm-reference-check)
    └── reference/
        ├── package.json         the reference project: root dependencies chosen from the corpus
        └── package-lock.json    its lockfile (lockfileVersion 3), 707 entries today
```

## The npm reference project

Exit criterion E2.2 of [the roadmap](../docs/coverage/roadmap.md) runs three clients at once, each `npm ci` with an empty
cache, against this lockfile through the virtual repository `npm-all`. At least 500 packages are required; the lockfile has
707 entries (541 distinct package names; some appear nested in more than one version), all from the synthetic corpus of
[test/npmstub/corpus](../test/npmstub/corpus), so the counting test upstream serves every one of them.

| File | What it is |
|---|---|
| `reference/package.json` | `private`, `dependencies` only (59 roots whose closure by package name covers at least 540 packages), no scripts. Written by `npmstub reference` from `corpus.Reference`; the choice is deterministic |
| `reference/package-lock.json` | Resolved by the npm bundled in the pinned Node image against the stub: `npm install --package-lock-only --ignore-scripts`. `resolved` is `https://localhost:8080/api/npm/npm-all/<name>/-/<file>.tgz` (the dev profile's `npm-all`), `integrity` is the corpus's |

### Regenerate

```shell
make npm-reference          # builds the stub image, resolves, overwrites the two files
make npm-reference-check    # resolves into .out/npm-reference and diffs; fails on any difference (needs Docker)
```

Both need Docker and Go, not a cluster and not the internet: the script starts the stub image on a throwaway Docker
network with a throwaway CA and runs `npm` from `E2E_CLIENTS_NODE_IMAGE` ([tools/versions.env](../tools/versions.env),
Node 26.13.0, the version of `web/.nvmrc`; the script refuses an image whose version differs) with the stub as its registry.
The writer is therefore always that image's npm (11.20.0 at this pin), never a host npm.

When to regenerate: after any change of the corpus generator (its digest test fails first), of the Node image pin, or of
`corpus.ReferenceMinNames`. Commit the files with the change.

### Checks that do not need Docker

`TestReferenceLockfileMatchesTheCorpus` ([test/npmstub/lockfile_test.go](../test/npmstub/lockfile_test.go)) runs in
`make test`: `package.json` equals the corpus's reference project, every locked package exists in the corpus with the
corpus's integrity and the `npm-all` address, no entry has an install script, and there are at least 500 entries.

## What is not here yet

The pnpm workspace of R-NPM-040 (3 internal and 300 public packages) is not generated: it belongs to the e2e work package
that runs pnpm. Yarn and pnpm lockfiles are produced at test time, not committed. The CI job that runs
`make npm-reference-check` is not added by this change (the workflow job block belongs to the e2e work package).
