import { DOCUMENT } from '@angular/common';
import { Component, computed, inject, input } from '@angular/core';
import { Guide, GuideSection } from '../../api/guides-api';
import { PackageReadme } from '../../api/packages-api';
import { CopyButton } from '../../shared/copy-button';
import { PackageReadmeView } from '../packages/package-readme';

/**
 * The sections of a guide (R-UI-049): a contents list, then one region per section with its heading,
 * the server's sanitized HTML bound the way a README is (R-UI-008, the same component and the same
 * finishing pass) and the setup snippets the server attached, with a copy button each. The text is
 * the format's and English (Q-UI-86), so the sections carry `lang="en"`; the frame is translated.
 */
@Component({
  selector: 'trove-guide-view',
  imports: [CopyButton, PackageReadmeView],
  template: `
    <nav class="toc" [attr.aria-labelledby]="prefix() + '-toc'">
      <h2 class="toc-title" [id]="prefix() + '-toc'" i18n="@@guide.toc">On this page</h2>
      <ol lang="en">
        @for (s of guide().sections; track s.id) {
          <li>
            <a [href]="'#' + anchor(s)" (click)="jump($event, s)">{{ s.title }}</a>
          </li>
        }
      </ol>
    </nav>
    @for (s of guide().sections; track s.id) {
      <section class="guide-section" [attr.aria-labelledby]="anchor(s)" [attr.data-section]="s.id">
        <h2 [id]="anchor(s)" tabindex="-1" lang="en">{{ s.title }}</h2>
        @if (s.html) {
          <trove-package-readme lang="en" [readme]="readmeOf(s)" />
        }
        @for (sn of s.snippets; track sn.id) {
          <div class="snippet" [attr.data-snippet]="sn.id">
            @if (sn.fileName) {
              <p class="target">
                <span i18n="@@guide.target-file">File</span>: <code translate="no">{{ sn.fileName }}</code>
              </p>
            }
            <div class="command">
              <pre class="code-block" translate="no" tabindex="0">{{ sn.content }}</pre>
              <trove-copy-button [value]="sn.content" [label]="copyLabel" />
            </div>
          </div>
        }
      </section>
    }
  `,
  styleUrl: './guide-view.scss',
})
export class GuideView {
  private readonly document = inject(DOCUMENT);

  readonly guide = input.required<Guide>();
  /** Keeps the anchors of two guides on one page apart. */
  readonly prefix = input('guide');

  protected readonly copyLabel = $localize`:@@guide.copy:Copy snippet`;
  private readonly readmes = computed(
    () =>
      new Map(
        this.guide().sections.map((s) => [
          s.id,
          { contentType: 'text/markdown', html: s.html, truncated: false, externalImagesBlocked: 0 } satisfies PackageReadme,
        ]),
      ),
  );

  protected anchor(s: GuideSection): string {
    return `${this.prefix()}-${s.id.replaceAll('.', '-')}`;
  }

  protected readmeOf(s: GuideSection): PackageReadme | null {
    return this.readmes().get(s.id) ?? null;
  }

  /** In-page links scroll and move focus; the document's base href would turn a bare fragment into a navigation. */
  protected jump(event: Event, s: GuideSection): void {
    event.preventDefault();
    const heading = this.document.getElementById(this.anchor(s));
    heading?.scrollIntoView?.({ block: 'start' });
    heading?.focus();
  }
}
