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

## 1 · Find the next phase

1. `git status` must be clean; then `git pull --rebase origin main`.
2. During the alpha (rule 10) a run works on two fronts at once: the first open task of
   [section 10](#10--owner-tasks-of-2026-11-07-the-next-registries) (T-16 Docker, then T-17 APT; its status line says where an
   earlier run stopped), cut into work packages with an agent and a branch each, and every open finding of section 8 with an agent
   each. The open items of BETA.md phase 7 are findings of the same kind. Outside the alpha: open [BETA.md](BETA.md), table
   **Where we are**; the current phase is the first row whose status is not 🟢, and a row at 🟡 is resumed (step 2, item 4).
3. If every row of BETA.md is 🟢, open [FULL.md](FULL.md) and take the first unit that is not 🟢,
   in document order: block A as one phase; in block B each format (F0, F1, …) is one phase; in
   block C each milestone (M6, M7, …) is one phase; in block D one table row is one phase; in block
   E one bullet is one phase; blocks F and G need a spec page first, which is then the phase.
4. Release numbers are not planned and not recorded: the pipeline releases (step 6), and BETA.md and FULL.md
   track only what is built (owner, 2026-11-02).
5. If `Test and Release` on `main` is red (`gh run list --workflow "Test and Release" --branch main --limit 5`),
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
- BETA.md or FULL.md shows the phase row at 🟢, the items at 🟢, and today's date
  under **Last updated**; the [roadmap](../coverage/roadmap.md) is consistent with what was built.

## 5 · Push and get the pipeline green

1. `git push origin main`.
2. Watch every workflow run the push triggered in the background, job by job (`gh api repos/guided-traffic/trove/actions/runs/<id>/jobs`
   every 60 s), and keep building meanwhile; never sit idle on a run (rule 10).
3. On failure: `gh run view <run-id> --log-failed`, let `voltagent-qa-sec:debugger` or the fitting
   specialist find the cause, fix it, commit, push, and wait again. Repeat until every run is
   green.

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
2. Push, and wait until `Test and Release` is green on that commit. Semantic Release has then cut the
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

### Product

- **F-111 · `TestSpoolAdmission` (`internal/blob`) failed once in a full `go test ./internal/...`** on a loaded host and passed
  alone. Cause not looked at.
- **F-113 · The integration stack's object store fills up in about a day** (2026-11-08): the tests leave their objects behind,
  and SeaweedFS in `test/integration/compose.yaml` holds 100 volumes of 512 MB; at 45 GB every upload failed until the stack was
  reset. Done when the tests remove what they write, or the stack cleans up on its own.

### Tests and tools

- **F-114 · Two npm mirror tests time out under host load** (2026-11-08, load about 48 from four builders):
  `TestR_REPO_053_AShutdownReleasesThePassAndASecondProcessFetchesEveryTarballOnce` and
  `TestR_REPO_051_NpmPrivateRegistryTakesNamesAndTheHeldPackages` ("a full pass … did not end complete within 1m30s"); both passed
  alone in 27 s on the same tree. Done when the cause of the pass's slowdown under load is known.
- **F-115 · Three integration timing bounds missed under host load** (2026-11-08, load about 40): `TestSnapshotAcrossReplicas`
  (a lookup took 360 ms against a 300 ms poll), `TestVirtualMembers` (a member-order change took 2.45 s against 2 s),
  `TestR_BAK_005_MigrationLock` (a migration gave up after its 1 s lock wait); all three passed alone on the same tree. Done when
  each bound is either structural or its cause under load is known.
- **F-116 · `TestBastion_R_BAS_049_RoutingByPath` failed once on CI** (Test and Release 42530616876, e2e-bastion): "0 hits for 36
  paths requested twice"; each repeated tarball request went to the main installation as a revalidation (304) instead of being a hit
  on the Bastion replica of its path. Green in the runs before and after with no Bastion change in between. Done when the cause of
  the revalidations is known.
