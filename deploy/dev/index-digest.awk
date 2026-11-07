# Prints the digest that a Helm chart repository index (index.yaml) lists for one chart at one version, one line per match:
#   awk -v chart=trove -v want=0.25.0 -f deploy/dev/index-digest.awk index.yaml
# helm writes the keys of an entry in alphabetical order, so the digest comes before the name and the version: the entry
# is collected whole and judged when the next one starts. deploy/dev/release-smoke.sh asserts exactly one line;
# test/chart/release_workflow_test.go (TestR_HA_047_TheIndexDigestOfAChartVersion) runs this file on a copy of the published index.
function flush() {
  if (name == chart && ver == want) print dig
}
/^  [A-Za-z0-9._-]+:$/ { flush(); dig = ""; name = ""; ver = ""; next }
/^  - /               { flush(); dig = ""; name = ""; ver = "" }
/^    digest: /       { dig = $2 }
/^    name: /         { name = $2; gsub(/["']/, "", name) }
/^    version: /      { ver = $2; gsub(/["']/, "", ver) }
END { flush() }
