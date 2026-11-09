#!/usr/bin/env bash
# End-to-end tests of the dev cluster (WP0.11, WP1.x): E0.3, E0.4, E0.6, E0.7, E0.11 to E0.16 and, for the access core,
# E1.1, E1.8, E1.11 (API part), E1.13 and E1.14.
# The trove release must be installed (make dev-up). The suite is Go (test/e2e, build tag e2e); this
# script prepares what it needs and runs it:
#
#   1. builds the driver (test/e2e/cmd/driver) and loads its image into the cluster,
#   2. starts the driver pod next to the release (same ConfigMap, Secrets and security contexts as a
#      worker pod; it only writes changes and enqueues jobs, it claims nothing),
#   3. checks that the pinned k6 image is present,
#   4. runs `go test -tags e2e`, writes the machine-readable report to .out/test-e2e.json (TROVE_E2E_REPORT names another file below .out/) and the
#      artifacts (k6 logs and summaries, pod watch, events, driver output) to .out/e2e/.
#
#   e2e.sh              every test except the profile switch of E0.11 (the default profile)
#   e2e.sh push         the set CI runs on every change: E0.3, E0.4, E0.6, E0.7, E0.12, E0.14 (with the upload and download
#                       loop of E1.13), E0.15, E0.16, the exposure of E1.14, then E0.11
#   e2e.sh full         everything, in the order of the milestone, ending with E0.11
#   e2e.sh failover     the E0.13 failover set and the limits of E1.8 (CI: nightly and on dispatch, HA and Performance)
#   e2e.sh access-long  the sign-in of E1.1, the replica scatter of E1.11, the 500 MB transfers of E1.13 (CI, every change)
#   e2e.sh access       the M1 tests alone: E1.1, E1.8, E1.11, E1.13, E1.14 (they switch to the scatter profile and back)
#   e2e.sh drain        E0.14 only (this is `make dev-drain`)
#   e2e.sh drain-roles  the drain per role of R-HA-059 (E6a.6): api, packages, the worker and the pool vuln evicted pod by pod under load,
#                       the npm load of npm-ha among it (three client pods, the counting test upstream, the scatter profile; Q-HA-94)
#                       (CI: job e2e of the nightly HA and Performance workflow; a long leg)
#   e2e.sh clients      the smoke test of the npm client pods (R-NPM-045: every gate row runs --version, the base URL
#                       reaches the cluster); builds the clients image and starts the pods, which stay up
#   e2e.sh clients-down removes the client pods
#   e2e.sh npm-ha       drain variant A and the failovers of R-HA-052 under the npm load (E2.11)
#   e2e.sh npm-ha-crash the pod crash of R-HA-052 under the npm load, three force-deletes (E2.11)
#   e2e.sh npm-ha-soak  the pod crash with ten force-deletes over 20 minutes, then the npm load scripts of R-ARCH-027
#                       (perf/k6/npm.js, reported); the nightly Performance workflow runs it. npm-ha-closing runs npm-ha,
#                       npm-ha-soak and the Valkey outage in one go
#   e2e.sh ha-suite    the failure scenarios of R-HA-052 that npm-ha does not run, under the npm load (TestHASuite_...): the audit forward of
#                       R-NPM-038 cut by a pod deletion, the object-store outage (30 s, and 5 min with a replica deleted), the PostgreSQL
#                       and the Valkey primary loss and the loss of a node (docker stop); the scatter profile, three client pods, the counting
#                       npm test upstream (nightly, dispatch, locally)
#   e2e.sh cold-crash   a packages replica force-deleted during a cold `npm ci` and a cold `dotnet restore` of three clients each (E5.2,
#                       TestColdCrash), then `trove admin storage verify` in a pod of the installation; the scatter profile, three npm and
#                       three NuGet client pods, both counting test upstreams (nightly, dispatch, locally)
#   e2e.sh ha-faults   the failure scenarios E5.10 adds, under the same npm load (TestHAFaults_...): the database at 0 instances for 2 minutes, the
#                       Sentinel quorum loss, FLUSHALL and a slow Valkey of R-HA-057 and a Valkey credential mismatch
#                       (R-HA-007); the scatter profile, three client pods, the counting npm test upstream (nightly, dispatch, locally)
#   e2e.sh ha-pooler   an instance of the request pooler and of the control pooler deleted, gracefully and forced, under the npm load (TestHAPooler_...,
#                       R-HA-058, R-HA-007): the scatter profile with the poolers (TROVE_POOLER=1); the cluster stays in the pooler overlay until the next `make dev-up`
#   e2e.sh nuget-clients  the smoke test of the NuGet client pods (R-NUGET-027: dotnet and nuget.exe run, the base URL reaches the
#                       cluster with a verified certificate); builds the clients image and starts the pods, which stay up
#   e2e.sh nuget-clients-down  removes the NuGet client pods
#   e2e.sh nuget        the NuGet acceptance tests (every TestNuGet... except TestNuGetClosing... and TestNuGetHA...), the counting
#                       NuGet test upstream and three client pods; CI: job e2e-nuget
#   e2e.sh nuget-closing  TestNuGetClosing...: the additions of the run that closes the phase (CI: job e2e-nuget, closing=true)
#   e2e.sh nuget-ha     TestNuGetHA...: drain variant A and the failovers of R-HA-052 under the npm and NuGet load together
#   e2e.sh drain-load-b, drain-load-c, drain-load-e  one variant of TestDrainLoad each (CI)
#   e2e.sh drain-load   TestDrainLoad: drain variants B, C and E of R-HA-059 under that load, the long-transfer probes and R-STOR-046's gc-full (E5.9)
#   e2e.sh drain-load-pooler  TestDrainLoadPooler: drain variant D (an instance of the request pooler) under the same load, in the pooler profile
#                       (CI: job e2e-nuget in the nightly HA and Performance workflow); starts the npm and the NuGet client pods
#                       and both counting test upstreams. A set may name no test (nuget-closing today): then it passes with a notice (Q-ARCH-276)
#   e2e.sh ci           the reference pipeline of GitLab CI OIDC (TestCI..., not TestCIHA...) against the issuer double with the npm and
#                       NuGet client pods, E4.1 and E4.4 to E4.6; CI: job e2e-ci
#   e2e.sh ci-ha        TestCIHA...: the pipeline under a drained node, with Valkey stopped, across a forced Sentinel failover and FLUSHALL,
#                       and under a deleted api pod, E4.7 (CI: job e2e-ci in the nightly HA and Performance workflow)
#   e2e.sh bastion      the short scenarios of R-BAS-049: the chart trove-bastion (volume backend; bucket backend with Valkey) in
#                       front of the main installation, npm, pnpm, Yarn 4 and dotnet restore from the client pods; CI: job
#                       e2e-bastion (a change to the Bastion) and the nightly HA and Performance workflow
#   e2e.sh bastion-long the long scenarios of R-BAS-049: the drain, the failure suite under client load (a replica lost, the main
#                       installation failing over, a rolling upgrade of either side), the flood and the anonymous flood (nightly, dispatch, locally)
#   e2e.sh vuln         the short scenarios of R-VULN-029: the chart in the `vuln` profile (three api and three packages replicas, the main worker and the pool vuln with two replicas each, the scatter
#                       edge, the scanning reading the test advisory source) with the npm, pnpm, Yarn 4 and dotnet clients, the audits,
#                       the blocked installs and restores, the replicas with Valkey stopped and the browser journeys; CI: job e2e-vuln
#                       (a change to the scanning) and the nightly HA and Performance workflow
#   e2e.sh vuln-long    the long scenarios of R-VULN-029: a full synchronization drained mid-run and one whose worker is killed, the client tests once
#                       more with Valkey stopped, a PostgreSQL switchover during an evaluation, and the block of four versions through a node
#                       drain and the failovers of Valkey and PostgreSQL, through drain variants B, C and E, and the restore drill of the vulnerability
#                       data (E5.13; nightly, dispatch, locally)
#   e2e.sh vuln-pooler  the block of a malicious version through drain variant D (E5.13): the `vuln` profile with the poolers of R-HA-058
#                       (TROVE_POOLER=1), no client pods; the cluster stays in the pooler overlay until the next `make dev-up`
#                       (CI: job e2e-vuln, nightly)
#   e2e.sh mirror       the short scenarios of R-REPO-059: the chart in the `mirror` profile (three api and three packages replicas, the main worker
#                       and a pool of two processes that claim the queue `mirror` alone, the scatter edge), npm and NuGet remotes in mode mirror
#                       over the counting test upstreams (the npm one also under the names of the public registry, through a CoreDNS rewrite
#                       the tests make and undo), `npm ci` and `dotnet restore` through the same remotes while a pass runs, the status from
#                       every replica, the dispatch of two processes, a pass with Valkey stopped and a restore with the upstream stopped;
#                       CI: job e2e-mirror (a change to the mirror)
#   e2e.sh mirror-long  the long scenario of R-REPO-058: `kubectl drain` of the node of the process that runs a pass of 300 packages
#                       (nightly, dispatch, locally: a red long leg is a finding, not a blocker, owner 2026-11-01)
#   e2e.sh oci          the OCI acceptance tests (every TestOCI... but TestOCIClosing... and TestOCILong...): the counting OCI test upstream and three
#                       client pods, each with the Docker Engine (Buildx) and Podman in a privileged pod of the namespace trove-e2e-clients; the scatter
#                       profile; login, push and pull through local, remote (pull-through and mirror), virtual and bound-host repositories, multi-platform
#                       images, an image pull secret, the setup snippets; CI: job e2e-oci
#   e2e.sh oci-closing  TestOCIClosing...: the additions of the run that closes the phase (CI: job e2e-oci, closing=true)
#   e2e.sh oci-long     TestOCILong...: a push whose upload is cut by the drain of a packages pod (R-OCI-011, R-HA-052); nightly, dispatch, locally
#   e2e.sh oci-clients  the smoke test of the OCI client pods (the pinned Docker Engine and Podman run, the CA is trusted, the registry answers);
#                       builds the client images and starts the pods, which stay up
#   e2e.sh oci-clients-down  removes the OCI client pods and their namespace
#   e2e.sh deb          the Debian (APT) acceptance tests (every TestDeb... and TestAPTClients): the counting Debian archive test upstream and one client pod per image
#                       (Debian 13, Debian 12, Ubuntu 24.04, Ubuntu 22.04, each a real APT in the namespace trove-e2e-apt); the scatter profile (three packages
#                       replicas, two workers); apt-get update --error-on=any and apt-get install through local, remote, mirror and virtual repositories,
#                       an anonymous read, a wrong keyring, the key rotation of R-DEB-017 and the flat root; CI: job e2e-deb
#   e2e.sh deb-closing  TestDebClosing...: the additions of the run that closes the phase (CI: job e2e-deb, closing=true): uploads, update and install
#                       through local, remote and virtual repositories with Valkey stopped, and the read-only check against https://deb.debian.org/debian/
#   e2e.sh apt-clients  the smoke test of the APT client pods (every image runs the release and the verifier its pin names, the CA is trusted, the
#                       edge answers); builds the four images and starts the pods, which stay up
#   e2e.sh apt-clients-down  removes the APT client pods and their namespace
#   e2e.sh backup       the backup and restore suite (R-BAK-029, E5.11, E5.12): a backup during a publishing, deleting and collecting load restored into
#                       the namespace trove-restore, the drain and kill -9 of a backup and of a restore, the journal across a restore with E5.12's CI
#                       bullets (the issuer double, TROVE_E2E_OIDCSTUB=1) (nightly, dispatch, locally)
#   e2e.sh backup-pitr  the point-in-time restore drill (R-HA-025, E5.1): the platform layer of R-HA-023 (TROVE_PITR=1) recovered into a new PostgreSQL
#                       cluster up to a time t, the post-restore Job with the journal replay, the object versions rolled back (nightly, dispatch, locally)
#   e2e.sh upgrade      the upgrade tests (E6a.7 and R-HA-020): the cut into roles in the two steps of R-HA-064 from v0.16.0, the rolling
#                       upgrade and rollback from the latest release, and the rolling upgrade of the working tree from its second UI
#                       bundle (make image-marked) to the image under test, each under npm, NuGet and browser loops; it replaces the
#                       installation and returns the cluster to the current dev release (nightly, dispatch, locally)
#   e2e.sh release-smoke the scenarios of the post-release check (E5.8, Q-HA-165): the client smoke test and the matrix of E2.1 against the
#                       published chart and image (TROVE_RELEASE_VERSION and TROVE_RELEASE_DIGEST set, see deploy/dev/up.sh); CI: job smoke-published
#   e2e.sh edge         R-HA-039 through the chart's Ingress and through an HTTPRoute on a platform Gateway (Envoy Gateway, profile `edge`): a 6 GiB upload, a
#                       download of 17 minutes at 2 MiB/s, a 1 GiB NuGet push and the scoped npm package, and R-HA-038's criteria (E5.5); nightly, dispatch, locally
#   e2e.sh alerts       the alerts of R-HA-045 in their R-HA-052 scenarios against the Prometheus of the profile `alerts` (R-OPS-045, R-OPS-046, E5.5): Valkey
#                       outage, failover flapping, a replica cut off from Valkey, a role below two replicas, a failed and an overdue backup, and "Maintenance
#                       stays quiet" (a node drain, a planned failover, a rolling restart); nightly, dispatch, locally
#   e2e.sh <regex>      any other argument is a `go test -run` expression
#
# Environment: TROVE_DEV_HTTP_PORT (the port the cluster was created with; read back from the node),
# TROVE_E2E_OUT (artifact directory, default .out/e2e), TROVE_E2E_TIMEOUT (go test timeout, default 90m),
# TROVE_E2E_DRIVER_BIN (a prebuilt linux driver binary instead of building one),
# TROVE_E2E_SCATTER_RETURN=0 (leave the cluster in the scatter profile after E0.11 and the M1 tests).
# The npm client pods (set clients): TROVE_E2E_NPM_CLIENTS_PODS (how many, default 1), TROVE_E2E_NPM_CLIENTS_TARGET
# (host:port the forwarder in each pod relays to, default the in-cluster HAProxy Ingress HTTPS port),
# TROVE_E2E_NPM_CLIENTS_IMAGE_BUILD=0 (use the image already built locally instead of rebuilding it).
# TROVE_E2E_REUSE_CLIENTS=1 keeps client pods that are already running in the right number (a test under development).
# The NuGet client pods (sets nuget*): TROVE_E2E_NUGET_CLIENTS_PODS, TROVE_E2E_NUGET_CLIENTS_TARGET and
# TROVE_E2E_NUGET_CLIENTS_IMAGE_BUILD=0 as for npm; TROVE_E2E_NUGETSTUB=1 deploys the counting NuGet test upstream.
# The APT client pods (the set deb): TROVE_E2E_APT_CLIENTS_TARGET and TROVE_E2E_APT_CLIENTS_IMAGE_BUILD=0 as for npm; TROVE_E2E_DEBSTUB=1
# deploys the counting Debian archive test upstream.
# The OCI client pods (sets oci*, mirror*): TROVE_E2E_OCI_CLIENTS_PODS (default 3), TROVE_E2E_OCI_CLIENTS_TARGET and
# TROVE_E2E_OCI_CLIENTS_IMAGE_BUILD=0 as for npm; TROVE_E2E_OCISTUB=1 deploys the counting OCI test upstream.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"
for tool in docker jq go helm kubectl; do command -v "$tool" >/dev/null 2>&1 || die "$tool is required"; done

