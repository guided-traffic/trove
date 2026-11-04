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
4. Release numbers are not planned and not recorded: the pipeline releases (step 6), and BETA.md and FULL.md
   track only what is built (owner, 2026-11-02).
5. If `Test and Release` on `main` is red (`gh run list --workflow "Test and Release" --branch main --limit 5`),
   repairing it is the first task of this run. A red leg of `HA and Performance (nightly)`
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
- BETA.md or FULL.md shows the phase row at 🟢, the items at 🟢, and today's date
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
   for fixes, `docs:` or `chore:` for the plan and the rest. Commit the final plan update (phase row 🟢)
   with them.
2. Push, and wait until `Test and Release` is green on that commit. Semantic Release has then cut the
   release of the push, if the commits held a `feat` or a `fix`, and `Release Docker & Helm` has run for
   it: `gh run list --workflow "Release Docker & Helm" --limit 3`, then `gh run watch <run-id> --exit-status`.
3. `git pull --rebase origin main` (the release commit moves `main` with the coverage badge). The release
   number goes into no plan: BETA.md and FULL.md track features only (owner, 2026-11-02).
4. Check the published artifacts of the release: `gh release view vX.Y.Z`, the image
   `guidedtraffic/trove:X.Y.Z` on Docker Hub, and the chart index
   `https://guided-traffic.github.io/trove/index.yaml`. The release notes are generated from the commit
   messages; they name the requirement IDs, so the commits carry them (rule 4, the commit messages).

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

Found on 2026-11-04 while cutting trove into task areas (phase 6a):

### Tests and tools

- **F-50 · A single request of `TestE1_14Exposure` ended in a transport `EOF` at the edge** (read, from the log of the `push` leg of
  Test and Release run 41871333853, on a GitHub-hosted runner). With Valkey stopped, `GET
  https://packages.trove.localhost:8080/files/generic-local/internal/x.bin` returned `EOF` instead of the `404`; the same subtest passed
  in run 41864646902 on the same code. The e2e clients set `DisableKeepAlives` (`newAPI` in `test/e2e/m1_env_test.go`), so every
  request is a fresh TLS connection through the kind port mapping and the edge, and Go retries none of them. The burst cases of the same
  test count such connections as status 0 in green runs too (53 and 68 of 1,000 in run 41864646902), so connections closed without a
  response are routine under load. Which side closes them (the port mapping, HAProxy or a trove pod) is not known: the cluster-state
  step keeps the last 500 lines per pod (`--tail=500`), which begin after the failure. Done when a run that keeps the edge's and the
  pods' logs of the failure window names the closing side and the cause is fixed or shown to lie outside trove.
- **F-49 · The drain per role (`TestDrainRoles`, set `drain-roles`) runs with a reduced load** (confirmed, by reading the test).
  R-HA-059 "A drain per role" asks for the npm and dotnet client loops, the long-transfer probes and a loop of a browser journey
  beside the ping, transfer and management-call loops; the test has the latter three and the driver's jobs, and the full advisory sync
  is drained by node in `TestVulnLong_R_VULN_004_FullSyncDrainedMidRun`, not per pod from a checkpoint. The test has not run on a
  cluster yet (WP6a.5 was written while the kind cluster was in use). Done when the client loops of `TestNPMHA` and the journey run
  beside the per-role drain and the sync is started before the first eviction of the pool `vuln`, or the owner narrows the criterion.

- **F-46 · `TestE1_08RateLimits` was red in the `failover` leg of the scheduled nightly run 41804996351** (read, from the
  log). The bound cases passed, with Valkey stopped included (`e1_08_ratelimit_test.go` up to line 202); the case after
  `valkey-cli.sh client pause 4000 all` (line 230) saw 15 of 483 requests take 400 ms or more, and the credential-failure
  burst that follows (line 351) is where the test failed; its assertion is not yet read. The same run was red in `npm-ha`
  (F-38) and in the performance run of R-VULN-028 (F-43). Done when the failing assertion is read, reproduced with
  `make e2e SET=failover` and its cause fixed, or shown to be the runner's load (F-37, F-40).

Found on 2026-11-03 while building phase 6:

### Tests and tools

