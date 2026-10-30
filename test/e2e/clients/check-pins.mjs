// Checks the pins of the npm client matrix (R-NPM-045) for `make versions-check`:
//   node test/e2e/clients/check-pins.mjs <expected Node version> <E2E_CLIENTS_NODE_IMAGE>
// 1. The Node image tag starts with the Node version of web/.nvmrc (one Node for the repository).
// 2. Every dependency of package.json is an alias with one exact version (npm:<package>@x.y.z).
// 3. package-lock.json resolves each alias to exactly that package and version.
// 4. Every alias has a wrapper in bin/ (the entry the e2e suite calls) and no wrapper is without a pin.
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dir = dirname(fileURLToPath(import.meta.url));
const [nodeVersion, nodeImage] = process.argv.slice(2);
const problems = [];

const tag = /^[^:@]+:([^@]+)@sha256:[0-9a-f]{64}$/.exec(nodeImage ?? '')?.[1];
if (!tag) problems.push(`E2E_CLIENTS_NODE_IMAGE '${nodeImage}' is not <image>:<tag>@sha256:<digest>`);
else if (!tag.startsWith(`${nodeVersion}-`)) problems.push(`E2E_CLIENTS_NODE_IMAGE tag '${tag}' does not start with web/.nvmrc ${nodeVersion}-`);

const pkg = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'));
const lock = JSON.parse(readFileSync(join(dir, 'package-lock.json'), 'utf8'));
const aliases = Object.keys(pkg.dependencies ?? {});
for (const alias of aliases) {
  const m = /^npm:(@?[^@]+)@(\d+\.\d+\.\d+)$/.exec(pkg.dependencies[alias]);
  if (!m) { problems.push(`package.json: ${alias} is '${pkg.dependencies[alias]}', want npm:<package>@<exact version>`); continue; }
  const locked = lock.packages?.[`node_modules/${alias}`];
  if (!locked || locked.name !== m[1] || locked.version !== m[2]) {
    problems.push(`package-lock.json: ${alias} is ${locked?.name ?? '<missing>'}@${locked?.version ?? '-'}, package.json pins ${m[1]}@${m[2]}`);
  }
}
const wrappers = readdirSync(join(dir, 'bin'));
for (const a of aliases) if (!wrappers.includes(a)) problems.push(`bin/${a} is missing`);
for (const w of wrappers) if (!aliases.includes(w)) problems.push(`bin/${w} has no pin in package.json`);

if (problems.length) {
  console.error(problems.join('\n'));
  process.exit(1);
}
