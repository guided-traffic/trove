/**
 * A link taken from package metadata (npm `homepage`, `repository.url`, `bugs.url`): a package
 * author sets it, so it becomes a link only when its scheme is `http` or `https` (R-UI-007).
 * Anything else, `javascript:` and `data:` included, stays text. Angular's URL sanitizer is a
 * second layer, not the rule. Returns the absolute URL, or null.
 */
export function safeHttpUrl(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  if (trimmed === '') return null;
  try {
    const url = new URL(trimmed);
    return url.protocol === 'http:' || url.protocol === 'https:' ? url.href : null;
  } catch {
    return null;
  }
}

/** The rel of every link that leaves trove for an address a package author chose. */
export const EXTERNAL_REL = 'nofollow noopener noreferrer';

/** The router commands of a package page: the name keeps its slash as a separator (`@team-a/ui`). */
export function packageCommands(format: string, name: string): string[] {
  return ['/packages', format, ...name.split('/')];
}