set_name="${1:-default}"
case "$set_name" in
  default) run='^Test(E03|E04|E06|E07|E12|E13|E14|E15|E16)' ;;
  push) run='^Test(E03|E04|E06|E07|E12|E14|E15|E16|E1_14|E99)' ;;
  full) run='^Test(E03|E04|E06|E07|E12|E13|E14|E15|E16|E1_|E99)'; export TROVE_E2E_OIDCSTUB=1 TROVE_E2E_NPMSTUB=1 ;;
  failover) run='^Test(E13|E1_08)' ;;
  # the journeys include 13-ci, which registers the issuer double and exchanges an ID token (docs/developer/e2e.md#the-ci-tests), and 14-mirror, which types the address of the npm test upstream into the wizard (TROVE_E2E_NPMSTUB_HOST)
  access-long) run='^TestE1_(01|11|13)'; export TROVE_E2E_OIDCSTUB=1 TROVE_E2E_NPMSTUB=1 ;;
  access) run='^TestE1_'; export TROVE_E2E_OIDCSTUB=1 TROVE_E2E_NPMSTUB=1 ;;
  drain) run='^TestE14' ;;
  # The drain per role runs the npm load of npm-ha beside its own (Q-HA-94): the client pods and the test upstream of the npm sets.
  drain-roles) run='^TestDrainRoles$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 ;;
  # >>> M2 npm-clients
  clients) run='^TestNPMClients$'; NPM_CLIENTS=1 ;;
  clients-down) NPM_CLIENTS=down ;;
  # The npm acceptance matrix and the criteria E2.1 to E2.12 (test/e2e/npm_*_test.go): the counting test upstream,
  # three client pods (one per packages replica), the scatter profile. CI runs it as the job e2e-npm on every change.
  npm) run='^TestNPM(Clients|Matrix|Replicas|Collapse|Upstream|Endpoint|Project)'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 ;;
  # The additions of the run that closes the phase: the matrix again with Valkey stopped, the read-only subset against
  # the public registry. Started by the workflow only when it is dispatched with closing=true (Q-NPM-46).
  npm-closing) run='^TestNPMClosing'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NPM_CLOSING=1 ;;
  # The post-release check (E5.8, docs/developer/releasing.md#the-post-release-check): the client smoke test and the E2.1 matrix, nothing
  # else of the npm set (Q-HA-165). The installation is the published chart and image; the profile switches the matrix makes run up.sh
  # with the same TROVE_RELEASE_* variables, so they do not replace it with a local build.
  release-smoke)
    # Without both variables the switches would install the working tree, and the matrix would pass against the wrong artifact.
    [ -n "${TROVE_RELEASE_VERSION:-}" ] && [ -n "${TROVE_RELEASE_DIGEST:-}" ] || die "the set release-smoke needs TROVE_RELEASE_VERSION and TROVE_RELEASE_DIGEST (the published mode of deploy/dev/up.sh)"
    run='^TestNPM(Clients|Matrix)$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 ;;
  # <<< M2 npm-clients
  # >>> M3 nuget-clients
  # The NuGet client pods (R-NUGET-027, docs/developer/e2e.md#the-nuget-clients): the smoke test of the clients alone, and
  # the NuGet acceptance tests (test/e2e/nuget_*_test.go, every test named TestNuGet...) with the counting test upstream and
  # three client pods. The closing and nightly tests (TestNuGetClosing..., TestNuGetHA...) are sets of their own and are
  # skipped by `nuget` (Q-NUGET-34). CI runs `nuget` as the job e2e-nuget on every change.
  nuget-clients) run='^TestNuGetClients$'; NUGET_CLIENTS=1 ;;
  nuget-clients-down) NUGET_CLIENTS=down ;;
  nuget) run='^TestNuGet'; skip='^TestNuGet(Closing|HA)'; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NUGETSTUB=1 ;;
  # empty_ok: the set may name no test yet (Q-ARCH-276); see the check after the case statement.
  nuget-closing) run='^TestNuGetClosing'; empty_ok=1; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NUGETSTUB=1 TROVE_E2E_NUGET_CLOSING=1 ;;
  # E5.2: the cold install of three clients with a packages replica force-deleted meanwhile; both client kinds and both test upstreams.
  cold-crash) run='^TestColdCrash$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 ;;
  # The drain runs the npm load and the NuGet load together (R-HA-059): both client pods and both test upstreams, the scatter profile.
  # The scatter profile with garbage collection without a grace period (TROVE_E2E_SCATTER_PROFILE=drain, deploy/dev/values-drain.yaml): the drain
  # test starts a full collection run that has blobs to delete (R-STOR-046's drain criterion, E5.9).
  nuget-ha) run='^TestNuGetHA'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 TROVE_E2E_SCATTER_PROFILE=drain ;;
  # Drain variants B, C and E of R-HA-059 under the whole load, the long-transfer probes and the full collection run of R-STOR-046 (E5.9,
  # docs/developer/e2e.md#the-drain-under-the-full-load): TestDrainLoad. `drain-load-pooler` runs variant D in the pooler profile (R-HA-058).
  # Both start both client pods and both test upstreams; nightly, on dispatch and locally.
  drain-load) run='^TestDrainLoad$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 TROVE_E2E_SCATTER_PROFILE=drain ;;
  # One variant per cluster (CI runs them as three legs: the runner of the single leg for B, C and E was lost three times in a row, Q-HA-242).
  drain-load-b) run='^TestDrainLoad$/drain_variant_B_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 TROVE_E2E_SCATTER_PROFILE=drain ;;
  drain-load-c) run='^TestDrainLoad$/drain_variant_C_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 TROVE_E2E_SCATTER_PROFILE=drain ;;
  drain-load-e) run='^TestDrainLoad$/drain_variant_E_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 TROVE_E2E_SCATTER_PROFILE=drain ;;
  drain-load-pooler) run='^TestDrainLoadPooler$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 TROVE_E2E_SCATTER_PROFILE=drain TROVE_POOLER=1 ;;
  # <<< M3 nuget-clients
  # Drain and failover under npm load (E2.11, test/e2e/npm_ha_test.go): drain variant A of R-HA-059, the PostgreSQL
  # switchover and the Valkey failover of R-HA-052 under eight npm ci loops, a publish loop and k6 (CI: e2e-npm leg
  # npm-ha of the nightly HA and Performance workflow); the pod crash in a set of its own (npm-ha-crash, three force-deletes,
  # run by hand). The soak
  # (nightly) runs the pod crash with ten force-deletes, then perf/k6/npm.js against one replica (TestNPMPerf, reported,
  # not gated). npm-ha-closing runs all of it and adds the 5-minute Valkey outage of the full suite (closing run).
  npm-ha) run='^TestNPMHA$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 ;;
  npm-ha-crash) run='^TestNPMHACrash$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 ;;
  npm-ha-soak) run='^TestNPM(HACrash|Perf)$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NPM_CRASH_SOAK=1 ;;
  npm-ha-closing) run='^TestNPM(HA|HACrash|HAClosing|Perf)$'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NPM_CLOSING=1 TROVE_E2E_NPM_CRASH_SOAK=1 ;;
  # The failure suite under npm load (test/e2e/ha_suite_test.go, docs/developer/e2e.md#the-failure-suite-under-npm-load): the
  # scenarios of R-HA-052 that npm-ha and npm-ha-crash leave out, with the same load (eight npm ci loops, a publish loop, k6).
  # A long set (about 50 minutes): nightly, on dispatch and locally, like the closing run's Valkey outage.
  ha-suite) run='^TestHASuite_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 ;;
  # The failure scenarios E5.10 adds (test/e2e/ha_faults_test.go, docs/developer/e2e.md#the-failure-suite-ha-faults), under the same npm load: the database at
  # 0 instances for 2 minutes, R-HA-057's Sentinel quorum loss, FLUSHALL and slow Valkey, and a Valkey credential mismatch
  # (about 45 minutes). `ha-pooler` runs a pooler instance deleted, gracefully and forced, in the pooler profile (TROVE_POOLER=1,
  # R-HA-058; about 25 minutes). Both are long sets: nightly, on dispatch and locally.
  ha-faults) run='^TestHAFaults_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 ;;
  ha-pooler) run='^TestHAPooler_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_POOLER=1 ;;
  # >>> M4 ci
  # GitLab CI OIDC (R-CI-028, roadmap M4 E4.1 to E4.7, docs/developer/e2e.md#the-ci-tests): the reference pipeline's steps
  # with the real npm and .NET clients of the client pods against the issuer double, in the scatter profile. `ci` runs on
  # every change (CI: job e2e-ci); `ci-ha` is the long leg (a drained node, Valkey stopped, a forced failover, a deleted
  # pod under 50 jobs) and runs in the nightly HA and Performance workflow and by hand (owner, docs/projektplan/promt.md).
  ci) run='^TestCI'; skip='^TestCIHA'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=2; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=2; export TROVE_E2E_OIDCSTUB=1 ;;
  ci-ha) run='^TestCIHA'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=2; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=2; export TROVE_E2E_OIDCSTUB=1 TROVE_E2E_OIDCSTUB_SINGLE=1 ;;
  # <<< M4 ci
  # >>> M5b bastion
  # The Bastions of R-BAS-049 (docs/developer/e2e.md#the-bastion-tests): the main installation in the `bastion` profile, the
  # chart trove-bastion twice in the namespace trove-bastion (deploy/dev/bastion.sh), and the npm and NuGet client pods
  # outside it. `bastion` is the short scenarios (CI: job e2e-bastion on a change to the Bastion, and nightly);
  # `bastion-long` the drain, the failure suite under client load, the flood and the anonymous flood (nightly, on dispatch,
  # locally: a red long leg is a finding, not a blocker, owner 2026-11-01). TROVE_E2E_BASTION_KEEP=1 leaves the Bastions installed.
  bastion) run='^TestBastion_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=2; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=2 ;;
  bastion-long) run='^TestBastionLong_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=2; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=2 ;;
  # <<< M5b bastion
  # >>> M4b vuln
  # The vulnerability scanning (R-VULN-029, docs/developer/e2e.md#the-vulnerability-tests): the `vuln` profile (up.sh, values-vuln.yaml),
  # the three test upstreams (npm, NuGet, advisory source) and three client pods of each kind. `vuln` is the short scenarios
  # (CI: job e2e-vuln on a change to the scanning, and nightly); `vuln-long` the drain of a full synchronization and the client tests
  # with Valkey stopped, the scanning under the failure suite of E5.13 (the blocks through drain variants A, B, C and E) and the restore drill of R-VULN-026 into the
  # namespace trove-restore (nightly, on dispatch, locally: a red long leg is a finding, not a blocker, owner 2026-11-01).
  vuln) run='^TestVuln_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 TROVE_E2E_OSVSTUB=1 ;;
  vuln-long) run='^TestVulnLong_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 TROVE_E2E_OSVSTUB=1 ;;
  # `vuln-pooler` runs the blocks through drain variant D of R-HA-059 (E5.13, Q-VULN-128): the cluster in the profile `vuln` with the poolers of R-HA-058
  # (TROVE_POOLER=1, up.sh), the three test upstreams, no client pods (the tests probe through port-forwards). After a local run the cluster stays in the
  # pooler overlay until the next `make dev-up`.
  vuln-pooler) run='^TestVulnPooler_'; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 TROVE_E2E_OSVSTUB=1 TROVE_POOLER=1 ;;
  # <<< M4b vuln
  # >>> T16 oci
  # The OCI acceptance tests (R-OCI-035, docs/developer/e2e.md#the-oci-tests): the counting OCI test upstream, three client pods (the Docker Engine with
  # Buildx and Podman in the namespace trove-e2e-clients, one per packages replica), the scatter profile. CI runs `oci` as the job e2e-oci on every change
  # that touches the format or the registry root; the closing and nightly tests (TestOCIClosing..., TestOCILong...) are sets of their own.
  oci-clients) run='^TestOCIClients$'; OCI_CLIENTS=1 ;;
  oci-clients-down) OCI_CLIENTS=down ;;
  oci) run='^TestOCI'; skip='^TestOCI(Closing|Long)'; OCI_CLIENTS=1; OCI_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_OCISTUB=1 ;;
  oci-closing) run='^TestOCIClosing'; OCI_CLIENTS=1; OCI_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_OCISTUB=1 TROVE_E2E_OCI_CLOSING=1 ;;
  oci-long) run='^TestOCILong'; OCI_CLIENTS=1; OCI_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_OCISTUB=1 ;;
  # <<< T16 oci
  # >>> T17 deb
  # The Debian (APT) acceptance tests (R-DEB-035, docs/developer/e2e.md#the-debian-tests): the counting Debian archive test upstream, one client pod per
  # image (Debian 13, Debian 12, Ubuntu 24.04, Ubuntu 22.04 in the namespace trove-e2e-apt), the scatter profile. CI runs `deb` as the job e2e-deb on
  # every change that touches the format or the registry root.
  apt-clients) run='^TestAPTClients$'; APT_CLIENTS=1 ;;
  apt-clients-down) APT_CLIENTS=down ;;
  deb) run='^Test(Deb|APTClients)'; skip='^TestDebClosing'; APT_CLIENTS=1; export TROVE_E2E_DEBSTUB=1 ;;
  # The additions of the run that closes the phase: the set deb with Valkey stopped, and the read-only check against the Debian archive (R-DEB-031, R-DEB-035).
  deb-closing) run='^TestDebClosing'; APT_CLIENTS=1; export TROVE_E2E_DEBSTUB=1 TROVE_E2E_DEB_CLOSING=1 ;;
  # <<< T17 deb
  # >>> M6b mirror
  # The remote full mirror (R-REPO-059, docs/developer/e2e.md#the-mirror-tests): the `mirror` profile (up.sh, values-mirror.yaml), the two counting
  # test upstreams and three client pods of each kind. `mirror` is the short scenarios (CI: job e2e-mirror on a change to the mirror);
  # `mirror-long` the drain of the process that runs a pass (nightly, on dispatch, locally: a red long leg is a finding, not a blocker,
  # owner 2026-11-01).
  mirror) run='^TestMirror_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=3; OCI_CLIENTS=1; OCI_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 TROVE_E2E_OCISTUB=1 ;;
  mirror-long) run='^TestMirrorLong_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=3; OCI_CLIENTS=1; OCI_CLIENTS_DEFAULT_PODS=3; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_OCISTUB=1 ;;
  # <<< M6b mirror
  # >>> M6a upgrade
  # The upgrade tests (R-HA-020, R-HA-064, E6a.7, docs/developer/e2e.md#the-upgrade-tests): the release is removed, the database
  # and Valkey are emptied, an older release is installed from the published chart and image (v0.16.0 for the cut into roles, the
  # latest release before the working tree for the rolling upgrade), upgraded to the chart of the working tree and the image under
  # test (and rolled back) under npm ci, npm publish and dotnet restore loops through a virtual repository over a remote and a local
  # repository, and a browser journey; the cluster ends with the current dev release again. The test across two bundles installs
  # the working tree with the image of `make image-marked` (TROVE_IMAGE_MARKED) instead of a release and upgrades it to the image
  # under test (Q-HA-124). Two client pods of each kind, the two counting test upstreams, and the browser tier on the host (npm ci
  # in web/, npx playwright install chromium). The release tags
  # must be in the checkout (git fetch --tags) and the cluster needs access to Docker Hub and https://guided-traffic.github.io/trove/.
  # A long leg: nightly, on dispatch, locally (a red long leg is a finding, not a blocker, owner 2026-11-01).
  upgrade) run='^TestUpgrade_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=2; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=2; export TROVE_E2E_NPMSTUB=1 TROVE_E2E_NUGETSTUB=1 ;;
  # <<< M6a upgrade
  # >>> M5 backup
  # The backup and restore suite (R-BAK-029, R-HA-052 "Restore drill, trove backup", docs/developer/e2e.md#the-backup-tests): the `backup`
  # profile (garbage collection without a grace period, values-backup.yaml), a second installation in the namespace trove-restore
  # (deploy/dev/restore-ns.sh) with a database, a Valkey and a blob bucket of its own, and one npm client pod (`npm view`, `npm unpublish`). The drill
  # backs up while a load publishes, deletes and collects, restores into the second installation and judges it. A long leg: nightly,
  # on dispatch, locally (a red long leg is a finding, not a blocker, owner 2026-11-01).
  # The double of the CI issuer (TROVE_E2E_OIDCSTUB=1) is for E5.12's CI bullets in TestBackupJournal (Q-BAK-60): a disabled mapping and a single-use token across a restore.
  backup) run='^TestBackup'; skip='^TestBackupPITR'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=1; export TROVE_E2E_OIDCSTUB=1 ;;
  # The point-in-time restore drill (R-HA-025, R-HA-023, R-HA-054, E5.1, docs/developer/e2e.md#the-point-in-time-restore-drill): the `backup` profile on a
  # cluster whose PostgreSQL archives its WAL through the Barman Cloud plugin (TROVE_PITR=1, up.sh), so the profile switch of the test installs the
  # plugin. The cluster stays archiving until it is recreated. A long leg: nightly (step of the job e2e-backup), on dispatch, locally.
  backup-pitr) run='^TestBackupPITR'; export TROVE_PITR=1 ;;
  # <<< M5 backup
  # >>> M5 edge
  # The edge requirements of R-HA-039 and the Gateway API of R-HA-038 (docs/developer/e2e.md#the-edge-and-alert-tests, exit criterion E5.5): the profile
  # `edge` (Envoy Gateway with the Gateway `shared`, an HTTPRoute beside the Ingress, spool and upload limit for 6 GiB; up.sh installs it), one npm and one
  # NuGet client pod whose forwarders the test points at the Ingress and at the Gateway in turn. The 6 GiB and 2 GiB bodies are streamed from /dev/urandom, no file of them is written.
  # A long leg: nightly (job e2e-edge), on dispatch, locally (a red long leg is a finding, not a blocker, owner 2026-11-01).
  edge) run='^TestEdge_'; NPM_CLIENTS=1; NPM_CLIENTS_DEFAULT_PODS=1; NUGET_CLIENTS=1; NUGET_CLIENTS_DEFAULT_PODS=1; export TROVE_E2E_TIMEOUT="${TROVE_E2E_TIMEOUT:-160m}" ;;
  # The alerts of R-HA-045 in their R-HA-052 scenarios (R-OPS-045, R-OPS-046): the profile `alerts` (kube-prometheus-stack in the namespace monitoring,
  # the ServiceMonitor and the PrometheusRule rendered) on the cluster without the poolers (drain variant D is not part of
  # "Maintenance stays quiet": H-798), no client pods.
  alerts) run='^TestAlerts_'; export TROVE_E2E_TIMEOUT="${TROVE_E2E_TIMEOUT:-190m}" ;;
  # <<< M5 edge
  *) run="$set_name" ;;
