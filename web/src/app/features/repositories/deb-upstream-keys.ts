import { Component, input } from '@angular/core';
import { CopyButton } from '../../shared/copy-button';
import { groupedFingerprint } from './deb-signing-keys';

/**
 * The v4 fingerprints of the certificates in a remote Debian repository's `upstreamKeys` (R-DEB-020): read-only, the server computes
 * them (`upstreamKeyFingerprints`), so that an operator can compare them with the archive's published fingerprints through a second
 * channel before trusting the pasted block.
 */
@Component({
  selector: 'trove-deb-upstream-keys',
  imports: [CopyButton],
  template: `
    <section class="fingerprints" aria-labelledby="deb-upstream-fpr-heading" data-testid="deb-upstream-fingerprints">
      <h3 id="deb-upstream-fpr-heading" i18n="@@deb.upstream.fingerprints">Fingerprints of the upstream keys</h3>
      <p class="hint" i18n="@@deb.upstream.fingerprints-hint">
        Read-only. Compare them with the fingerprints the archive publishes, through another channel, before you trust the keys above.
        They follow the saved keys.
      </p>
      <ul class="plain">
        @for (f of fingerprints(); track f) {
          <li>
            <code translate="no" [attr.title]="f">{{ grouped(f) }}</code>
            <trove-copy-button [value]="f" [label]="copyLabel" />
          </li>
        }
      </ul>
    </section>
  `,
  styles: `
    .fingerprints {
      margin-block-start: var(--trove-space-4);
    }
    h3 {
      margin-block: 0 var(--trove-space-2);
      font: var(--trove-type-title-small);
    }
  `,
})
export class DebUpstreamKeys {
  readonly fingerprints = input.required<readonly string[]>();
  protected readonly copyLabel = $localize`:@@deb.upstream.copy:Copy fingerprint`;
  protected readonly grouped = groupedFingerprint;
}