- **F-105 · `TestR_NPM_007_ASlowPublishOf200MiB` fails under load** (seen twice: by the F-83 builder, an S3 UploadPart
  cancellation followed by the package timeout, and by the F-101 builder, "1 connections held a transaction older than 1 s
  while the body arrived"; passes alone). The test asserts that no database transaction stays open longer than 1 s while
  the body of a slow publish arrives. Done when the cause is found: a transaction the publish really holds during the
  body (a product defect), or an unrelated transaction of the shared stack the test counts (a test defect).
- **F-102 · `TestR_BAS_014_ThousandWritesToOnePathCauseOneProbe` failed once under host load** (seen once, by the WP5
  builder; passes alone and three times in a row). Done when the cause is found and fixed.
- **F-95 · One Angular unit test failed once in 1,080, its name not recorded** (seen once, by the F-86 builder on a loaded
  host; the second run and the reviewer's run passed all 1,080). Done when the failing test is identified and its cause
  fixed; until then a recurrence is reported with the test's name and output.
- **F-96 · The registry's anonymous test failed once after 85 s, its message not captured** (seen once, by the WP3
  builder on a loaded host; 10 of 10 later runs passed). The settings wait it was suspected of takes under 1 ms; the test now
  logs that wait and fails with the replica's last answer. Done when a recurrence names its cause and that cause is fixed.

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

- **T-16 · Docker registry (OCI distribution), with pull-through and full mirror.** Scope from the owner: local, remote and virtual
  repositories for container images; the remote in both modes of R-REPO-050 (`pullThrough` and `mirror`). The planning agent takes
  FULL.md F4 as the starting list (registry API under `/v2/`, token challenge, chunked uploads across replicas, multi-architecture
  images, referrers; clients Docker CLI and Podman at least) and records which F4 items this phase builds and which stay in F4
  (BuildX/BuildKit caches, ORAS, Cosign, WebAssembly artifacts, image vulnerability scanning), as a decision. Full mirror for a
  registry cannot mean the whole upstream: what a mirror of Docker Hub or another registry discovers and keeps (named repositories,
  tag patterns, platforms, the newest n tags), within R-REPO-051's bounds and R-REPO-052's storage limits, is the central decision of
  the plan. Tests per step 3: integration (protocol over HTTP against the stack, the conformance kit) and e2e with the real clients
  through local, remote in both modes and virtual repositories on several replicas, an upload interrupted by a drain, a mirror pass on
  n replicas (R-REPO-058, R-REPO-059).
  Status 2026-11-08: the spec (`docs/spec/formats/oci.md`, R-OCI-001 to R-OCI-035, Q-OCI-1 to Q-OCI-34) and WP0 (format contract
  additions ADR-161, migrations 00054 to 00056, config keys, the `oci` stub answering 501, `Ingestor.Link` by source name,
  `ItemsEvent.Cascade`, the upload-session contract, withheld manifests in the serve facts) are built and reviewed (architecture and
  code review, every finding fixed) on the branch `t16-oci`; the cut into WP1 to WP14 is `docs/projektplan/oci-work-packages.md` there.
  Not merged to main on purpose: migration 00056 tightens the key grammar of every repository and project key (Q-OCI-3) and stops at
  the first key that breaks it. Checked on 2026-11-08 on the owner's installation (read-only, schema `trove`): no repository, project,
  principal, policy or CI-mapping name breaks the new CHECKs (0 repositories, 1 project `test`), so `t16-oci` may go to main.
  Next, in this order: (1) a fresh agent rebased `t16-oci` onto main on 2026-11-08 and, if every check passed, pushed it squashed as
  the branch `t16-oci-rebased` (one `feat(oci)` commit on top of main): check it and fast-forward main to it; if that branch does not
  exist, rebase `t16-oci` (ca928052) onto main yourself. Main has migrations 00052 and 00053, ADR-159 and ADR-160 (F-76
  added `catalog.Facts.ChecksumAlwaysVerified`: merge it with ADR-161's contract additions and regenerate the contract file), and the
  jsonb RawMessage override of sqlc (regenerate `q/`); keep 00054 to 00056, ADR-161, the Q-OCI numbers and H-579, H-580 unless main
  took one; squash into one `feat(oci)` commit and bring it to main without a force push. (2) Wave 1 of the cut in parallel: WP1
  upload sessions, WP2 registry tokens, WP4 the OCI format (local class), WP6 the remote engine's handshake, WP7 the counting stub
  registry, planned, built and checked by separate agents as this section says.
  Status at the end of 2026-11-08: on `main` are WP0 to WP10, WP12 and WP13: the local class end to end through `/v2/`, the remote class
  (pull-through from Docker Hub, GHCR and other registries), the mirror (`mode: mirror`: selected names, tags and platforms kept and
  pullable with the upstream gone, ADR-182), the virtual class (pulls in member order, pushes to the deploy target, ADR-180), and the
  OCI distribution conformance suite v1.1.1 (all four categories through a local repository; pull, push and discovery through a
  virtual one, Q-OCI-211). Running: WP11 (e2e with Docker and Podman on kind). Not started: WP14 (closing documentation, FULL.md F4).
  The cut is `docs/projektplan/oci-work-packages.md`; FULL.md block B F4 stays 🟡.
- **T-17 · APT registry (Debian and Ubuntu), with pull-through and full mirror.** Scope from the owner: local, remote and virtual
  repositories for `apt` and `apt-get`; the remote in both modes of R-REPO-050. The planning agent writes `docs/spec/formats/debian.md`
  from [debian.md](../features/formats/debian.md): the repository layout (`dists/<suite>/Release`, `InRelease`, `Release.gpg`,
  `Packages` and its compressed forms, `by-hash`, `pool/`), signing of the indexes trove generates for local and virtual repositories
  (key management is a security decision: where the key lives, rotation, how clients get it) and pass-through of the upstream's signed
  indexes for a remote, flat repositories, and what a full mirror covers (suites, components, architectures, sources or not, the
  bounds of R-REPO-051/052). Tests per step 3: integration and e2e with `apt-get update` and `apt-get install` in Debian and Ubuntu
  containers through local, remote in both modes and virtual repositories, signature verification on the client included.
  Status 2026-11-08: the spec `docs/spec/formats/debian.md` (R-DEB-001 to R-DEB-035, Q-DEB-1 to Q-DEB-43, H-620 to H-630) and the cut
  `docs/projektplan/apt-work-packages.md` (WP0 contracts, WP1 to WP13 in four waves) are on main, planned by one agent and checked in
  three rounds by another that verified the client behaviour on Debian 13, Debian 12, Ubuntu 24.04 and Ubuntu 22.04.
  Status at the end of 2026-11-08: on `main` are WP0 (the contracts: schema 00060 to 00062, ports, routes answering 501, ADR-170 to
  ADR-172), WP5 (the counting stub archive `test/debstub`) and the OpenPGP library of WP2 (`internal/openpgp`, verified against `sqv`
  and `gpgv` on all four client images). Running: WP1 with WP3 (the local class), WP2's key store and rotation. Next: WP4 (index
  builder), WP13 and WP6 (remote), WP7, WP8, WP9 to WP12. FULL.md block D row "Debian / APT repositories" is 🟡.

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
