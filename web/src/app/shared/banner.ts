import { Component, DestroyRef, inject } from '@angular/core';
import { BannerStore, Notice } from '../core/banner-store';
import { parseBannerText } from './banner-text';

/**
 * The notices above the content (R-UI-002, R-UI-033): the system banner and, while the installation is read-only, the maintenance
 * message. Renders nothing while there is none. A message is text and `https` links only, bound as text and as `href` (R-OPS-025);
 * the severity is an icon and a word as well as a colour (R-UI-044).
 */
@Component({
  selector: 'trove-banner',
  template: `
    @for (notice of store.notices(); track notice.kind) {
      <section
        class="banner"
        role="region"
        [attr.data-kind]="notice.kind"
        [attr.data-severity]="notice.severity"
        [attr.aria-label]="label(notice)"
      >
        <span class="material-icons icon" aria-hidden="true">{{ icon(notice) }}</span>
        <p>
          <strong class="lead">{{ lead(notice) }}</strong>
          @for (part of parts(notice); track $index) {
            @if (part.kind === 'link') {
              <a [href]="part.href" target="_blank" rel="noopener noreferrer">{{ part.text }}</a>
            } @else {
              {{ part.text }}
            }
          }
        </p>
      </section>
    }
  `,
  styles: `
    .banner {
      display: flex;
      align-items: flex-start;
      gap: var(--trove-space-2);
      padding: var(--trove-space-2) var(--trove-space-4);
      background: var(--trove-banner-bg);
      color: var(--trove-banner-fg);
      white-space: pre-line;
    }
    .banner[data-severity='warning'] {
      background: var(--trove-badge-warn-bg);
      color: var(--trove-badge-warn-fg);
    }
    .banner[data-severity='critical'] {
      background: var(--trove-badge-bad-bg);
      color: var(--trove-badge-bad-fg);
    }
    p {
      margin: 0;
    }
    a {
      color: inherit;
      text-decoration: underline;
    }
    .icon {
      flex: none;
      font-size: 1.25em;
    }
  `,
})
export class Banner {
  protected readonly store = inject(BannerStore);

  constructor() {
    this.store.watch(inject(DestroyRef));
  }

  protected parts(n: Notice) {
    return parseBannerText(n.message);
  }

  protected icon(n: Notice): string {
    if (n.kind === 'maintenance') return 'construction';
    return n.severity === 'info' ? 'info' : n.severity === 'warning' ? 'warning' : 'error';
  }

  protected label(n: Notice): string {
    return n.kind === 'maintenance'
      ? $localize`:@@banner.maintenance-label:Maintenance notice`
      : $localize`:@@banner.label:System notice`;
  }

  /** The words that carry the severity and the title, so the colour is never the only signal. */
  protected lead(n: Notice): string {
    if (n.kind === 'maintenance') {
      return $localize`:@@banner.maintenance-lead:Read-only maintenance mode.`;
    }
    const title = n.title;
    switch (n.severity) {
      case 'critical':
        return title === ''
          ? $localize`:@@banner.critical:Critical.`
          : $localize`:@@banner.critical-title:Critical: ${title}:title:.`;
      case 'warning':
        return title === ''
          ? $localize`:@@banner.warning:Warning.`
          : $localize`:@@banner.warning-title:Warning: ${title}:title:.`;
      default:
        return title === ''
          ? $localize`:@@banner.info:Notice.`
          : $localize`:@@banner.info-title:Notice: ${title}:title:.`;
    }
  }
}
