#!/bin/sh
# PID 1 of a NuGet clients pod: builds the trust of the two runtimes from the mounted dev CA, then becomes the
# forwarder (test/e2e/clients/fwd). The root filesystem is read-only, so everything is written below /tmp and
# $HOME, both emptyDirs of the pod (deploy/dev/e2e.sh).
#
#   TROVE_CA_FILE   the dev CA, PEM (default /etc/trove-ca/ca.crt, a ConfigMap mount)
#   SSL_CERT_FILE   where the bundle for .NET is written (default /tmp/trove-ca/bundle.crt); the pod sets the same
#                   value in its environment, so every `kubectl exec` finds it
#
# .NET on Linux builds its X.509 trust from OpenSSL: SSL_CERT_FILE replaces the system bundle file, so the bundle
# is the system roots plus the dev CA. Mono has a store of its own, $HOME/.config/.mono/certs, which cert-sync
# fills from a PEM file; it gets the same bundle. The file `ready` next to the bundle is written last.
set -eu
ca="${TROVE_CA_FILE:-/etc/trove-ca/ca.crt}"
bundle="${SSL_CERT_FILE:-/tmp/trove-ca/bundle.crt}"
[ -s "$ca" ] || { echo "entrypoint: the dev CA $ca is missing or empty" >&2; exit 1; }
mkdir -p "$(dirname "$bundle")"
cat /etc/ssl/certs/ca-certificates.crt "$ca" >"$bundle.tmp"
mv "$bundle.tmp" "$bundle"
cert-sync --quiet --user "$bundle" >/dev/null
# Every `dotnet restore` runs NuGet's migrations once per HOME, under a named mutex, and the marker of the run is the empty file
# $HOME/.local/share/NuGet/Migrations/1. The runtime of the .NET 8 SDK cannot create a named mutex in the pod (`dotnet restore` fails
# with an IOException on 'NuGet-Migrations'; pre-creating /tmp/.dotnet and $HOME/.dotnet/corefx/Runtime/SharedMemory with the
# directories the .NET 10 runtime makes does not help, the cause of the refusal was not investigated); the .NET 9 and 10 runtimes
# can. A restore of any SDK that finds the marker takes no mutex, so the pod writes the marker the first run would write. The SDKs of the
# vulnerability tests (R-VULN-029) are .NET 8, 9 and 10.
mkdir -p "$HOME/.local/share/NuGet/Migrations"
: >"$HOME/.local/share/NuGet/Migrations/1"
# the pod's readiness probe (deploy/dev/e2e.sh): the trust of both runtimes is complete
: >"$(dirname "$bundle")/ready"
exec /usr/local/bin/fwd "$@"
