// R-UI-001 bundle budget: the initial JavaScript of every locale is at most 400 kB compressed.
// Angular's own budget measures raw bytes, so this check gzips the files that index.html loads
// at start-up (module scripts, modulepreload links) and fails above the limit.
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { gzipSync } from 'node:zlib';

export const LIMIT_BYTES = 400 * 1000;

export function initialJsBytes(localeDir) {
  const html = readFileSync(join(localeDir, 'index.html'), 'utf8');
  const files = new Set();
  for (const m of html.matchAll(/<(?:script|link)\b[^>]*?(?:src|href)="([^"]+\.js)"/g)) {
    files.add(m[1]);
  }
  let total = 0;
  for (const f of files) {
    total += gzipSync(readFileSync(join(localeDir, f)), { level: 9 }).length;
  }
  return { total, files: [...files] };
}

export function checkBudget(browserDir) {
  let failed = false;
  for (const name of readdirSync(browserDir)) {
    const dir = join(browserDir, name);
    if (!statSync(dir).isDirectory()) continue;
    const { total, files } = initialJsBytes(dir);
    const ok = total > 0 && total <= LIMIT_BYTES;
    console.log(
      `budget ${name}: ${(total / 1000).toFixed(1)} kB gzip over ${files.length} files (limit ${LIMIT_BYTES / 1000} kB)`,
    );
    if (!ok) failed = true;
  }
  return !failed;
}

if (process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href) {
  const dir = process.argv[2] ?? join(new URL('..', import.meta.url).pathname, 'dist', 'web', 'browser');
  process.exit(checkBudget(dir) ? 0 : 1);
}
