#!/usr/bin/env bash
# Merges the coverage profiles of one run into the coverage of the repository. Reads <dir>/unit.out
# and <dir>/integration-*.out (the profiles `make test` and `make test-integration` write) and writes
# into <dir>:
#
#   combined.out   the merged profile: a block counts once, covered when any profile covered it
#   coverage.txt   go tool cover -func of the merged profile, the total on its last line
#   summary.md     the job summary: the total and one row per package
#   coverage.json  the badge (shields.io endpoint schema), colour by the thresholds of
#                  docs/developer/releasing.md
#
# Usage: report.sh <dir>
set -euo pipefail

dir="${1:?usage: report.sh <dir>}"
shopt -s nullglob
profiles=()
for p in "$dir/unit.out" "$dir"/integration-*.out; do
  [ -f "$p" ] && profiles+=("$p")
done
[ "${#profiles[@]}" -gt 0 ] || { echo "report.sh: no coverage profile in $dir" >&2; exit 1; }

mode="$(head -n 1 "${profiles[0]}")"
for p in "${profiles[@]}"; do
  head -n 1 "$p" | grep -qE '^mode: (set|count|atomic)$' || { echo "report.sh: $p is not a coverage profile" >&2; exit 1; }
  [ "$(head -n 1 "$p")" = "$mode" ] || { echo "report.sh: $p has another mode than ${profiles[0]}" >&2; exit 1; }
done

merged="$dir/combined.out"
{
  echo "$mode"
  # Every block appears once per profile that ran it: the statement count is the same in each, and
  # the count is the largest one any profile recorded.
  tail -q -n +2 "${profiles[@]}" \
    | awk 'NF == 3 { if (!($1 in n)) n[$1] = $2; if (!($1 in c) || $3 + 0 > c[$1] + 0) c[$1] = $3 }
           END { for (b in c) print b, n[b], c[b] }' \
    | sort
} > "$merged"

go tool cover -func="$merged" > "$dir/coverage.txt"
total="$(awk '$1 == "total:" { sub("%", "", $3); print $3 }' "$dir/coverage.txt")"
[ -n "$total" ] || { echo "report.sh: no total in $dir/coverage.txt" >&2; exit 1; }

color="$(awk -v c="$total" 'BEGIN {
  if (c >= 90) print "brightgreen"; else if (c >= 80) print "green"; else if (c >= 70) print "yellow";
  else if (c >= 60) print "orange"; else print "red" }')"
printf '{\n  "schemaVersion": 1,\n  "label": "coverage",\n  "message": "%s%%",\n  "color": "%s"\n}\n' \
  "$total" "$color" > "$dir/coverage.json"

prefix="github.com/guided-traffic/trove/"
{
  echo "## Test coverage"
  echo
  names=""
  for p in "${profiles[@]}"; do names="$names $(basename "$p")"; done
  echo "Profiles merged:${names}. Total: **${total}%** of the statements."
  echo
  echo "| Package | Coverage | Statements |"
  echo "|---|---:|---:|"
  awk 'NR > 1 {
         split($1, a, ":"); pkg = a[1]; sub(/\/[^\/]*$/, "", pkg)
         n[pkg] += $2; if ($3 > 0) cov[pkg] += $2
       }
       END { for (p in n) printf "%s %d %d\n", p, cov[p] + 0, n[p] }' "$merged" \
    | sort \
    | while read -r pkg covered stmts; do
        short="${pkg#"$prefix"}"
        pct="$(awk -v c="$covered" -v s="$stmts" 'BEGIN { if (s > 0) printf "%.1f", 100 * c / s; else print "0.0" }')"
        echo "| \`$short\` | ${pct}% | $covered/$stmts |"
      done
} > "$dir/summary.md"

echo "coverage ${total}% ($color), $(wc -l < "$dir/coverage.txt" | tr -d ' ') lines of the function report in $dir/coverage.txt"
