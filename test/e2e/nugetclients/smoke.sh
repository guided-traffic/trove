#!/usr/bin/env bash
# The smoke test of the NuGet clients image (R-NUGET-027, docs/developer/e2e.md#the-nuget-clients), without a cluster:
# the image runs as the pod does (non-root, read-only root filesystem, /tmp and /work writable, the CA mounted), and
# the checks are
#   1. `dotnet --info` runs and names the pinned SDK,
#   2. nuget.exe on Mono prints the pinned version,
#   3. the entrypoint built the CA bundle (system roots plus the CA) and filled the Mono store,
#   4. both runtimes verify a TLS server of that CA through the forwarder at https://localhost:<port>: `dotnet restore`
#      and `nuget install` fetch a package from the counting NuGet test upstream (test/nugetstub),
#   5. without the CA both refuse the certificate (so 4 proves the trust logic and not a lax client).
#
#   smoke.sh [image]    default image trove-e2e-nugetclients:dev (build it first: make nuget-clients-image)
#
# Needs Docker and the stub image (make nuget-stub-image IMAGE_ARCH=<arch>, tag trove-e2e-nugetstub:dev; STUB_IMAGE
# overrides). `make nuget-clients-smoke` builds both and runs this.
set -euo pipefail
# shellcheck source=deploy/dev/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../deploy/dev" && pwd)/lib.sh"

image="${1:-trove-e2e-nugetclients:dev}"
stub_image="${STUB_IMAGE:-trove-e2e-nugetstub:dev}"
port=8443
id=Contoso.Util
version=2.1.0
run="smoke-$$"
net="$run"
tmp="$(mktemp -d)"
chmod 777 "$tmp"
fails=0

