// The hashed files of the production build (R-UI-001, Q-UI-140): the pattern of internal/webui/hashed.go, which serves such a
// file as immutable and is the only kind of file a replica serves from the shared asset store (R-HA-020, Q-HA-120). The two
// regular expressions are the Go ones character for character; TestHashedPatternOfTheBuild in internal/webui compares them.
import { readdirSync, statSync } from 'node:fs';
import { join, relative, sep } from 'node:path';

export const HASHED_NAME = /^[A-Za-z0-9][A-Za-z0-9_.-]*-[A-Za-z0-9_-]{8}\.[A-Za-z0-9]{1,8}$/;
export const HASHED_DIR = /^[A-Za-z0-9_-]{1,64}$/;
export const MAX_HASHED_DEPTH = 2;

// isHashed is IsHashed of internal/webui: a path relative to a locale directory.
export function isHashed(path) {
  if (path.length === 0 || path.length > 255) return false;
  const segs = path.split('/');
  if (segs.length - 1 > MAX_HASHED_DEPTH) return false;
  if (!segs.slice(0, -1).every((d) => HASHED_DIR.test(d))) return false;
  return HASHED_NAME.test(segs[segs.length - 1]);
}

function files(dir) {
  const out = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...files(p));
    else out.push(p);
  }
  return out;
}

// checkHashed returns the problems of one locale directory of the build: every file but index.html and the copies of
// public/ must be hashed, and no copy of public/ may look hashed, or it would be cached as immutable and published to the
// shared asset store under a name that does not change with its content.
export function checkHashed(localeDir, publicDir) {
  const unhashed = new Set(files(publicDir).map((p) => relative(publicDir, p).split(sep).join('/')));
  const problems = [];
  for (const p of files(localeDir)) {
    const rel = relative(localeDir, p).split(sep).join('/');
    if (rel === 'index.html') continue;
    if (unhashed.has(rel)) {
      if (isHashed(rel)) problems.push(`${rel} comes from public/ and looks hashed`);
    } else if (!isHashed(rel)) {
      problems.push(`${rel} is not named <name>-<8 hash characters>.<extension>`);
    }
  }
  return problems;
}