esac

# TROVE_E2E_RUN replaces the expression of any set (a single subtest while developing: TROVE_E2E_RUN='^TestNPMMatrix/rows/R-NPM-045_E2.1_npm11').
run="${TROVE_E2E_RUN:-${run:-}}"
# A set may skip tests (go test -skip); TROVE_E2E_RUN names the tests itself, so it drops the skip.
skip="${skip:-}"
[ -z "${TROVE_E2E_RUN:-}" ] || skip=

# A set declared empty_ok (Q-ARCH-276) passes with a notice while its expression names no test in test/e2e. The names are
# read from the sources (func Test... in *_test.go): the check needs neither a cluster nor a compile, because TestMain of
# the package needs both. Once a test of the name exists, the set runs it and the rule below applies (a run that ran
# nothing fails).
if [ "${empty_ok:-}" = 1 ] && [ -z "${TROVE_E2E_RUN:-}" ]; then
  named="$(grep -h -o '^func Test[A-Za-z0-9_]*' "$REPO_ROOT"/test/e2e/*_test.go | sed 's/^func //' | grep -c -E "$run" || true)"
  if [ "$named" = 0 ]; then
    log "e2e set '$set_name' names no test yet ('$run' matches nothing in test/e2e): nothing to run (Q-ARCH-276)"
    exit 0
  fi