cleanup() {
  docker rm -f "$run-stub" "$run-clients" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT

ok() { printf 'ok   %s\n' "$*"; }
bad() { printf 'FAIL %s\n' "$*" >&2; fails=$((fails + 1)); }
# check <name> <command...>: the command's output is shown only when it fails
check() {
  local name="$1" out; shift
  if out="$("$@" 2>&1)"; then ok "$name"; else bad "$name"; printf '%s\n' "$out" | tail -15 >&2; fi
}
# refuses <name> <command...>: the command must fail and its output must name the certificate
refuses() {
  local name="$1" out; shift
  if out="$("$@" 2>&1)"; then bad "$name: the command succeeded"; return; fi
  if printf '%s' "$out" | grep -qiE 'SSL|certificate|trust|NU1301|RemoteCertificateNameMismatch|UntrustedRoot'; then ok "$name"; else bad "$name: it failed, but not on the certificate"; printf '%s\n' "$out" | tail -10 >&2; fi
}
cx() { docker exec "$run-clients" "$@"; }

docker network create "$net" >/dev/null
# The stub makes its own CA and a certificate for localhost; the documents name https://localhost:$port, which in the
# clients container is the forwarder (127.0.0.1:$port -> the stub).
docker run -d --name "$run-stub" --network "$net" --network-alias nugetstub -v "$tmp:/out" "$stub_image" \
  serve -listen ":$port" -self-signed localhost -ca-out /out/ca.crt -public-url "https://localhost:$port" >/dev/null
for _ in $(seq 1 50); do [ -s "$tmp/ca.crt" ] && break; sleep 0.2; done
[ -s "$tmp/ca.crt" ] || { docker logs "$run-stub" >&2; die "the stub wrote no CA"; }

# The pod's shape: non-root user of the image, read-only root, the CA read-only at /etc/trove-ca/ca.crt, SSL_CERT_FILE in the environment.
docker run -d --name "$run-clients" --network "$net" --read-only \
  --tmpfs /tmp:rw,mode=1777,size=512m --tmpfs /work:rw,uid=1000,gid=1000,size=1g \
  --cap-drop ALL --security-opt no-new-privileges \
  -v "$tmp/ca.crt:/etc/trove-ca/ca.crt:ro" -e SSL_CERT_FILE=/tmp/trove-ca/bundle.crt \
  "$image" -listen "127.0.0.1:$port" -to "nugetstub:$port" >/dev/null
for _ in $(seq 1 50); do cx test -f /tmp/trove-ca/ready 2>/dev/null && break; sleep 0.2; done
cx test -f /tmp/trove-ca/ready || { docker logs "$run-clients" >&2; die "the clients container did not become ready"; }

url="https://localhost:$port/v3/index.json"
work=/work/smoke
cx mkdir -p "$work/p" "$work/n"

check "dotnet --info names SDK $(printf '%s' "$E2E_NUGETCLIENTS_SDK_IMAGE" | sed 's/.*sdk:\([^@]*\)@.*/\1/')" bash -c \
  "docker exec $run-clients dotnet --info | grep -E '^ Version: +$(printf '%s' "$E2E_NUGETCLIENTS_SDK_IMAGE" | sed 's/.*sdk:\([^@]*\)@.*/\1/' | sed 's/\./\\./g')\$'"
check "nuget.exe on Mono prints $E2E_NUGETCLIENTS_NUGET_EXE_VERSION" bash -c \
  "docker exec $run-clients nuget help | head -1 | grep -F 'NuGet Version: $E2E_NUGETCLIENTS_NUGET_EXE_VERSION.'"
check "the bundle holds the system roots and the CA" bash -c \
  "docker exec $run-clients cat /tmp/trove-ca/bundle.crt | grep -c 'BEGIN CERTIFICATE' | awk '{exit !(\$1 > 50)}' && docker exec $run-clients grep -qF \"\$(sed -n 2p '$tmp/ca.crt')\" /tmp/trove-ca/bundle.crt"
check "the Mono store was filled for the pod user" bash -c \
  "docker exec $run-clients sh -c 'ls /work/.config/.mono/certs/Trust | wc -l' | awk '{exit !(\$1 > 50)}'"

# A project that needs one package of the stub. net10.0 is resolved from the SDK's own packs (no nuget.org).
cat >"$tmp/smoke.csproj" <<EOF
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup>
  <ItemGroup><PackageReference Include="$id" Version="$version" /></ItemGroup>
</Project>
EOF
docker exec -i "$run-clients" sh -c "cat >$work/p/smoke.csproj" <"$tmp/smoke.csproj"
check "dotnet restore fetches $id $version over TLS" cx env NUGET_PACKAGES="$work/p/packages" NUGET_HTTP_CACHE_PATH="$work/p/http" \
  dotnet restore "$work/p/smoke.csproj" --source "$url"
check "the restored package is in NUGET_PACKAGES" cx test -s "$work/p/packages/$(printf '%s' "$id" | tr '[:upper:]' '[:lower:]')/$version/$(printf '%s' "$id" | tr '[:upper:]' '[:lower:]').$version.nupkg"
check "nuget.exe install fetches $id $version over TLS" cx env NUGET_PACKAGES="$work/n/packages" NUGET_HTTP_CACHE_PATH="$work/n/http" \
  nuget install "$id" -Version "$version" -Source "$url" -OutputDirectory "$work/n/out" -NonInteractive
check "nuget.exe left $id.$version.nupkg in the output directory" cx test -s "$work/n/out/$id.$version/$id.$version.nupkg"

# Without the CA: .NET with the system bundle alone, Mono with an empty user store.
refuses "dotnet without the CA refuses the certificate" cx env SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt NUGET_PACKAGES="$work/p/none" NUGET_HTTP_CACHE_PATH="$work/p/none-http" \
  dotnet restore "$work/p/smoke.csproj" --source "$url" --force --no-cache
refuses "nuget.exe without the CA refuses the certificate" cx env HOME=/tmp/emptyhome NUGET_PACKAGES="$work/n/none" NUGET_HTTP_CACHE_PATH="$work/n/none-http" \
  nuget install "$id" -Version "$version" -Source "$url" -OutputDirectory "$work/n/none-out" -NonInteractive

[ "$fails" -eq 0 ] || { log "smoke FAILED ($fails)"; exit 1; }
log "smoke passed"
