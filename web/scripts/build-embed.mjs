// Runs the localized production build and copies the per-locale output to
// ../internal/webui/dist/<locale>/ (R-ARCH-004). The committed placeholder
// internal/webui/dist/index.html is never touched; stale locale directories are removed first.
//
// TROVE_UI_BUNDLE_MARK=1 builds the second bundle of the upgrade test across two bundles (R-HA-020, Q-HA-124): the licence
// comments of the libraries stay in the scripts instead of going to 3rdpartylicenses.txt, which changes the bytes and so
// the hashed name of nearly every script and leaves what the scripts do as it is.
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { checkBudget } from './check-budget.mjs';
import { checkHashed } from './hashed.mjs';

const root = fileURLToPath(new URL('..', import.meta.url));
const built = join(root, 'dist', 'web', 'browser');
const target = join(root, '..', 'internal', 'webui', 'dist');
const marker = 'TROVE_CSP_NONCE';

const mark = process.env['TROVE_UI_BUNDLE_MARK'] ?? '';
if (mark !== '' && mark !== '1') {
  console.error(`build-embed: TROVE_UI_BUNDLE_MARK is "${mark}"; it is empty or 1`);
  process.exit(1);
}
const args = ['--no-install', 'ng', 'build', '--configuration', 'production'];
if (mark === '1') {
  args.push('--extract-licenses=false');
  console.log('build-embed: TROVE_UI_BUNDLE_MARK=1, the second bundle: licence comments stay in the scripts');
}

rmSync(join(root, 'dist'), { recursive: true, force: true });
const build = spawnSync('npx', args, {
  cwd: root,
  stdio: 'inherit',
});
if (build.status !== 0) {
  process.exit(build.status ?? 1);
}

if (!checkBudget(built)) {
  console.error('build-embed: initial bundle exceeds the budget of R-UI-001');
  process.exit(1);
}

const isDir = (p) => statSync(p).isDirectory();
const locales = readdirSync(built).filter((name) => isDir(join(built, name)));
if (locales.length === 0) {
  console.error(`build-embed: no locale directory under ${built}`);
  process.exit(1);
}
for (const locale of locales) {
  const index = join(built, locale, 'index.html');
  if (!existsSync(index) || !readFileSync(index, 'utf8').includes(marker)) {
    console.error(`build-embed: ${index} is missing or lacks the ${marker} placeholder`);
    process.exit(1);
  }
  // R-UI-001, Q-UI-140: the server's pattern of hashed files must hold for this build, both ways.
  const problems = checkHashed(join(built, locale), join(root, 'public'));
  if (problems.length > 0) {
    console.error(`build-embed: the files of ${locale} do not match the hashed pattern of internal/webui/hashed.go:`);
    for (const p of problems) console.error(`  ${p}`);
    process.exit(1);
  }
}

for (const name of readdirSync(target)) {
  const path = join(target, name);
  if (isDir(path)) rmSync(path, { recursive: true, force: true });
}
for (const locale of locales) {
  cpSync(join(built, locale), join(target, locale), { recursive: true });
}
console.log(`build-embed: copied ${locales.join(', ')} to ${target}`);