fi
require_cluster

EDGE_PORT="$(docker port "${CLUSTER}-control-plane" 30443/tcp 2>/dev/null | head -1 | sed 's/.*://' || true)"
EDGE_PORT="${EDGE_PORT:-$DEV_HTTP_PORT}"
OUT="${TROVE_E2E_OUT:-$REPO_ROOT/.out/e2e}"
mkdir -p "$OUT" "$REPO_ROOT/.out"

helm --kube-context "$CTX" -n "$NS" status trove >/dev/null 2>&1 || die "the trove release is not installed; run make dev-up"

# ── k6 ───────────────────────────────────────────────────────────────────────
[ "${K6_IMAGE#*:}" != "$K6_IMAGE" ] || die "K6_IMAGE is not set in tools/versions.env"
tag="${K6_IMAGE#*:}"; tag="${tag%%@*}"
[ "v$tag" = "$K6_VERSION" ] || die "K6_IMAGE tag $tag does not match K6_VERSION $K6_VERSION in tools/versions.env"
docker image inspect "$K6_IMAGE" >/dev/null 2>&1 || { log "pulling $K6_IMAGE"; docker pull "$K6_IMAGE" >/dev/null; }
# the helper container of the clients-first readings (test/e2e/mirror_readings_test.go) reads the cgroup of the kind nodes
docker image inspect "$INTEGRATION_TOOLS_IMAGE" >/dev/null 2>&1 || { log "pulling $INTEGRATION_TOOLS_IMAGE"; docker pull "$INTEGRATION_TOOLS_IMAGE" >/dev/null || log "$INTEGRATION_TOOLS_IMAGE not pulled: the readings record the nodes' cgroup as absent"; }

# ── the driver ───────────────────────────────────────────────────────────────
ARCH="$(arch)"
DRIVER_IMAGE="trove-e2e-driver:dev"
BUILD="$REPO_ROOT/.out/e2e-driver"
rm -rf "$BUILD"; mkdir -p "$BUILD"
if [ -n "${TROVE_E2E_DRIVER_BIN:-}" ]; then
  log "using the prebuilt driver $TROVE_E2E_DRIVER_BIN"
  cp "$TROVE_E2E_DRIVER_BIN" "$BUILD/driver-linux-$ARCH"
else
  log "building the driver for linux/$ARCH"
  (cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -o "$BUILD/driver-linux-$ARCH" ./test/e2e/cmd/driver)
fi
docker buildx build --load --platform "linux/$ARCH" --build-arg RUNTIME_BASE_IMAGE="$RUNTIME_BASE_IMAGE" \
  -f "$REPO_ROOT/test/e2e/Dockerfile" -t "$DRIVER_IMAGE" "$BUILD" >"$OUT/driver-image.log" 2>&1 || { tail -20 "$OUT/driver-image.log" >&2; die "driver image build failed"; }
kind load docker-image "$DRIVER_IMAGE" --name "$CLUSTER" >"$OUT/driver-load.log" 2>&1 || { tail -20 "$OUT/driver-load.log" >&2; die "kind load failed"; }

# The pod is the worker pod's template with another command: the same ConfigMap and Secrets, the same
# security contexts, no probes and no ports. It carries the labels the Valkey NetworkPolicy admits, a
# component label no Service, PodDisruptionBudget or spread constraint selects, and runs on the control-
# plane node so a drain of a worker node never evicts it.
log "starting the driver pod"
kn delete pod trove-e2e-driver --ignore-not-found --wait=true >/dev/null
kn get deployment trove-worker -o json | jq --arg image "$DRIVER_IMAGE" --arg ns "$NS" '
  .spec.template as $t
  | { apiVersion: "v1", kind: "Pod",
      metadata: { name: "trove-e2e-driver", namespace: $ns,
                  labels: ($t.metadata.labels | .["app.kubernetes.io/component"] = "e2e-driver") },
      spec: ($t.spec
        | del(.topologySpreadConstraints)
        | .terminationGracePeriodSeconds = 5
        | .nodeSelector = {"node-role.kubernetes.io/control-plane": ""}
        | .tolerations = [{"key": "node-role.kubernetes.io/control-plane", "operator": "Exists", "effect": "NoSchedule"}]
        | .containers[0] |= (.image = $image | .imagePullPolicy = "IfNotPresent" | .args = ["idle"] | del(.ports, .startupProbe, .livenessProbe, .readinessProbe))) }' \
  | kn apply -f - >/dev/null
kn wait --for=condition=Ready pod/trove-e2e-driver --timeout=120s >/dev/null || { kn describe pod trove-e2e-driver >&2; die "the driver pod did not become ready"; }

# >>> M2 npm-stub
# ── the counting npm test upstream ───────────────────────────────────────────
# TROVE_E2E_NPMSTUB=1 builds the image of test/npmstub (make npm-stub-image), loads it into kind and (re)starts
# the stub of deploy/dev/npmstub.yaml: Service npmstub.<ns>.svc:443, TLS from the dev CA, control token in Secret
# trove-npmstub. The tests that need it read TROVE_E2E_NPMSTUB_HOST. Nothing here runs without the switch.
if [ "${TROVE_E2E_NPMSTUB:-0}" = "1" ]; then
  log "building the npm test upstream image for linux/$ARCH"
  (cd "$REPO_ROOT" && make --no-print-directory npm-stub-image IMAGE_ARCH="$ARCH") >"$OUT/npmstub-image.log" 2>&1 || { tail -20 "$OUT/npmstub-image.log" >&2; die "npm test upstream image build failed"; }
  kind load docker-image trove-e2e-npmstub:dev --name "$CLUSTER" >"$OUT/npmstub-load.log" 2>&1 || { tail -20 "$OUT/npmstub-load.log" >&2; die "kind load of the npm test upstream failed"; }
  log "starting the npm test upstream"
  render "$DEV_DIR/npmstub.yaml" | kn apply -f - >/dev/null
  kn wait --for=condition=Ready certificate/trove-npmstub-tls --timeout=3m >/dev/null
  kn rollout restart deployment/trove-npmstub >/dev/null   # same tag, new image content
  kn rollout status deployment/trove-npmstub --timeout=3m >/dev/null || { kn describe pod -l app.kubernetes.io/name=trove-npmstub >&2; die "the npm test upstream did not become ready"; }
  export TROVE_E2E_NPMSTUB_HOST="npmstub.$NS.svc"
fi
# <<< M2 npm-stub

