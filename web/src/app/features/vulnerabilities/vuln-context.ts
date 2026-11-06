import { InjectionToken } from '@angular/core';

/**
 * Where the vulnerability screens are mounted. The same screens (report, finding, advisory, sources) serve the content
 * area and the admin area's security dashboard (Q-UI-130); a route that mounts them provides this token, so that the
 * links between them stay inside the area they were opened in.
 */
export interface VulnContext {
  /** The absolute path the screens live under, without a trailing slash. */
  readonly base: string;
  /** The report reads every project and shows the project as a column and a search (the admin area's dashboard). */
  readonly instanceWide: boolean;
}

export const VULN_CONTEXT = new InjectionToken<VulnContext>('VULN_CONTEXT', {
  providedIn: 'root',
  factory: () => ({ base: '/vulnerabilities', instanceWide: false }),
});

/** The context of the admin area's security dashboard (Q-UI-130). */
export const SECURITY_CONTEXT: VulnContext = { base: '/admin/security', instanceWide: true };

/**
 * The canonical spelling of an advisory identifier typed by hand: the API compares identifiers exactly, and the
 * prefix of every family is upper case (`CVE-2021-23337`, `MAL-2026-1234`, `OSV-2026-12`) except that a GitHub
 * advisory carries its three groups in lower case (`GHSA-35jh-r3h4-6jhm`). Anything else is left as typed.
 */
export function canonicalAdvisoryId(input: string): string {
  const text = input.trim();
  if (/^ghsa-/i.test(text)) return `GHSA-${text.slice(5).toLowerCase()}`;
  if (/^(cve|mal|osv|pysec|go|rustsec)-/i.test(text)) return text.toUpperCase();
  return text;
}
