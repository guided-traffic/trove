// Checks the pins of the APT clients images (R-DEB-035) for `make versions-check`:
//   node test/e2e/aptclients/check-pins.mjs <E2E_APTCLIENTS_DEBIAN13_IMAGE> <..._DEBIAN12_IMAGE> <..._UBUNTU2404_IMAGE> <..._UBUNTU2204_IMAGE>
// 1. Each pin is <image>:<tag>@sha256:<digest> of the image and release its name says (debian 13, debian 12, ubuntu 24.04,
//    ubuntu 22.04): the four clients are four different releases, so a pin never moves to another tag.
// 2. The four pins are four different images.
// 3. The Dockerfile takes its base as a build argument without a default and has no literal pin of its own; build.sh passes all four pins.
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dir = dirname(fileURLToPath(import.meta.url));
const pins = process.argv.slice(2);
const want = [
  ['E2E_APTCLIENTS_DEBIAN13_IMAGE', 'docker.io/library/debian', '13'],
  ['E2E_APTCLIENTS_DEBIAN12_IMAGE', 'docker.io/library/debian', '12'],
  ['E2E_APTCLIENTS_UBUNTU2404_IMAGE', 'docker.io/library/ubuntu', '24.04'],
  ['E2E_APTCLIENTS_UBUNTU2204_IMAGE', 'docker.io/library/ubuntu', '22.04'],
];
const problems = [];

want.forEach(([name, image, tag], i) => {
  const pin = pins[i] ?? '';
  if (!new RegExp(`^${image.replaceAll('.', '\\.')}:${tag.replaceAll('.', '\\.')}@sha256:[0-9a-f]{64}$`).test(pin)) {
    problems.push(`${name} '${pin}' is not ${image}:${tag}@sha256:<digest>`);
  }
});
if (new Set(pins).size !== pins.length) problems.push('two APT client pins are the same image');

const text = readFileSync(join(dir, 'Dockerfile'), 'utf8');
if (!/^ARG BASE_IMAGE\s*$/m.test(text)) problems.push('Dockerfile: ARG BASE_IMAGE is missing or has a default');
for (const m of text.matchAll(/^FROM\s+(\S+)/gm)) if (m[1] !== '${BASE_IMAGE}') problems.push(`Dockerfile: FROM ${m[1]} is not the pinned \${BASE_IMAGE}`);

const build = readFileSync(join(dir, 'build.sh'), 'utf8');
for (const [name] of want) if (!build.includes(`$${name}`)) problems.push(`build.sh does not pass ${name}`);

if (problems.length) {
  console.error(problems.join('\n'));
  process.exit(1);
}