# >>> M3 nuget-stub
# ── the counting NuGet test upstream ─────────────────────────────────────────
# TROVE_E2E_NUGETSTUB=1 builds the image of test/nugetstub (make nuget-stub-image), loads it into kind and (re)starts
# the stub of deploy/dev/nugetstub.yaml: Services nugetstub.<ns>.svc (index, registrations) and nugetstub-content.<ns>.svc
# (flat container, search) on 443 in front of one pod, TLS from the dev CA, control token in Secret trove-nugetstub. The
# tests read TROVE_E2E_NUGETSTUB_HOST (the API host; the upstream URL of a remote repository is
# https://$TROVE_E2E_NUGETSTUB_HOST/v3/index.json) and TROVE_E2E_NUGETSTUB_CONTENT_HOST (the second host, which the
# repository names in its extraHosts, R-NUGET-014). trove trusts the dev CA for upstreams through
# proxy.outbound.tls.ca_files (values-dev.yaml). Nothing here runs without the switch.
if [ "${TROVE_E2E_NUGETSTUB:-0}" = "1" ]; then
  log "building the NuGet test upstream image for linux/$ARCH"
  (cd "$REPO_ROOT" && make --no-print-directory nuget-stub-image IMAGE_ARCH="$ARCH") >"$OUT/nugetstub-image.log" 2>&1 || { tail -20 "$OUT/nugetstub-image.log" >&2; die "NuGet test upstream image build failed"; }
  kind load docker-image trove-e2e-nugetstub:dev --name "$CLUSTER" >"$OUT/nugetstub-load.log" 2>&1 || { tail -20 "$OUT/nugetstub-load.log" >&2; die "kind load of the NuGet test upstream failed"; }
  log "starting the NuGet test upstream"
  render "$DEV_DIR/nugetstub.yaml" | kn apply -f - >/dev/null
  kn wait --for=condition=Ready certificate/trove-nugetstub-tls --timeout=3m >/dev/null
  kn rollout restart deployment/trove-nugetstub >/dev/null   # same tag, new image content
  kn rollout status deployment/trove-nugetstub --timeout=3m >/dev/null || { kn describe pod -l app.kubernetes.io/name=trove-nugetstub >&2; die "the NuGet test upstream did not become ready"; }
  export TROVE_E2E_NUGETSTUB_HOST="nugetstub.$NS.svc" TROVE_E2E_NUGETSTUB_CONTENT_HOST="nugetstub-content.$NS.svc"
fi
# <<< M3 nuget-stub

# >>> T16 oci-stub
# ── the counting OCI test upstream ───────────────────────────────────────────
# TROVE_E2E_OCISTUB=1 builds the image of test/ocistub (make oci-stub-image), loads it into kind and (re)starts the stub of
# deploy/dev/ocistub.yaml: the Services ocistub.<ns>.svc (the registry), ocistub-auth.<ns>.svc (the token realm) and
# ocistub-cdn.<ns>.svc (the blob CDN) on 443 in front of one pod, TLS from the dev CA, control token in Secret trove-ocistub. The tests read
# TROVE_E2E_OCISTUB_HOST (the registry; the upstream URL of a remote repository is https://$TROVE_E2E_OCISTUB_HOST/) and
# TROVE_E2E_OCISTUB_REALM_HOST and TROVE_E2E_OCISTUB_CDN_HOST (the two hosts a remote lists in its extraHosts, R-OCI-021). trove trusts the
# dev CA for upstreams through proxy.outbound.tls.ca_files (values-dev.yaml). Nothing here runs without the switch.
if [ "${TROVE_E2E_OCISTUB:-0}" = "1" ]; then
  log "building the OCI test upstream image for linux/$ARCH"
  (cd "$REPO_ROOT" && make --no-print-directory oci-stub-image IMAGE_ARCH="$ARCH") >"$OUT/ocistub-image.log" 2>&1 || { tail -20 "$OUT/ocistub-image.log" >&2; die "OCI test upstream image build failed"; }
  kind load docker-image trove-e2e-ocistub:dev --name "$CLUSTER" >"$OUT/ocistub-load.log" 2>&1 || { tail -20 "$OUT/ocistub-load.log" >&2; die "kind load of the OCI test upstream failed"; }
  log "starting the OCI test upstream"
  render "$DEV_DIR/ocistub.yaml" | kn apply -f - >/dev/null
  kn wait --for=condition=Ready certificate/trove-ocistub-tls --timeout=3m >/dev/null
  kn rollout restart deployment/trove-ocistub >/dev/null   # same tag, new image content
  kn rollout status deployment/trove-ocistub --timeout=3m >/dev/null || { kn describe pod -l app.kubernetes.io/name=trove-ocistub >&2; die "the OCI test upstream did not become ready"; }
  export TROVE_E2E_OCISTUB_HOST="ocistub.$NS.svc" TROVE_E2E_OCISTUB_REALM_HOST="ocistub-auth.$NS.svc" TROVE_E2E_OCISTUB_CDN_HOST="ocistub-cdn.$NS.svc"
fi
# <<< T16 oci-stub

# >>> T17 deb-stub
# ── the counting Debian archive test upstream ────────────────────────────────
# TROVE_E2E_DEBSTUB=1 builds the image of test/debstub (make deb-stub-image), loads it into kind and (re)starts the stub of
# deploy/dev/debstub.yaml: Service debstub.<ns>.svc on 443, TLS from the dev CA, control token in Secret trove-debstub. It serves the suites
# stable and testing under /debian/, the flat repository /flat/ and the flat root /flatroot/ (docs/developer/deb.md). The tests read
# TROVE_E2E_DEBSTUB_HOST (the upstream URL of a remote repository is https://$TROVE_E2E_DEBSTUB_HOST/debian/). trove trusts the dev CA for upstreams
# through proxy.outbound.tls.ca_files (values-dev.yaml). Nothing here runs without the switch.
if [ "${TROVE_E2E_DEBSTUB:-0}" = "1" ]; then
  log "building the Debian archive test upstream image for linux/$ARCH"
  (cd "$REPO_ROOT" && make --no-print-directory deb-stub-image IMAGE_ARCH="$ARCH") >"$OUT/debstub-image.log" 2>&1 || { tail -20 "$OUT/debstub-image.log" >&2; die "Debian archive test upstream image build failed"; }
  kind load docker-image trove-e2e-debstub:dev --name "$CLUSTER" >"$OUT/debstub-load.log" 2>&1 || { tail -20 "$OUT/debstub-load.log" >&2; die "kind load of the Debian archive test upstream failed"; }
  log "starting the Debian archive test upstream"
  render "$DEV_DIR/debstub.yaml" | kn apply -f - >/dev/null
  kn wait --for=condition=Ready certificate/trove-debstub-tls --timeout=3m >/dev/null
  kn rollout restart deployment/trove-debstub >/dev/null   # same tag, new image content
  kn rollout status deployment/trove-debstub --timeout=3m >/dev/null || { kn describe pod -l app.kubernetes.io/name=trove-debstub >&2; die "the Debian archive test upstream did not become ready"; }
  export TROVE_E2E_DEBSTUB_HOST="debstub.$NS.svc"
fi
# <<< T17 deb-stub

# >>> M4 oidc-stub
# ── the GitLab-shaped OIDC issuer double ─────────────────────────────────────
# TROVE_E2E_OIDCSTUB=1 builds the image of test/oidcstub (make oidc-stub-image), loads it into kind and (re)starts the
# double of deploy/dev/oidcstub.yaml with STUB=oidcstub: Service oidcstub.<ns>.svc, port 443 for the issuer endpoints and
# 8444 for the control API (mint, rotate, faults, counters), TLS from the dev CA, control token in Secret trove-oidcstub.
# The tests read TROVE_E2E_OIDCSTUB_HOST (the issuer is https://$TROVE_E2E_OIDCSTUB_HOST) and TROVE_E2E_OIDCSTUB_CONTROL_URL.
# TROVE_E2E_OIDCSTUB_SINGLE=1 starts a second double, STUB=oidcstub-single, the issuer of the single-use tests (its own
# issuer URL, keys, counters and control token; TROVE_E2E_OIDCSTUB_SINGLE_HOST). trove trusts the dev CA for outbound
# requests through proxy.outbound.tls.ca_files (values-dev.yaml). Nothing here runs without the switch.
if [ "${TROVE_E2E_OIDCSTUB:-0}" = "1" ]; then
  log "building the OIDC issuer double image for linux/$ARCH"
  (cd "$REPO_ROOT" && make --no-print-directory oidc-stub-image IMAGE_ARCH="$ARCH") >"$OUT/oidcstub-image.log" 2>&1 || { tail -20 "$OUT/oidcstub-image.log" >&2; die "OIDC issuer double image build failed"; }
  kind load docker-image trove-e2e-oidcstub:dev --name "$CLUSTER" >"$OUT/oidcstub-load.log" 2>&1 || { tail -20 "$OUT/oidcstub-load.log" >&2; die "kind load of the OIDC issuer double failed"; }
  stubs="oidcstub"
  [ "${TROVE_E2E_OIDCSTUB_SINGLE:-0}" != "1" ] || stubs="oidcstub oidcstub-single"
  for stub in $stubs; do
    log "starting the OIDC issuer double $stub"
    render "$DEV_DIR/oidcstub.yaml" | sed -e "s|\${STUB}|$stub|g" | kn apply -f - >/dev/null
    kn wait --for=condition=Ready "certificate/trove-$stub-tls" --timeout=3m >/dev/null
    kn rollout restart "deployment/trove-$stub" >/dev/null   # same tag, new image content
    kn rollout status "deployment/trove-$stub" --timeout=3m >/dev/null || { kn describe pod -l "app.kubernetes.io/name=trove-$stub" >&2; die "the OIDC issuer double $stub did not become ready"; }
  done
  export TROVE_E2E_OIDCSTUB_HOST="oidcstub.$NS.svc" TROVE_E2E_OIDCSTUB_CONTROL_URL="https://oidcstub.$NS.svc:8444"
  [ "${TROVE_E2E_OIDCSTUB_SINGLE:-0}" != "1" ] || export TROVE_E2E_OIDCSTUB_SINGLE_HOST="oidcstub-single.$NS.svc"
fi
# <<< M4 oidc-stub

# >>> M4b osv-stub
# ── the test advisory source (R-VULN-029) ────────────────────────────────────
# TROVE_E2E_OSVSTUB=1 builds the image of test/osvstub (make osv-stub-image), loads it into kind and (re)starts the stub
# of deploy/dev/osvstub.yaml: Service osvstub.<ns>.svc on 443, TLS from the dev CA, control token in Secret
# trove-osvstub. It serves osv.dev's bucket layout (<ecosystem>/all.zip, modified_id.csv, <id>.json, ecosystems.txt) and
# the known-exploited catalog from a synthetic fixture for the test packages of the npm and NuGet test upstreams. The tests
# read TROVE_E2E_OSVSTUB_HOST (vulnerabilities.sources.osv.base_url is https://$TROVE_E2E_OSVSTUB_HOST/, the catalog is
# https://$TROVE_E2E_OSVSTUB_HOST/sites/default/files/feeds/known_exploited_vulnerabilities.json). trove trusts the dev CA
# for upstreams through proxy.outbound.tls.ca_files (values-dev.yaml). Nothing here runs without the switch.
if [ "${TROVE_E2E_OSVSTUB:-0}" = "1" ]; then
  log "building the test advisory source image for linux/$ARCH"
  (cd "$REPO_ROOT" && make --no-print-directory osv-stub-image IMAGE_ARCH="$ARCH") >"$OUT/osvstub-image.log" 2>&1 || { tail -20 "$OUT/osvstub-image.log" >&2; die "test advisory source image build failed"; }
  kind load docker-image trove-e2e-osvstub:dev --name "$CLUSTER" >"$OUT/osvstub-load.log" 2>&1 || { tail -20 "$OUT/osvstub-load.log" >&2; die "kind load of the test advisory source failed"; }
  log "starting the test advisory source"
  render "$DEV_DIR/osvstub.yaml" | kn apply -f - >/dev/null
  kn wait --for=condition=Ready certificate/trove-osvstub-tls --timeout=3m >/dev/null
  kn rollout restart deployment/trove-osvstub >/dev/null   # same tag, new image content
  kn rollout status deployment/trove-osvstub --timeout=3m >/dev/null || { kn describe pod -l app.kubernetes.io/name=trove-osvstub >&2; die "the test advisory source did not become ready"; }
  export TROVE_E2E_OSVSTUB_HOST="osvstub.$NS.svc"
