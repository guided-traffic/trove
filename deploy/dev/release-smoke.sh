#!/usr/bin/env bash
# The post-release check of E5.8 (R-HA-047, Q-HA-163 to Q-HA-165): the artifacts of a release as an installer gets them.
#
#   release-smoke.sh verify <version>   no cluster. Waits until the chart repository lists the version, pulls the chart
#                                       package and compares its SHA-256 with the index, resolves the image tag to its
#                                       digest, and checks the attestations of both platforms and the SPDX asset of the release.
#   release-smoke.sh guard              needs the cluster of a published install (up.sh with TROVE_RELEASE_VERSION and
#                                       TROVE_RELEASE_DIGEST): every pod of the trove release must run that digest.
#
# <version> is the release without the v (0.25.0). The cases are named in the messages, so that the log says which one
# failed (Q-HA-163): "release not found", "chart not published", "chart package differs from the index", "image not
# published", "attestation missing", "release asset missing", "wrong image in the cluster".
#
# Writes .out/release-smoke/evidence.json (and, in a GitHub Actions job, the step summary and the output `digest`).
# Environment: TROVE_RELEASE_SMOKE_WAIT_SECONDS (how long to wait for the chart repository, default 1200),
# TROVE_RELEASE_SMOKE_POLL_SECONDS (default 30), TROVE_RELEASE_SMOKE_REPO (default guided-traffic/trove); GH_TOKEN or GITHUB_TOKEN
# is sent to the GitHub API when set (the rate limit of an anonymous caller is low).
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

CHART_REPO_URL="https://guided-traffic.github.io/trove/"
IMAGE_REPO="guidedtraffic/trove"
GH_REPO="${TROVE_RELEASE_SMOKE_REPO:-guided-traffic/trove}"
WAIT_SECONDS="${TROVE_RELEASE_SMOKE_WAIT_SECONDS:-1200}"
POLL_SECONDS="${TROVE_RELEASE_SMOKE_POLL_SECONDS:-30}"
OUT="$REPO_ROOT/.out/release-smoke"
EVIDENCE="$OUT/evidence.json"
mkdir -p "$OUT"

need() { local t; for t in "$@"; do command -v "$t" >/dev/null 2>&1 || die "$t is required"; done; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}

# evidence_set <jq filter> [jq args]: merges one fact into the evidence file.
evidence_set() {
  local filter="$1"; shift
  [ -s "$EVIDENCE" ] || echo '{}' > "$EVIDENCE"
  jq "$@" "$filter" "$EVIDENCE" > "$EVIDENCE.tmp" && mv "$EVIDENCE.tmp" "$EVIDENCE"
}

summary() { [ -z "${GITHUB_STEP_SUMMARY:-}" ] || printf '%s\n' "$@" >> "$GITHUB_STEP_SUMMARY"; }

# github_get <api path> <output file> <what>: GET from the GitHub API of $GH_REPO with the token of GH_TOKEN or GITHUB_TOKEN
# (the unauthenticated rate limit is low). 200 writes the body; 404 is "release not found"; 401, 403 and 429 are named as
# the API refusing the request, with its message; anything else is a network failure.
github_get() {
  local path="$1" file="$2" what="$3" code token="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
  local -a auth=()
  [ -z "$token" ] || auth=(-H "Authorization: Bearer $token")
  code="$(curl -sS -o "$file" -w '%{http_code}' --retry 3 -H 'Accept: application/vnd.github+json' ${auth[@]+"${auth[@]}"} \
    "https://api.github.com/repos/$GH_REPO/$path" 2>"$OUT/github.err")" || code="000"
  case "$code" in
    200) ;;
    404) die "release not found: $GH_REPO has no $what" ;;
    401|403|429) die "the GitHub API refused the request for $what (HTTP $code, authentication or rate limit): $(jq -r '.message // empty' "$file" 2>/dev/null)" ;;
    000) die "the GitHub API cannot be reached for $what: $(cat "$OUT/github.err")" ;;
    *) die "the GitHub API answered HTTP $code for $what" ;;
  esac
}

