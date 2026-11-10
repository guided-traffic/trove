// Checks the pins of the Ansible clients images (R-ANS-032) for `make versions-check`:
//   node test/e2e/ansibleclients/check-pins.mjs <E2E_ANSIBLECLIENTS_CORE217_IMAGE> <..._CORE217_VERSION> <..._CORE221_IMAGE> <..._CORE221_VERSION>
// 1. Each version is a release of the series its name says (2.17.x, 2.21.x), and each image pin is
//    docker.io/library/python:<python>-<variant>@sha256:<digest> of the Python that series runs on (3.11 for 2.17, which supports 3.10 to
//    3.12; 3.13 for 2.21, which supports 3.12 to 3.14): the two clients are two different releases on two different Pythons.
// 2. The two image pins are two different images.
// 3. The Dockerfile takes its base and the version as build arguments without a default and has no literal pin of its own, and build.sh
//    passes both pins and both versions.
// 4. Each constraints file names ansible-core at the pinned version and every line as name==version: no dependency floats.
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dir = dirname(fileURLToPath(import.meta.url));
const [image217, version217, image221, version221] = process.argv.slice(2);
const want = [
  { series: '2.17', python: '3.11', imageName: 'E2E_ANSIBLECLIENTS_CORE217_IMAGE', versionName: 'E2E_ANSIBLECLIENTS_CORE217_VERSION', image: image217, version: version217 },
  { series: '2.21', python: '3.13', imageName: 'E2E_ANSIBLECLIENTS_CORE221_IMAGE', versionName: 'E2E_ANSIBLECLIENTS_CORE221_VERSION', image: image221, version: version221 },
];
const problems = [];
const escape = (s) => s.replaceAll('.', '\\.');

for (const w of want) {
  if (!new RegExp(`^${escape(w.series)}\\.[0-9]+$`).test(w.version ?? '')) problems.push(`${w.versionName} '${w.version}' is not a ${w.series}.<patch> release`);
  if (!new RegExp(`^docker\\.io/library/python:${escape(w.python)}-[a-z0-9-]+@sha256:[0-9a-f]{64}$`).test(w.image ?? '')) {
    problems.push(`${w.imageName} '${w.image}' is not docker.io/library/python:${w.python}-<variant>@sha256:<digest>`);
  }
  let constraints = '';
  try {
    constraints = readFileSync(join(dir, `constraints-${w.series}.txt`), 'utf8');
  } catch {
    problems.push(`constraints-${w.series}.txt is missing`);
    continue;
  }
  const lines = constraints.split('\n').filter((l) => l.trim() !== '' && !l.startsWith('#'));
  for (const l of lines) if (!/^[A-Za-z0-9._-]+==[A-Za-z0-9._+!-]+$/.test(l)) problems.push(`constraints-${w.series}.txt: '${l}' is not name==version`);
  if (!lines.includes(`ansible-core==${w.version}`)) problems.push(`constraints-${w.series}.txt does not pin ansible-core==${w.version}`);
}
if (image217 === image221) problems.push('the two Ansible client pins are the same image');

const text = readFileSync(join(dir, 'Dockerfile'), 'utf8');
for (const arg of ['BASE_IMAGE', 'ANSIBLE_CORE_VERSION', 'ANSIBLE_SERIES']) {
  if (!new RegExp(`^ARG ${arg}\\s*$`, 'm').test(text)) problems.push(`Dockerfile: ARG ${arg} is missing or has a default`);
}
for (const m of text.matchAll(/^FROM\s+(\S+)/gm)) if (m[1] !== '${BASE_IMAGE}') problems.push(`Dockerfile: FROM ${m[1]} is not the pinned \${BASE_IMAGE}`);

const build = readFileSync(join(dir, 'build.sh'), 'utf8');
for (const w of want) for (const name of [w.imageName, w.versionName]) if (!build.includes(`$${name}`)) problems.push(`build.sh does not pass ${name}`);

if (problems.length) {
  console.error(problems.join('\n'));
  process.exit(1);
}
