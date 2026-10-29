import { Injectable } from '@angular/core';

/**
 * Full-page navigation (to the identity provider, to another locale build). A seam so that
 * specs can observe the target without leaving the test page.
 */
@Injectable({ providedIn: 'root' })
export class BrowserNavigation {
  /** Leaves the application for `url`; the browser history keeps the current page. */
  assign(url: string): void {
    window.location.assign(url);
  }

  /** The address the user sees: path, query and fragment. */
  current(): string {
    const l = window.location;
    return l.pathname + l.search + l.hash;
  }
}

/**
 * Accepts only a server-relative path as a sign-in return target, so that `returnTo` can never
 * send the browser to another origin (open redirect). Anything else becomes `/`.
 */
export function safeReturnTo(value: string | null | undefined): string {
  if (!value || !value.startsWith('/') || value.startsWith('//') || value.startsWith('/\\')) {
    return '/';
  }
  for (const ch of value) {
    if (ch.charCodeAt(0) < 0x20 || ch === '\\') return '/';
  }
  return value;
}
