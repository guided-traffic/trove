// Repository rules that are plain text searches over web/. They run as part of `npm run lint`.
//  - R-ARCH-004: no NgModule(, provideZoneChangeDetection or zone.js in src or angular.json.
//  - R-UI-001: no bypassSecurityTrust or addSvgIcon in src.
//  - R-ARCH-007: no string literal that starts with /files/ or /api/<format>/ in src, except the
//    files client in src/app/api/ and generated code.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(fileURLToPath(new URL('.', import.meta.url)), '..');

function* walk(dir) {
  for (const name of readdirSync(dir)) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) yield* walk(path);
    else yield path;
  }
}

const sources = [...walk(join(root, 'src'))].filter((p) => /\.(ts|html|scss|css)$/.test(p));
const rules = [
  { id: 'R-ARCH-004', re: /NgModule\(|provideZoneChangeDetection|zone\.js/, files: [...sources, join(root, 'angular.json')] },
  { id: 'R-UI-001', re: /bypassSecurityTrust|addSvgIcon/, files: sources },
  {
    id: 'R-ARCH-007',
    re: /['"`]\/(files\/|api\/(?!v[0-9]+\/)[a-z][a-z0-9-]*\/)/,
    files: sources.filter((p) => {
      const rel = relative(root, p).split(sep).join('/');
      return !rel.startsWith('src/app/api/');
    }),
  },
];

let failed = false;
for (const rule of rules) {
  for (const file of rule.files) {
    readFileSync(file, 'utf8')
      .split('\n')
      .forEach((line, i) => {
        if (rule.re.test(line)) {
          failed = true;
          console.error(`${rule.id}: ${relative(root, file)}:${i + 1}: ${line.trim()}`);
        }
      });
  }
}
if (failed) process.exit(1);
console.log('check-rules: ok');
