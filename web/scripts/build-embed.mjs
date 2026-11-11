// Runs the localized production build and copies the per-locale output to
// ../internal/webui/dist/<locale>/ (R-ARCH-004). The committed placeholder
// internal/webui/dist/index.html is never touched; stale locale directories are removed first.
//
// Every locale directory gets 3rdpartylicenses.txt (R-UI-001, Q-UI-144): the notices of the scripts and styles that
// Angular collects, the licence of the icon font, which carries no licence comment and so is not in that file, and the
// owners, sources, licences and trademark notices of the format logos in public/formats (Q-UI-227).
// The file is written after the hashed-name check, because it is not a hashed file and is never published to the store.
//
// TROVE_UI_BUNDLE_MARK=1 builds the second bundle of the upgrade test across two bundles (R-HA-020, Q-HA-124): the licence
// comments of the libraries stay in the scripts instead of going to 3rdpartylicenses.txt, which changes the bytes and so
// the hashed name of nearly every script and leaves what the scripts do as it is. That bundle gets its notices from a
// second build that only extracts them, so every binary ships the notices of its own scripts.
import { spawnSync } from 'node:child_process';
import {
  cpSync,
  existsSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { checkBudget } from './check-budget.mjs';
import { checkHashed } from './hashed.mjs';

const root = fileURLToPath(new URL('..', import.meta.url));
const built = join(root, 'dist', 'web', 'browser');
const target = join(root, '..', 'internal', 'webui', 'dist');
const marker = 'TROVE_CSP_NONCE';
// The file the handler serves as <locale>/3rdpartylicenses.txt (TestR_UI_001_ThirdPartyNotices in internal/webui).
const notices = '3rdpartylicenses.txt';
const rule = '-'.repeat(80);

const mark = process.env['TROVE_UI_BUNDLE_MARK'] ?? '';
if (mark !== '' && mark !== '1') {
  console.error(`build-embed: TROVE_UI_BUNDLE_MARK is "${mark}"; it is empty or 1`);
  process.exit(1);
}
const release = ['--no-install', 'ng', 'build', '--configuration', 'production'];
const args = mark === '1' ? [...release, '--extract-licenses=false'] : release;
if (mark === '1') {
  console.log(
    'build-embed: TROVE_UI_BUNDLE_MARK=1, the second bundle: licence comments stay in the scripts',
  );
}

function ng(cmd) {
  const r = spawnSync('npx', cmd, { cwd: root, stdio: 'inherit' });
  if (r.status !== 0) process.exit(r.status ?? 1);
}

rmSync(join(root, 'dist'), { recursive: true, force: true });
ng(args);

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
    console.error(
      `build-embed: the files of ${locale} do not match the hashed pattern of internal/webui/hashed.go:`,
    );
    for (const p of problems) console.error(`  ${p}`);
    process.exit(1);
  }
}

// R-UI-002, Q-UI-167: the generated avatar needs no host but the origin's own. DiceBear also runs as an HTTP service
// (api.dicebear.com); no script of the build may name it.
for (const locale of locales) {
  const dir = join(built, locale);
  for (const name of readdirSync(dir).filter((n) => n.endsWith('.js'))) {
    if (/api\.dicebear\.com/.test(readFileSync(join(dir, name), 'utf8'))) {
      console.error(`build-embed: ${join(locale, name)} names the DiceBear HTTP service`);
      process.exit(1);
    }
  }
}

// The notices (Q-UI-144). Angular writes its file next to the locale directories; the second bundle extracts its own.
function angularNotices() {
  if (mark !== '1') return readFileSync(join(root, 'dist', 'web', notices), 'utf8');
  const extract = join(root, 'dist', 'licences');
  ng([...release, '--output-path', extract]);
  return readFileSync(join(extract, notices), 'utf8');
}

// The icon font (material-icons, Apache-2.0) is not in Angular's file: its CSS carries no licence comment.
function iconFontNotice() {
  const dir = join(root, 'node_modules', 'material-icons');
  const pkg = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'));
  const text = readFileSync(join(dir, 'LICENSE'), 'utf8');
  return `Package: ${pkg.name}\nVersion: ${pkg.version}\nLicense: "${pkg.license}"\n\n${text}`;
}

// The format logos (Q-UI-225, Q-UI-227): public/formats/NOTICE.txt names each logo file with its owner, source, licence and
// trademark notice. The file is also served next to the logos; here it joins the notices of the build.
const formatLogos = 'formats/NOTICE.txt';
function formatLogoNotice() {
  return readFileSync(join(root, 'public', formatLogos), 'utf8');
}

// Angular's file ends with the rule that opens each package's section; the icon font's section follows it, then the logos.
const angular = angularNotices().trimEnd();
const text = `${angular.endsWith(rule) ? angular : `${angular}\n${rule}`}\n${iconFontNotice()}\n${rule}\n${formatLogoNotice()}`;
// The notices must name Inter with its OFL-1.1 licence, the icon font with its Apache-2.0 licence, DiceBear's core with the
// licence of the Bottts style (the artist's "free for personal and commercial use", Q-UI-167), and each format logo with
// its trademark notice and the line that no owner endorses trove (Q-UI-227), or the build fails.
const required = [
  '\nPackage: @fontsource-variable/inter\n',
  'License: "OFL-1.1"',
  'SIL OPEN FONT LICENSE Version 1.1',
  '\nPackage: material-icons\n',
  'License: "Apache-2.0"',
  '\nPackage: @dicebear/core\n',
  '\nPackage: @dicebear/styles\n',
  '### Bottts\n',
  'Free for personal and commercial use',
  'trove is not affiliated with, sponsored or endorsed by any of them.',
  'File: formats/deb/openlogo-nd.svg\n',
  'Debian is a registered trademark owned by Software in the Public Interest, Inc.',
  'Files: formats/nuget/nuget.svg, formats/nuget/nuget_96_mono_w.png',
  'Creative Commons Attribution 2.0',
  'Files: formats/oci/oci-icon-color.svg, formats/oci/oci-icon-white.svg',
  'Open Container Initiative(R) is a registered trademark',
];
const missing = required.filter((s) => !text.includes(s));
if (missing.length > 0) {
  console.error(
    `build-embed: ${notices} lacks ${missing.map((s) => JSON.stringify(s.trim())).join(', ')}`,
  );
  process.exit(1);
}

for (const name of readdirSync(target)) {
  const path = join(target, name);
  if (isDir(path)) rmSync(path, { recursive: true, force: true });
}
for (const locale of locales) {
  cpSync(join(built, locale), join(target, locale), { recursive: true });
  writeFileSync(join(target, locale, notices), text);
}
console.log(`build-embed: copied ${locales.join(', ')} to ${target}, each with ${notices}`);
