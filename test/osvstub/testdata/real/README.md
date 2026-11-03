# Frozen real OSV records

Twenty-two real records of the OSV format, taken on **2026-11-03** from `https://api.osv.dev/v1/vulns/<id>` (indented, otherwise
as served; [SOURCE.json](SOURCE.json) lists the SHA-256 of every file). They are the real-data corpus of the
[test advisory source](../../../../docs/developer/e2e.md#the-test-advisory-source) and of the matching tests: the tests read them from this directory and need no network.

| Ecosystem | Records |
|---|---|
| npm (13) | GHSA-vr7g-88fq-vhq3, GHSA-35jh-r3h4-6jhm, GHSA-xvch-5gv4-984h, GHSA-5rqg-jm4f-cqx7, GHSA-mh6f-8j2x-4483, GHSA-jj78-5fmv-mv28, GHSA-c7hr-j4mj-j2w6, GHSA-29xp-372q-xqph, GHSA-pjwm-rvh2-c87w, GHSA-3h52-269p-cp9r, GHSA-27h2-hvpr-p74q (withdrawn), MAL-2025-46969, MAL-2026-5587 (withdrawn) |
| NuGet (9) | GHSA-473q-m89c-ghf8, GHSA-5crp-9r3c-p9vr, GHSA-2hr5-cvwp-jr5w, GHSA-29rv-fqx2-4c9f, GHSA-2m65-m22p-9wjw (withdrawn), GHSA-23cv-jh4v-vffm, GHSA-2pqj-h3vj-pqgw, MAL-2024-11505, MAL-2024-11506 |

What the set covers: `SEMVER` ranges (npm) and `ECOSYSTEM` ranges (NuGet, and `GHSA-3h52-269p-cp9r` for npm with the partial
version `13.0`), `last_affected` (`GHSA-35jh-r3h4-6jhm`, `GHSA-5rqg-jm4f-cqx7`, `GHSA-29rv-fqx2-4c9f`, `GHSA-2hr5-cvwp-jr5w`), `versions[]` lists,
`MAL-` records with `versions[]` only, withdrawn records, CVSS 3.1 only, CVSS 4.0 only, both (`GHSA-jj78-5fmv-mv28`,
`GHSA-2hr5-cvwp-jr5w`), no CVSS vector with `database_specific.severity` only (`GHSA-5rqg-jm4f-cqx7`, `GHSA-c7hr-j4mj-j2w6`,
`GHSA-23cv-jh4v-vffm`), and records with `affected[]` entries of other ecosystems (`GHSA-35jh-r3h4-6jhm`, `GHSA-2pqj-h3vj-pqgw`), which
the archive of an ecosystem also carries and which a matcher must ignore.

## Licenses

| Records | Source | License |
|---|---|---|
| `GHSA-…` | [GitHub Advisory Database](https://github.com/github/advisory-database) | CC-BY 4.0; attribution: advisories of the GitHub Advisory Database, as redistributed by osv.dev |
| `MAL-…` | [OpenSSF malicious-packages](https://github.com/ossf/malicious-packages) | Apache-2.0 |

The files are test data and are not shipped in any release artifact (the stub image of `make osv-stub-image` embeds them).

## Refreshing

`make osv-real-refresh` runs [cmd/osvfreeze](../../cmd/osvfreeze) over [ids.txt](ids.txt) (one `<ecosystem> <id>` per line): it
fetches every record again and rewrites the files and SOURCE.json with the day of the run. A refresh changes records that
the public sources have edited since; tests that name a field of a real record are rechecked against the new copy in
the same change. To add a record, add its line to ids.txt and refresh. The stub serves these records only when its toggle
`real` is on (with a new `modified`, so an incremental sync sees them as changes).
