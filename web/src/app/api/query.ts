import { QueryParams } from './client';

/** Drops empty filter values so that a cleared field does not become `?q=`. */
export function cleanQuery(filters: object): QueryParams {
  const out: Record<string, string | number | boolean | readonly string[]> = {};
  for (const [name, value] of Object.entries(filters)) {
    if (value === undefined || value === null || value === '') continue;
    out[name] = value as string | number | boolean | readonly string[];
  }
  return out;
}

/** One path segment: usernames hold `@` and `+`, principal references hold `:`. */
export const seg = encodeURIComponent;