fi
# <<< M4b osv-stub

# ── the tests ────────────────────────────────────────────────────────────────
# The whole path is TLS (R-ARCH-035): the edge serves the dev CA's certificate for localhost, and the
# pods' :8081 is HTTPS too. The scheme and the CA stay parameters: TROVE_DEV_SCHEME (https|http),
# TROVE_DEV_OPS_SCHEME (the scheme of the pods' :8081, default https), TROVE_DEV_CA_FILE (a PEM bundle that
# signs the edge's certificate, default .dev/pki/ca.crt written by up.sh), TROVE_E2E_K6_TARGET (default
# https://localhost:30443, the edge's HTTPS NodePort; the name must be one the certificate carries) and
# TROVE_E2E_K6_HOSTS_ADDR (host:port that name resolves to inside the k6 container, default the control-plane
# node's address on the Docker network `kind`).
SCHEME="${TROVE_DEV_SCHEME:-https}"
OPS_SCHEME="${TROVE_DEV_OPS_SCHEME:-https}"
CA_FILE="${TROVE_DEV_CA_FILE:-}"
if [ "$SCHEME" = "https" ] || [ "$OPS_SCHEME" = "https" ]; then
  CA_FILE="${CA_FILE:-$STATE_DIR/pki/ca.crt}"
  [ -s "$CA_FILE" ] || die "the dev CA $CA_FILE is missing; run make dev-up"
fi
if [ "$SCHEME" = "https" ]; then
  node_ip="$(docker inspect -f '{{.NetworkSettings.Networks.kind.IPAddress}}' "${CLUSTER}-control-plane")"
  K6_TARGET="${TROVE_E2E_K6_TARGET:-https://localhost:30443}"
  K6_HOSTS_ADDR="${TROVE_E2E_K6_HOSTS_ADDR:-$node_ip:30443}"
else
  K6_TARGET="${TROVE_E2E_K6_TARGET:-http://${CLUSTER}-control-plane:30080}"
  K6_HOSTS_ADDR="${TROVE_E2E_K6_HOSTS_ADDR:-}"
fi
export TROVE_E2E_REPO="$REPO_ROOT" TROVE_E2E_CONTEXT="$CTX" TROVE_E2E_NAMESPACE="$NS" \
  TROVE_E2E_EDGE_URL="$SCHEME://localhost:$EDGE_PORT" TROVE_E2E_K6_TARGET="$K6_TARGET" \
  TROVE_E2E_K6_IMAGE="$K6_IMAGE" TROVE_E2E_OUT="$OUT" \
  TROVE_E2E_OPS_SCHEME="$OPS_SCHEME" TROVE_E2E_CA_FILE="$CA_FILE" TROVE_E2E_K6_HOSTS_ADDR="$K6_HOSTS_ADDR" \
  TROVE_E2E_OPENSSL_IMAGE="$OPENSSL_IMAGE" TROVE_E2E_TOOLS_IMAGE="$INTEGRATION_TOOLS_IMAGE" \
  TROVE_E2E_DEV_DIR="$DEV_DIR" TROVE_E2E_SCATTER_RETURN="${TROVE_E2E_SCATTER_RETURN:-1}" TROVE_DEV_HTTP_PORT="$EDGE_PORT"

# >>> M2 npm-clients
# The client pods of the npm acceptance matrix (R-NPM-045, docs/developer/e2e.md#the-npm-client-matrix). Each pod
# runs the forwarder fwd as its only process: it listens on 127.0.0.1:<edge port> inside the pod and relays to
# the in-cluster edge, so the base URL of the setup snippets (https://localhost:<port>) works unchanged and the
# certificate is verified for `localhost`. The dev CA is mounted from a ConfigMap, never baked into the image.
# The clients run through `kubectl exec <pod> -- <row> ...` (npm10 ... yarn1, see test/e2e/clients/bin).
NPM_CLIENTS_IMAGE="trove-e2e-clients:dev"
NPM_CLIENTS_LABEL="app.kubernetes.io/component=e2e-npm-client"
NPM_CLIENTS_PREFIX="trove-e2e-npm-"
NPM_CLIENTS_COUNT="${TROVE_E2E_NPM_CLIENTS_PODS:-${NPM_CLIENTS_DEFAULT_PODS:-1}}"

npm_clients_down() {
  kn delete pod -l "$NPM_CLIENTS_LABEL" --ignore-not-found --wait=true >/dev/null
  kn delete configmap trove-e2e-npm-ca --ignore-not-found >/dev/null
}

# npm_clients_exec <index> <command...>: what the tests do through kubectl, for a shell session.
npm_clients_exec() {
  local i="$1"; shift
  kn exec "${NPM_CLIENTS_PREFIX}${i}" -- "$@"
}

npm_clients_up() {
  # TROVE_E2E_REUSE_CLIENTS=1 keeps client pods that are already there (the right number, all ready): loading the images into
  # the cluster takes minutes, and a test written against the same image needs no new pods.
  if [ "${TROVE_E2E_REUSE_CLIENTS:-0}" = 1 ] && [ "$(kn get pods -l "$NPM_CLIENTS_LABEL" --field-selector=status.phase=Running -o name 2>/dev/null | wc -l | tr -d ' ')" = "$NPM_CLIENTS_COUNT" ]; then
    log "reusing the $NPM_CLIENTS_COUNT npm client pod(s) (TROVE_E2E_REUSE_CLIENTS=1)"
    return 0
  fi
  local target="${TROVE_E2E_NPM_CLIENTS_TARGET:-haproxy-ingress.ingress-controller.svc.cluster.local:443}" i
  [ -n "${CA_FILE:-}" ] && [ -s "$CA_FILE" ] || die "the client pods verify the edge against the dev CA, which is missing (TROVE_DEV_CA_FILE, TROVE_DEV_SCHEME=https; run make dev-up)"
  if [ "${TROVE_E2E_NPM_CLIENTS_IMAGE_BUILD:-1}" != 0 ]; then
    "$REPO_ROOT/test/e2e/clients/build.sh" "$NPM_CLIENTS_IMAGE" >"$OUT/npm-clients-image.log" 2>&1 || { tail -20 "$OUT/npm-clients-image.log" >&2; die "clients image build failed"; }
  fi
  # the pods run on the control-plane node only (nodeSelector below): one copy of the image, not one per node
  kind load docker-image "$NPM_CLIENTS_IMAGE" --name "$CLUSTER" --nodes "${CLUSTER}-control-plane" >"$OUT/npm-clients-load.log" 2>&1 || { tail -20 "$OUT/npm-clients-load.log" >&2; die "kind load failed"; }
  npm_clients_down
  kn create configmap trove-e2e-npm-ca --from-file=ca.crt="$CA_FILE" >/dev/null
  log "starting $NPM_CLIENTS_COUNT client pod(s) forwarding localhost:$EDGE_PORT to $target"
  for i in $(seq 0 $((NPM_CLIENTS_COUNT - 1))); do
    jq -n --arg name "${NPM_CLIENTS_PREFIX}${i}" --arg ns "$NS" --arg image "$NPM_CLIENTS_IMAGE" \
      --arg listen "127.0.0.1:$EDGE_PORT" --arg target "$target" '
      { apiVersion: "v1", kind: "Pod",
        metadata: { name: $name, namespace: $ns, labels: { "app.kubernetes.io/name": "trove-e2e", "app.kubernetes.io/component": "e2e-npm-client" } },
        spec: {
          terminationGracePeriodSeconds: 5,
          nodeSelector: { "node-role.kubernetes.io/control-plane": "" },
          tolerations: [{ key: "node-role.kubernetes.io/control-plane", operator: "Exists", effect: "NoSchedule" }],
          automountServiceAccountToken: false,
          # the host of the customers endpoint (values-dev.yaml) is the forwarder too: E2.12 uses its registry URLs unchanged;
          # so are the hosts of the e2e Bastions (deploy/dev/bastion.sh)
          hostAliases: [{ ip: "127.0.0.1", hostnames: ["packages.trove.localhost", "bastion-volume.trove.localhost", "bastion-bucket.trove.localhost"] }],
          securityContext: { runAsNonRoot: true, runAsUser: 1000, runAsGroup: 1000, seccompProfile: { type: "RuntimeDefault" } },
          containers: [{
            name: "clients", image: $image, imagePullPolicy: "IfNotPresent",
            args: ["-listen", $listen, "-to", $target, "-target-file", "/tmp/target"],
            env: [{ name: "NODE_EXTRA_CA_CERTS", value: "/etc/trove-ca/ca.crt" }],
            securityContext: { allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: { drop: ["ALL"] } },
            volumeMounts: [
              { name: "ca", mountPath: "/etc/trove-ca", readOnly: true },
              { name: "work", mountPath: "/work" },
              { name: "tmp", mountPath: "/tmp" } ] }],
          volumes: [
            { name: "ca", configMap: { name: "trove-e2e-npm-ca" } },
            { name: "work", emptyDir: { sizeLimit: "2Gi" } },
            { name: "tmp", emptyDir: { sizeLimit: "512Mi" } } ] } }' \
      | kn apply -f - >/dev/null
  done
  kn wait --for=condition=Ready pod -l "$NPM_CLIENTS_LABEL" --timeout=120s >/dev/null || { kn describe pod -l "$NPM_CLIENTS_LABEL" >&2; die "the client pods did not become ready"; }
}

if [ "${NPM_CLIENTS:-}" = down ]; then npm_clients_down; log "client pods removed"; exit 0; fi
if [ "${NPM_CLIENTS:-}" = 1 ]; then
  npm_clients_up
  export TROVE_E2E_NPM_CLIENTS_POD_PREFIX="$NPM_CLIENTS_PREFIX" TROVE_E2E_NPM_CLIENTS_PODS="$NPM_CLIENTS_COUNT"