verify() {
  local version="${1:-}" tag started index_digest pkg_sha tgz image_digest deadline n commit
  version="${version#v}"
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "the version must look like 0.25.0 (no v, no suffix), not '$version'"
  tag="v$version"
  need curl awk helm docker jq
  started="$(date +%s)"
  rm -f "$EVIDENCE"

  # 1. The release exists. A version without a release is a wrong input, not a Pages lag: fail at once. The three kinds of
  # failure are told apart: no such release (404), an answer that is not about the release (auth, rate limit), no answer.
  github_get "releases/tags/$tag" "$OUT/release.json" "release $tag"
  commit="$(github_get "commits/$tag" "$OUT/commit.json" "commit for the tag $tag" && jq -r '.sha' "$OUT/commit.json")"
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "release not found: the tag $tag does not resolve to a commit"

  # 2. and 3. The chart repository lists the version and serves the package (GitHub Pages serves the gh-pages branch a little
  # after the push, the index and the package possibly at different moments), and the package as helm pulls it matches the
  # digest the index lists. A fresh pull each round: the cache would hide a replaced package.
  log "waiting for the chart repository to list and serve trove $version (up to ${WAIT_SECONDS}s)"
  deadline=$(( $(date +%s) + WAIT_SECONDS ))
  while :; do
    if curl -fsSL --retry 3 "${CHART_REPO_URL}index.yaml" -o "$OUT/index.yaml" 2>"$OUT/index.err"; then
      awk -v chart=trove -v want="$version" -f "$DEV_DIR/index-digest.awk" "$OUT/index.yaml" > "$OUT/index-digest.txt"
      n="$(wc -l < "$OUT/index-digest.txt" | tr -d ' ')"
      [ "$n" -le 1 ] || die "chart package differs from the index: ${CHART_REPO_URL}index.yaml lists trove $version $n times"
      if [ "$n" = 1 ]; then
        rm -f "$STATE_DIR/charts/trove-$version.tgz"
        if tgz="$(chart_tgz "$CHART_REPO_URL" trove "$version" 2>"$OUT/pull.err")"; then break; fi
      fi
    fi
    [ "$(date +%s)" -lt "$deadline" ] || die "chart not published: ${CHART_REPO_URL} does not list and serve trove $version after ${WAIT_SECONDS}s (the release job or GitHub Pages did not publish it); the last helm pull said: $(tail -n 3 "$OUT/pull.err" 2>/dev/null | tr '\n' ' ')"
    sleep "$POLL_SECONDS"
  done
  index_digest="$(cat "$OUT/index-digest.txt")"
  pkg_sha="$(sha256_of "$tgz")"
  [ "$pkg_sha" = "$index_digest" ] || die "chart package differs from the index: the package is $pkg_sha, the index lists $index_digest"
  log "chart trove $version: package SHA-256 $pkg_sha equals the index digest"

  # 4. The image tag resolves to an index with both platforms.
  docker buildx imagetools inspect "$IMAGE_REPO:$version" --format '{{json .Manifest}}' > "$OUT/image-manifest.json" 2> "$OUT/image.err" \
    || { cat "$OUT/image.err" >&2; die "image not published: $IMAGE_REPO:$version cannot be resolved"; }
  image_digest="$(jq -r '.digest' "$OUT/image-manifest.json")"
  [[ "$image_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "image not published: $IMAGE_REPO:$version resolved to '$image_digest'"
  local platform
  for platform in amd64 arm64; do
    jq -e --arg a "$platform" '[.manifests[] | select(.platform.os == "linux" and .platform.architecture == $a)] | length == 1' "$OUT/image-manifest.json" >/dev/null \
      || die "image not published: the index of $IMAGE_REPO:$version has no single linux/$platform manifest"
  done
  log "image $IMAGE_REPO:$version resolves to $image_digest"

  # 5. The attestations, per platform, read from the registry by digest. Nothing is verified cryptographically here: no
  # signature is part of the pipeline (R-HA-047), and no cluster verifies an attestation (H-79).
  docker buildx imagetools inspect "$IMAGE_REPO@$image_digest" --format '{{json .Provenance}}' > "$OUT/provenance.json" 2>"$OUT/provenance.err" \
    || { cat "$OUT/provenance.err" >&2; die "attestation missing: the provenance of $image_digest cannot be read"; }
  docker buildx imagetools inspect "$IMAGE_REPO@$image_digest" --format '{{json .SBOM}}' > "$OUT/sbom.json" 2>"$OUT/sbom.err" \
    || { cat "$OUT/sbom.err" >&2; die "attestation missing: the SBOM of $image_digest cannot be read"; }
  for platform in amd64 arm64; do
    jq -e --arg p "linux/$platform" '.[$p].SLSA.buildDefinition != null and .[$p].SLSA.runDetails != null' "$OUT/provenance.json" >/dev/null \
      || die "attestation missing: no provenance for linux/$platform under $image_digest"
    jq -e --arg p "linux/$platform" '(.[$p].SPDX.spdxVersion // "") | startswith("SPDX-")' "$OUT/sbom.json" >/dev/null \
      || die "attestation missing: no SPDX SBOM for linux/$platform under $image_digest"
    jq -e --arg p "linux/$platform" '(.[$p].SPDX.packages // []) | length > 0' "$OUT/sbom.json" >/dev/null \
      || die "attestation missing: the SPDX SBOM for linux/$platform lists no package"
  done
  log "provenance and SPDX SBOM present for linux/amd64 and linux/arm64"

  # 6. The release has its SPDX file.
  local asset="guidedtraffic-trove_${version}.spdx.json"
  jq -e --arg a "$asset" '[.assets[].name] | index($a) != null' "$OUT/release.json" >/dev/null \
    || die "release asset missing: $tag has no $asset"
  log "release $tag carries $asset"

  # shellcheck disable=SC2016 # the jq program's variables, not the shell's
  evidence_set '. + {
      version: $version, tag: $tag, commit: $commit, runId: $run, runAttempt: $attempt,
      chart: {indexDigest: $index, packageSha256: $pkg, match: ($index == $pkg)},
      image: {ref: ($repo + ":" + $version), indexDigest: $digest},
      platforms: ($platforms | fromjson), spdxAsset: $asset,
      verifySeconds: $secs
    }' \
    --arg version "$version" --arg tag "$tag" --arg commit "$commit" --arg run "${GITHUB_RUN_ID:-}" --arg attempt "${GITHUB_RUN_ATTEMPT:-}" \
    --arg index "$index_digest" --arg pkg "$pkg_sha" --arg repo "$IMAGE_REPO" --arg digest "$image_digest" --arg asset "$asset" \
    --argjson secs "$(( $(date +%s) - started ))" \
    --arg platforms "$(jq -c --slurpfile prov "$OUT/provenance.json" --slurpfile sbom "$OUT/sbom.json" '
      [.manifests[] | select(.platform.os == "linux") | {key: .platform.architecture, value: {
        manifest: .digest,
        provenance: ($prov[0]["linux/" + .platform.architecture].SLSA != null),
        sbom: ($sbom[0]["linux/" + .platform.architecture].SPDX != null),
        sbomCreators: ($sbom[0]["linux/" + .platform.architecture].SPDX.creationInfo.creators // [])}}] | from_entries' "$OUT/image-manifest.json")"

  if [ -n "${GITHUB_OUTPUT:-}" ]; then echo "digest=$image_digest" >> "$GITHUB_OUTPUT"; fi
  summary "### Published release $version" "" \
    "| Check | Result |" "|---|---|" \
    "| Tag commit | \`$commit\` |" \
    "| Chart package SHA-256 | \`$pkg_sha\` (equals the index digest) |" \
    "| Image index digest | \`$image_digest\` |" \
    "| Manifest linux/amd64 | \`$(jq -r '.platforms.amd64.manifest' "$EVIDENCE")\` |" \
    "| Manifest linux/arm64 | \`$(jq -r '.platforms.arm64.manifest' "$EVIDENCE")\` |" \
    "| Provenance, SBOM (both platforms) | present |" \
    "| SBOM creator tools (linux/amd64) | $(jq -r '.platforms.amd64.sbomCreators | join(", ")' "$EVIDENCE") |" \
    "| SPDX asset | \`$asset\` |"
  log "published release $version verified in $(elapsed "$started")"
}

guard() {
  need kubectl jq helm
  local digest="${TROVE_RELEASE_DIGEST:-}" bad count role
  if [ -z "$digest" ] && [ -s "$EVIDENCE" ]; then digest="$(jq -r '.image.indexDigest // empty' "$EVIDENCE")"; fi
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "wrong image in the cluster: no digest to compare with (set TROVE_RELEASE_DIGEST or run verify first)"

  # Every pod of the release except the e2e driver, which the harness builds from the working tree (deploy/dev/e2e.sh).
  # The image ID of a container pulled by an index digest is that digest (verified on the dev cluster for the pinned
  # cert-manager images), so the comparison is exact.
  kn get pods -l 'app.kubernetes.io/instance=trove,app.kubernetes.io/component!=e2e-driver' -o json > "$OUT/pods.json"
  jq -r '.items[] | .metadata.labels["app.kubernetes.io/component"] as $c | .metadata.name as $n
    | ((.status.initContainerStatuses // []) + (.status.containerStatuses // []))[] | [$n, $c, .name, .imageID] | @tsv' "$OUT/pods.json" > "$OUT/pod-images.tsv"
  count="$(wc -l < "$OUT/pod-images.tsv" | tr -d ' ')"
  [ "$count" -gt 0 ] || die "wrong image in the cluster: the release has no pod with an image ID"
  for role in api packages worker; do
    awk -F'\t' -v r="$role" '$2 == r' "$OUT/pod-images.tsv" | grep -q . || die "wrong image in the cluster: no $role pod reports an image ID"
  done
  bad="$(awk -F'\t' -v d="@$digest" 'length($4) < length(d) || substr($4, length($4) - length(d) + 1) != d {print}' "$OUT/pod-images.tsv")"
  if [ -n "$bad" ]; then
    printf 'pod\tcomponent\tcontainer\timageID\n%s\n' "$bad" >&2
    die "wrong image in the cluster: the containers above do not run $digest"
  fi
  local values_digest
  values_digest="$(helm --kube-context "$CTX" -n "$NS" get values trove -o json | jq -r '.image.digest // empty')"
  [ "$values_digest" = "$digest" ] || die "wrong image in the cluster: helm values carry image.digest '$values_digest', not $digest"
  log "$count container(s) of the trove release run $digest"
  # shellcheck disable=SC2016 # the jq program's variables, not the shell's
  evidence_set '. + {cluster: {digest: $d, containers: $n, pods: $pods}}' --arg d "$digest" --argjson n "$count" \
    --argjson pods "$(jq -c '[.items[].metadata.name]' "$OUT/pods.json")"
  summary "### Installation" "" "| Check | Result |" "|---|---|" \
    "| Containers of the release (pods without the e2e driver) | $count, all with image ID \`...@$digest\` |" \
    "| \`helm get values\` image.digest | \`$values_digest\` |"
}

case "${1:-}" in
  verify) shift; verify "$@" ;;
  guard) guard ;;
  *) echo "usage: $0 verify <version> | guard" >&2; exit 2 ;;
esac
