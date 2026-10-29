#!/usr/bin/env bash
# Writes the notes of a release: the existing notes (stdin) with the section of the published
# artifacts appended. An earlier section of this script is replaced, and nothing else of the
# existing notes changes. Called by job release-notes of .github/workflows/release.yml.
#
# Input: the environment variables listed below, set by the workflow.
# Output: the complete notes on stdout, for `gh release edit --notes-file`.
set -euo pipefail

: "${VERSION:?}" "${COMMIT:?}" "${RUN_URL:?}" "${IMAGE:?}" "${IMAGE_DIGEST:?}"
: "${CHART_REPOSITORY:?}" "${CHART_DIGEST:?}" "${IDENTITY:?}" "${GITHUB_REPOSITORY:?}"

start='<!-- release-pipeline:start -->'
end='<!-- release-pipeline:end -->'
issuer='https://token.actions.githubusercontent.com'

existing="$(cat)"
# The section is found by its markers only. A missing or doubled marker would make the removal
# below cut text that is not ours, so the script stops instead.
starts=$(grep -cxF "$start" <<<"$existing" || true)
ends=$(grep -cxF "$end" <<<"$existing" || true)
if [ "$starts" -gt 1 ] || [ "$ends" -gt 1 ] || [ "$starts" != "$ends" ]; then
  echo "::error::the notes have $starts start and $ends end markers of the artifact section, expected 0 or 1 of each" >&2
  exit 1
fi

kept="$(awk -v s="$start" -v e="$end" '
  $0 == s { skip = 1; next }
  $0 == e { skip = 0; next }
  !skip { print }
' <<<"$existing")"

# Blank lines at the end of the kept text are dropped, so that repeated runs give the same notes.
kept="$(awk '{ line[NR] = $0 } END { n = NR; while (n > 0 && line[n] ~ /^[ \t]*$/) n--; for (i = 1; i <= n; i++) print line[i] }' <<<"$kept")"

section="$(cat <<EOF
$start
## Published artifacts

Published by [this release run]($RUN_URL) from commit \`$COMMIT\`. The image and the chart are signed keyless with cosign. The image also carries a SLSA provenance attestation and an SPDX SBOM attestation; the SBOM describes its linux/amd64 variant. Both artifacts carry a provenance attestation in the registry.

| Artifact | Reference | Digest |
|---|---|---|
| Image, linux/amd64 and linux/arm64 | \`$IMAGE:$VERSION\` | \`$IMAGE_DIGEST\` |
| Helm chart | \`oci://$CHART_REPOSITORY/trove\`, version \`$VERSION\` | \`$CHART_DIGEST\` |

Verify the signatures against the identity of this run:

\`\`\`bash
cosign verify $IMAGE@$IMAGE_DIGEST \\
  --certificate-identity $IDENTITY \\
  --certificate-oidc-issuer $issuer
cosign verify $CHART_REPOSITORY/trove@$CHART_DIGEST \\
  --certificate-identity $IDENTITY \\
  --certificate-oidc-issuer $issuer
\`\`\`

<details>
<summary>Provenance, SBOM and installation</summary>

\`\`\`bash
gh attestation verify oci://$IMAGE@$IMAGE_DIGEST --repo $GITHUB_REPOSITORY
cosign verify-attestation --type spdxjson $IMAGE@$IMAGE_DIGEST \\
  --certificate-identity $IDENTITY \\
  --certificate-oidc-issuer $issuer
helm install trove oci://$CHART_REPOSITORY/trove --version $VERSION
\`\`\`

</details>
$end
EOF
)"

if [ -n "$kept" ]; then
  printf '%s\n\n%s\n' "$kept" "$section"
else
  printf '%s\n' "$section"
fi
