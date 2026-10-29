import { Component, inject } from '@angular/core';
import { BannerStore } from '../core/banner-store';

/** Banner area above the content (R-UI-002). Renders nothing while no banner is set. */
@Component({
  selector: 'trove-banner',
  template: `
    @if (store.text(); as text) {
      <section
        class="banner"
        role="region"
        i18n-aria-label="@@banner.label"
        aria-label="System notice"
      >
        {{ text }}
      </section>
    }
  `,
  styles: `
    .banner {
      padding: var(--trove-space-2) var(--trove-space-4);
      background: var(--trove-banner-bg);
      color: var(--trove-banner-fg);
      white-space: pre-line;
    }
  `,
})
export class Banner {
  protected readonly store = inject(BannerStore);
}
