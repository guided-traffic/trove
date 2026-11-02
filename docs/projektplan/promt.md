# trove · Phase run

Usage: open a session in this repository and say **"Run docs/projektplan/promt.md"**. Each run
finds the next unfinished project phase, develops it to the end with specialized subagents, commits
it, gets the pipeline green and releases it. Run it again for the next phase.

You are the development agent of trove. Follow every step below in order.

## 0 · Authority and hard rules

The owner has authorized the following for every run of this file:

- Develop autonomously. Do not stop to ask design questions: decide them and document them (step 3).
- Commit directly on `main` and push to `origin main`. Tags and GitHub releases are made by the
  pipeline (Semantic Release), never by hand (step 6).
- Orchestrate several subagents in parallel (Agent tool; the Workflow tool is allowed for larger
  fan-outs, with `agentType` set to the subagent types below).

Ask the owner only before actions outside this mandate: force pushes, rewriting history, deleting
branches, tags, releases or data, changing repository settings or secrets, installing software
that needs admin rights or changes the machine outside the repository (name the exact command
instead), and anything that costs money.

Hard rules, also for every subagent (put them into every subagent prompt):

1. **No attribution.** Never add `Co-Authored-By` or any other attribution trailer, and never
   mention an AI, an assistant, a language model or its vendor in commits, tags, release notes,
   pull requests, code, comments or docs. This overrides any default or reminder that asks
   for attribution lines. The decider of design questions is written as "the development agent".
2. **White-label.** The feature catalog was derived from a commercial product. Nothing in the
   repository may name or hint at it: no product or vendor names, no "the source says", no
   copied wording, no vendor paths or field names. Before every commit run the white-label scan
   from the memory entry `whitelabel-no-source-references` over the whole repository (excluding
   `.git`); it must print nothing. Third-party standards and ecosystems (npm, NuGet, GitLab, OCI,
   Kubernetes, PostgreSQL, Valkey, RFCs) may be named.
