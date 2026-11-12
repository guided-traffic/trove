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
5. **One version per tool.** Go 1.27.4, Angular 22, every tool pinned once and read from that pin
   by local builds and CI (see the global rule in your instructions).
6. **Documentation in the same change.** Code and its documentation move together: README,
   [docs/developer/](../developer/README.md), [docs/security/](../security/README.md), spec,
   project plans. English for code, comments, commits and docs. German for messages to the owner.
7. **No shortcuts to green.** Never skip, disable or weaken a test or a check to make the pipeline
   pass, never use `--no-verify`. A red job of a CI run may be started again up to three times to reach a green run
   (owner, 2026-11-09: `gh run rerun <run-id> --failed`); every re-run and its result is noted, and the red runs of a job
   that turned green on a re-run are recorded as a finding in [section 8](#8--open-findings) with what failed. A job still red
   after its third re-run is fixed at its cause. Local test runs are not repeated until they pass: fix the cause.
   The one exception is the owner's: the compatibility checks are switched off until the first stable
   release ([Q-ARCH-340](../spec/architecture.md#q-arch-340--no-backward-compatibility-until-the-first-stable-release),
   the checklist in [releasing.md](../developer/releasing.md)); they stay off, and no other check joins them.
8. **No force push, no history rewrite.**
9. **No backward compatibility until the first stable release** (owner, 2026-11-06; `CLAUDE.md` at the repository root).
   trove has no users and no installations: change APIs, URLs, configuration, schema and stored formats the way that
   is technically best, with no deprecations, aliases, fallbacks or migration paths; a changed migration needs a fresh
   database, which is acceptable until then.
10. **ALPHA working mode: features first, no review rounds, findings only when they block (owner, 2026-11-08 evening).**
   - **Features in parallel, at most four builders.** Each builder owns a whole feature end to end (for example OCI remote with its
     mirror, OCI virtual, APT local, APT remote), not a slice of 14 small work packages that share files. More than four builders
     overload the host (load 80 to 160 on 20 cores on 2026-11-08), and the overload turned tests into false failures.
   - **No review subagents during ALPHA.** No code reviewer, security auditor, penetration tester, architecture reviewer or
     re-check round after a builder. A builder's work lands once its own gates are green: the unit tests of the touched packages,
     the integration areas it touches (one run each, through the slot wrapper), lint, `make cyclo`, `make generate-check`, the
     coverage check and the white-label scan. Security is checked in one pass before BETA, not per package.
   - **Findings: only blockers at once.** A finding is fixed in the run only when it blocks: `main`'s CI is red, a security hole is
     live and exploitable, or data can be lost. Every other finding gets one line in [section 8](#8--open-findings) and is fixed in
     one batch at the end of the run, after the features. A test that fails only under host load gets no agent of its own.
   - **No repeated race or stress runs.** No `-count=N` loops, no repeated `-race` runs in the Linux image, as a gate or as a proof.
     One normal run of the touched tests is enough.
   - **Never wait.** A run pushes, watches the CI runs per job in the background, fixes a red job at once and keeps building. No
     criterion waits for N green runs in a row.
   - **Why:** the run of 2026-11-08 spent hours in three or four review rounds per work package and per finding, recorded 28 new
     findings (most of them false failures under its own load), and delivered only the local OCI class: OCI remote stayed in
     review, OCI virtual and the mirror and APT were not started ([section 11](#11--run-record)).
11. **A UI change is rendered and looked at before it lands (owner, 2026-11-12).** The builder renders every screen it changed with
   `make ui-renders` (light and dark, wide and narrow, with data that has short and long names, several rows and an empty state),
   opens the pictures itself and judges them against the checklist of [ui-style-guide.md](../developer/ui-style-guide.md#checklist)
   before it commits; what it found and fixed goes into its report with the paths of the pictures. A unit test or a browser test that
   checks a value is not a look at the screen.
   - **Why:** T-20's address fields passed every test and shipped with a different width per row and a list that scrolled sideways;
     the owner found it in the running installation, not the run.

## 1 · Find the next phase

1. `git status` must be clean; then `git pull --rebase origin main`.
2. During the alpha (rule 10) a run works on two fronts at once: the first open task of
   [section 10](#10--owner-tasks-of-2026-11-07-the-next-registries), cut into work packages with an agent and a branch each, and
   every open finding of section 8 with an agent each. Open there now: the owner task of 2026-11-11 **T-18 (visibility levels)** and, beside it, the owner task of 2026-11-12 **T-21 (a UI that looks right)**; both come before the next unit of item 3, and **T-22 (a search with suggestions)** follows them. The open items of BETA.md phase 7 are
   findings of the same kind. Outside the alpha: open [BETA.md](BETA.md), table
   **Where we are**; the current phase is the first row whose status is not 🟢, and a row at 🟡 is resumed (step 2, item 4).
3. If every row of BETA.md is 🟢, open [FULL.md](FULL.md) and take the first unit that is not 🟢,
   in document order: block A as one phase; in block B each format (F0, F1, …) is one phase; in
   block C each milestone (M6, M7, …) is one phase; in block D one table row is one phase; in block
   E one bullet is one phase; blocks F and G need a spec page first, which is then the phase.
4. Release numbers are not planned and not recorded: the pipeline releases (step 6), and BETA.md and FULL.md
   track only what is built (owner, 2026-11-02).
5. If a job that gates Semantic Release is red on `main` (`gh run list --workflow "Test and Release" --branch main --limit 5`; until the first stable release the
   `integration` and `e2e` legs do not gate it, [releasing.md](../developer/releasing.md#until-the-first-stable-release), and a red one is still worked on),
   repairing it is the first task of this run, done by an agent beside the feature work, not instead of it. A red leg of `HA and Performance (nightly)`
   (`gh run list --workflow "HA and Performance (nightly)" --limit 3`) is not: it becomes an open finding in
   [section 8](#8--open-findings), unless the phase's own exit criteria are that leg.
6. Tell the owner in two or three German sentences which phase and what it delivers, then continue
   without waiting.

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
5. Open findings ([section 8](#8--open-findings)) and the tasks of
   [section 9](#9--owner-review-of-2026-11-06-tasks-and-open-questions): a blocking finding (rule 10) is fixed at once; the others are
   fixed in one batch at the end of the run, after the features, never queued in front of them and never left out. A finding the
   run cannot fix keeps its line in section 8 with what was found (cause, evidence, the next step), so no finding is forgotten. The registries of
   [section 10](#10--owner-tasks-of-2026-11-07-the-next-registries) are the next phases, ahead of the next unit of FULL.md. Before fixing one, reproduce it (a failing test first); a
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
| Reviews (not during ALPHA, rule 10) | `voltagent-qa-sec:code-reviewer` (every phase), `voltagent-qa-sec:architect-reviewer` (contracts and structure), `voltagent-qa-sec:security-auditor` and `voltagent-qa-sec:penetration-tester` (anything that authenticates, authorizes, parses untrusted input or faces the network) |
| Failing tests and pipelines | `voltagent-qa-sec:debugger`, `voltagent-qa-sec:error-detective` |
| Documentation | `voltagent-dev-exp:documentation-engineer`, `voltagent-biz:technical-writer` |

Rules while building:

- **Tests for every registry.** Each registry needs integration tests (protocol over HTTP against
  PostgreSQL, Valkey with Sentinel and the S3 store in containers, plus the conformance kit) and
  e2e tests (its real clients against trove deployed by the Helm chart with several `web`
  replicas and random distribution, through local, remote and virtual repositories). Other code
  gets unit tests and, where it crosses PostgreSQL, Valkey or S3, integration tests.
- **Integrate continuously.** A builder returns one commit on top of the current `main`, with a
  release-notes message (step 6, item 1); the development agent runs the touched gates once,
  fast-forwards or cherry-picks it, and pushes once per landed feature. Set its checklist items
  to 🟢.
- **Design questions.** Decide them yourself. Record each one as a decision block in the
  `## Decisions` section of the owning spec page (status line "Decided by the development agent —
  selected from the options below; the owner reviews it at the walkthrough.", options table,
  choice, rationale with a revisit condition, applied edits) and add an ADR for significant ones.
  A UI question that really needs a visual reference becomes a note for the walkthrough, not a
  blocker.
- **Review before done.** Not during ALPHA (rule 10). After ALPHA: run `voltagent-qa-sec:code-reviewer`
  over the phase's diff, plus the security and architecture reviewers where the table says so.

## 4 · Definition of done

The phase is done only when all of the following hold:

- Every checklist item of the phase is 🟢, and every exit criterion of the roadmap section passes.
  For each exit criterion note the evidence (test name, CI job, command and output).
- Unit and the other jobs that gate Semantic Release are green in `Test and Release`; integration and e2e run locally for what the phase touches and do not hold the
  release until the first stable release (owner, 2026-11-10, Q-ARCH-457). The long legs
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
- BETA.md or FULL.md shows the phase row at 🟢, the items at 🟢, and today's date
  under **Last updated**; the [roadmap](../coverage/roadmap.md) is consistent with what was built.

## 5 · Push and get the pipeline green

1. `git push origin main`.
2. Watch every workflow run the push triggered in the background, job by job (`gh api repos/guided-traffic/trove/actions/runs/<id>/jobs`
   every 60 s), and keep building meanwhile; never sit idle on a run (rule 10).
3. On failure: `gh run view <run-id> --log-failed` (or the job's log while the run goes on). A failure the change caused is
   fixed at once: find the cause, fix it, commit, push, and watch again. A failure the change did not cause (an outage, a row
   that fails under the runner's load) may be started again up to three times with `gh run rerun <run-id> --failed` (rule 7);
   the re-runs are noted and the red runs recorded as a finding. Repeat until the jobs that gate Semantic Release are green; a red `integration` or `e2e` leg does not hold the release and is still worked on until its cause is fixed (owner, 2026-11-10, Q-ARCH-457).

## 6 · Release

The pipeline releases; the run does not tag, create releases or write notes by hand. Semantic Release
cuts the release from the commits on `main` ([release flow](../developer/releasing.md#release-flow)),
and `Release Docker & Helm` publishes the image `guidedtraffic/trove` and the chart.

1. Commit the phase's work with conventional commit messages: `feat:` for what the phase adds, `fix:`
   for fixes, `docs:` or `chore:` for the plan and the rest. Commit the final plan update (phase row 🟢)
   with them. **The subject is the line of the release notes (owner, 2026-11-08):** a plain-language sentence
   that tells a user what is new or fixed and what it does, never a list of IDs. The body explains it in two
   or three sentences and names the requirement, decision and finding IDs on its last line. A subject such as
   "the registry root serves /v2/ through one enforcement point (R-OCI-003, …, T-16 WP3)" is what the owner
   called useless: nobody can tell from it what changed.
2. Push, and wait until the jobs that gate Semantic Release are green on that commit; until the first stable release e2e and integration do not gate it and are run locally as needed (owner, 2026-11-10, Q-ARCH-457). Semantic Release has then cut the
   release of the push, if the commits held a `feat` or a `fix`, and `Release Docker & Helm` has run for
   it: `gh run list --workflow "Release Docker & Helm" --limit 3`, then `gh run watch <run-id> --exit-status`.
   Push nothing to `main` while that run is going, not even a `[skip ci]` commit: Semantic Release publishes only when the
   run's commit is still the tip of `main` ("The local branch main is behind the remote one, therefore a new version won't be
   published", run 42410791254 on 2026-11-07), and a manual dispatch does not release (the job runs on `push` only).
3. `git pull --rebase origin main` (the release commit moves `main` with the coverage badge). The release
   number goes into no plan: BETA.md and FULL.md track features only (owner, 2026-11-02).
4. Check the published artifacts of the release: `gh release view vX.Y.Z`, the image
   `guidedtraffic/trove:X.Y.Z` on Docker Hub, and the chart index
   `https://guided-traffic.github.io/trove/index.yaml`. The release notes are generated from the commit
   subjects (item 1); the IDs stay in the commit bodies.

## 7 · Report to the owner

In German, short: the phase; what was built; how it was verified (CI run links,
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

Found on 2026-11-08 during the run of T-16, by the penetration test of the F-82 fix and the code reviews of WP4b and WP12:

### Tests and tools

- **F-123 · `TestR_DEB_035_LargeIndex` timed out once in the unit tests** (Test and Release 42582225034, 2026-11-09, job unit tests;
  the run was cancelled by a later push before a re-run): the test's client gave up after its 30 s timeout while it waited for the
  headers of `Packages.gz` of the stub's `bulk` suite (test/debstub/stub_test.go:275), under `-race` and coverage on the self-hosted
  runner. Green in every other run of the day. Read, not reproduced: the stub builds the large index on the first request. Done when a
  recurrence shows whether the build or the runner took the time; next step: log the stub's build time of the index with a failure.
- **F-121 · The nightly performance run misses V-7b on a loaded runner** (runs 42571414393 with p99 314.7 ms and 42595813923 with
  610.957 ms against 300 ms; V-7a 17 to 60 ms in the same runs; both shared the nodes with other kind legs). Not reproduced as a
  regression, looked at on 2026-11-09: the path is the one [Q-VULN-81](../spec/vulnerability-scanning.md) decided and no commit since
  2026-11-06 changed the findings list, its queries or the report scope (the authz commits 08eef8ba and 8f192491 touch the
  version-independence test of `ReadScope`, which a package-pattern grant does not trip: the page still runs the pair read and the
  page statement). On the integration stack at a load average of about 10, `TestR_VULN_028_TheFirstPageOfAPatternGrantAtTheShapeOfTheRun`
  with the run's 100 requests, the first included, gives first request 60 ms, median 67 ms, p99 76 ms, slowest 76 ms; without the
  `ANALYZE` of the seed 74, 76, 102 and 118 ms. Every request runs exactly two statements (`VulnScopePackages`, `VulnFindingsList`).
  In the log of 42595813923 the 100 requests of V-7b took 9.3 s in all, a mean of 93 ms: the typical request is where it was
  (112 ms on 2026-11-06), the p99 of 100 is the second slowest request, so two stalls on a shared, CPU-limited VM are enough to
  miss. The test now asserts the two statements, so a regression of the path fails before a runner does. Open: the cause of the two
  slow requests on the runner is not measured (a neighbour, the replica's garbage collection, or the autovacuum of the 100,000 rows
  the run inserts without an `ANALYZE`). Next step: a run of `vuln-perf` alone on the large class (after the kind legs, as
  [Q-ARCH-325](../spec/architecture.md) set) that logs the slowest five requests with their request IDs; revisit if V-7b misses
  there as well, which would make it the reference environment's number ([H-491](../security/vulnerability-scanning.md#h-491--the-performance-run-is-not-the-reference-environment)).
- **F-116 · `TestBastion_R_BAS_049_RoutingByPath` failed once on CI** (Test and Release 42530616876, e2e-bastion): "0 hits for 36
  paths requested twice"; each repeated tarball request went to the main installation as a revalidation (304) instead of being a hit
  on the Bastion replica of its path. Green in the runs before and after with no Bastion change in between. Done when the cause of
  the revalidations is known. Read on 2026-11-09, two candidates in `engine.fresh` (`internal/bastion/engine/entries.go`), not
  settled: (a) the e2e Bastion keeps an artifact for `artifact_revalidate_after: 90s` (`deploy/dev/bastion-chart-common.yaml`), and
  the test relies on the tarballs the scenario before it fetched, whose last runs refresh the npm tarballs and then run the other
  clients; past 90 s every npm tarball is stale, which explains the first round of revalidations but not the second, since a
  304 sets `validatedAt` anew; (b) a content entry of the feed that names the npm repository and arrived less than 15 s before
  makes every entry unusable, also one revalidated a moment ago, so all 72 requests revalidate while they run inside those 15 s.
  Done on 2026-11-09 (`go vet -tags e2e` only, no kind run): the test now requests every path once before it reads `before`, which
  removes (a), and a failure logs, per replica, the content entries of the feed applied since that warm-up and the feed lag and
  resets (the Bastion exposes the count of applied entries by kind and no per-entry record, so this tells whether a content
  entry arrived during the measurement and not which repository it named). Open: (b) is not ruled out and the fix is not run
  on kind. Next step: the next recurrence names the cause with those lines; if it is (b), the Bastion needs a per-entry record
  (a debug log of the applied content entries with their repositories) before the test can wait for a quiet feed.
- **F-117 · The self-hosted runners lost the Ubuntu archive for a few minutes** (Test and Release 42553790224, 2026-11-09 03:02):
  every job that installs `build-essential` at that moment (GoSec, cyclomatic complexity, both builds, the image, the source malware
  scan, Trivy) failed in `apt-get` with "Could not connect to archive.ubuntu.com:80" over IPv6 and IPv4; jobs that had installed
  earlier passed. An outage outside the repository (at 03:10 UTC `archive.ubuntu.com` did not answer from the owner's network either,
  `security.ubuntu.com` did); it makes every such job depend on the archive. Done when the runner image carries
  `make` and the C compiler (the owner's runner) or the jobs keep a local package cache. Again on 2026-11-11 from 14:25 (Test and Release 43037911704): `azure.archive.ubuntu.com`
  did not resolve for about three minutes on the GitHub-hosted runners (the self-hosted ones were not affected), and `e2e (nuget)` and `e2e (ci)`
  failed in their `apt-get install build-essential`; again at 15:05 for `e2e (nuget)` in 43044075096 (re-run). `e2e (vuln)` and `e2e (bastion)` on
  the same kind of runner passed in both runs; `e2e (nuget)` and `e2e (ci)` stayed red after the first re-run. Since then the jobs call
  [build-tools](../../.github/actions/build-tools/action.yml), which installs only what the runner lacks, so the GitHub-hosted runners no longer touch
  the archive; the self-hosted image still lacks `make` and the compiler, so this finding stays open for it. The outage went on past 15:50 (Test and
  Release 43052141784): the step passed, and `e2e (nuget)` and `e2e (ci)` then failed in building the NuGet clients image, whose `RUN apt-get
  install mono-complete` could not resolve `archive.ubuntu.com` while `dist.nuget.org` resolved in the same build. Those two legs depend on the archive
  through that image; a prebuilt clients image in a registry would remove it.
- **F-119 · The transfer loop of the drain tests saw 429 and 502 when the cluster stalled** (Test and Release 42561072166, 2026-11-09, and
  the nightly dispatch 42578594755 of the set `drain-roles` alone on the large class). Cause found and fixed for the transfer loop (Q-HA-230):
  the cluster stalled for about 40 s (07:50:08 to 07:50:50), the open-loop `transfer.js` released its piled-up iterations in one burst of
  36 PUTs in a second, and the write class (50 per 5 s for one principal) refused 16 of them; 7 of the 16 reached the client as `502`
  with the edge's flag `SH`. Not the fallback of a fresh replica (the policy header shows the shared counters, Valkey was available
  before the replicas turned ready) and not the drain (the refusals came five minutes after the last eviction). Open: (1) what stalled the
  whole cluster at 07:50:08 (every `npm ci` took 40 s, the edge's connect time to the pods reached its 5 s bound, the `packages` pods
  served a third of their rate), with no pod event, restart or failover in that minute; the load of the runner was not measured;
  (2) is closed ([Q-ARCH-446](../spec/architecture.md#q-arch-446--a-response-to-a-request-whose-body-is-unread-is-sent-first-then-the-body-is-read-and-discarded-within-a-bound): reproduced behind HAProxy 3.0, the pod's close with an unread body sent an RST that destroyed the answer, fixed in the listener); (3) the 13 of 11,529 requests answered 502 in the
  certificate rotation row of the run 42561072166, which the transfer loop does not explain, and the two later runs of the set `push`
  (42563355283: 10 of 3,245 through the scatter edge with a slowest request of 15 s). Looked at on 2026-11-09 in the recurrence
  42618038153 (job `e2e on kind (push)`, 13 of 9,476): **the reload is not the cause.** The 502s are GETs of `/system/ping` and
  `/system/replicas` that the edge logged with flag `SH` and connect time 1.4 to 2.8 s, at 12:54:37; the first pod reload is at
  12:55:12 (`TLS material reloaded`, trove_tls_reloads_total 0 to 2 per pod), 35 s later, and the test's own log dates the 502s
  at 12:54:20 to 12:54:23, the first seconds of its open-loop load of 100 requests a second (`startK6`, `load.js`), which piled up to about 1,000 connections at the edge (k6: p99 13 s, slowest request 19.4 s, avg 1 s for a ping).
  The pods logged `http: TLS handshake error ... i/o timeout` for 8 connections from the two edge pods at 12:54:34 to 12:54:36,
  which is the 10 s `read_header_timeout` expiring on a handshake that did not finish: the pod was starved of CPU by the ramp
  (valkey circuit open at 12:54:27, pings of 1.6 to 3.5 s), the listener gave up on the handshake, the edge saw the abort before
  any header. Reproduced no 502 with the real listener behind HAProxy 3.0 under 1,000 and 2,500 closed-loop GET clients on 1 and 0.3 CPU
  (13,164 and 12,896 answers, all 200), so the starvation of the kind node is the precondition and was not rebuilt. Nothing in a
  reload closes a connection: `ListenerTLS` swaps the certificate pair through `GetConfigForClient` and keeps the listener and
  every established connection. Same family as (1) and (4): the rotation row is run with the load that stalls the cluster. Next
  step: measure what starves the pods at the ramp (the row's own k6 on the same node, and whether the open loop piles up as the transfer loop did) before the product is touched; the rotation row runs `load.js` as a closed loop since Q-HA-288, so the pile-up is gone there.
  (4) the p99 conditions of `TestDrainRoles` (R-HA-059, Q-HA-94) failed in two nightly dispatches of the branch of the fix, which had other large legs
  running at the same time (documents 1.64 s against a 755 ms warm-up; then 5.25 s against 840 ms and all requests 13.8 s against 1.7 s), while the transfer loop
  had 0 failures in both. Revisit when a run shows the stall again. The evidence is now recorded in `.out/e2e/evidence/` of every e2e artifact, green or red ([e2e.md](../developer/e2e.md#reading-a-stall)); read it at the next occurrence.
- **F-105 · `TestR_NPM_007_ASlowPublishOf200MiB` fails under load** (seen twice: by the F-83 builder, an S3 UploadPart
  cancellation followed by the package timeout, and by the F-101 builder, "1 connections held a transaction older than 1 s
  while the body arrived"; passes alone). The test asserts that no database transaction stays open longer than 1 s while
  the body of a slow publish arrives. Done when the cause is found: a transaction the publish really holds during the
  body (a product defect), or an unrelated transaction of the shared stack the test counts (a test defect).
  Read on 2026-11-09: the publish holds none while the body arrives (`handlePublish` decides and pre-checks ownership before the
  body, `publish.Scan` streams the attachment through `Ingest().Spool` into the local spool file with no database handle, and the
  first statement after the body is `VersionFacts`); the watch counts every connection of the stack's database, so the candidates
  are a background transaction of the replica slowed past 1 s on the loaded host, or the commit's transaction counted because
  `bodySent` closes only after the client goroutine returns from its last write. The test now names the transactions it counted;
  next step: read that name in a recurrence and narrow the watch to it, or measure the request's own transactions instead.
- **F-102 · `TestR_BAS_014_ThousandWritesToOnePathCauseOneProbe` failed once under host load** (seen once, by the WP5
  builder; passes alone and three times in a row). Done when the cause is found and fixed. Read on 2026-11-09, no structural
  defect found: the writes share one flight per key and its leader reads the kept answer again before it probes (`write` in
  `engine/probe.go`), and the kept answer is a synchronous LRU on the harness's fake clock, so a second probe needs a first flight
  that failed or was cancelled, which answers the waiting writes with an error instead of 403; the failure's message was not
  recorded. The test now prints the body of a non-403 answer and every request the main double saw; next step: read those in a
  recurrence.
- **F-95 · One Angular unit test failed once in 1,080, its name not recorded** (seen once, by the F-86 builder on a loaded
  host; the second run and the reviewer's run passed all 1,080). Done when the failing test is identified and its cause
  fixed; until then a recurrence is reported with the test's name and output. Looked at on 2026-11-09: without the test's name
  there is no test to read; the next step stays the recurrence with its name.
- **F-96 · The registry's anonymous test failed once after 85 s, its message not captured** (seen once, by the WP3
  builder on a loaded host; 10 of 10 later runs passed). The settings wait it was suspected of takes under 1 ms; the test now
  logs that wait and fails with the replica's last answer. Done when a recurrence names its cause and that cause is fixed.
  Read on 2026-11-09 (`setAnonymous` in `test/integration/registry/anonymous_test.go`): the wait is on the replica's own answer to an
  anonymous token request, bounded by five times `auth.revocation_bound` and naming the last answer; no further structural
  defect found, so the next step stays the recurrence with that message.

Found on 2026-11-03 while closing phase 5:

### Tests and tools

- **F-36 · The issuer double's fixtures were recorded on a local GitLab 19.0.1-ee, not on the owner's instance**
  (confirmed). The owner's GitLab (namespace `gitlab` of his cluster) cannot create projects: the web service dials Gitaly
  under a name its certificate does not carry. Machine-level for the owner (his GitOps repository). Done when, after the
  certificate is fixed, `make oidc-fixtures` against his instance records the fixtures again (the licensed
  `environment_protected: "true"` included) and the double still reproduces them.

Found on 2026-11-01 while writing the manual (`docs/manual`) and the design change of Q-UI-88:

### Tests and tools

- **F-17 · A leftover of an old tool version on the owner's machine** (confirmed, checked again on 2026-11-02). The shadowed
  `kubectl` and `kind` in `/usr/local/bin` are gone and the Command Line Tools are at 27.2. Left: since Docker Desktop's
  restart of 2026-11-02 (after the disk ran full) `~/.docker/cli-plugins/docker-buildx` points at Docker Desktop's own
  buildx again (v0.28.0-desktop.1, pin v0.38.1), so `make versions-check` fails locally; the Homebrew copy is in place
  (`ln -sf /opt/homebrew/bin/docker-buildx ~/.docker/cli-plugins/docker-buildx`, no admin password; Docker Desktop may
  relink it on its next update unless its CLI plugin management is turned off). Machine-level: the owner runs it; a run
  does not change the machine. Checked again on 2026-11-08: unchanged (`docker-buildx` still links to Docker Desktop's v0.28.0-desktop.1,
  `make versions-check` exits 2 on that line only).

### Open at the end of the run of 2026-11-10

Handed over at the end of the session of 2026-11-10/11 ([section 11](#11--run-record)). v0.43.2 is released (Go 1.27.4); the push of
f8719487 (the format logos, v0.44.0) ran green in every job (Test and Release 42995232524). The owner's installation follows the chart
by `version: "*"`. Red pipelines are worked on even though they no longer hold a release
([releasing.md](../developer/releasing.md#until-the-first-stable-release)). The next run starts with the owner's tasks of
[section 10](#owner-tasks-of-2026-11-11), **T-18**, with the red legs below beside them; the next unit of
[FULL.md](FULL.md) (block B, F1) comes after them.

**Decisions only the owner can take**

- **Q-HA-293, to be discussed, not decided**: a replica whose first Valkey attempts get no answer becomes ready degraded (Q-HA-13 (d)), so a wrong password behind an unanswered first attempt is noticed only later. Should the first readiness wait for a verdict? Waiting would stall a replica started during a Valkey failover, which Q-HA-13 (c) rejected.

- **H-812** ([image-scanning.md](../security/image-scanning.md)): Trivy shares the file system and network of the worker pod, so a crafted image that compromises it can read the secrets mounted there. Close it before images from untrusted remotes are scanned (a sandbox or a pod per scan, a further component), or accept it? A dedicated `vuln` worker pool would shrink it; the owner kept the plain workers on 2026-11-10 (Q-VULN-137).
- **Browse**: stays, or goes now that the repository content pages exist (R-UI-052)? It also carries upload, copy, move and the trash can (R-UI-010 to R-UI-014). The owner decides after seeing the new pages.

**Gating jobs: green**

- `unit tests` is green in every Test and Release run since b0b4538d (eight runs on 2026-11-10). `GOMAXPROCS` 8 was set on 2026-11-10 because six time-bound unit tests failed at once with 32 threads (run 42825155671); at 8 threads only `TestR_VULN_004` failed (run 42839263211), and that test no longer has a wall-clock bound. `TestR_BAK_006_AbortAfterCancellationStillGoesOutWithinTheGrace` and `TestR_STOR_033_AReaderThatPausesIsNotAStalledStore` rest on timing inside the product (the abort grace, the idle timeout), so a test-only event would not hold them: if they fail again at 8 threads they need their own change. Elsewhere replace a bound by a real hang or an event where the test allows it, as in `TestR_BAK_013`, `TestR_BAS_039` and `TestR_VULN_004`.

**Integration and e2e legs (no release gate, still to be fixed)**

- **H-822, the control connection of a replica stays unusable for 10 s and longer on the runners** (runs 42788335321, 42793751322, 42831867192, in `TestR_CI_001`, `TestR_CI_009`, `TestR_CI_029`, `TestR_CI_030` (again in 42907942036), `TestR_STOR_027`, `TestR_REPO_056`, `TestR_NPM_010`, `TestJobs`, `TestCollapse`; shard b once). What turned this into a red test is fixed (Q-ARCH-460, Q-HA-283, Q-HA-284): the drain step no longer spends the shutdown budget, the draining mark runs on the request pool, and a readiness check no longer cuts the control connection (shown to fail on the old code). Open: why the pool hands out no connection, not reproduced locally. The next overrun logs the three pools' counts and a grouped goroutine dump that shows the holder (Q-ARCH-461). The `Flights` change (732572ba) touches the credential check and not the pools, and run 42774543420 on it was green; the runner limits removed on 2026-11-09 are not separated.
- H-823: the burst of `TestClusterWideRateLimit` is bound by 2 s of wall-clock time (one process sends it and runs the three replicas; 1.8 s on a loaded runner). Its "with Valkey" subtest gives up on no Valkey command for the host's speed (Q-HA-285); what the limiter does when a command is given up on stays tested by the other subtests.
- Other integration binaries that take a free port from port 0 can collide as `TestEndpoints` did (`freeAddr` of `test/integration/server` no longer hands out an address twice, Q-HA-285).
- **`ha-faults`** (run 42774548669): `TestHAFaults_PostgresOutage` is green since 732572ba. `ValkeyFlushAll`: 503 from 07:53:20, 70 s before the FLUSHALL, while the run shared the runners with Test and Release; pod logs of the window are not kept. `ValkeyCredentialMismatch`: the `/readyz` 503s were the draining pod and its replacement, as R-HA-007 requires, so readiness is no defect (read on 2026-11-11); 53 tarball requests of the `blob` package hit k6's 45 s timeout, started within 6 s of the pod deletion, pod logs not kept: same shape as F-124.
- **F-124 · Tarballs of 1 to 5 MB stall at the edge for about 40 s in one window** (nightly 42994478570 `drain-roles`, and `ha-faults` `ValkeyCredentialMismatch` of 42774548669): 53 to 72 requests for the `blob` package's tarballs hit k6's 45 s timeout within about 5 s; HAProxy logs them `CD` with 3.2 to 4.9 MB streamed in 36 to 38 s and about 1,800 connections open. Not the drain (in `drain-roles` it was the warm-up, 3 minutes before the first eviction). Probably a load near the VM's capacity, not verified. Next step: record the edge's connection count, bytes per second and the VM's CPU pressure in the e2e artifacts, and keep the logs of evicted packages pods. The evidence is now recorded in `.out/e2e/evidence/` of every e2e artifact, green or red ([e2e.md](../developer/e2e.md#reading-a-stall)); read it at the next occurrence.
- F-126 · `TestConcurrentWritersOnOnePath` R-STOR-003 refused one of the PUTs or DELETEs of six writers on one folder once (Test and Release 43013671879, shard b); the status was not logged. The check now names the first refused request; passed locally. Next step: read it in a recurrence.
- F-127 · Test and Release 43022311289 (9220e777), green on its first re-run: `e2e (push)` failed from its first test on, about four minutes after `up.sh`'s ping check had passed, with `x509: ECDSA verification failure` against the job's own `trove dev CA` on every request through the edge and `password authentication failed for user "trove"` in the migration Job of `TestE04MigrationFailure`; both are secrets the in-cluster generator writes. Hypothesis, not verified: the secrets operator (see the crash-looping operator below) wrote the CA and the database secret again during the run. Next step: keep the operator's log and the secrets' resource versions in the e2e artifacts. In the same run `integration (shard c)` failed `TestR_DEB_015_ImportRules` (the control pool held one connection at the close deadline, readiness timeouts of the database at 12:55, H-822) and `TestR_DEB_023_TheShadowingCheck…` (a name the stub does not list: known false right after the scope settled for another name; `debremote` passed locally on the same tree), both in the stall window of 12:55 to 13:01. The evidence is now recorded in `.out/e2e/evidence/` of every e2e artifact, green or red ([e2e.md](../developer/e2e.md#reading-a-stall)); read it at the next occurrence.
- **502 bursts at the edge (H-795)**: `npm-ha`, `drain-load-e`, `TestE14Drain` (runs 42885398204 and 42902356913: PUTs of the upload loop answered 502 after 17.5 to 18 s; not the drain, which had ended 1.5 minutes before with no request cut, but a stall of the whole CI cluster of about 17 s: HAProxy `SH--` 502s on the API pings as well, TLS handshake timeouts on every packages pod, `valkey degraded`; the test passes on kind on the same tree), node loss. HAProxy `SH--`, 40 of 41 with a connect time over 1 s on three backends within 2 s; k6 measures 12 to 14 s where HAProxy measures 2 to 3 s, so the time may be lost before the accept. No reload in the window. Cause open. On kind on 2026-11-10 one 502 `SH--` of `TestE15TLS` had a connect time of 3,301 ms, 3.4 s after the certificate trigger and 12 s before the pods reloaded; dropped SYNs are ruled out: during bursts of 1 to 2.7 s on kind no ListenOverflows, ListenDrops, ReqQFull, backlog drop, SYN retransmit or conntrack drop moved on any pod, edge pod or node. The slow requests pause and resume together, at a CPU pressure of the shared VM of 450 ms/s on average (load 50 to 79 on 20 cores); at load 13 to 23 the slowest request took 22 to 25 ms. The E0.15 probe itself used more CPU than the installation and is cut 12 to 25 times (Q-HA-292). Open: the `SH--` itself (seen once, not reproduced under the counters), and a CPU pressure reading in the CI e2e artifacts that would settle the runner's share. The npm, NuGet and drain loads were not measured against the same yardstick. Again in nightly 42994478570 (2026-11-11), `e2e (npm-ha)` drain variant A: the edge fell from about 400 to about 10 requests a second at 09:33:56 for 12 s, TLS handshake `i/o timeout` on every packages pod, 502/503 with 10 to 13 s latency; the drain had ended 16 s before, no pod event. The evidence is now recorded in `.out/e2e/evidence/` of every e2e artifact, green or red ([e2e.md](../developer/e2e.md#reading-a-stall)); read it at the next occurrence.
- **A stall of the whole cluster in the set `push`** (`TestE15TLS`, run 42839263211, 15:15:46 to 15:15:56): the edge answered 1 to 15 requests a second instead of about 55, the api pods 3 to 6 instead of 45 to 60, PostgreSQL statements took 60 to 290 ms, Valkey went `circuit_open`. The 353 of 9602 answers with status 0 were requests this stall held for up to 10 s and the test's stop cancelled (Q-HA-288); the certificate is not involved. The rotation row now runs a closed loop and no longer piles up on a stall; its request count, a bound of the open loop that failed run 42870155749 with every answer 200, is replaced by "every client ran through the window and every api pod answered in it" (Q-HA-291). Open: what stalls the cluster (the e2e artifacts do not record the runner's load), and the 7 of about 10,000 answers 502 of run 42818570044, whose evidence was not re-read. Again in run 42907942036 (24cf0250): `TestE06Lease` got 11 of the 15 samples it wants with a p99 of 6.4 s for P-14, and `TestE15TLS` saw one ops listener serve the new certificate 4.6 s after its file changed against a bound of 3 s, every answer 200. Both are wall-clock bounds in a stalled run; a CPU pressure reading in the e2e artifacts is the next step before either bound or the product is touched. The evidence is now recorded in `.out/e2e/evidence/` of every e2e artifact, green or red ([e2e.md](../developer/e2e.md#reading-a-stall)); read it at the next occurrence.
- The step `Install the Docker Compose plugin` took 20 minutes on one runner (Test and Release 42861234699, `integration (shard c)`, 17:06:53 to 17:26:51 on 2026-11-10; 13 s in the run before) and pushed the shard past its 45-minute limit: the job was cancelled while `vulnsync` ran, no test failed. Where the step lost the time is not known. Same family as F-117: the jobs download their tools during setup.
- `e2e (access-long)`, journey `01-sign-in` "R-AUTH-014 the local sign-in waits out the break-glass rotation hold" timed out once at the login answer (run 42894886354). Read, not reproduced: no click can silently send nothing; the real third request had the 15 s of a UI action while the cluster stalled. The wait is now a 60 s hang bound and a timeout says whether the request was sent (Q-ARCH-469); the next recurrence names the cause.
- `drain-load-e`: the replacement of the evicted backup pod still ran after 15 minutes (`Job var-e-281d-1`); its log is not in the artifacts.
- `vuln-pooler`: 12 of 4683 answers were the ingress's 503 in drain variant D.
- `alerts`, `MaintenanceStaysQuiet` in nightly 42793416589: `valkey-failover.sh` reported "the primary did not move within 60 s" right after drain variant B; not found, possibly the Sentinel that variant B restarted had not rebuilt its view (9.6 s locally). (`TroveDatabaseUnreachable` during maintenance was the rule's five-minute rates, fixed by Q-HA-290.)
- `bastion`, `TestBastion_R_BAS_049_RoutingByPath`: 0 hits on both replicas, once in twelve runs.
- `TestE14Drain` (a packages pod unready for 5 s at the end, Valkey `circuit_open`), R-ARCH-034 (26 held against 24, once), `TestR_DEB_010_DelayBound` (503 `database_unavailable`, once in nightly 42677190569; the suspicion on the retry budget of Q-HA-259 is unverified).
- A job can stay queued for hours when the ARC listener loses its assignment (P-13, 2026-11-10, `assigned job=0` after a broker retry); the cause was not established, and a stuck run holds the nightly group until it is cancelled.

**Product work still to do**

- H-829: the listing of a remote OCI repository (view `oci_remote_tags`, Q-OCI-215) matches a regular expression on the path of every tag entry, without an index, and was not measured on a cache with very many tags. Next step: measure it; a derived table or an expression index if it is slow.
- Image scanning: no scan seen yet on the owner's installation; push an image and read the page. Not run: a push through the whole app with the real binary, a drain or worker loss in a cluster, and the release image and chart in a cluster. Gaps H-810 to H-818 and H-871 to H-873 are on [image-scanning.md](../security/image-scanning.md); H-819 is closed: a scan takes its working space from the spool budget (Q-VULN-145 to 148). Not checked: the thresholds of `TroveSpoolNearlyFull` against the larger worker spool of 8 GiB.
- Format logos (owner, 2026-11-11, Q-UI-225 to 227): Debian, NuGet and OCI show their owners' logos. npm, Ansible Galaxy and the Docker
  whale may be shown only with the owner's permission (npm's logo policy, Red Hat's trademark guidelines, Docker's trademark guidelines,
  read 2026-11-11), so npm and Ansible Galaxy keep the neutral glyph. To show them, the owner asks npm, Inc. and Red Hat for permission.
  At the walkthrough: the logos in the running app, light and dark, in Firefox and Safari (only Chrome on a static page was looked at).
- T-19, the documentation of Ansible collections, is built for local, remote and virtual repositories (the set `ansible`, 87 tests, green on kind on 2026-11-11). Open: its four wiki seam sites still ask the repository read until T-18's check replaces them (T-18 WP1d), so a wiki-denied member of a virtual repository has no proof yet; gaps H-900 to H-903 ([ansible-documentation.md](../security/ansible-documentation.md)); the job `e2e (ansible)` ran it green on CI with Node.js and Playwright (Test and Release 43044075096, 87 tests, the five `TestAnsibleDocs*` included).
- T-20's address field (R-UI-065): the browser check in `web/e2e/tests/02-repository-and-files.spec.ts` passed on kind in `e2e (access-long)` of Test and Release 43007020170; at the walkthrough: Q-UI-274 gives the field the editable control's fill instead of the read-only grey, not compared with GitLab's clone field in a browser.
- T-21 (2026-11-12): what is done, all of it on main:
  - `make ui-renders` renders every screen and the creation flow as the plain user, the power user and the administrator: light and
    dark, three widths, a contact sheet. The renders are never committed.
  - In the repository lists every address field has one width and shows the address from its start, and the lists stack below
    48 rem (Q-UI-300 to 306 replace Q-UI-270 and 271).
  - The three kinds of user are specified: R-UI-067, Q-UI-310 to 319.
  - The audit of every screen, 49 lines, is [ui-audit.md](ui-audit.md); the fix waves follow it.
  - Fix wave 1 is on main, five commits from 50c485fc to 175ade9e:
    - every list has a column budget, one alignment and a stacked row below 48 rem;
    - a shared page header and a shared empty state;
    - the repository page shows its address and Set up client at the top;
    - the package list has four columns, and the package page shows the install panel and the README first;
    - the start page has a package search and the recently opened repositories;
    - a reader sees no administration.
  - Four browser journeys were changed with the wave and not run on kind before the push: 11 (nuget), 12 (projects), 17 (deb) and
    20 (ansible). The e2e legs of the push run them.
  - Open, seen in the renders of wave 1 by the development agent:
    - two address fields of one repository (the client's and the files') stand unlabelled on the start page and in the
      repository page's header;
    - the install panel's two selects differ in width, and its command's copy button is not joined to the field the way the
      address field's is;
    - a disclosure ("About this project", "Details") is styled as a large heading with a chevron;
    - a reader's repository list puts "Recently viewed" (the start page says "Recently opened") and "About this project" above the
      list;
    - the repository page offers Set up client both as the header's button and as a tab;
    - the package page's version choice looks like a text field.
- A repository search for readers across projects needs an API that readers can read; until then the start page searches packages
  only (Q-UI-311).
- A Details section does not remember whether it was open (Q-UI-319).
  - The creation and the settings of a repository are planned on T-18's tree: UI-A-31 to 40, the owner's rules in
    [CLAUDE.md](../../CLAUDE.md#product-simple-by-default-details-on-demand).
- UI-A-41: the project owner's service-accounts page calls `GET /api/v1/service-accounts?project=`, which needs `identities:read`. It
  gets 403 and shows "You do not have the permission" (R-UI-041).
- UI-A-45: System status reports a single replica of role `all` as "api/packages/worker live 0".
- UI-A-49 (UI development profile): a sign-in returns to `/ui/en/`, which the dev server does not serve ("Page not found"). The
  login builds `return_to` from the locale instead of the base href.
- UI development profile: the volumes of `deploy/ui-dev` are shared by the whole machine, but the key ring in `.dev/ui-dev` belongs to
  each checkout, so a second checkout writes a new key into the same database. [web-ui.md](../developer/web-ui.md) describes the
  workaround.
- `repository-detail` is the only settings page not yet in the one-column form of the [style guide](../developer/ui-style-guide.md#settings-pages) (Q-UI-222): what its tabs become needs the owner's reference at the walkthrough.
- H-836: a holder of `tokens:read` alone sees which token owners are administrators; only a custom role is affected, because the three system roles with `tokens:read` also hold `identities:read`.
- The committed `messages.xlf` and `messages.de.xlf` carry stale location notes, so `extract-i18n` rewrites about 17,000 lines of them.
- A disk watcher in the jobs, so that a lost runner can be read, was proposed and not built.
- The coverage badge and the evidence record stand still until the first stable release (Q-ARCH-457).
- Ansible Galaxy, open gaps on [formats/ansible.md](../spec/formats/ansible.md) and its security pages: H-861 (digest conflicts counted per merge), H-863 (a role lookup is not merged across members), H-868 (one name with versions in a local and a remote member is not run with the real clients), H-851 (the core's archive bounds), H-842 (the hub exchange is tested against the stub realm only). The base64 body of a publish is held to the system upload limit before `ansible.max_archive_bytes` sees the decoded bytes (Q-ANS-53), not tested.
- The internal secrets operator of the local kind cluster crash-loops (300 restarts on 2026-11-10); a fresh `make e2e SET=ansible` needs the operator pod deleted once so the stubs' tokens are generated. On 2026-11-11 its pod on the local cluster had restarted 216 times; its log is now kept in the e2e evidence (`kept-pod-logs/`) and not yet read. It is the suspect of F-127.

**Phase 7, not closed** (BETA.md stays 🟡): E5.9, E5.10 and E5.5 need green runs, E5.1 a run of the nightly job on a fresh cluster (the drill with the new commands passed on kind on 2026-11-10, Q-HA-289), H-798 is unexplained (drained pods killed after their grace period), and E5.6 misses 18 MVP IDs. The beta candidate is published only when every criterion is met.

**Housekeeping**: finished worktrees are removed at the end of a run, with the local branches of work that has landed (owner, 2026-11-11). On
2026-11-11 all 88 worktrees and 125 local branches were removed. Kept: two local branches whose work never landed, each with a WIP commit of
the changes left uncommitted on 2026-11-08: `worktree-agent-a135bce1ea0ac051f` (the clients-first test of the mirror) and
`worktree-agent-aa6a3feeee93a6b37` (the slow npm publish test, F-105, a temp sweep and an S3 cleanup of the test stack). The origin branches
`p7-*` still exist; deleting them is for the owner to approve. The local `docker buildx` is still behind its pin (see above).

## 9 · Owner review of 2026-11-06: tasks and open questions

The owner reviewed the UI, the documentation and the domain model with the development agent on 2026-11-06, on the branch
`ui-review` (worktree `/Users/hfi/repos/trove-ui-review`). What was decided is recorded in the spec; what is left is below. The
tasks come before the phase's own work packages (step 2, item 5), in this order; remove a task in the commit that finishes it.

### Build what the owner decided (spec first, then code)

- **T-12 · The logo's open point** ([design-language.md](../developer/design-language.md#logo)): `web/public/brand/drafts/` and
  the preview on the sign-in page removed once the owner calls the logo final. The reduced form for the favicon and the
  wordmark as paths are built ([Q-UI-145](../spec/api-and-ui.md#q-ui-145--the-logos-wordmark-as-paths-from-inter-bold-and-a-reduced-mark-as-the-favicon)).

The owner answered every open question of the review on 2026-11-06; a new question during the run is asked one at a time, with
options and a recommendation, and what depends on it is not built before the answer.

## 10 · Owner tasks of 2026-11-07: the next registries

The owner set the next registries on 2026-11-07: Docker, then APT, each with pull-through **and** full mirror from the start. They
come before the next unit of [FULL.md](FULL.md) (step 1, item 3) and after the findings of section 8; each is one phase. Set the
matching FULL.md entry (block B F4 for Docker, the block D row "Debian / APT repositories" for APT) to 🟡 when the phase starts and
to 🟢 with the evidence when it is done; remove the task here in the commit that closes it.

**Working mode (owner, 2026-11-07; changed for ALPHA on 2026-11-08 evening, rule 10).** Planning and building are done by separate
agents. The check step (3) below does not run during ALPHA: a builder lands its feature on its own green gates.

1. **Plan.** A planning agent (`voltagent-qa-sec:architect-reviewer` or `voltagent-core-dev:api-designer` with the format's specialist)
   writes the spec first: the format's spec page under [docs/spec/formats/](../spec/formats/) from its catalog page under
   [docs/features/formats/](../features/formats/), the requirements with IDs, the decisions as decision blocks, and the cut into work
   packages with their contracts (Go interfaces, OpenAPI, schema, chart values) and dependencies. The development agent reads the plan
   against the catalog and the existing formats before anything is built.
2. **Build.** One agent per work package (the format's specialist from the table of step 3), with disjoint directories or
   `isolation: "worktree"`; each returns its changed files and test results and does not commit.
3. **Check.** Agents that did not build the package: `voltagent-qa-sec:code-reviewer` over each package's diff,
   `voltagent-qa-sec:security-auditor` (and `voltagent-qa-sec:penetration-tester` for what parses upstream or client input),
   `voltagent-qa-sec:architect-reviewer` for the contracts, and a verifier that runs the real clients against the chart on kind. Every
   finding goes back to a builder, and the checker reads the fix. The development agent integrates, runs the checks of step 3 and
   commits.

No registry task is open: T-16 (Docker) and T-17 (APT, closed with the FULL.md block D row "Debian / APT repositories" at 🟢,
evidence in [apt-work-packages.md](apt-work-packages.md#24--closing)) are done. A new registry task of the owner is added here.

### Owner tasks of 2026-11-11

Set by the owner on 2026-11-11 (T-18 and T-19 after reading the galaxy_ng screens of his collections; T-20, the client addresses in full, is built: R-UI-065). All are confirmed features, not parked ideas, and
the next run takes them in this order, each planned by a planning agent (spec and a work-package plan) before anything is built.
T-18 is planned in [visibility-work-packages.md](visibility-work-packages.md) (R-AUTHZ-033 to 035, ADR-192). T-19 is built (run of 2026-11-11,
[galaxy-docs-work-packages.md](galaxy-docs-work-packages.md), R-ANS-033 to 046): the set `ansible` ran green on kind with the real clients and both browser journeys.

- **T-18 · Visibility levels.** Private, internal and public, as in the owner's GitLab:
  - **private**: only members, that is principals that hold a grant on it;
  - **internal**: seen automatically by every signed-in user except users flagged **external** (owner, 2026-11-11); trove therefore gets a
    user flag "external" as GitLab has it, set by an administrator, and an external user sees only what it is a member of or what is public;
    decide whether the technical accounts of external parties of a project (phase 5a) and the Bastion's partners count as external;
  - **public**: readable without authentication.

  A default per installation (the general settings), a setting per project and a setting per repository; on a repository the **wiki** part
  is set separately from the artifacts, for example "the artifacts of this repository are public, its wiki is internal". What exists and must
  be read first: the built-in principals `anonymous` (R-AUTHZ-003) and `authenticated` (R-AUTHZ-030), the runtime switch `anonymousAccess` and
  its ceiling `auth.anonymous.allowed` (R-AUTH-037), policies, path-scoped grants and the decision point with `ReadScope`. trove is pre-alpha
  (CLAUDE.md): one mechanism, not a second one beside the anonymous switch; replacing it is allowed. To decide in the spec: inheritance and
  whether a repository may be more open than its project (GitLab: no); what "wiki" is per format (the package pages' READMEs, a README inside
  a protocol document such as npm's packument, the collection documentation of T-19) and what a client gets when the artifacts are visible and
  the wiki is not (an absent field, 404, a redacted document) on the protocol routes, in the API and in the UI; how visibility composes with
  path-scoped grants, the Bastion, exposure rules, endpoints and search; the settings pages (one-column form), the OpenAPI operations, the
  audit events, the schema, and the caches a change must reach (the authz snapshot, the claims index) within the existing bound. Security
  pages change (who can read what), with their gaps. e2e with real clients: "public artifacts, internal wiki" (an anonymous `npm install` works,
  an anonymous request for the documentation gets what was decided), and an external user who sees nothing internal.
### Owner task of 2026-11-12

Set by the owner on 2026-11-12, after the repository list showed an address field of a different width in every row: "Our UI still
looks really bad and I expect more. A consistent design. Easy access to the product for simple users. Optional details for power users
and admins." A confirmed feature, not a parked idea; it runs beside T-18 and is planned and built under rule 11.

- **T-21 · A UI that looks right.**
  - **Renders.** A command that brings up the UI development profile with realistic data and renders every screen, in both schemes and
    at three widths, as the three kinds of user below, into `.out/ui-renders/` with a contact sheet.
  - **Address fields.** In a list every address field has the same width and the list does not scroll sideways at the usual desktop
    widths; this replaces Q-UI-270 and Q-UI-271.
  - **Audit.** Every screen rendered and judged against the style guide, each defect one line with its screen and picture. The audit
    list is the work list of the fix waves.
  - **Three kinds of user.** The plain user who reads and installs, the power user who publishes and maintains, and the administrator.
    The plain user reaches the common job (find the repository or package, copy the address, set up the client) without detours and
    without settings in the way. What only power users and administrators need sits behind an optional disclosure, not in the main view.
    To decide in the spec: what each kind sees on the home page, the lists, the repository and package pages, and where the details go.
  - **Consistency.** One width, height, spacing and alignment per kind of element across screens. Row actions, empty states and page
    headers follow one pattern each. T-18's screens are rendered under the same rule before T-18 lands.
  - **Creating and configuring a repository, simple by default** (owner, 2026-11-12, after the creation flow of a local repository
    showed include and exclude patterns, "Disabled", "Require authentication" and "Hide existence" as the access step): the rules of
    [CLAUDE.md](../../CLAUDE.md#product-simple-by-default-details-on-demand) apply to every format and class.
    - The standard flow asks only what the repository needs to work. Access is the members and the visibility level of T-18.
    - Every other setting stays, in a detail view or behind an advanced-settings disclosure.
    - A remote repository's connection is tested where its upstream is entered, before the save. **Test connection** exists today
      (`upstream-settings.ts`, `POST /api/v1/projects/{projectKey}/upstream-tests`), but only inside the step "Access, upstream, OCI (Docker)
      and mirror settings".
    - Well-known public upstreams are one-click choices: for OCI, Docker Hub, Quay, GitHub (ghcr.io) and GitLab; the other formats'
      choices are decided in the spec.
    - Defaults are values in their fields. A field's width fits its expected input. The title names the format ("New OCI repository").
    - Defects seen in the owner's screenshots, to fix with it:
      - a step label that lists four topics;
      - a key field red with "does not match the pattern" before anything was typed;
      - administrator notes in the standard flow.
    - Planned in [repository-setup-work-packages.md](repository-setup-work-packages.md) (Q-UI-340 to 353, Q-REPO-320 to 327):
      - a page of format tiles, then one form per format titled with the format's display name;
      - the standard fields per class, with members and the deployment target for a virtual repository;
      - the upstream presets per format, served by the format plugins, and **Test connection** under the credentials;
      - one closed **Advanced settings** section;
      - the settings page as one column of sections.
      T-18 changes the same flow, so two builders build it after T-18 lands.
    - Open for the owner, not blocking: should administrators add the installation's own registries (for example the owner's GitLab
      registry) as preset buttons? The development agent's recommendation is not now, and as its own task if wanted.

### Owner task of 2026-11-12, for later: a search with suggestions

The owner showed his GitLab's search on 2026-11-12: "A sensible search, with suggestions and everything. This is how I picture it for us
later." It is a confirmed direction, not a parked idea, and it is built after T-18 and T-21, each planned by a planning agent first.

- **T-22 · One search with suggestions.**
  - **The field.** One search field in the top bar for every user who is signed in, not only for those who may list every repository.
    Today the field **Find a repository** shows only for `canSearch()` ([app.html](../../web/src/app/app.html)). The field has a
    filter control that narrows the search to one kind.
  - **Before anything is typed**, the opened field already offers, as the reference does:
    - **Places:** the start page, the guides, the profile and its settings, and for an administrator the administration;
    - **frequently visited repositories:** the format's icon, the key, and the project and path in muted text;
    - **View all my projects;**
    - **frequently visited projects.**
  - **While typing**, suggestions are grouped by kind: repositories, packages, projects, and for an administrator users and groups.
    Each suggestion shows where the thing is. Everything is reachable by keyboard.
  - **Visibility.** A suggestion names only what the caller may read, under T-18's levels, and the hiding of a repository's existence
    holds. To decide in the spec:
    - the API that readers can read across projects (today the start page of T-21 searches packages only, Q-UI-311);
    - where "frequently visited" is kept: in the browser, as the reference does, or on the server;
    - whether the start page's package search becomes this same field.

## 11 · Run record

What a run did, in short, so the next run starts where it stopped. The plan items, findings and decisions carry the details.

**Run of 2026-11-07/08** (releases v0.25.1 to v0.26.0).

- Built: the findings F-71 (a narrowed token no longer acts on its owner's account or other tokens), F-72 and F-74 (every narrowing
  and every block the evaluation sets is journaled and replayed fail-closed), F-73 (one oversized OSV record no longer stops an
  ecosystem's import; the owner's npm advisory data is back from v0.25.3), F-63 (the slot split of Q-REPO-235; the install-time bound
  held at 97 %), F-76 and F-77 (npm verifies every tarball against its strongest digest); E5.7 (the security pages of the whole
  beta), E5.8 (each release is installed by digest and smoke-tested; the SBOM comes from the pinned syft), E5.13 (the vulnerability
  blocks through drain variants B to E, the pooler profile, the restore drill), with three product defects found on the way (jsonb
  through a transaction-mode pooler, a policy update replayed as suspended, the restore Jobs without their CA volume).
- Started late: T-16, the Docker registry: spec and WP0 (contracts, schema, format stub) built and reviewed on the branch `t16-oci`,
  not yet on main. T-17 (APT) not started.
- Broke: v0.25.1 has no image and no chart (the build's RUN steps had no DNS on a per-run docker network; fixed in v0.25.2).
- Where the time went: the run worked sequentially. For about eight hours it took the findings one after the other through
  planning, building and three or four review rounds, merged six parallel worktrees and waited for Test and Release, the release
  runs and a dispatched nightly run; the Docker registry started only at the end. The owner's verdict: features and findings run in
  parallel from the first hour, an agent and a branch per feature package and an agent per finding (rule 10). The next run starts
  T-16's wave 1 and an agent for each open finding together in its first hour.

**Run of 2026-11-08** (releases v0.27.0 to v0.29.0; v0.30.0 waits for Test and Release on the last push).

- Built and on `main`: T-16's local OCI registry. WP1 upload sessions across replicas, WP2 registry tokens and one enforcement point
  in front of `/v2/`, WP3 the `/v2/` registry root (logins by token, pulls, manifest pushes, tags, catalog), WP4 the local
  registry class (manifests, tags, referrers, deletes, sweep, replay), WP5 blob uploads and cross-repository mounts, WP6 the remote
  engine's token handshake, WP7 the counting stub registry, WP12 the OCI screens of the UI. A local `docker push` and `docker pull`
  through `/v2/` work end to end.
- Findings closed: F-43, F-50, F-75, F-78 to F-82, F-85 to F-87, F-89 to F-94, F-97 to F-104, F-107. The important ones: path-scoped
  grants now apply on the npm and NuGet protocol routes (F-82); the stall timeout no longer cuts a request while the server works,
  over HTTP/1.1 and HTTP/2 (F-91, F-92); a replica's shutdown keeps its deadline (F-94, F-97); two first publishes cannot both claim
  a name (F-101).
- Landed at the end of the run: WP8, the OCI remote class (pull-through from Docker Hub, GHCR and other registries), F-83 (a path
  pattern that hides a version's file hides that version everywhere) and F-84 (a large delete stops when its requester loses the
  right). Not done: WP9 (virtual), WP10 (mirror), WP11 (e2e on kind), WP13, WP14; T-17 (APT) not started.
- Where the time went: three or four review rounds per work package and per finding, 28 new findings (most of them false failures
  under the load of 10 to 14 parallel agents on 20 cores), and a full Docker disk at the end (252 GB) that stopped the integration
  stack. The owner's verdict (rule 10, changed): during ALPHA no review subagents, findings only when they block, no repeated race
  or stress runs, at most four builders, each a whole feature, and release notes that say in plain words what changed.
- Next run: first the blockers F-109 and F-110 (main red), then four builders: OCI virtual (WP9), OCI mirror (WP10), APT local,
  APT remote; the open findings of section 8 in one batch at the end.

**Run of 2026-11-08/09** (ALPHA mode of rule 10; releases v0.30.1 and v0.31.0).

- Built and on `main`: the rest of T-16, the Docker registry — the virtual class (pulls in member order, pushes to the deploy target),
  the mirror (selected names, tags and platforms kept and pullable with the upstream gone), the conformance suite v1.1.1 in all four
  categories, the e2e set `oci` with Docker and Podman on several replicas, the closing set `oci-closing`, the documentation; T-16 is
  closed. All of T-17, the APT registry for Debian and Ubuntu: the contracts, the `.deb` and deb822 libraries, the OpenPGP library and
  a signing key per repository with co-signing rotation, the local class with signed indexes built by a job, the remote class that
  passes archives through with their signatures verified, the mirror of named suites, the virtual class that merges local and remote
  members into one signed index, the UI, the e2e set `deb` on Debian 13 and 12 and Ubuntu 24.04 and 22.04, the documentation.
- Product defects the clients and tests found on the way: a mount challenge without the source's pull scope (the conformance suite), a
  mirror with its upstream down answering 502 to Docker's referrers request, a Docker Hub mirror that never ran a pass because Hub
  refuses the catalog scope, APT ignoring every index until the Release listed the uncompressed checksums, a lost first build of a new
  suite, a reindex of unchanged rows that published nothing, a manual and an automatic name claim deciding at the same time (F-108), the
  build metrics counting virtual builds as local.
- Findings closed: F-106, F-108, F-109, F-110, F-111, F-112 (decided, Q-NPM-86), F-113, F-114, F-115. Recorded: F-116 to F-120.
- Release: v0.31.0 (2026-11-09) carries both registries. Test and Release 42565678133 green; Release Docker & Helm 42569132981 green on
  its third attempt after two re-runs under the changed rule 7 (GitHub answered 504 for the syft download, F-120); checked: the GitHub
  release with plain-language notes, the image `guidedtraffic/trove:0.31.0` on Docker Hub, the chart 0.31.0 in the index, the smoke
  test of the published release. Before it, main was red six runs in a row for causes outside the product: the Ubuntu archive's outage
  (F-117), shard b past its 45 minutes (now three shards), the apt client containers on the Linux runner (host networking, the key
  through the environment), test preconditions written for one timing, and the set `push` on a starved runner cluster (F-119).
- Rule change by the owner on 2026-11-09: a red CI job may be started again up to three times to reach a green run (rule 7, step 5).
- Builders: at most four at a time, each a whole feature over several stages, resumed with their context; no review rounds. Load stayed
  between 15 and 50 on 20 cores, and the timing failures under it were fixed in the tests (F-114, F-115) instead of being chased.
- Merges: every builder branch was cherry-picked onto `main`, the touched gates rerun on the merged tree; the conflicts were the shared
  plan and documentation pages, two migrations that both took 00064 (renumbered), and doc links one directory too short.
- Evidence of the closing: the e2e set `deb` (52 rows, the virtual row on) and the closing set `deb-closing` (Valkey stopped on all
  four images; `apt-get install hello` from `deb.debian.org` through a remote, 3 upstream requests, Debian's archive key checked against
  its pinned fingerprint) ran green on kind (`linux/arm64`); `oci-closing` 7 of 7. Left as findings: F-118 (architecture `all` through
  a virtual repository); the Debian passes of the sets `mirror` and `mirror-long` are not built. `HA and Performance (nightly)` was
  dispatched on the release commit (run 42571414393) for the long OCI legs; its result is read at the start of the next run.
- Next run: T-17 has no open package. Section 10 holds no registry task, so the next unit of FULL.md follows (step 1, item 3), with
  the open findings of section 8 in one batch at the end.

**Run of 2026-11-10** (ALPHA mode of rule 10; releases v0.37.0 to v0.43.0, and the push of 24cf0250 after them).

- Started from the handover list. The gating `unit tests` job is green since b0b4538d (`TestR_VULN_004` without a wall-clock bound). The red
  integration shards had a product side: a stopping replica spent its whole budget in the drain step on a stalled control connection, and a
  readiness check cut that connection (Q-ARCH-460, Q-HA-283, Q-HA-284); why the pool stalls on the runners is still open (H-822). `e2e (push)`:
  the rotation row runs a closed loop judged by whether its clients ran through the window (Q-HA-288, Q-HA-291), and its certificate probe
  used more CPU than the installation (Q-HA-292). The alert `TroveDatabaseUnreachable` no longer fires during maintenance (Q-HA-290). E5.1's
  drill ran on kind with the new commands, 13 of 13.
- Product: the content page of an OCI remote lists its cached images through a view (Q-OCI-215); administrator tokens for service accounts and
  a registry refusal that names the missing scope without telling whether a repository exists (Q-OCI-220); the settings pages in one column
  but the repository's; `trove admin import` for npm and NuGet (FULL.md block A); the manual's backup and restore page and runbook (block A);
  the image scan inside the spool's budget (H-819, Q-VULN-145 to 148).
- F0, Ansible Galaxy: planned by one agent (R-ANS-001 to 032), built by four in stages (core and local; stub, remote, mirror and hub exchange;
  virtual and import; setup, UI and e2e). The real clients found three defects every integration test had missed: ansible-core sends the
  archive base64-encoded (every publish failed), the netrc snippet sent its variable name as the password, the mirror lacked the collection
  document. The set `ansible` passed on kind with both clients (67 tests) and its CI leg is green. The import against the owner's galaxy_ng followed on 2026-11-11: a dry run, then the copy of 3 collections and 7 versions into the owner's installation, installed from there by ansible-galaxy 2.21.
- Leads that were wrong, with the evidence that refuted them: dropped SYNs behind the edge 502s (no kernel counter moved), the drain
  change behind `TestE14Drain` (the 502s came 1.5 minutes after the drain, in a stall of the whole CI cluster). The common factor of most red
  e2e and integration runs of the day is a stalled runner; no CI job records its CPU pressure yet.
- Where the time went: merging the Ansible builders, whose decision blocks were appended at the same anchor of the spec every time (resolved by
  sorting the blocks by number), and three rounds on the rotation row's load bound before it fit a closed loop. A gate that a builder's own run
  could not see: a new format changes every test that pins the formats (the guide's client list, the guide journey's first format).
- Next run: the red legs of section 8 that are not the runner, then FULL.md block B, F1 (generic files: remote and virtual).
