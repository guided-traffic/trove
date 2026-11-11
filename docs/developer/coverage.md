# Coverage generator

The coverage pages in [docs/coverage/](../coverage/README.md) are generated from the feature catalog
and the specification by one Go program, [internal/tools/coverage](../../internal/tools/coverage/main.go)
(roadmap WP0.9, exit criterion E0.8). It uses only the standard library. The rules it enforces and
the numbers it produces are described for readers in
[How the numbers are produced](../coverage/README.md#how-the-numbers-are-produced); this page is
for people who change or run it.

## Modes

| Command | What it does | Exit code |
|---|---|---|
| `go run ./internal/tools/coverage` | Regenerates [checklist.md](../coverage/checklist.md), the four marked blocks of [coverage/README.md](../coverage/README.md) and [implemented.txt](../coverage/implemented.txt). Implementation status comes from the committed `implemented.txt`. | 0, or 1 on a rule violation |
| `go run ./internal/tools/coverage -check` | Writes nothing. Fails when a generated file differs from what would be generated. This is the `git diff --exit-code` of E0.8 without needing git. | 0, 1 rule violation, 2 stale file |
| `go run ./internal/tools/coverage -tests <report>` | Reads test reports, recomputes the implemented requirements, rewrites `implemented.txt` and everything rendered from it. | as above |
| `go run ./internal/tools/coverage -check -tests <report>` | Writes nothing; also fails when `implemented.txt` differs from the reports, in either direction. A local comparison; CI uses `-claims`. | as above |
| `go run ./internal/tools/coverage -claims -tests <report> [-carried <report>]` | The CI form on `main`: writes `implemented.txt` and everything rendered from it from the reports, prints the requirements proven for the first time, and fails when the committed `implemented.txt` lists a requirement the reports do not prove (it then writes nothing). A requirement that only a failing test in a `-carried` report takes away is written out of the record and listed as withdrawn, and does not fail ([Q-ROAD-12](../coverage/roadmap.md#q-road-12--a-failing-test-in-the-report-of-an-earlier-run-withdraws-the-requirement-and-does-not-fail-the-evidence-job)). | 0, 1 rule violation, 3 unproven claim |
| `go run ./internal/tools/coverage -links` | Checks every relative Markdown link and `#anchor` in the repository. Replaces the interim `check_links.py` and `check_anchors.py`. | 0, or 1 on a broken link |

Flags:

| Flag | Meaning |
|---|---|
| `-repo <dir>` | Repository root. Default: the nearest parent directory of the working directory that holds `go.mod`. |
| `-check` | Compare only, write nothing. |
| `-claims` | With `-tests`: the committed list may lag behind the reports but must not claim more than they prove. Does not combine with `-check` or `-links`. |
| `-tests <report>` | A test report; repeatable. File arguments after the flags count as reports too. `-` reads standard input. |
| `-carried <report>` | A report of an earlier run of a leg that did not run in this one; repeatable; needs `-tests`. It proves and vetoes like a `-tests` report, but a requirement it alone vetoes does not fail `-claims`. |
| `-links` | Link and anchor check instead of generation. Does not combine with `-check` or `-tests`. |

Usage errors exit with 64. The generator prints the counts per priority, the number of `Covers:`
references, the implemented counts and every heading it skipped inside a code fence.

## Inputs and rules

| Input | Read for |
|---|---|
| `docs/features/**/*.md` | `### <ID> · <Title>` with `Priority: **<P>**` directly below. Page order follows the page table of [features/README.md](../features/README.md). |
| `docs/spec/**/*.md` | `### R-… · <Title>` with `Priority: **<P>** · Covers: <ID>, …` or `Covers: trove-only` directly below. |
| `docs/**/*.md` except `features/` | `### Q-… · …` and `### H-<n> · …` headings, for the uniqueness check only. |
| `docs/coverage/implemented.txt` | The requirements with passing evidence, as of the last `-tests` run. |
| `docs/coverage/README.md` | The prose around the marker pairs `<!-- coverage-gen:begin NAME -->` and `<!-- coverage-gen:end NAME -->`, kept untouched. |

The program exits with 1 and prints every problem (not only the first) for:

- a catalog entry or requirement whose priority line is missing or whose value is not one of
  `MVP`, `P1`, `P2`, `Later`, `Out of scope`;
- a `Covers:` ID that is not in the catalog, a token that is neither an ID nor `trove-only`, an
  ID listed twice, `trove-only` mixed with IDs;
- a duplicate catalog ID, requirement ID, `Q-` ID or `H-` number;
- an unclosed code fence;
- a test name that carries a requirement ID no spec page defines;
- a missing marker pair, an audience page without entries.

Lines inside fenced code blocks are ignored by all of these, which is why the format example in
[spec/README.md](../spec/README.md) can reuse `R-NPM-012`. Each fixture for a rule is a small docs
tree under [testdata/](../../internal/tools/coverage/testdata/), and the test that uses it is named
after E0.8 in [coverage_test.go](../../internal/tools/coverage/coverage_test.go).

## Implementation status: the test-naming rule

This is the rule of [Q-ROAD-4](../coverage/roadmap.md#q-road-4--how-the-coverage-generator-learns-implementation-status),
with one decision on top (below).

- A test names a requirement by carrying its ID in the test name. In Go, put it in the subtest
  name; the testing package turns spaces into underscores, and the generator reads
  `TestSomething/R-ARCH-002_forbidden_import_names_the_edge` as naming `R-ARCH-002`. A function
  name cannot hold a hyphen, so the generator also reads the underscore form
  ([Q-ARCH-251](../spec/architecture.md#q-arch-251--the-coverage-generator-reads-a-requirement-id-in-a-go-function-name-in-its-underscore-form)):
  `TestR_API_053_TheGuideOfAFormat` names `R-API-053`.
- A requirement is **implemented** when at least one passing test names it and **no** failing
  test names it. A skipped test is neither.
- A catalog feature is **implemented** when at least one requirement covers it and every
  requirement that covers it is implemented. Otherwise it stays *specified*.
- The ID must stand alone in the name. The characters next to it may be anything but a letter,
  digit or hyphen: `R-ARCH-002_x`, `/R-ARCH-002 x` and `R-ARCH-002/2` name it; `PR-ARCH-002` and
  `R-ARCH-002-x` do not. Only `R-` IDs count.
- The underscore form is `R_<AREA>_<digits>` (`idsIn`, `underscoreID` in
  [evidence.go](../../internal/tools/coverage/evidence.go)): an upper-case area of one segment and one
  number, so `TestR_AUTH_027_E1_2_Token` names `R-AUTH-027` and nothing else. It follows the same
  standalone rule, and may also stand directly after `Test`, `Benchmark`, `Fuzz` or `Example` at the
  start of a function name or a subtest, and next to underscores (`TestName_R_API_053`).
  `PR_API_053`, `TestPR_API_053`, `TestR_API_053x` and `TestR_api_053` name nothing. An ID the spec
  does not define is reported as unknown, in either form. `TestIDsInTestNames` in
  [coverage_test.go](../../internal/tools/coverage/coverage_test.go) holds the cases. A criterion suffix such as `/2` is ignored today;
  Q-ROAD-4 names it as the answer if one name per requirement proves too coarse.

```go
func TestImportRules(t *testing.T) {
	t.Run("R-ARCH-002 forbidden import names the edge", func(t *testing.T) {
		// ...
	})
}
```

A test that verifies several requirements carries several IDs in one name
(`R-HA-002 R-HA-023 worker count`). The test names of the generator's own tests must not contain
sample IDs, because CI feeds them to the generator too.

### Why a failing test vetoes

Decision by the development agent: a requirement with a passing and a failing test that name it is
not implemented.

| Option | Effect | For | Against |
|---|---|---|---|
| Passing evidence alone | One green test marks the requirement. | Simple. | A regression hides behind an older green test, which is the drift Q-ROAD-4 exists to prevent. |
| **A failing test vetoes** | Any red test that names the ID withdraws the mark. | Status follows the worst evidence. A red `main` shows as a drop in the numbers. | A flaky test flickers the status. |

Revisit if flaky tests make the numbers unusable; the fix then belongs in the test, not in the
generator.

## Report formats

`-tests` detects the format from the first non-blank character of each file.

| Format | Detected by | Reading |
|---|---|---|
| `go test -json` stream | anything but `<` | One event per line. `pass` and `fail` events with a `Test` field count; `skip` does not. Lines that are not JSON objects (build output mixed in) are skipped; a file without a single event is an error. |
| JUnit XML | `<` | Every `<testcase>` (any nesting, `<testsuites>` or `<testsuite>` at the root). `<failure>` or `<error>` child: failed. `<skipped>` child: skipped. Otherwise passed. IDs are searched in `classname` and `name`. |

What CI produces, by tier:

| Tier | Report | Written by | Artifact |
|---|---|---|---|
| Go unit | `go test -json` | `make test` | `test-unit` |
| Angular unit (Vitest) | JUnit, `.out/test-web.xml` | `ng test` through the reporters in [angular.json](../../web/angular.json) | `test-web` |
| Angular lint rules (R-UI-046) | JUnit, `.out/test-web-lint.xml` | `npm run test:lint` ([lint-rules.test.mjs](../../web/scripts/lint-rules.test.mjs), `node --test`) | `test-web` |
| Chart and promtool | `go test -json` | `make chart-test` | `test-chart` |
| Integration | `go test -json` | `make test-integration` | `test-integration-a`, `-b`, `-c` |
| Kind legs (Go e2e sets, with the Playwright journeys as subtests named by their titles) | `go test -json`, `.out/test-e2e.json` | `make e2e SET=…` ([e2e.sh](../../deploy/dev/e2e.sh)) | `e2e-report-<set>`, `e2e-<group>-report-<set>`, `e2e-backup-report` |
| Nightly latency gates | `go test -json` | `make test-performance` | `test-performance` |

The Playwright journeys are run by a Go test (`TestE1_11Journeys` and the bastion, vulnerability, mirror and upgrade counterparts), which
reads Playwright's JSON report and turns every spec into a subtest named by the spec's title. The Playwright configurations therefore need no
reporter of their own, and a journey that names a requirement in its title is evidence once the Go test that ran it is in a report. The
kind-leg reports are kept 30 days.

Pass **all** reports of a run in one invocation. `implemented.txt` is recomputed from exactly the
reports given, so a run that omits the e2e report drops the requirements only e2e proves.

## How CI feeds it

| Where | Command | Purpose |
|---|---|---|
| Every pull request | `go run ./internal/tools/coverage -check` | Rule violations, and generated files that the change did not regenerate. |
| Every pull request | `go run ./internal/tools/coverage -links` | Broken relative links and anchors. |
| After the test jobs and the kind legs (`coverage evidence`) | `go run ./internal/tools/coverage -claims -tests …` over the unit, Angular, chart and integration reports and the kind-leg reports of the run, and `-carried …` over the newest report of every kind leg that has none in this run | No requirement is claimed without a passing test; the record written from the reports is uploaded as `coverage-evidence` and committed by Semantic Release with the release. |

The kind legs are path-filtered per change and the long legs run in the nightly workflow, so a run holds only some of the end-to-end
reports. [fetch-e2e-reports.sh](../../tools/fetch-e2e-reports.sh) fills the rest: it lists the last 40 completed runs of Test and Release and
the last 14 of the nightly on `main` through the Actions API (the job holds `actions: read`), leaves out the cancelled ones (a push cancels
the run before it, and a leg cut off mid-set uploads the report of the tests it reached), and for each artifact name that is not in this
run takes the newest one, whichever workflow wrote it ([Q-ROAD-11](../coverage/roadmap.md#q-road-11--the-evidence-record-reads-the-reports-of-every-test-tier)).
A leg without an unexpired report proves nothing until it runs again, and the job summary lists what was carried and from which run. A failing
test in a carried report vetoes its requirement as in a report of this run; the requirement then leaves the record and is listed under
"withdrawn", and the job does not fail for it, so a red long leg does not hold a release (owner rule of 2026-11-01), while a claim that no
report proves still does. To see the effect locally, download the reports of a run (`gh run download <run> -p 'test-*' -D <dir>`) and the
carried ones with `tools/fetch-e2e-reports.sh <dir of this run's e2e reports> <destination>` (it needs `GITHUB_TOKEN`, `GITHUB_REPOSITORY` and
`GITHUB_RUN_ID`), then run the generator with the `-tests` and `-carried` flags.

Nobody refreshes `implemented.txt` by hand or from a local run: CI writes it from the reports of every run on `main`, and
Semantic Release commits it, `checklist.md` and the generated blocks of the coverage README with the release (the release
commit is `[skip ci]`). Two things stay manual. A change to the catalog or the spec reruns `go run ./internal/tools/coverage`
(`make coverage`), which needs no tests. A change that removes or renames the only test of a requirement on purpose takes
the ID out of `implemented.txt` in the same change; otherwise `coverage evidence` fails with exit 3 and names it. The CI
wiring is not part of the generator: the workflow calls the commands above.

### Why CI writes the record

Decided by the owner (2026-11-07), on the development agent's proposal.

| Option | Effect | For | Against |
|---|---|---|---|
| The committed record must equal the reports of the run (`-check -tests`) | A push whose tests prove a requirement for the first time fails until the record is refreshed and pushed again | The record is exact on every commit | Every such push costs a second pipeline run, and the refresh needs every test tier locally (2.5 h on 2026-11-07, and still stale: the test binary was older than the last commit). 4 of 11 red runs of 2026-11-06/07 were this job |
| **CI writes the record, Semantic Release commits it; the check fails only on an unproven claim (`-claims`)** | The record is written from the run that releases; a claim without a passing test fails the run | No local evidence run, no second pipeline run; the record never claims more than the tests prove | The record lags behind a push that makes no release, until the next release |
| CI commits the record on every push | Exact after every push | — | A bot commit after every push, and a pipeline run for each |

## Format changes against the interim script

The output is byte-identical to the interim Python script's, with two intended differences.

- The *Implemented* row of the headline table in [coverage/README.md](../coverage/README.md) has the
  kind "measured from passing tests that name requirements (Q-ROAD-4)" where the script wrote "fact,
  not computed: the repository holds no code yet", which stopped being true with the first Go file.
- Once a requirement is implemented, `checklist.md` marks it with ` (implemented)`, adds an
  *Implemented* column to the page table and rewrites the status paragraph. With an empty
  `implemented.txt` none of this appears.

The link check differs from the scripts in three ways: it skips fenced code and inline code per line
instead of with a multi-line regular expression, numbers repeated headings the way the Markdown
hosts do (`#same`, `#same-1`), and does not descend into `node_modules`, `.git` or `testdata`.

## Files

| File | Responsibility |
|---|---|
| [main.go](../../internal/tools/coverage/main.go) | Flags, modes, exit codes, writing and comparing files. |
| [parse.go](../../internal/tools/coverage/parse.go) | Fence-aware reading of catalog and spec, the rule checks, ID uniqueness. |
| [model.go](../../internal/tools/coverage/model.go) | Pages, counts, feature status, page order, heading anchors. |
| [evidence.go](../../internal/tools/coverage/evidence.go) | Test reports, ID extraction from test names, `implemented.txt`. |
| [render.go](../../internal/tools/coverage/render.go) | `checklist.md`. |
| [readme.go](../../internal/tools/coverage/readme.go) | The four marked blocks of `coverage/README.md`, the audience page lists. |
| [links.go](../../internal/tools/coverage/links.go) | The `-links` mode. |

To change the audience page lists, edit `audiences()` in `readme.go`; the generator fails when a
listed page has no entries.
