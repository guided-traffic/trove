import { Component, computed, inject, resource } from '@angular/core';
import { RouterLink } from '@angular/router';
import { ManualApi } from '../../api/manual-api';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';

/**
 * The start of the help area (R-UI-050): the two guides of the manual with every page's title and
 * summary. The manual is English (Q-UI-89), so its text carries `lang="en"`; the frame is translated.
 */
@Component({
  selector: 'trove-help-overview',
  imports: [RouterLink, ErrorNotice],
  template: `
    <h1 i18n="@@help.title">Documentation</h1>
    <p class="help-lead" i18n="@@help.lead">
      The manual of the trove version this installation runs, for the people who use it and the people who run it.
    </p>
    @if (contents.error(); as e) {
      <trove-error-notice [error]="error(e)" (retry)="contents.reload()" />
    } @else if (value(); as c) {
      <div class="help-sections">
        @for (s of c.sections; track s.id) {
          <section class="help-section" [attr.aria-labelledby]="'help-section-' + s.id">
            <h2 [id]="'help-section-' + s.id" lang="en">{{ s.title }}</h2>
            <ul class="help-cards" lang="en">
              @for (p of s.pages; track p.id) {
                <li>
                  <a class="help-card" [routerLink]="['/help', s.id, p.id]">
                    <span class="help-card-title">{{ p.title }}</span>
                    <span class="help-card-summary">{{ p.summary }}</span>
                  </a>
                </li>
              }
            </ul>
          </section>
        }
      </div>
      <p class="help-more" i18n="@@help.guides">
        The commands of a package client with the addresses of this installation are in the
        <a routerLink="/guides">guides</a>.
      </p>
    } @else {
      <p role="status" i18n="@@help.loading">Loading the manual…</p>
    }
  `,
  styleUrl: './help.scss',
})
export class HelpOverview {
  private readonly api = inject(ManualApi);

  protected readonly contents = resource({ loader: () => this.api.contents() });
  protected readonly value = computed(() => valueOf(this.contents));
  protected readonly error = asApiError;
}