fi
# <<< M2 npm-clients

# >>> M3 nuget-clients
# The client pods of the NuGet acceptance tests (R-NUGET-027, docs/developer/e2e.md#the-nuget-clients), built like the npm
# ones: fwd is PID 1 behind entrypoint.sh, which first builds the trust of .NET and Mono from the mounted dev CA (the
# bundle at SSL_CERT_FILE, the Mono store below HOME) and then marks the pod ready. The clients run through
# `kubectl exec <pod> -- dotnet|nuget ...`; every test sets NUGET_PACKAGES and NUGET_HTTP_CACHE_PATH below /work.
NUGET_CLIENTS_IMAGE="trove-e2e-nugetclients:dev"
NUGET_CLIENTS_LABEL="app.kubernetes.io/component=e2e-nuget-client"
NUGET_CLIENTS_PREFIX="trove-e2e-nuget-"
NUGET_CLIENTS_COUNT="${TROVE_E2E_NUGET_CLIENTS_PODS:-${NUGET_CLIENTS_DEFAULT_PODS:-1}}"

nuget_clients_down() {
  kn delete pod -l "$NUGET_CLIENTS_LABEL" --ignore-not-found --wait=true >/dev/null
  kn delete configmap trove-e2e-nuget-ca --ignore-not-found >/dev/null
}

nuget_clients_up() {
  if [ "${TROVE_E2E_REUSE_CLIENTS:-0}" = 1 ] && [ "$(kn get pods -l "$NUGET_CLIENTS_LABEL" --field-selector=status.phase=Running -o name 2>/dev/null | wc -l | tr -d ' ')" = "$NUGET_CLIENTS_COUNT" ]; then
    log "reusing the $NUGET_CLIENTS_COUNT NuGet client pod(s) (TROVE_E2E_REUSE_CLIENTS=1)"
    return 0
  fi
  local target="${TROVE_E2E_NUGET_CLIENTS_TARGET:-haproxy-ingress.ingress-controller.svc.cluster.local:443}" i
  [ -n "${CA_FILE:-}" ] && [ -s "$CA_FILE" ] || die "the client pods verify the edge against the dev CA, which is missing (TROVE_DEV_CA_FILE, TROVE_DEV_SCHEME=https; run make dev-up)"
  if [ "${TROVE_E2E_NUGET_CLIENTS_IMAGE_BUILD:-1}" != 0 ]; then
    "$REPO_ROOT/test/e2e/nugetclients/build.sh" "$NUGET_CLIENTS_IMAGE" >"$OUT/nuget-clients-image.log" 2>&1 || { tail -20 "$OUT/nuget-clients-image.log" >&2; die "NuGet clients image build failed"; }
  fi
  # the pods run on the control-plane node only (nodeSelector below): one copy of the 2.3 GB image, not one per node
  kind load docker-image "$NUGET_CLIENTS_IMAGE" --name "$CLUSTER" --nodes "${CLUSTER}-control-plane" >"$OUT/nuget-clients-load.log" 2>&1 || { tail -20 "$OUT/nuget-clients-load.log" >&2; die "kind load failed"; }
  nuget_clients_down
  kn create configmap trove-e2e-nuget-ca --from-file=ca.crt="$CA_FILE" >/dev/null
  log "starting $NUGET_CLIENTS_COUNT NuGet client pod(s) forwarding localhost:$EDGE_PORT to $target"
  for i in $(seq 0 $((NUGET_CLIENTS_COUNT - 1))); do
    jq -n --arg name "${NUGET_CLIENTS_PREFIX}${i}" --arg ns "$NS" --arg image "$NUGET_CLIENTS_IMAGE" \
      --arg listen "127.0.0.1:$EDGE_PORT" --arg target "$target" --arg work "${TROVE_E2E_NUGET_CLIENTS_WORK:-4Gi}" '
      { apiVersion: "v1", kind: "Pod",
        metadata: { name: $name, namespace: $ns, labels: { "app.kubernetes.io/name": "trove-e2e", "app.kubernetes.io/component": "e2e-nuget-client" } },
        spec: {
          terminationGracePeriodSeconds: 5,
          nodeSelector: { "node-role.kubernetes.io/control-plane": "" },
          tolerations: [{ key: "node-role.kubernetes.io/control-plane", operator: "Exists", effect: "NoSchedule" }],
          automountServiceAccountToken: false,
          # the host of the customers endpoint (values-dev.yaml) is the forwarder too: R-NUGET-034 uses its registry URLs unchanged;
          # so are the hosts of the e2e Bastions (deploy/dev/bastion.sh)
          hostAliases: [{ ip: "127.0.0.1", hostnames: ["packages.trove.localhost", "bastion-volume.trove.localhost", "bastion-bucket.trove.localhost"] }],
          securityContext: { runAsNonRoot: true, runAsUser: 1000, runAsGroup: 1000, seccompProfile: { type: "RuntimeDefault" } },
          containers: [{
            name: "clients", image: $image, imagePullPolicy: "IfNotPresent",
            args: ["-listen", $listen, "-to", $target, "-target-file", "/tmp/target"],
            env: [{ name: "TROVE_CA_FILE", value: "/etc/trove-ca/ca.crt" }, { name: "SSL_CERT_FILE", value: "/tmp/trove-ca/bundle.crt" }],
            readinessProbe: { exec: { command: ["test", "-f", "/tmp/trove-ca/ready"] }, periodSeconds: 2 },
            securityContext: { allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: { drop: ["ALL"] } },
            volumeMounts: [
              { name: "ca", mountPath: "/etc/trove-ca", readOnly: true },
              { name: "work", mountPath: "/work" },
              { name: "tmp", mountPath: "/tmp" } ] }],
          volumes: [
            { name: "ca", configMap: { name: "trove-e2e-nuget-ca" } },
            { name: "work", emptyDir: { sizeLimit: $work } },
            { name: "tmp", emptyDir: { sizeLimit: "512Mi" } } ] } }' \
      | kn apply -f - >/dev/null
  done
  kn wait --for=condition=Ready pod -l "$NUGET_CLIENTS_LABEL" --timeout=180s >/dev/null || { kn describe pod -l "$NUGET_CLIENTS_LABEL" >&2; die "the NuGet client pods did not become ready"; }
}

if [ "${NUGET_CLIENTS:-}" = down ]; then nuget_clients_down; log "NuGet client pods removed"; exit 0; fi
if [ "${NUGET_CLIENTS:-}" = 1 ]; then
  nuget_clients_up
  export TROVE_E2E_NUGET_CLIENTS_POD_PREFIX="$NUGET_CLIENTS_PREFIX" TROVE_E2E_NUGET_CLIENTS_PODS="$NUGET_CLIENTS_COUNT"
fi
# <<< M3 nuget-clients

# >>> T16 oci-clients
# The client pods of the OCI acceptance tests (R-OCI-035, Q-OCI-21, docs/developer/e2e.md#the-oci-clients): the Docker Engine (dockerd, CLI, Buildx) and Podman
# in one privileged pod each, in the namespace trove-e2e-clients (Pod Security level privileged, deploy/dev/oci-clients.yaml). Each pod's forwarder listens
# on 127.0.0.1:<edge port> and relays to the in-cluster edge, so `localhost:<edge port>` is the registry host of the setup snippets and the certificate is
# verified for `localhost`; the dev CA is mounted from a ConfigMap, never baked into the images. The tests run the clients through
# `kubectl -n trove-e2e-clients exec -c docker|podman <pod> -- ...`.
#
# The Kubernetes pull row (a pod whose image comes from a trove repository) is pulled by the kubelet of the control-plane node, whose containerd
# reaches neither the pod's forwarder nor the edge's host port. The node gets, once per run: a forwarder from 127.0.0.1:<edge port> to the edge's NodePort on the
# node's own address, and the dev CA for that host in /etc/containerd/certs.d (hosts.toml; kind's containerd reads that directory, config_path). The
# image reference of the row is localhost:<edge port>/..., the token realm of the registry is https://localhost:<edge port>/v2/_token, and both
# resolve on the node to the forwarder.
OCI_CLIENTS_NS="trove-e2e-clients"
OCI_CLIENTS_LABEL="app.kubernetes.io/component=e2e-oci-client"
OCI_CLIENTS_PREFIX="trove-e2e-oci-"
OCI_CLIENTS_COUNT="${TROVE_E2E_OCI_CLIENTS_PODS:-${OCI_CLIENTS_DEFAULT_PODS:-1}}"
OCI_DOCKER_IMAGE="trove-e2e-ociclients-docker:dev"
OCI_PODMAN_IMAGE="trove-e2e-ociclients-podman:dev"

oci_kc() { kubectl --context "$CTX" -n "$OCI_CLIENTS_NS" "$@"; }

oci_clients_down() {
  if k get namespace "$OCI_CLIENTS_NS" >/dev/null 2>&1; then
    k delete namespace "$OCI_CLIENTS_NS" --ignore-not-found --wait=true >/dev/null
  fi
}

# oci_node_setup puts the forwarder and the containerd host configuration on the control-plane node (see above).
oci_node_setup() {
  local node="${CLUSTER}-control-plane" node_ip host="localhost:$EDGE_PORT"
  node_ip="$(docker inspect -f '{{.NetworkSettings.Networks.kind.IPAddress}}' "$node")"
  docker exec "$node" grep -q config_path /etc/containerd/config.toml \
    || die "the containerd of $node has no registry config_path: the cluster was created before deploy/dev/kind.yaml set it; create it again (deploy/dev/down.sh, make dev-up)"
  mkdir -p "$OUT/oci-node"
  (cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -o "$OUT/oci-node/fwd" ./test/e2e/clients/fwd)
  # the forwarder of an earlier run is replaced (its pid is in a file; the node image has no pkill)
  docker exec "$node" sh -c 'if [ -s /run/trove-fwd.pid ]; then kill "$(cat /run/trove-fwd.pid)" 2>/dev/null || true; fi; rm -f /usr/local/bin/trove-fwd'
  docker cp "$OUT/oci-node/fwd" "$node:/usr/local/bin/trove-fwd" >/dev/null
  docker exec -d "$node" sh -c "echo \$\$ >/run/trove-fwd.pid; exec /usr/local/bin/trove-fwd -listen 127.0.0.1:$EDGE_PORT -to $node_ip:30443 >/var/log/trove-fwd.log 2>&1"
  docker exec "$node" mkdir -p "/etc/containerd/certs.d/$host"
  docker cp "$CA_FILE" "$node:/etc/containerd/certs.d/$host/ca.crt" >/dev/null
  printf 'server = "https://%s"\n\n[host."https://%s"]\n  capabilities = ["pull", "resolve"]\n  ca = "/etc/containerd/certs.d/%s/ca.crt"\n' "$host" "$host" "$host" \
    | docker exec -i "$node" sh -c "cat >'/etc/containerd/certs.d/$host/hosts.toml'"
}

