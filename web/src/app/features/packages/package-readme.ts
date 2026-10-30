import {
  Component,
  ElementRef,
  ViewEncapsulation,
  afterRenderEffect,
  inject,
  input,
} from '@angular/core';
import { PackageReadme } from '../../api/packages-api';
import { EXTERNAL_REL } from './package-links';

/** The prefix of every heading anchor of a README (R-UI-008). */
const USER_CONTENT = 'user-content-';

/** A link target a README may keep: a fragment, http, https or mailto. */
const LINK_TARGET = /^(#|https?:|mailto:)/i;
/** An image source a README may keep: an https address, or a path on this origin the renderer generated. */
const IMAGE_SOURCE = /^(https:\/\/|\/(?!\/))/i;

/**
 * The anchor name of a heading, the way GitHub-style renderers spell it: lower case, punctuation
 * dropped, spaces to hyphens. `seen` counts a repeated name so that the second `Usage` is `usage-1`.
 */
export function headingSlug(heading: string, seen: Map<string, number>): string {
  const base = heading
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s_-]/gu, '')
    .replace(/\s/g, '-');
  const n = seen.get(base) ?? 0;
  seen.set(base, n + 1);
  return n === 0 ? base : `${base}-${n}`;
}

/**
 * The README of a version (R-UI-008). The server renders Markdown and sanitizes the result; this
 * component binds that HTML with `[innerHTML]`, so Angular's own sanitizer runs a second time and
 * the Trusted Types policy of the document is the third layer. It never builds HTML from Markdown
 * or from strings. Because Angular's sanitizer is the second layer, it also removes every `id` and
 * `name` and lets `vbscript:` links through; the component therefore finishes the job:
 *
 * - a link target other than a fragment, http, https or mailto is removed (the text stays); links
 *   that leave trove carry `rel="nofollow noopener noreferrer"` and no `target`;
 * - an image whose source is neither an https address nor a path of this origin is removed;
 * - each heading gets the anchor `user-content-<slug>` again, so that `#user-content-…` links of a
 *   table of contents work. The server derives its heading anchors with the same rule ([headingSlug]);
 * - such a link scrolls inside the README and does not navigate, because the document's
 *   `<base href>` would turn a bare fragment into a navigation to the UI root;
 * - code blocks can take focus, so that a keyboard user can scroll them sideways.
 */
@Component({
  selector: 'trove-package-readme',
  template: `
    @if (readme(); as r) {
      @if (r.truncated) {
        <p class="readme-note" role="note" i18n="@@pkg.readme.truncated">
          This README is shortened because it is larger than the display limit.
        </p>
      }
      @if (r.externalImagesBlocked === 1) {
        <p class="readme-note" role="note" i18n="@@pkg.readme.image-blocked">
          One external image is shown as a link.
        </p>
      } @else if (r.externalImagesBlocked > 1) {
        <p class="readme-note" role="note" i18n="@@pkg.readme.images-blocked">
          {{ r.externalImagesBlocked }} external images are shown as links.
        </p>
      }
      <div class="readme" [innerHTML]="r.html"></div>
    }
  `,
  styleUrl: './package-readme.scss',
  encapsulation: ViewEncapsulation.None,
  // Clicks on the links inside the bound HTML bubble to the host; the links are the focusable elements.
  host: { '(click)': 'inPageLink($event)' },
})
export class PackageReadmeView {
  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef);

  readonly readme = input.required<PackageReadme | null>();

  constructor() {
    afterRenderEffect(() => {
      this.readme();
      const root = this.host.nativeElement.querySelector('.readme');
      if (!root) return;
      for (const a of root.querySelectorAll<HTMLAnchorElement>('a[href]')) {
        const href = (a.getAttribute('href') ?? '').trim();
        if (!LINK_TARGET.test(href)) {
          a.removeAttribute('href');
        } else if (!href.startsWith('#')) {
          a.removeAttribute('target');
          a.setAttribute('rel', EXTERNAL_REL);
        }
      }
      for (const img of root.querySelectorAll('img')) {
        if (!IMAGE_SOURCE.test((img.getAttribute('src') ?? '').trim())) img.remove();
      }
      for (const pre of root.querySelectorAll('pre')) pre.setAttribute('tabindex', '0');
      const seen = new Map<string, number>();
      for (const h of root.querySelectorAll('h1, h2, h3, h4, h5, h6')) {
        const slug = headingSlug(h.textContent ?? '', seen);
        if (slug) h.id = USER_CONTENT + slug;
      }
    });
  }

  protected inPageLink(event: Event): void {
    const link = (event.target as Element | null)?.closest('a[href^="#"]');
    if (!link) return;
    event.preventDefault();
    let id: string;
    try {
      id = decodeURIComponent((link.getAttribute('href') ?? '').slice(1));
    } catch {
      return;
    }
    if (!id.startsWith(USER_CONTENT)) return;
    const target = Array.from(this.host.nativeElement.querySelectorAll<HTMLElement>('.readme [id]')).find(
      (e) => e.id === id,
    );
    target?.scrollIntoView?.({ block: 'start' });
  }
}
