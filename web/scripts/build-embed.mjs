// Runs the localized production build and copies the per-locale output to
// ../internal/webui/dist/<locale>/ (R-ARCH-004). The committed placeholder
// internal/webui/dist/index.html is never touched; stale locale directories are removed first.
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { checkBudget } from './check-budget.mjs';

const root = fileURLToPath(new URL('..', import.meta.url));
const built = join(root, 'dist', 'web', 'browser');
const target = join(root, '..', 'internal', 'webui', 'dist');
const marker = 'TROVE_CSP_NONCE';

rmSync(join(root, 'dist'), { recursive: true, force: true });
const build = spawnSync('npx', ['--no-install', 'ng', 'build', '--configuration', 'production'], {
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
}

for (const name of readdirSync(target)) {
  const path = join(target, name);
  if (isDir(path)) rmSync(path, { recursive: true, force: true });
}
for (const locale of locales) {
  cpSync(join(built, locale), join(target, locale), { recursive: true });
}
console.log(`build-embed: copied ${locales.join(', ')} to ${target}`);