# oci_render_pod <name> prints the manifest of one client pod with the variables of deploy/dev/oci-clients.yaml replaced.
oci_render_pod() {
  sed -e "s|\${POD}|$1|g" -e "s|\${DOCKER_IMAGE}|$OCI_DOCKER_IMAGE|g" -e "s|\${PODMAN_IMAGE}|$OCI_PODMAN_IMAGE|g" -e "s|\${EDGE_PORT}|$EDGE_PORT|g" \
    -e "s|\${TARGET}|$OCI_TARGET|g" -e "s|\${REGISTRY_HOSTS}|localhost:$EDGE_PORT oci-mirror.trove.localhost:$EDGE_PORT|g" \
    -e "s|\${ENGINE_MIRROR}|https://oci-mirror.trove.localhost:$EDGE_PORT|g" "$DEV_DIR/oci-clients.yaml"
}

oci_clients_up() {
  if [ "${TROVE_E2E_REUSE_CLIENTS:-0}" = 1 ] && [ "$(oci_kc get pods -l "$OCI_CLIENTS_LABEL" --field-selector=status.phase=Running -o name 2>/dev/null | wc -l | tr -d ' ')" = "$OCI_CLIENTS_COUNT" ]; then
    log "reusing the $OCI_CLIENTS_COUNT OCI client pod(s) (TROVE_E2E_REUSE_CLIENTS=1)"
    oci_node_setup
    return 0
  fi
  local i image
  OCI_TARGET="${TROVE_E2E_OCI_CLIENTS_TARGET:-haproxy-ingress.ingress-controller.svc.cluster.local:443}"
  [ -n "${CA_FILE:-}" ] && [ -s "$CA_FILE" ] || die "the client pods verify the edge against the dev CA, which is missing (TROVE_DEV_CA_FILE, TROVE_DEV_SCHEME=https; run make dev-up)"
  if [ "${TROVE_E2E_OCI_CLIENTS_IMAGE_BUILD:-1}" != 0 ]; then
    "$REPO_ROOT/test/e2e/ociclients/build.sh" "$OCI_DOCKER_IMAGE" "$OCI_PODMAN_IMAGE" >"$OUT/oci-clients-image.log" 2>&1 || { tail -20 "$OUT/oci-clients-image.log" >&2; die "OCI clients image build failed"; }
  fi
  # the pods run on the control-plane node only (nodeSelector in the manifest): one copy of the images, not one per node
  for image in "$OCI_DOCKER_IMAGE" "$OCI_PODMAN_IMAGE"; do
    kind load docker-image "$image" --name "$CLUSTER" --nodes "${CLUSTER}-control-plane" >>"$OUT/oci-clients-load.log" 2>&1 || { tail -20 "$OUT/oci-clients-load.log" >&2; die "kind load of $image failed"; }
  done
  oci_clients_down
  # the first apply of the file creates the namespace (with the pod, which waits for the ConfigMap); the ConfigMap follows at once
  oci_render_pod "${OCI_CLIENTS_PREFIX}0" | k apply -f - >/dev/null
  oci_kc create configmap trove-e2e-oci-ca --from-file=ca.crt="$CA_FILE" >/dev/null
  log "starting $OCI_CLIENTS_COUNT OCI client pod(s) forwarding localhost:$EDGE_PORT to $OCI_TARGET"
  i=1
  while [ "$i" -lt "$OCI_CLIENTS_COUNT" ]; do
    oci_render_pod "${OCI_CLIENTS_PREFIX}${i}" | k apply -f - >/dev/null
    i=$((i + 1))
  done
  oci_kc wait --for=condition=Ready pod -l "$OCI_CLIENTS_LABEL" --timeout=300s >/dev/null || { oci_kc describe pod -l "$OCI_CLIENTS_LABEL" >&2; die "the OCI client pods did not become ready"; }
  oci_node_setup
}

if [ "${OCI_CLIENTS:-}" = down ]; then oci_clients_down; log "OCI client pods removed"; exit 0; fi
if [ "${OCI_CLIENTS:-}" = 1 ]; then
  oci_clients_up
  export TROVE_E2E_OCI_CLIENTS_POD_PREFIX="$OCI_CLIENTS_PREFIX" TROVE_E2E_OCI_CLIENTS_PODS="$OCI_CLIENTS_COUNT" TROVE_E2E_OCI_CLIENTS_NAMESPACE="$OCI_CLIENTS_NS" TROVE_E2E_OCI_NODE="${CLUSTER}-control-plane"
fi
# <<< T16 oci-clients

# >>> T17 apt-clients
# The client pods of the Debian acceptance tests (R-DEB-035, Q-DEB-27, docs/developer/e2e.md#the-apt-clients): one pod per image, each a real APT with
# the verifier of its release, in the namespace trove-e2e-apt (Pod Security level baseline, deploy/dev/apt-clients.yaml). Each pod's forwarder listens on
# 127.0.0.1:<edge port> and relays to the in-cluster edge, so `https://localhost:<edge port>/api/deb/...` is the repository URL of the setup snippets
# and the certificate is verified for `localhost`; the dev CA is mounted from a ConfigMap, never baked into the images. The tests run the clients
# through `kubectl -n trove-e2e-apt exec <pod> -- ...`.
APT_CLIENTS_NS="trove-e2e-apt"
APT_CLIENTS_LABEL="app.kubernetes.io/component=e2e-apt-client"
APT_CLIENTS_PREFIX="trove-e2e-apt-"
APT_CLIENTS_IMAGE_PREFIX="trove-e2e-aptclients-"
APT_CLIENTS_NAMES="debian13 debian12 ubuntu2404 ubuntu2204"

apt_kc() { kubectl --context "$CTX" -n "$APT_CLIENTS_NS" "$@"; }

apt_clients_down() {
  if k get namespace "$APT_CLIENTS_NS" >/dev/null 2>&1; then
    k delete namespace "$APT_CLIENTS_NS" --ignore-not-found --wait=true >/dev/null
  fi
}

# apt_render_pod <name> prints the manifest of one client pod with the variables of deploy/dev/apt-clients.yaml replaced.
apt_render_pod() {
  sed -e "s|\${POD}|${APT_CLIENTS_PREFIX}$1|g" -e "s|\${IMAGE}|${APT_CLIENTS_IMAGE_PREFIX}$1:dev|g" -e "s|\${EDGE_PORT}|$EDGE_PORT|g" \
    -e "s|\${TARGET}|$APT_TARGET|g" "$DEV_DIR/apt-clients.yaml"
}

apt_clients_up() {
  local name
  if [ "${TROVE_E2E_REUSE_CLIENTS:-0}" = 1 ] && [ "$(apt_kc get pods -l "$APT_CLIENTS_LABEL" --field-selector=status.phase=Running -o name 2>/dev/null | wc -l | tr -d ' ')" = "4" ]; then
    log "reusing the 4 APT client pods (TROVE_E2E_REUSE_CLIENTS=1)"
    return 0
  fi
  APT_TARGET="${TROVE_E2E_APT_CLIENTS_TARGET:-haproxy-ingress.ingress-controller.svc.cluster.local:443}"
  [ -n "${CA_FILE:-}" ] && [ -s "$CA_FILE" ] || die "the client pods verify the edge against the dev CA, which is missing (TROVE_DEV_CA_FILE, TROVE_DEV_SCHEME=https; run make dev-up)"
  if [ "${TROVE_E2E_APT_CLIENTS_IMAGE_BUILD:-1}" != 0 ]; then
    "$REPO_ROOT/test/e2e/aptclients/build.sh" "$APT_CLIENTS_IMAGE_PREFIX" >"$OUT/apt-clients-image.log" 2>&1 || { tail -20 "$OUT/apt-clients-image.log" >&2; die "APT clients image build failed"; }
  fi
  # the pods run on the control-plane node only (nodeSelector in the manifest): one copy of each image, not one per node
  for name in $APT_CLIENTS_NAMES; do
    kind load docker-image "${APT_CLIENTS_IMAGE_PREFIX}${name}:dev" --name "$CLUSTER" --nodes "${CLUSTER}-control-plane" >>"$OUT/apt-clients-load.log" 2>&1 || { tail -20 "$OUT/apt-clients-load.log" >&2; die "kind load of the $name client image failed"; }
  done
  apt_clients_down
  # the first apply of the file creates the namespace (with the pod, which waits for the ConfigMap); the ConfigMap follows at once
  apt_render_pod debian13 | k apply -f - >/dev/null
  apt_kc create configmap trove-e2e-apt-ca --from-file=ca.crt="$CA_FILE" >/dev/null
  log "starting 4 APT client pods forwarding localhost:$EDGE_PORT to $APT_TARGET"
  for name in $APT_CLIENTS_NAMES; do
    [ "$name" = debian13 ] || apt_render_pod "$name" | k apply -f - >/dev/null
  done
  apt_kc wait --for=condition=Ready pod -l "$APT_CLIENTS_LABEL" --timeout=300s >/dev/null || { apt_kc describe pod -l "$APT_CLIENTS_LABEL" >&2; die "the APT client pods did not become ready"; }
}

if [ "${APT_CLIENTS:-}" = down ]; then apt_clients_down; log "APT client pods removed"; exit 0; fi
if [ "${APT_CLIENTS:-}" = 1 ]; then
  apt_clients_up
  export TROVE_E2E_APT_CLIENTS_POD_PREFIX="$APT_CLIENTS_PREFIX" TROVE_E2E_APT_CLIENTS_NAMESPACE="$APT_CLIENTS_NS"
fi
# <<< T17 apt-clients

REPORT_REL="${TROVE_E2E_REPORT:-.out/test-e2e.json}"
log "go test -tags e2e -run '$run' (report: $REPORT_REL, artifacts: $OUT)"
cd "$REPO_ROOT"
set -o pipefail
if ! go test -count=1 -tags e2e -timeout "${TROVE_E2E_TIMEOUT:-90m}" -json -run "$run" ${skip:+-skip "$skip"} ./test/e2e \
  | tee "$REPO_ROOT/$REPORT_REL" \
  | jq -r --unbuffered 'select(.Action=="output") | .Output' | { grep -v -E '^(=== |PASS$)' || true; }; then
  log "e2e FAILED (report: $REPORT_REL, artifacts: $OUT)"
  exit 1
fi
# A -run expression that matches nothing is a pass for `go test`; for this suite it is a mistake.
ran="$(jq -s '[.[] | select(.Action == "pass" and .Test != null)] | length' "$REPO_ROOT/$REPORT_REL")"
[ "$ran" -gt 0 ] || { log "e2e FAILED: the expression '$run' matched no test"; exit 1; }
log "e2e passed ($ran tests and subtests)"
