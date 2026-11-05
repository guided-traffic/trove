# perf

Inputs of the performance and end-to-end tests that are data rather than code, and the load scripts of R-ARCH-027. The P-12
driver and the scripts of the other formats join this directory with the work packages that need them.

```
perf/
├── README.md                    this file
├── vuln/                        the performance run of the vulnerability scanning (R-VULN-028, V-1 to V-9), see below
├── k6/
│   └── npm.js                   the targets of R-ARCH-027 an npm client meets (P-1, P-3, P-5, P-7, P-8), against one web replica
├── nuget/
│   └── reference/               the NuGet reference solution (Reference.sln, Reference/Reference.csproj, nuget.config), see below
└── npm/
    ├── reference.sh             writes or checks the reference project below (make npm-reference, make npm-reference-check)
    └── reference/
        ├── package.json         the reference project: root dependencies chosen from the corpus
        └── package-lock.json    its lockfile (lockfileVersion 3), 707 entries today
```

## The vulnerability scanning run

[vuln](vuln) is a Go test package (build tag `integration`) that measures the scenarios V-1 to V-9 of R-VULN-028. It seeds 100,000
stored versions in SQL (90,000 npm in 9,000 packages across `npm-local` and `npm-remote`, 10,000 NuGet in `nuget-local`), starts the
test advisory source ([test/osvstub](../test/osvstub)) with 231,000 npm and 7,400 NuGet records, which are the frozen dumps (the
generator is deterministic), and runs one `web` and one `worker` replica of the composition root against the integration stack. The
package names are those of the filler records, so version 1.0.0 of every package has one finding.

| File | What it is |
|---|---|
| `vuln/env_test.go` | the report (`.out/perf-vuln.json`), the percentile and histogram helpers, the publish body |
| `vuln/perf_test.go` | `TestR_VULN_028_PerformanceRun`: V-1 to V-9 in order |
| `vuln/seed_test.go` | `TestR_VULN_028_TheSeededInstallation`: the shape of the seeded installation |

```shell
make integration-up
set -a; . .dev/integration/env; set +a
go test -count=1 -p 1 -tags integration -timeout 115m -run 'TestR_VULN_028' ./perf/vuln/...     # reports
TROVE_PERF_GATE=1 go test -count=1 -p 1 -tags integration -timeout 115m -run 'TestR_VULN_028' ./perf/vuln/...   # a miss fails
```

