// Checks the pins of the NuGet clients image (R-NUGET-027) for `make versions-check`:
//   node test/e2e/nugetclients/check-pins.mjs <E2E_NUGETCLIENTS_SDK_IMAGE> <NUGETVECTORS_SDK_IMAGE> <E2E_NUGETCLIENTS_MONO_VERSION> \
//        <E2E_NUGETCLIENTS_NUGET_EXE_VERSION> <E2E_NUGETCLIENTS_NUGET_EXE_SHA256> <E2E_NUGETCLIENTS_JQ_VERSION> \
//        <E2E_NUGETCLIENTS_SDK8_IMAGE> <E2E_NUGETCLIENTS_SDK9_IMAGE>
// 1. The SDK image is <image>:<tag>@sha256:<digest> with a .NET 10 tag, and it is the very image of the golden vectors
//    (one .NET SDK in the repository).
// 2. The .NET 8 and .NET 9 SDK images (the audit scenarios of R-VULN-018) are <image>:<tag>@sha256:<digest> with a tag of their major.
// 3. The Mono and jq versions are exact apt versions, the nuget.exe version is x.y.z, its digest is a SHA-256.
// 4. The Dockerfile takes all of them as build arguments and has no literal pin of its own; the wrapper bin/nuget
//    runs the nuget.exe the Dockerfile installs; build.sh passes every pin.
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dir = dirname(fileURLToPath(import.meta.url));
const [sdkImage, vectorsImage, mono, exe, sha, jq, sdk8, sdk9] = process.argv.slice(2);
const problems = [];

const m = /^([^:@]+):([^@]+)@sha256:[0-9a-f]{64}$/.exec(sdkImage ?? '');
if (!m) problems.push(`E2E_NUGETCLIENTS_SDK_IMAGE '${sdkImage}' is not <image>:<tag>@sha256:<digest>`);
else if (!/^10\.\d+\.\d+$/.test(m[2])) problems.push(`E2E_NUGETCLIENTS_SDK_IMAGE tag '${m[2]}' is not a .NET 10 SDK (R-NUGET-027 pins the .NET 10 SDK)`);
for (const [name, image, major] of [['E2E_NUGETCLIENTS_SDK8_IMAGE', sdk8, 8], ['E2E_NUGETCLIENTS_SDK9_IMAGE', sdk9, 9]]) {
  const x = /^([^:@]+):([^@]+)@sha256:[0-9a-f]{64}$/.exec(image ?? '');
  if (!x) problems.push(`${name} '${image}' is not <image>:<tag>@sha256:<digest>`);
  else if (!new RegExp(`^${major}\\.\\d+\\.\\d+$`).test(x[2])) problems.push(`${name} tag '${x[2]}' is not a .NET ${major} SDK`);
}
if (sdkImage !== vectorsImage) problems.push(`E2E_NUGETCLIENTS_SDK_IMAGE (${sdkImage}) is not NUGETVECTORS_SDK_IMAGE (${vectorsImage}): one .NET SDK`);
if (!/^\d+\.\d+\.\d+\.\d+\+[A-Za-z0-9.+~-]+$/.test(mono ?? '')) problems.push(`E2E_NUGETCLIENTS_MONO_VERSION '${mono}' is not an exact apt version (x.y.z.w+suffix)`);
if (!/^\d+\.\d+(\.\d+)?-[A-Za-z0-9.+~-]+$/.test(jq ?? '')) problems.push(`E2E_NUGETCLIENTS_JQ_VERSION '${jq}' is not an exact apt version (x.y.z-suffix)`);
if (!/^\d+\.\d+\.\d+$/.test(exe ?? '')) problems.push(`E2E_NUGETCLIENTS_NUGET_EXE_VERSION '${exe}' is not x.y.z`);
if (!/^[0-9a-f]{64}$/.test(sha ?? '')) problems.push(`E2E_NUGETCLIENTS_NUGET_EXE_SHA256 '${sha}' is not a SHA-256`);

const docker = readFileSync(join(dir, 'Dockerfile'), 'utf8');
for (const arg of ['SDK_IMAGE', 'SDK8_IMAGE', 'SDK9_IMAGE', 'MONO_VERSION', 'JQ_VERSION', 'NUGET_EXE_VERSION', 'NUGET_EXE_SHA256']) {
  if (!new RegExp(`^ARG ${arg}\\s*$`, 'm').test(docker)) problems.push(`Dockerfile: ARG ${arg} is missing or has a default`);
}
const froms = [...docker.matchAll(/^FROM\s+(\S+)/gm)].map((x) => x[1]);
for (const f of froms) if (!['${SDK_IMAGE}', '${SDK8_IMAGE}', '${SDK9_IMAGE}'].includes(f)) problems.push(`Dockerfile: FROM ${f} is not a pinned SDK image`);
if (/mono-complete=(?!\$\{MONO_VERSION\})/.test(docker) || /ca-certificates-mono=(?!\$\{MONO_VERSION\})/.test(docker)) problems.push('Dockerfile: a Mono package is pinned to something other than ${MONO_VERSION}');

if (/jq=(?!\$\{JQ_VERSION\})/.test(docker)) problems.push('Dockerfile: jq is pinned to something other than ${JQ_VERSION}');

const wrapper = readFileSync(join(dir, 'bin', 'nuget'), 'utf8');
const installed = /COPY --from=nugetexe (\S+) (\S+)/.exec(docker)?.[2];
if (!installed || !wrapper.includes(`mono ${installed}`)) problems.push(`bin/nuget does not run ${installed ?? '<the nuget.exe the Dockerfile installs>'} with mono`);

const build = readFileSync(join(dir, 'build.sh'), 'utf8');
for (const v of ['E2E_NUGETCLIENTS_SDK_IMAGE', 'E2E_NUGETCLIENTS_SDK8_IMAGE', 'E2E_NUGETCLIENTS_SDK9_IMAGE', 'E2E_NUGETCLIENTS_MONO_VERSION', 'E2E_NUGETCLIENTS_JQ_VERSION', 'E2E_NUGETCLIENTS_NUGET_EXE_VERSION', 'E2E_NUGETCLIENTS_NUGET_EXE_SHA256']) {
  if (!build.includes(`--build-arg`) || !build.includes(`$${v}`)) problems.push(`build.sh does not pass ${v}`);
}

if (problems.length) {
  console.error(problems.join('\n'));
  process.exit(1);
}