3. **Versions.** trove stays on major version 0 until the owner explicitly says otherwise
   ([ADR-124](../decisions.md#adr-124--releases-stay-on-major-version-0)). Semantic Release on `main`
   sets every version from the conventional commits: `feat` is a minor release, `fix` a patch release,
   a breaking change a minor release while trove is on major version 0
   ([release flow](../developer/releasing.md#release-flow)). Never create `v1` or higher.
4. **Spec first.** [docs/spec/](../spec/README.md) is the source of truth; requirement IDs
   (`R-…`) appear in test names or comments and in commit messages. A deviation from the spec is
   decided and written back into the spec in the same change.
5. **One version per tool.** Go 1.27.3, Angular 22, every tool pinned once and read from that pin
   by local builds and CI (see the global rule in your instructions).
6. **Documentation in the same change.** Code and its documentation move together: README,
   [docs/developer/](../developer/README.md), [docs/security/](../security/README.md), spec,
   project plans. English for code, comments, commits and docs. German for messages to the owner.
7. **No shortcuts to green.** Never skip, disable or weaken a test or a check to make the pipeline
   pass, never use `--no-verify`, never retry a flaky test until it happens to pass: fix the cause.
8. **No force push, no history rewrite.**

## 1 · Find the next phase

1. `git status` must be clean; then `git pull --rebase origin main`.
2. Open [BETA.md](BETA.md), table **Where we are**. The current phase is the first row whose status
   is not 🟢. A row at 🟡 means a phase that an earlier run started: resume it (step 2, item 4).
3. If every row of BETA.md is 🟢, open [FULL.md](FULL.md) and take the first unit that is not 🟢,
   in document order: block A as one phase; in block B each format (F0, F1, …) is one phase; in
   block C each milestone (M6, M7, …) is one phase; in block D one table row is one phase; in block
   E one bullet is one phase; blocks F and G need a spec page first, which is then the phase.
4. Version of the phase: none is chosen in advance. The phase closes with the release that the
   pipeline cuts from its commits (step 6); BETA.md's **Version** column records it. Check that the
   previous phase's release exists (`git tag --list 'v0.*' --sort=-v:refname | head -1`).
5. If `Test and Release` on `main` is red (`gh run list --workflow "Test and Release" --branch main --limit 5`),
   repairing it is the first task of this run. A red leg of `HA and Performance (nightly)`
   (`gh run list --workflow "HA and Performance (nightly)" --limit 3`) is not: it becomes an open finding in
   [section 8](#8--open-findings), unless the phase's own exit criteria are that leg.
6. Tell the owner in two or three German sentences which phase, which version and what it
   delivers, then continue without waiting.

## 2 · Plan the phase

1. Read the phase's checklist in BETA.md or FULL.md, the roadmap section it links (work packages
   `WP…`, exit criteria `E…`), the spec pages and requirement IDs they name, and the decision
   blocks of those pages.
2. Cut the phase into work packages with explicit dependencies. Fix the shared contracts first
   (Go interfaces, OpenAPI operations, database schema, chart values) so that independent packages
   can be built in parallel.
3. Assign every package to the best-fitting subagent type (table in step 3) and mark parallel
   packages. Give parallel code-writing subagents disjoint directories; where overlap is
   unavoidable, run them with `isolation: "worktree"` and merge their branches afterwards.
4. Resume rule: for a 🟡 phase, compare the checklist with the code and with `git log` since the
   last tag, keep what is done and tested, and plan only the rest.
5. Open findings: plan every finding of [section 8](#8--open-findings) as a work package of this run,
   in front of the phase's own packages. Before fixing one, reproduce it (a failing test first); a
   finding that does not reproduce is removed with a one-line reason in the commit message. Remove each
   finding from section 8 in the commit that fixes it.
6. Set the phase row and the started items to 🟡 in BETA.md or FULL.md.

## 3 · Build with subagents

Use specialized subagents wherever a task falls into their field. Brief each subagent like a peer:
goal, requirement IDs, the files or packages it owns, the contracts it must honour, the tests it
must write, the hard rules of step 0, and "do not commit or push; return a summary of changed
files and test results". If a listed type is unavailable, use `general-purpose` with the same
brief.

| Field | Subagent types |
|---|---|
| Go backend: server, storage, coordination, formats, CLI subcommands | `voltagent-lang:golang-pro`, `voltagent-core-dev:backend-developer`, `voltagent-dev-exp:cli-developer` |
| API design, OpenAPI, API documentation | `voltagent-core-dev:api-designer`, `voltagent-domains:api-documenter` |
| PostgreSQL schema, migrations, queries, HA PostgreSQL, backup and restore | `voltagent-data-ai:postgres-pro`, `voltagent-data-ai:database-optimizer`, `voltagent-infra:database-administrator` |
| Valkey with Sentinel, failover behavior, operations | `voltagent-infra:sre-engineer`, `voltagent-lang:golang-pro` |
| Angular UI | `voltagent-lang:angular-architect`, `voltagent-lang:typescript-pro`, `voltagent-core-dev:frontend-developer`, `voltagent-core-dev:ui-designer` |
| UI quality | `voltagent-qa-sec:accessibility-tester`, `voltagent-qa-sec:ui-ux-tester` |
| Helm chart, Kubernetes, local stack | `voltagent-infra:kubernetes-specialist`, `voltagent-infra:platform-engineer` |
| Container image | `voltagent-infra:docker-expert` |
| CI pipeline, release pipeline, task runner, builds | `voltagent-infra:deployment-engineer`, `voltagent-infra:devops-engineer`, `voltagent-dev-exp:build-engineer`, `voltagent-dev-exp:tooling-engineer` |
| Dependencies and tool pins | `voltagent-dev-exp:dependency-manager` |
| Test strategy, integration and e2e frameworks, browser tests | `voltagent-qa-sec:test-automator`, `voltagent-qa-sec:qa-expert` |
| npm protocol and npm, pnpm, Yarn clients | `voltagent-lang:node-specialist`, `voltagent-lang:javascript-pro` |
| NuGet protocol and .NET clients | `voltagent-lang:dotnet-core-expert`, `voltagent-lang:csharp-developer` |
| OIDC login, tokens, permissions, GitLab CI OIDC | `voltagent-infra:security-engineer`, `voltagent-core-dev:backend-developer` |
| Drains, failovers, chaos tests | `voltagent-qa-sec:chaos-engineer`, `voltagent-infra:sre-engineer` |
| Load and performance | `voltagent-qa-sec:performance-engineer` |
| Later formats | `voltagent-lang:python-pro` (PyPI), `voltagent-infra:docker-expert` (OCI and Docker), `voltagent-lang:java-architect` (Maven, Gradle), `voltagent-lang:golang-pro` (Go modules), `voltagent-infra:kubernetes-specialist` (Helm charts), `voltagent-infra:devops-engineer` (Ansible Galaxy) |
| Reviews | `voltagent-qa-sec:code-reviewer` (every phase), `voltagent-qa-sec:architect-reviewer` (contracts and structure), `voltagent-qa-sec:security-auditor` and `voltagent-qa-sec:penetration-tester` (anything that authenticates, authorizes, parses untrusted input or faces the network) |
| Failing tests and pipelines | `voltagent-qa-sec:debugger`, `voltagent-qa-sec:error-detective` |
| Documentation | `voltagent-dev-exp:documentation-engineer`, `voltagent-biz:technical-writer` |

Rules while building:

- **Tests for every registry.** Each registry needs integration tests (protocol over HTTP against
  PostgreSQL, Valkey with Sentinel and the S3 store in containers, plus the conformance kit) and
  e2e tests (its real clients against trove deployed by the Helm chart with several `web`
  replicas and random distribution, through local, remote and virtual repositories). Other code
  gets unit tests and, where it crosses PostgreSQL, Valkey or S3, integration tests.
- **Integrate continuously.** After each work package: review the diff yourself, run the task
  runner's lint, unit and integration targets, then commit one coherent change with a message
  in imperative English that names the requirement IDs, and set its checklist items to 🟢. Push
  whenever the local checks are green.
- **Design questions.** Decide them yourself. Record each one as a decision block in the
  `## Decisions` section of the owning spec page (status line "Decided by the development agent —
  selected from the options below; the owner reviews it at the walkthrough.", options table,
  choice, rationale with a revisit condition, applied edits) and add an ADR for significant ones.
  A UI question that really needs a visual reference becomes a note for the walkthrough, not a
  blocker.
- **Review before done.** Run `voltagent-qa-sec:code-reviewer` over the phase's diff, plus the
  security and architecture reviewers where the table says so. Have the fitting specialist fix
  every finding, or record why it does not apply.

## 4 · Definition of done

The phase is done only when all of the following hold:

- Every checklist item of the phase is 🟢, and every exit criterion of the roadmap section passes.
  For each exit criterion note the evidence (test name, CI job, command and output).
- Unit, integration and the per-change e2e legs are green locally and in `Test and Release`. The long legs
  (drain and failover, the npm load under faults, the soak and the performance bounds) run in
  `HA and Performance (nightly)` every night and on dispatch, not per change (owner, 2026-11-01): a phase whose
  exit criteria are long legs dispatches that workflow on its closing commit
  (`gh workflow run "HA and Performance (nightly)" --ref main`) and notes the run as evidence; any other red
  long leg becomes an open finding in [section 8](#8--open-findings) and does not hold the release.
- Documentation is current: README (naming tables; the TL;DR once trove is installable),
  docs/developer, docs/security (a page per mechanism that now exists, open gaps as `H-…` items),
  spec deviations recorded, coverage numbers regenerated.
- The white-label scan prints nothing, and `git log <previous tag>..HEAD --format=%B` contains no
  attribution trailer and no mention of an AI.
- BETA.md or FULL.md shows the phase row at 🟢 with its version, the items at 🟢, and today's date
  under **Last updated**; the [roadmap](../coverage/roadmap.md) is consistent with what was built.

## 5 · Push and get the pipeline green

1. `git push origin main`.
2. Wait for every workflow run the push triggered: `gh run list --branch main --limit 10`, then
   `gh run watch <run-id> --exit-status` for each.
3. On failure: `gh run view <run-id> --log-failed`, let `voltagent-qa-sec:debugger` or the fitting
   specialist find the cause, fix it, commit, push, and wait again. Repeat until every run is
   green.

## 6 · Release

The pipeline releases; the run does not tag, create releases or write notes by hand. Semantic Release
cuts the release from the commits on `main` ([release flow](../developer/releasing.md#release-flow)),
and `Release Docker & Helm` publishes the image `guidedtraffic/trove` and the chart.

1. Commit the phase's work with conventional commit messages: `feat:` for what the phase adds, `fix:`
   for fixes, `docs:` or `chore:` for the plan and the rest. Commit the final plan update (phase row 🟢,
   Version column still open) with them.
2. Push, and wait until `Test and Release` is green on that commit. Semantic Release has then cut the
   release of the push, if the commits held a `feat` or a `fix`, and `Release Docker & Helm` has run for
   it: `gh run list --workflow "Release Docker & Helm" --limit 3`, then `gh run watch <run-id> --exit-status`.
3. Record the release that closes the phase: `git pull --rebase origin main` (the release commit moves
   `main` with the coverage badge), then the newest release (`gh release list --limit 3`) into the
   **Version** column of the phase's row in BETA.md. Commit it as `docs(plan): record vX.Y.Z as the
   release of phase N` and push. That commit cuts no release.
4. Check the published artifacts of the release: `gh release view vX.Y.Z`, the image
   `guidedtraffic/trove:X.Y.Z` on Docker Hub, and the chart index
   `https://guided-traffic.github.io/trove/index.yaml`. The release notes are generated from the commit
   messages; they name the requirement IDs, so the commits carry them (rule 4, the commit messages).

## 7 · Report to the owner

In German, short: the phase and its version; what was built; how it was verified (CI run links,
test counts, exit criteria with their evidence); the decisions taken (their IDs); deviations and
open items; the next phase.

If the run cannot finish the phase (for example because the context runs out), leave it
resumable: commit and push the finished, green work packages, keep the phase and its open items at
🟡 in the plan, and report precisely what is done and what is left. The next run picks it up.

## 8 · Open findings

Defects and gaps found outside a phase, waiting for a run (step 2, item 5). Each entry names where it
is, what was seen and how far it is confirmed: **confirmed** means checked in the code by the
development agent, **read** means found by reading the code and not reproduced. Add new findings
here; remove a finding in the commit that fixes it.

Found on 2026-11-02 by the code review and the security audit of phase 4 (NuGet), at commit 42f7a1d. A fix wave was
started and stopped before it changed the tree; phase 4 stays open until these are fixed (BETA.md):

### NuGet

- **F-20 · The forwarded search of a remote is not built** (confirmed). R-NUGET-016 is MVP and in M3's scope (WP3.2), and
  Q-NUGET-55 left it to "the package that forwards". A remote searches only what it has cached, and `searchRemote` in
  [search_virtual.go](../../internal/formats/nuget/search_virtual.go) returns nothing, so the search of `nuget-all` lists no
  package of nuget.org. Also: `Classify("v3/search")` names the package `v3`, so a populate check on a forwarded search would
  be decided for that package. Done when R-NUGET-016's and R-NUGET-018's criteria pass, the two-replica and Valkey-stopped
  timing case in e2e.
- **F-21 · NuGet search: hidden versions, the addressed repository, the fan-out and the patterns** (confirmed in the code).
  Search terms match versions the caller cannot read ([H-341](../security/endpoints-and-exposure.md#h-341--a-nuget-search-term-matches-versions-the-caller-cannot-read));
  the merged search decides exposure and the scope lift on the members instead of the addressed virtual repository
  ([H-342](../security/endpoints-and-exposure.md#h-342--a-nuget-search-through-a-virtual-repository-decides-exposure-on-its-members)),
  which breaks E3.7 when `nuget-customers` is virtual; the fan-out over members has no concurrency bound and a scan budget per
  member ([H-335](../security/permissions.md#h-335--a-nuget-search-by-a-caller-with-row-level-grants-scans-up-to-200000-matches-per-request));
  the search of a local repository ignores its patterns ([H-344](../security/repository-model.md#h-344--the-nuget-search-of-a-local-repository-ignores-its-include-and-exclude-patterns)).
  Tests to add: E3.7 with a virtual `nuget-customers`, a token scoped to the virtual key, PostgreSQL cases where a `version:`
  and a `description:` term match only a hidden version.
- **F-22 · A virtual repository with a nuget.org member answers 502 on registration documents** (confirmed by a probe test of
  the code review). `merge.DecodeUpstream` and `decodeFlat` ([merge/](../../internal/formats/nuget/merge)) do not inflate the
  gzip bytes a remote stores unchanged, and refuse an index whose pages are listed by URL (more than 128 versions), although
  Q-NUGET-56 wants every page inlined for the merge (Q-NUGET-67 hands over the raw index). Every nuget.org ID through
  `nuget-all` fails; the stub only gzips when asked and its IDs have two versions, so
  `TestR_NUGET_018_AVirtualRepositoryMergesALocalAndARemoteMember` passes, and
  `TestR_NUGET_017_AnIndexWithPagesByUrlIsAnUpstreamFailureNotAShortList` pins the defect. With it (read): the merge counts a
  version as cached from any cached file, not only its `.nupkg` (`formathost/virtual.go` `CachedVersion`); the merge drops parts
  of a remote entry (reuse `remote.Convert`); `MergeFingerprint` covers only the index digest, so a changed page under an unchanged
  index keeps the old ETag.
- **F-23 · Local NuGet documents** (read, except the first). `is_semver2` ignores a SemVer-2-specific bound of a dependency range
  for pushed and cached packages (`publish.go` `versionInput`, `remote_fill.go`; confirmed by a probe test), so such a version
  shows to clients older than 4.3. On an endpoint with rules the ETag changes with hidden versions and the virtual ETag lacks the
  ID ([H-343](../security/endpoints-and-exposure.md#h-343--on-an-endpoint-with-rules-a-nuget-documents-etag-changes-with-hidden-versions)).
  A push of a new version and a hard delete of another version of the same ID take the folder and the package row in opposite
  order (40P01, resolved by the retry). The local flat index renders with revision 0 (`handlers_flat.go`).
- **F-24 · Upstream addresses in `deprecation` and `vulnerabilities`** (read). The remote and the merge copy them verbatim,
  with `advisoryUrl` and any upstream `@id`; [nuget.md](../spec/formats/nuget.md) R-NUGET-015 says both "passed through" and
  "every URL except projectUrl, licenseUrl and iconUrl is trove's". Decide, write back, and add a vulnerability and a deprecation to
  the stub's corpus; the run against nuget.org (F-27) would see it on `newtonsoft.json` below 13.0.1.
- **F-25 · The cache fill of a remote** (confirmed in the code). Every refusal of the archive reader skips the identity check
  ([H-338](../security/nuget-remote.md#h-338--a-package-the-archive-reader-refuses-is-cached-without-its-identity-check)); an index
  may list any number of pages ([H-339](../security/nuget-remote.md#h-339--one-registration-index-of-an-upstream-can-list-any-number-of-pages));
  the identity check folds Unicode case while the rows keep the declared ID
  ([H-340](../security/nuget-remote.md#h-340--the-identity-check-folds-unicode-case-the-rows-keep-the-declared-spelling)). And (seen in
  e2e): the first answer to a cache miss on a remote file has no `ETag`, `Accept-Ranges` or `Cache-Control`, a stored answer has them.
- **F-26 · The package reader may read another central directory than `archive/zip`** (read, not checked against Go 1.27.3).
  `nupkg/scan.go` takes the end record whose comment ends at the end of the file, `archive/zip` the last signature; the entries
  are compared by name, flags, method and sizes, not by data offset. Names and sizes must still match, so the limits hold.
- **F-27 · The run against nuget.org of E3.1 has no test** (confirmed). npm has a read-only subset against the public
  registry in `TestNPMClosing`; NuGet's closing set has none. Planned: a remote of `https://api.nuget.org/v3/index.json` and a
  virtual repository with it, `dotnet restore` of `Newtonsoft.Json` and of an ID with more than 128 versions, nuget.exe through the
  remote, the URLs of the registration, `dotnet package search` (needs F-20, and F-22 for the virtual registration).
- **F-28 · `TestE1_11Scatter` of `access-long` meets the write budget** (confirmed, 2 of 5 runs of R-API-014 and R-API-052 alone on
  kind at 42f7a1d). The class `api_write` of R-API-016 is a sliding window of 50 writes per 5 s per token; one token carries both
  subtests, 18 and 41 writes in about 5 s, and the pacing comment at `e1_11_scatter_test.go:253` still describes the token bucket
  that Q-API-3 replaced. CI has not hit it (R-API-052 takes 4.8 to 6.7 s there). Fix in the test: a token per subtest, and a guard
  that reads the class's `RateLimit-Policy` and fails a write that would exceed it.
- **F-29 · `Discoverer` has no kit check** (confirmed). R-ARCH-025 wants a kit check with every new behaviour; the engine caps a
  discovery document's size and NuGet's own tests cover its parser. A crafted-document fixture needs its own design.

Found on 2026-11-01 while writing the manual (`docs/manual`) and the design change of Q-UI-88:

### Tests and tools

- **F-19 · The failover set is red on the CI runners** (read, from the artifacts of run 41332313390, job `e2e on kind
  (failover)`). `pg-kill.sh` took 296 s to recover; the CloudNativePG operator failed its readiness and liveness probes for
  about 6 minutes, restarted three times and refused the webhook call of `pg-stop.sh`, so the later subtests ran without a
  database. A `web` pod and both `worker` pods exited 1 and restarted once (no OOM; cause not established). The same kind of
  CPU starvation as the npm-ha diagnosis in BETA.md phase 7. Since 2026-11-01 the set runs in `HA and Performance
  (nightly)`. Done when the set is green there, or the exits of the trove pods are explained and fixed and the rest is shown
  to be the runner's.

- **F-17 · Leftovers of old tool versions on the owner's machine that need an admin password** (confirmed).
  On 2026-11-01 helm (4.3.1), kubectl (1.37.2), docker buildx (0.38.1), kind (0.33.0) and Node.js (26.13.0)
  were brought to their pins through Homebrew, and `make versions-check` prints `versions ok`. Left: two
  shadowed copies from Docker Desktop's time in `/usr/local/bin`, `kubectl` (v1.32.2) and `kind` (v0.20.0),
  owned by root (`sudo rm /usr/local/bin/kubectl /usr/local/bin/kind`), and the Command Line Tools 26.6,
  which Homebrew reports as too old for Xcode 27
  (`sudo softwareupdate --install "Command Line Tools for Xcode 27.2-27.2"`, no restart).
  Since Docker Desktop's restart of 2026-11-02 (after the disk ran full) `~/.docker/cli-plugins/docker-buildx` points at
  Docker Desktop's own buildx again (v0.28.0-desktop.1, pin v0.38.1), so `make versions-check` fails locally; the Homebrew
  copy is in place (`ln -sf /opt/homebrew/bin/docker-buildx ~/.docker/cli-plugins/docker-buildx`, no admin password; Docker
  Desktop may relink it on its next update unless its CLI plugin management is turned off).
  Machine-level: the owner runs these; a run does not change the machine.