It runs in the job `vuln-perf` of [HA and Performance (nightly)](../.github/workflows/performance.yml) with the gate on, and never in
Test and Release. Every value of a run is in `perf-vuln.json` (artifact `vuln-perf`, 30 days); a miss opens an issue. The
environment is the integration stack with the replicas in the test process, not the reference environment of R-ARCH-027
([H-491](../docs/security/vulnerability-scanning.md#h-491--the-performance-run-is-not-the-reference-environment)).

### Reading a run

Each row of `perf-vuln.json` has an `outcome`. Only a `measured` value is compared with its target (`met`); the gate fails the run on a
measured value above its target, and a scenario that does not finish fails it with or without the gate.

| Outcome | When | `value` |
|---|---|---|
| `measured` | the scenario ran to its end | the measurement |
| `not finished` | its wait ran out, or the job it waits for ended without the result (V-1: the full sync ended and npm is not `complete`; V-3: the incremental sync ended other than `succeeded`) | the time it waited, in minutes |
| `not finished` | it failed before it measured (a request answered otherwise than expected) | `null` |
| `not measured` | a scenario it needs did not finish; it did not run | `null` |

| Scenario | Needs | Why |
|---|---|---|
| V-1 | nothing | the import into empty tables |
| V-2 | V-1 | its clock starts when the import completed, and the full evaluation is the one that completion requests |
| V-9 | V-1 | the size of the tables after the import |
| V-3 to V-8 | V-1 and V-2 | each measures an installation that is imported and evaluated; V-3 is an incremental sync only on a complete ecosystem, a full one otherwise |

The regression issue lists every row that does not meet its target with its outcome and the note (`its wait ran out after 40m0s`,
`V-1 did not finish`). A wait that gives up logs, in the test output: the advisory source state of each ecosystem (`state`,
`failures`, `last_error`, `attempted_at`, `synced_at`, `records`), every job of the scanning (state, attempt of max attempts, `run_at`,
`locked_until`, progress, checkpoint, error), the rows queued for evaluation and the names an unfinished sync left in
`vuln_sync_touched`, and the last 80 lines of each replica's log that are WARN or ERROR, from the components `vulnerabilities` and
`jobs`, or requests to the advisory source. Each replica's whole log up to that moment, and again at the end of a failed run, is in
`.out/perf-vuln-logs/<replica>.log`, which the workflow uploads with the report.

Runs before 2026-11-05 do not compare with later ones on V-1, V-8 and V-9: the test source then kept every filler record numbered
10,000 or above when the NuGet filler was lowered from 231,000 to 7,400, so NuGet held 228,403 records instead of 7,403. V-1 included
their import (NuGet runs before npm), V-8 served a page of the 228,403 (28,569,143 bytes, computed with the page's renderer; the page
of 7,403 is 903,143 bytes), and V-9 counted 459,415 advisories. `TestR_VULN_028_TheSeededInstallation` checks the sizes of the dumps.

## The npm load scripts

[k6/npm.js](k6/npm.js) runs five k6 scenarios one after the other against one `web` pod's listener, from inside the cluster: P-1 (time
to first byte of a 1 MiB tarball), P-3 (a document of `npm-local`), P-5 (a document of `npm-all`), P-7 (documents at 2,000 requests/s)
and P-8 (a 50 KiB tarball at 1,000 requests/s). Its inputs are a token file and a fixture file in `/out`, which `TestNPMPerf`
([test/e2e/npm_perf_test.go](../test/e2e/npm_perf_test.go)) writes; the result is `/out/perf-npm.json`, with every answer of a
scenario counted by status, as `shed` (the replica's own `503 server_busy`) or by the error class of a request without an answer. Without `GATE=1` it reports, which
is how the nightly job `npm-ha-soak` and the closing run run it on the dev profile; with `GATE=1` the targets are k6 thresholds, for the reference environment of
R-ARCH-027 (roadmap E5.3, [Q-ROAD-9](../docs/coverage/roadmap.md#q-road-9--what-wp28-builds-of-r-arch-027s-load-scripts-and-where-they-run)).
How it is started: [e2e.md](../docs/developer/e2e.md#the-load-scripts-of-r-arch-027).

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

## The NuGet reference solution

Exit criterion E3.2 runs three clients at once, each `dotnet restore --no-cache`, against this solution through the virtual
repository `nuget-all`. At least 100 packages are required in its restore graph; the graph has at least 120, all from the generated
package set of [test/nugetstub](../test/nugetstub), so the counting test upstream serves every one of them
([Q-NUGET-27](../docs/spec/formats/nuget.md#q-nuget-27--the-reference-solution-of-e32-how-it-is-built-and-checked)).

| File | What it is |
|---|---|
| `nuget/reference/Reference.sln` | one project |
| `nuget/reference/Reference/Reference.csproj` | `net10.0`, `PackageReference`s on eight roots of the set, each at its latest stable version |
| `nuget/reference/nuget.config` | `<clear />` and one source, the dev profile's `nuget-all` (`https://localhost:8080/api/nuget/nuget-all/v3/index.json`); the e2e tests pass `--source` instead |

Written by `go run ./test/nugetstub/cmd/nugetstub reference -out perf/nuget/reference`; `-check perf/nuget/reference` compares.
The files are deterministic. `TestR_NUGET_027_ReferenceSolutionIsCurrent` fails when they differ from a regeneration, and
`TestR_NUGET_027_ARestoreOfTheReferenceSolution` walks the graph through the stub the way a restore does (every dependency is
the range `1.0.0`, every version of a package has the same dependencies, so the walk and the client agree) and requires at
least 100 packages with one request per `.nupkg`. Whether the real client resolves the same graph is what E3.2's e2e run shows.
No lock file is committed: the .NET SDK is not part of this change's tooling.

## What is not here yet

The pnpm workspace of R-NPM-040 (3 internal and 300 public packages) is not generated: it belongs to the e2e work package
that runs pnpm. Yarn and pnpm lockfiles are produced at test time, not committed. The CI job that runs
`make npm-reference-check` is not added by this change (the workflow job block belongs to the e2e work package).
