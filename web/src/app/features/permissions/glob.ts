/*
 * A local copy of the pattern syntax of R-AUTHZ-005, for the tester field of the policy editor
 * ("Does `@team-a/ui` match?"). The server is the authority: it validates the patterns on save and
 * in the preview. This module only answers the question for a pattern the user is typing.
 */

type Tok = { kind: 'lit'; ch: string } | { kind: 'one' } | { kind: 'star' } | { kind: 'any' };

/** Parses one name or one path segment; null when the pattern is malformed. */
function tokens(src: string, name: boolean): Tok[] | null {
  const chars = [...src];
  const out: Tok[] = [];
  for (let i = 0; i < chars.length; i++) {
    const c = chars[i]!;
    if (c === '\\') {
      i++;
      const next = chars[i];
      if (next === undefined) return null;
      out.push({ kind: 'lit', ch: next });
    } else if (c === '?') {
      out.push({ kind: 'one' });
    } else if (c === '*') {
      if (chars[i + 1] === '*') {
        if (!name) return null;
        while (chars[i + 1] === '*') i++;
        out.push({ kind: 'any' });
      } else {
        out.push({ kind: 'star' });
      }
    } else {
      out.push({ kind: 'lit', ch: c });
    }
  }
  return out;
}

function regexOf(toks: readonly Tok[]): RegExp {
  const body = toks
    .map((t) => {
      switch (t.kind) {
        case 'lit':
          return t.ch.replace(/[.*+?^${}()|[\]\\/]/g, '\\$&');
        case 'one':
          return '[^/]';
        case 'star':
          return '[^/]*';
        default:
          return '[\\s\\S]*';
      }
    })
    .join('');
  return new RegExp(`^${body}$`, 'u');
}

/** `*`, `**`, `?` and `\` against a normalized package name; null for a malformed pattern. */
export function matchName(pattern: string, value: string): boolean | null {
  if (pattern === '') return null;
  const toks = tokens(pattern, true);
  return toks === null ? null : regexOf(toks).test(value);
}

/** Splits at `/` unless a backslash escapes it. */
function splitPath(pattern: string): string[] {
  const parts: string[] = [];
  let cur = '';
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern[i]!;
    if (c === '\\' && i + 1 < pattern.length) {
      cur += c + pattern[i + 1]!;
      i++;
    } else if (c === '/') {
      parts.push(cur);
      cur = '';
    } else {
      cur += c;
    }
  }
  parts.push(cur);
  return parts;
}

/** Path patterns: `**` as a whole segment is any number of segments, zero included. */
export function matchPath(pattern: string, value: string): boolean | null {
  if (pattern === '' || pattern.startsWith('/')) return null;
  const segs: (RegExp | 'globstar')[] = [];
  for (const raw of splitPath(pattern)) {
    if (raw === '') return null;
    if (raw === '**') {
      segs.push('globstar');
      continue;
    }
    const toks = tokens(raw, false);
    if (toks === null) return null;
    segs.push(regexOf(toks));
  }
  if (value === '') return false;
  const input = value.split('/');
  // reach[i]: the first i pattern segments consumed the segments seen so far.
  let reach = new Array<boolean>(segs.length + 1).fill(false);
  reach[0] = true;
  const close = (r: boolean[]): void => {
    for (let i = 0; i < segs.length; i++) if (r[i] && segs[i] === 'globstar') r[i + 1] = true;
  };
  close(reach);
  for (const part of input) {
    const next = new Array<boolean>(segs.length + 1).fill(false);
    reach.forEach((ok, i) => {
      const seg = segs[i];
      if (!ok || seg === undefined) return;
      if (seg === 'globstar') next[i] = true;
      else if (seg.test(part)) next[i + 1] = true;
    });
    close(next);
    reach = next;
  }
  return reach[segs.length] === true;
}

/** What a dimension of a resource set says about one value (R-AUTHZ-005). */
export type SetVerdict =
  | { readonly kind: 'unrestricted' }
  | { readonly kind: 'included'; readonly pattern: string }
  | { readonly kind: 'excluded'; readonly pattern: string }
  | { readonly kind: 'not-included' }
  | { readonly kind: 'invalid'; readonly pattern: string };

/**
 * Applies the include and exclude lists of one dimension to a value: exclude beats include, an
 * empty include list is unrestricted, a non-empty one needs a matching pattern.
 */
export function verdict(
  dimension: 'packages' | 'paths',
  include: readonly string[],
  exclude: readonly string[],
  value: string,
): SetVerdict {
  const test = dimension === 'packages' ? matchName : matchPath;
  for (const pattern of [...include, ...exclude]) {
    if (test(pattern, value) === null) return { kind: 'invalid', pattern };
  }
  const excluded = exclude.find((p) => test(p, value) === true);
  if (excluded !== undefined) return { kind: 'excluded', pattern: excluded };
  if (include.length === 0) return { kind: 'unrestricted' };
  const included = include.find((p) => test(p, value) === true);
  return included === undefined ? { kind: 'not-included' } : { kind: 'included', pattern: included };
}
