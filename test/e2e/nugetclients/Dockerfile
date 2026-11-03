# The NuGet clients of the acceptance suite (R-NUGET-027, docs/developer/e2e.md#the-nuget-clients): the .NET SDK
# (`dotnet`) of the pinned image and nuget.exe on Mono, with the forwarder that lets them use the registry's base
# URL unchanged. It is a test image: never pushed, built by test/e2e/nugetclients/build.sh, loaded into kind by
# deploy/dev/e2e.sh. Not baked in: the dev CA (the pod mounts it; entrypoint.sh builds the trust at pod start) and
# any credential.
#
#   SDK_IMAGE        E2E_NUGETCLIENTS_SDK_IMAGE of tools/versions.env (the .NET SDK image, pinned by digest)
#   SDK8_IMAGE, SDK9_IMAGE   E2E_NUGETCLIENTS_SDK8_IMAGE and _SDK9_IMAGE: the .NET 8 and 9 SDKs of the audit scenarios of R-VULN-018. Only
#                    the SDK, its runtime and its host resolver are copied next to the .NET 10 SDK; a project picks one with a global.json
#   MONO_VERSION     E2E_NUGETCLIENTS_MONO_VERSION: the apt version of the Mono packages of the SDK image's Ubuntu
#   JQ_VERSION       E2E_NUGETCLIENTS_JQ_VERSION: the exact apt version of jq, which the exchange step of the reference pipeline
#                    (R-CI-028) runs next to the curl of the SDK image
#   NUGET_EXE_VERSION, NUGET_EXE_SHA256   E2E_NUGETCLIENTS_NUGET_EXE_*: the nuget.exe on dist.nuget.org and its digest
#   --build-context fwd=<dir>   a directory with the static forwarder fwd-linux-<arch> (build.sh builds it)
ARG SDK_IMAGE
ARG SDK8_IMAGE
ARG SDK9_IMAGE

FROM ${SDK8_IMAGE} AS sdk8
FROM ${SDK9_IMAGE} AS sdk9

FROM ${SDK_IMAGE} AS nugetexe
ARG NUGET_EXE_VERSION
ARG NUGET_EXE_SHA256
RUN curl --fail --silent --show-error --location --output /nuget.exe \
      "https://dist.nuget.org/win-x86-commandline/v${NUGET_EXE_VERSION}/nuget.exe" \
 && echo "${NUGET_EXE_SHA256}  /nuget.exe" | sha256sum --check --strict -

FROM ${SDK_IMAGE}
ARG TARGETARCH
ARG MONO_VERSION
ARG JQ_VERSION
# mono-complete: the runtime and every class library nuget.exe may load (the dependencies of the package are
# pinned to its version, so this one pin fixes all of Mono); ca-certificates-mono carries cert-sync, which fills the
# Mono trust store from a PEM bundle (entrypoint.sh). Exactly the pinned version: a superseded package fails the
# build instead of changing the client. jq is the second tool of the reference pipeline's exchange step (R-CI-028).
RUN apt-get update \
 && apt-get install --yes --no-install-recommends "mono-complete=${MONO_VERSION}" "ca-certificates-mono=${MONO_VERSION}" "jq=${JQ_VERSION}" \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir /work && chown 1000:1000 /work
COPY --from=nugetexe /nuget.exe /opt/nuget/nuget.exe
# The .NET 8 and 9 SDKs next to the .NET 10 one: the SDK directory, the runtime each of them runs on and the host resolver
# of that runtime. `dotnet` of the .NET 10 SDK is the muxer; global.json chooses the SDK, and a restore of a project that
# sets DisableImplicitFrameworkReferences needs no targeting pack.
COPY --from=sdk8 /usr/share/dotnet/sdk/ /usr/share/dotnet/sdk/
COPY --from=sdk8 /usr/share/dotnet/shared/Microsoft.NETCore.App/ /usr/share/dotnet/shared/Microsoft.NETCore.App/
COPY --from=sdk8 /usr/share/dotnet/host/fxr/ /usr/share/dotnet/host/fxr/
COPY --from=sdk9 /usr/share/dotnet/sdk/ /usr/share/dotnet/sdk/
COPY --from=sdk9 /usr/share/dotnet/shared/Microsoft.NETCore.App/ /usr/share/dotnet/shared/Microsoft.NETCore.App/
COPY --from=sdk9 /usr/share/dotnet/host/fxr/ /usr/share/dotnet/host/fxr/
COPY bin/ /usr/local/bin/
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
COPY --from=fwd fwd-linux-${TARGETARCH} /usr/local/bin/fwd
USER 1000:1000
WORKDIR /work
# A client runs per `kubectl exec`, with this environment plus the pod's (deploy/dev/e2e.sh sets SSL_CERT_FILE
# there). HOME is the writable /work: dotnet and nuget.exe write their user state there, and the tests give every
# test its own NUGET_PACKAGES and NUGET_HTTP_CACHE_PATH below it.
ENV HOME=/work \
    DOTNET_CLI_TELEMETRY_OPTOUT=1 \
    DOTNET_NOLOGO=1 \
    DOTNET_SKIP_FIRST_TIME_EXPERIENCE=1 \
    DOTNET_GENERATE_ASPNET_CERTIFICATE=false \
    DOTNET_SKIP_WORKLOAD_INTEGRITY_CHECK=1 \
    NUGET_XMLDOC_MODE=skip
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
