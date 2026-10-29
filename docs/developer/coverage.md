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
| `go run ./internal/tools/coverage -check -tests <report>` | The CI form on `main`: also fails when `implemented.txt` differs from the reports. | as above |
| `go run ./internal/tools/coverage -links` | Checks every relative Markdown link and `#anchor` in the repository. Replaces the interim `check_links.py` and `check_anchors.py`. | 0, or 1 on a broken link |

Flags:

| Flag | Meaning |
|---|---|
| `-repo <dir>` | Repository root. Default: the nearest parent directory of the working directory that holds `go.mod`. |
| `-check` | Compare only, write nothing. |
| `-tests <report>` | A test report; repeatable. File arguments after the flags count as reports too. `-` reads standard input. |
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
  `TestSomething/R-ARCH-002_forbidden_import_names_the_edge` as naming `R-ARCH-002`.
- A requirement is **implemented** when at least one passing test names it and **no** failing
  test names it. A skipped test is neither.
- A catalog feature is **implemented** when at least one requirement covers it and every
  requirement that covers it is implemented. Otherwise it stays *specified*.
- The ID must stand alone in the name. The characters next to it may be anything but a letter,
  digit or hyphen: `R-ARCH-002_x`, `/R-ARCH-002 x` and `R-ARCH-002/2` name it; `PR-ARCH-002` and
  `R-ARCH-002-x` do not. Only `R-` IDs count. A criterion suffix such as `/2` is ignored today;
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

JUnit XML is the format for the end-to-end reports: Playwright and gotestsum write it, and a Go
e2e suite can use `go test -json`. The e2e test titles follow the same naming rule.

Pass **all** reports of a run in one invocation. `implemented.txt` is recomputed from exactly the
reports given, so a run that omits the e2e report drops the requirements only e2e proves.

## How CI feeds it

| Where | Command | Purpose |
|---|---|---|
| Every pull request | `go run ./internal/tools/coverage -check` | Rule violations, and generated files that the change did not regenerate. |
| Every pull request | `go run ./internal/tools/coverage -links` | Broken relative links and anchors. |
| `main`, after the test jobs | `go test -json ./... > unit.json`, then `go run ./internal/tools/coverage -check -tests unit.json [e2e.xml]` | `implemented.txt` and the pages rendered from it are current. |

Locally, `go test -json ./... | go run ./internal/tools/coverage -tests -` regenerates
`implemented.txt`; commit it with the change that adds the tests. The CI wiring is not part of the
generator: the Makefile target and the workflow call the commands above.

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
