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
5. Open findings: plan every finding of [section 8](#8--open-findings), and every task of
   [section 9](#9--owner-review-of-2026-11-06-tasks-and-open-questions) in its order, as a work package of this run,
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

Found on 2026-11-06 while building the upgrade tests of phase 6a and phase 7:

### Product

- **F-66 · The object-store outage of 30 s under the npm load stalls the `packages` replicas since this run's changes** (confirmed from
  the job log of the dispatched nightly run 42258656348, leg `e2e-npm (ha-suite)`, release v0.20.2; cause not established).
  `TestHASuite_ObjectStoreOutage` (30 s): `/readyz` of both `packages` pods answered non-200 11 and 2 times and `/healthz` 10 and 2 times
  during the outage, and 2,094 k6 requests failed other than as a tarball `503`, the first ones `request_timeout` after 45 s, all
  starting within 0.6 s (17:19:05.0 to 17:19:05.6); every npm command passed (188, 0 failed), the uploads were refused before the body.
  In the dispatched nightly run 42211913572 (release v0.19.5) the same subtest had no non-200 `/readyz` or `/healthz` at all. The 300 s
  outage, node loss and both primary losses behaved as before. Changed between the two runs on the object store's paths: the read of a
  lost object and the recheck of a `missing` mark (F-62, Q-STOR-151, Q-STOR-153), the shared UI asset store (F-58, not on `packages`),
  the lease sessions' `idle_session_timeout` (F-65). Not known: which of them stalls the replicas. The run's artifact
  `e2e-npm-cluster-state-ha-suite` has the pods' logs. Done when the cause is named and fixed and the set passes in a nightly run.
- **F-63 · `TestMirror_R_REPO_055_ClientsFirst/npm` missed its install-time bound in the scheduled nightly run 42194413882** (confirmed from
  the run's pod logs, 2026-11-06, release v0.19.4). `npm ci` with a pass running took 161 % of the best install without it (22.6 s against
  14.0 s, the bound 120 %, R-REPO-055, Q-REPO-197). Cause: the leg ran on a GitHub-hosted runner (4 vCPUs for the cluster, both stubs and the
  clients); the upstream fetches took 122 ms at the median with and without a pass, and what grew in the two slow pairs was trove's own work around
  them (storing and answering: 12 to 26 s against 82 s summed over 707 tarballs), the pass's CPU taken from the installs. The overlap with another
  run that was suspected first does not apply to that class (each hosted job has its own VM). Fixed in CI: the nightly row `mirror` runs on
  `large` since [Q-REPO-211](../spec/repositories-and-proxy.md#q-repo-211--the-nightly-install-time-ratio-of-clients-first-runs-on-the-large-class).
  The dispatched nightly run 42226311597 (2026-11-06, `large`) passed it: 96 % (pairs 90 %, 133 %, 96 %; the open requests within the
  bound, the pre-emptions grew); one pair of three above the bound on `large` too, so the scheduled nightly run is the second reading.
  The dispatched nightly run 42258656348 (release v0.20.2, `large`) missed it: 131 % (pairs 16.4/16.8 s, 14.3/19.5 s, 12.8/12.7 s), so by
  Q-REPO-211 this is a product finding now: the pass slows the installs on a 16-CPU runner too. Next: split the phases as Q-REPO-211 did,
  from that run's artifact `e2e-mirror-cluster-state-mirror`. Done when the cause is named and the bound holds.

Found on 2026-11-04 while cutting trove into task areas (phase 6a):

### Tests and tools

- **F-50 · A single request of `TestE1_14Exposure` ended in a transport `EOF` at the edge** (read, from the log of the `push` leg of
  Test and Release run 41871333853, on a GitHub-hosted runner; not reproduced locally). With Valkey stopped, `GET
  https://packages.trove.localhost:8080/files/generic-local/internal/x.bin` returned `EOF` instead of the `404`; the same subtest passed
  in run 41864646902 on the same code. The e2e clients set `DisableKeepAlives` (`newAPI` in `test/e2e/m1_env_test.go`), so every
  request is a fresh TLS connection through the kind port mapping and the edge, and Go retries none of them. Burst cases count
  status 0 in green runs too (53 and 68 of 1,000 in run 41864646902); locally the zeros of a burst came from the port-forwards to
  single replicas (18 of 1,000 in `TestE1_08RateLimits` "one replica"), not from the edge. Read on the configuration: the edge
  (HAProxy Ingress, `deploy/dev/haproxy-values.yaml`) closes an idle connection after 50 s (`timeout server`, `timeout client`) and
  runs `option http-server-close` toward trove, whose `server.idle_timeout` is 120 s, so the edge closes first and reuses no backend
  connection: no keep-alive mismatch that closes a connection without a response. `timeout http-request 5s` and `maxconn 2000` are
  the other limits a loaded runner could meet. Not known: which side closed the connection (the port mapping, HAProxy or a trove pod).
  Built for the next failure (2026-11-05): the edge writes an access line per request, also for a connection that ends before a
  request (`syslog-endpoint: stdout`, `no option dontlognull`; before, HAProxy logged nothing), with the termination state that names the
  side; the kind nodes keep 200Mi of log per container instead of 10Mi, and `deploy/dev/cluster-logs.sh` (every cluster-state step)
  keeps the logs of every `trove` pod and of the edge whole, timestamped, compressed, with the previous instance of a restarted
  container, instead of the last 500 lines. Six local runs of `TestE1_14Exposure` (three with the edge logging, 17,891 access lines
  in the three, every one with the clean state `----`) and three of `TestE1_08RateLimits` showed no `EOF` at the edge. Done when a
  failed run's artifact names the closing side and the cause is fixed or shown to lie outside trove; remove the entry then.
  Checked on 2026-11-06: the logs of the 26 Test and Release runs since 2026-11-05 (42035411725 to 42207790533) hold no `EOF` at the
  edge; `TestE1_14Exposure` passed in the `push` leg (42207790533 among them). Their `EOF` lines are expected negative cases of the unit
  tests, Valkey's replication messages, `EOF`s on port-forwards that were repeated (the `vuln` and `mirror` legs), and two failures of
  `e2e-vuln (vuln)` in run 42160309458 (04:22 and 04:23 UTC, `vuln_npm_test.go:126` and `vuln_replicas_test.go:81`) whose request went
  through the port-forward of one `packages` pod (`apiAtPod`), not through the edge, and was not repeated at that commit (ee274214); both
  call sites repeat over a new forward now (`getOverForward`, `safeOverForward`). No run named a closing side yet.

Found on 2026-11-03 while building phase 6:

### Tests and tools

- **F-43 · The bounds V-4 and V-7b of R-VULN-028 are missed on loaded runners, and a run on the reference environment is
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
  so the fix is shown by the benchmark and not by the end-to-end time. The dispatched nightly run 41902592855 on a GitHub-hosted
  runner (no CPU limit): V-1 3.5 min, V-2 0.02 min, V-3 0.10 min, V-5a, V-5b 0.83 transactions, V-6 83 ms, V-7a 24 ms and V-9 0.33 GiB
  met; V-4 7.7 ms (the exact p99 of 300 publishes, median 2.2 ms) against 5 ms, V-7b 329 ms against 300 ms and V-8 52 ms against
  25 ms missed. The scheduled nightly run 42002617340 on the same code: V-4 2.98 ms (median 1.22 ms) and V-7b 205 ms met, so their
  misses of 41902592855 were the hosted runner's spread; V-8 32.8 ms missed again; V-1 did not finish within 40 min (the npm import
  never reached `complete`, the second time after 41748522448; a run that ended on a lost lease, a panic or a cancel recorded nothing,
  fixed with Q-VULN-80), and the gate reported only V-8 because a scenario that did
  not finish left no row (fixed: Q-VULN-79, a scenario that does not finish is reported as missed and what needs it as not measured).
  V-4 and V-7b are to be read on a run on the reference environment (H-491). Done when that run reports all nine
  within their targets, or a decision revises a bound with the measured cost (the next steps for V-7b are named in Q-VULN-77).
  Every run above had a NuGet dump of 228,403 records instead of 7,403 (the test source kept the filler records numbered 10,000 and
  above when the NuGet filler was lowered; fixed 2026-11-05): V-1 included their import, V-8 served a page of 28,569,143 bytes
  (computed) instead of 903,143, and V-9 counted 459,415 advisories. The first run with the fixed dump (integration stack, 2026-11-05,
  load average about 12): V-1 2.25 min, V-8 1.6 ms, V-9 0.19 GiB for 238,415 advisories, all met; V-4 15.5 ms missed under that load.
  The runs above also found 9,000 findings, not 10,000: the NuGet packages were seeded under mixed-case names, which the lower-cased
  advisory names never match (fixed 2026-11-05, the seed stores the lower-cased ID as a push does); the same run found 10,000 and
  measured V-2 at 0.69 min and V-7b at 213 ms.
  The dispatched nightly run 42126196109 (2026-11-05, phase 6b's closing commit, the self-hosted class `large`, the fixed dump): V-1 1.5 min, V-2
  0.72 min, V-3 0.10 min, V-5a, V-5b 0.92 transactions, V-6 62 ms, V-7a 20 ms, V-8 3.3 ms and V-9 0.19 GiB met; V-4 9.7 ms (median
  2.5 ms) against 5 ms and V-7b 340 ms against 300 ms missed, as in 41902592855.
  Worked on 2026-11-06 ([Q-VULN-81](../spec/vulnerability-scanning.md#q-vuln-81--a-scope-whose-decision-does-not-look-at-the-version-decides-each-package-once-and-the-statements-compare-the-package),
  [Q-VULN-82](../spec/vulnerability-scanning.md#q-vuln-82--the-commit-hooks-evaluation-reads-in-one-round-trip-and-writes-its-claimed-name-matches-in-a-second-through-a-pipeline-over-the-generated-statements)).
  V-7b: the first page of a pattern grant spent its time reading and deciding every version's triple and planning an 11,000-element array
  (50 ms of planning for 2 ms of execution); a scope whose decision does not look at the version (`authz.VersionIndependent`) now decides
  each (repository, package) once: 160 ms to 59 ms interleaved at the run's shape, and V-7b p99 75 to 148 ms in five whole runs on the
  integration stack (load 5 to 25). V-4: a publish's evaluation made 8 sequential round trips of 0.42 to 0.70 ms; it makes 2 now
  (`q.Pipeline`): V-4 p99 4.6 ms (median 1.32 ms) in the last whole run at load 8 to 13, 5.8 ms in the run before; the margin is thin. The
  perf jobs of the nightly run after its kind legs since Q-ARCH-325. Done when two nightly runs in a row meet all nine, or a decision
  revises V-4 with the measured floor (Q-VULN-82 names the next step). V-1 met its statement timeout under load: the advisory
  group statement was planned as a sequential scan and read the aliases index's pending list in full; fixed with Q-VULN-90 and Q-VULN-91
  (V-1 1.45 min on the integration stack, the whole run passed, V-5b 1.036 transactions against 1).
  The dispatched nightly run 42211913572 (release v0.19.5, perf after the kind legs, Q-ARCH-325) met ten of eleven: V-1 1.01 min (F-57
  fixed: the import's statements by primary key, no planner statistics on `ecosystem`, Q-VULN-95 to Q-VULN-97), V-2 0.65 min, V-3 0.10 min,
  V-4 3.18 ms (median 1.02 ms), V-5a 0.81 ms, V-6 125 ms, V-7a 21 ms, V-7b 112 ms, V-8 2.5 ms, V-9 0.19 GiB; V-5b missed with 1.065
  transactions per cached download against 1 (0.83 to 0.94 in earlier nightly runs, 1.036 once on the integration stack).
  V-5b's cause, established on 2026-11-06: the measurement, not the downloads. A run on the integration stack with `log_statement` on for
  its database showed every cached download running exactly one transaction (`BEGIN`, `GetLiveItem`, `COMMIT`), 4,000 for 4,000. V-5b was
  the commits of the whole database over the window minus an idle window: the health checks, registration and change-log loops of three
  replicas, the worker's scheduler tick (15 inserts every 10 s), the batched writers and the autovacuum, and PostgreSQL publishes a
  session's counters up to 10 s after its transactions (a burst of 2,000 commits appeared 10.5 s later, all at once), so transactions of
  V-4's publishes before the window and of the last downloads fell into the wrong window; in 42211913572 the window began 1.4 s after the
  last of 300 publishes of about 7.6 transactions each (read from the replicas' logs, not verified by a statement log there). Fixed with
  [Q-VULN-98](../spec/vulnerability-scanning.md#q-vuln-98--v-5b-is-counted-in-the-replicas-by-request-not-from-the-databases-commit-counter)
  and [Q-VULN-99](../spec/vulnerability-scanning.md#q-vuln-99--v-5b-is-the-largest-count-of-a-download-after-the-refills-r-arch-027-exempts-not-the-mean-of-the-transactions-of-a-window):
  V-5b is counted in the replicas by request ID; after the refills R-ARCH-027 exempts (the token check's caches, by name) every download
  must run exactly one statement, and the value is the largest, bound 1 unchanged; a statement off the list fails the run with its name
  and the download. Local runs on the integration stack, V-5b and in brackets the old database-wide count of the same window: 1 (0.909),
  1 (1.050), 1 (0.806), 1 (0.803; that run met all eleven values with the gate on), every download exactly 1 after one to three refills
  per replica; earlier the same day, read with a p99, 1 (0.956), 1 (0.859), 1 (1.051), 1 (0.806), 1 (0.930), and the base commit's
  measure 0.983 and 0.767. With a cost added to the lookup on
  purpose (not committed) it read 2 and named the downloads: a second statement in every download (2,000 named; old count 0.949), a
  transaction of its own in one download of 25 (80 named; old count 0.875) and a second statement in one download of 500 (4 named; old
  count 0.971; a p99 would have read 1), all passes of the old measure. Under the shared host's load six local runs missed V-4 (5.0 to
  12.1 ms at load averages of 10 to 120; the base commit's code missed it too, 5.4 ms, so it is the thin margin named above, not the
  counter), two missed V-5a (1.12 ms beside three concurrent runs, 7.3 ms in a window whose p99 latencies were 46 to 53 ms), and in one
  at a load average of 110 to 123 a download from the replica without the scanning answered `503 authz_stale` (its grant index unconfirmed
  within `authz.cache.max_staleness`, R-AUTHZ-028), so V-5 did not finish there; none of these was seen on a CI runner. Left: two nightly
  runs in a row with every value met.

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
  Checked on 2026-11-06: none of the 26 Test and Release runs since 2026-11-05 (42035411725 to 42207790533) overlapped a nightly run
  in time (each nightly run since 42126196109 was created 27 to 78 s after the Test and Release run before it ended, or after it), so
  the criterion has no run to read yet; `e2e on kind (push)` passed in every completed run (on `large`, once on a GitHub-hosted
  runner in 42035411725), and no run has an `e2e-cluster-state-push` artifact. The two workflows share the runner class `large`
  (`e2e on kind (push)`; the nightly `e2e`, `e2e-npm`, `npm-ha-soak`, `e2e-nuget`, `latency`, `vuln-perf` and `mirror-long`), and their
  concurrency groups are per workflow, so nothing but timing keeps them apart.
- **F-40 · The scheduled nightly run 41611487424 was red in `failover` and `ci-ha` on the runner's DNS, not on a test** (read).
  Both jobs failed in their artifact upload step: `ci-ha` with `getaddrinfo EAI_AGAIN` for the artifact service after its test
  step passed, `failover` in the step "Upload the go test -json report", whose log is no longer retrievable (`BlobNotFound`), so
  its test result is unconfirmed. `npm-ha` in the same run was F-38 (closed with the phase 7 item on 2026-11-06). The same DNS error is part of F-37. Test and Release run
  41637173366 lost `e2e-nuget on kind (nuget)` the same way, in "Set up job", when the runner could not download
  `docker/login-action`. Machine-level (the self-hosted runners' network and resolver). Done when two scheduled nightly runs pass their upload steps, or the `failover` test is shown
  red in a run whose log is kept, which makes it a test finding of its own.
  The dispatched nightly run 41657499686 (2026-11-03, phase 6) was red in five legs on the same machine level: `ci-ha` and
  `bastion-long` before any test, on a chart repository unreachable over IPv6 (`dial tcp [2606:50c0:8001::153]:443`) and an
  interrupted pull of `mcr.microsoft.com/dotnet/sdk`; the logs of `npm-ha`, `npm-ha-soak` and `bastion` are not retrievable
  (`BlobNotFound`). The Bastion's long legs have therefore not run in CI yet.
  The dispatched nightly run 41739280012 (2026-11-04) lost `e2e-bastion (bastion)` the same way, on an interrupted pull of
  `mcr.microsoft.com/dotnet/sdk:9.0.320` and `getaddrinfo EAI_AGAIN`.
  The scheduled nightly run 42002617340 (2026-11-05, GitHub-hosted runner) lost `e2e-bastion (bastion)` after 11 minutes inside the
  step `make e2e SET=bastion`: no step after it has a conclusion and the job's log is not retrievable (`BlobNotFound`), so the
  runner was lost, not a test; `bastion-long` passed in the same run. The job's check-run annotation (read on 2026-11-06) names the
  cause: `System.IO.IOException: No space left on device` for the runner's own diagnostics log, so the hosted runner's disk filled
  during the set.
  Checked on 2026-11-06: of the scheduled runs since this finding, 41804996351 (2026-11-04) and 42194413882 (2026-11-06, 53 upload
  steps) passed every upload step, and 42002617340 between them did not run the upload steps of `e2e-bastion (bastion)` (above). Read
  as two scheduled runs in a row, the criterion is not met yet: the next scheduled run decides.
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

## 9 · Owner review of 2026-11-06: tasks and open questions

The owner reviewed the UI, the documentation and the domain model with the development agent on 2026-11-06, on the branch
`ui-review` (worktree `/Users/hfi/repos/trove-ui-review`). What was decided is recorded in the spec; what is left is below. The
tasks come before the phase's own work packages (step 2, item 5), in this order; remove a task in the commit that finishes it.

### Build what the owner decided (spec first, then code)

- **T-8 · Storage per project** ([Q-STOR-155](../spec/storage.md#q-stor-155--a-project-is-charged-the-logical-size-of-what-it-holds),
  gap H-547): a project shows and is charged the logical size of the files it holds; the admin dashboard keeps the physical
  figure of the store.
- **T-12 · The logo's open point** ([design-language.md](../developer/design-language.md#logo)): `web/public/brand/drafts/` and
  the preview on the sign-in page removed once the owner calls the logo final. The reduced form for the favicon and the
  wordmark as paths are built ([Q-UI-145](../spec/api-and-ui.md#q-ui-145--the-logos-wordmark-as-paths-from-inter-bold-and-a-reduced-mark-as-the-favicon)).

The owner answered every open question of the review on 2026-11-06; a new question during the run is asked one at a time, with
options and a recommendation, and what depends on it is not built before the answer.

### Housekeeping of the review session

- **T-14 · The shared browser window.** The review read the owner's GitLab through a separate Chrome for Testing window with a
  debugging port on `127.0.0.1:9333` and its own profile, which holds the owner's GitLab session:
  `/private/tmp/claude-501/-Users-hfi-repos-trove-ui-review/1ac9eb74-ba5b-44d7-bd76-154bcdbbefa7/scratchpad/gitlab-profile`. When
  the owner has finished comparing, close the window and delete the profile (ask first: it is the owner's session).