- **F-43 · Two of the bounds V-1 to V-9 of R-VULN-028 are missed on the shared stack, and a run on the reference environment is
  missing** (confirmed by the run, narrowed by the fix of 2026-11-04). The performance run `TestR_VULN_028_PerformanceRun` on the
  integration stack (not the reference environment, H-491), 2026-11-04: V-1 7.1 min, V-2 2.1 min, V-3 0.16 min, V-6 131 ms, V-7a
  37 ms and V-9 0.33 GiB met their targets; V-5b counts 2.04 transactions per cached download against the one of R-ARCH-027 (F-41,
  fixed since: a cached download costs one statement, `test/integration/querybudget`; V-5b is to be read again); V-7b took 1,168 ms against 300 ms and V-8 29 ms against
  25 ms. The nightly run 41748522448 on the CPU-limited CI runner: V-1 did not finish within 40 min (the first import of the npm
  archive; so V-2 was not measured), V-3 2.3 min, V-4 ≤ 10 ms (bucket bound), V-5b 1.94 transactions, V-7b 446 ms, V-8 52 ms
  missed; V-5a, V-6 (63 ms), V-7a (33 ms) and V-9 (0.33 GiB) met.
  Done by the fix: V-4 is measured per call (Q-VULN-76: the p99 of 300 exact samples, the difference of the histogram's sum between
  two scrapes around each publish; the histogram stays for production). V-7b: the decision point derived the subject's state for
  every row, about 3 µs of the cost; a scope derives it once (Q-VULN-77, `BenchmarkReadScopeMayRead` about 1 µs a row against about 3.4 µs, interleaved on a loaded host, a
  focused test of 3,000 findings and a pattern grant pages exactly the pattern's findings). V-8: profiled (Q-VULN-78): a 777 KB
  page, median 2.2 ms through the whole client path, the time in the socket write and the client's collection, no cost of the page's
  own path to remove; the p99 over the bound came with a host load average above 190.
  Left: V-7b still reads the triple of every finding of a repository read in part (about 100 to 120 ms of statement and the
  transfer of 100,000 rows) and cannot be judged under the shared stack's load: `TestR_VULN_020_TheFirstPageAtScale` measured 1.7 to
  3.2 s for the pattern grant (and 60 ms to 1.2 s for the administrator) at a load average of 240, before and after the fix alike,
  so the fix is shown by the benchmark and not by the end-to-end time; V-5b waits for F-41; V-4's exact value, V-7b and V-8 are
  to be read on a run on the reference environment (H-491) and on the next nightly run. Done when that run reports all nine
  within their targets, or a decision revises a bound with the measured cost (the next steps for V-7b are named in Q-VULN-77).

Found on 2026-11-03 while closing phase 5:

### Tests and tools

- **F-37 · `TestE14Drain` failed in run 41575195688 on a PostgreSQL failover that the runner's load caused, before the drain**
  (confirmed from the job's artifacts; not reproduced). The refusal was not a request a trove pod served and not the drain:
  `driver probe` (`e14_drain_test.go:237`, which comes before `drain.sh`) is a peer that opens its own pool to
  `trove-db-rw` (`test/e2e/cmd/driver/main.go`), and the service had no ready endpoint (`connection refused`). The
  artifact `e2e-cluster-state-push` shows why: from about 04:13:50 the probes of both PostgreSQL pods timed out
  (`Client.Timeout exceeded`, `context deadline exceeded`), the primary `trove-db-1` was killed by its liveness probe, and
  CloudNativePG initiated the failover (`FailingOver`, `PodDisruptionBudget trove-db-primary: NoPods`) at about 04:16:20,
  the moment of the refusal. The drained node (`worker2`) held a web and a worker pod, not a PostgreSQL pod. A local
  `make e2e SET=drain` under 30 busy processes and a 200-run integration loop passed (176 s), so the defect is not in
  trove or in the drain. The load was the self-hosted runners' CPU, shared with the dispatched nightly run 41575201390. The
  same run's `integration (shard a)` failed on a DNS error (`EAI_AGAIN`), the runner's network. Open as a runner capacity
  matter: done when `e2e on kind (push)` runs next to a nightly run without PostgreSQL probe timeouts in
  `events-trove.txt` of the cluster-state artifact (more CPU for the runners or runner labels that keep the two apart);
  a probe-timeout setting in `deploy/dev/postgres.yaml` would hide the starvation and is not the fix.
- **F-40 · The scheduled nightly run 41611487424 was red in `failover` and `ci-ha` on the runner's DNS, not on a test** (read).
  Both jobs failed in their artifact upload step: `ci-ha` with `getaddrinfo EAI_AGAIN` for the artifact service after its test
  step passed, `failover` in the step "Upload the go test -json report", whose log is no longer retrievable (`BlobNotFound`), so
  its test result is unconfirmed. `npm-ha` in the same run is F-38. The same DNS error is part of F-37. Test and Release run
  41637173366 lost `e2e-nuget on kind (nuget)` the same way, in "Set up job", when the runner could not download
  `docker/login-action`. Machine-level (the self-hosted runners' network and resolver). Done when two scheduled nightly runs pass their upload steps, or the `failover` test is shown
  red in a run whose log is kept, which makes it a test finding of its own.
  The dispatched nightly run 41657499686 (2026-11-03, phase 6) was red in five legs on the same machine level: `ci-ha` and
  `bastion-long` before any test, on a chart repository unreachable over IPv6 (`dial tcp [2606:50c0:8001::153]:443`) and an
  interrupted pull of `mcr.microsoft.com/dotnet/sdk`; the logs of `npm-ha`, `npm-ha-soak` and `bastion` are not retrievable
  (`BlobNotFound`). The Bastion's long legs have therefore not run in CI yet.
  The dispatched nightly run 41739280012 (2026-11-04) lost `e2e-bastion (bastion)` the same way, on an interrupted pull of
  `mcr.microsoft.com/dotnet/sdk:9.0.320` and `getaddrinfo EAI_AGAIN`.
- **F-38 · `npm-ha` and `npm-ha-soak` were red in the nightly run 41575201390** (read). The npm load under drains and
  failovers is the phase 7 item "npm load in the drain and failover tests reliably green"; `npm-ha` passed in run 41568514247
  hours before. The dispatched nightly run 41739280012 (2026-11-04) was red in `npm-ha` again, in the drain variant A and the
  PostgreSQL switchover case under the npm load. Done with that item.
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
  does not change the machine.
