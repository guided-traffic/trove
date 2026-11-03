# Reference data of R-VULN-017

The public npm registry's answers to two bulk audit requests and the OSV records behind them, taken on **2026-11-03** in one run
([SOURCE.json](SOURCE.json) lists the day, the counts and the SHA-256 of every file). They are what the reference comparison of
[R-VULN-017](../../../../docs/spec/vulnerability-scanning.md#r-vuln-017--npm-audit-from-troves-advisory-data) (exit criterion E4b.5)
compares trove's answer with: `TestR_VULN_017_TheReferenceLockfileMatchesThePublicAnswer` in
[test/integration/vulnaudit](../../../integration/vulnaudit/reference_test.go) needs no network.

| File | What it is |
|---|---|
| `public-request.json` | The bulk body of `perf/npm/reference/package-lock.json`: every installed package with a version, `{"<name>": ["<version>", …]}` (541 names, 594 versions); the test checks that it is still the body of the lockfile |
| `public-bulk.json` | The answer of `POST https://registry.npmjs.org/-/npm/v1/security/advisories/bulk` (gzip body, as npm 7 and later send it) to it: `{}`. The reference lockfile resolves from the synthetic corpus only; the public registry has no advisory for any of its names |
| `control-request.json` | The control body, kept by hand: ten real packages with known advisories (`lodash`, `axios`, `handlebars`, `event-stream`, …), also versions that are fixed or unaffected, so that the comparison names identifiers |
| `control-bulk.json` | The public answer to the control body: 9 names, 67 distinct GHSA identifiers |
| `records/<id>.json` | The OSV record of every GHSA identifier in the answers, from `https://api.osv.dev/v1/vulns/<id>` (indented, otherwise as served), and every `MAL-` record those records alias (none today) |

The test advisory source serves `records/` when its toggle `reference` is on (`osvstub.ReferenceRecords()`).

## Licenses

| Records | Source | License |
|---|---|---|
| `GHSA-…` | [GitHub Advisory Database](https://github.com/github/advisory-database) | CC-BY 4.0; attribution: advisories of the GitHub Advisory Database, as redistributed by osv.dev |
| `MAL-…` | [OpenSSF malicious-packages](https://github.com/ossf/malicious-packages) | Apache-2.0 |

The two answers are advisory metadata of the public registry (identifiers, titles, severities, version ranges, links to the GitHub Advisory Database), taken
for a test comparison. The files are test data and are not shipped in any release artifact (the stub image of `make osv-stub-image` embeds them).

## Refreshing

`make osv-reference-refresh` runs [cmd/osvreference](../../cmd/osvreference): it builds the body from the lockfile, posts it and the control body, writes
the answers and the request, fetches the record of every identifier the answers name and rewrites SOURCE.json with the day of the run. The answers and the
records must be taken in the same run: the public answer reflects the advisory database of that day, and the OSV records are those of that day. A refresh
needs network access to `registry.npmjs.org` and `api.osv.dev`; review the diff, and analyse a failing comparison afterwards (a record changed since, a
difference in the matching) instead of changing the expectation. To change the control, edit `control-request.json` and refresh.
