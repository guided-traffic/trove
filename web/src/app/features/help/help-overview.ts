import { NgTemplateOutlet } from '@angular/common';
import { Component, computed, inject, resource } from '@angular/core';
import { RouterLink } from '@angular/router';
import { ManualApi } from '../../api/manual-api';
import { SessionStore } from '../../core/session-store';
import { ErrorNotice } from '../../shared/error-notice';
import { Disclosure } from '../../shared/ui/disclosure';
import { asApiError, valueOf } from '../../shared/paged-resource';

/**
 * The start of the help area (R-UI-050): the two guides of the manual with every page's title and
 * summary. The manual is English (Q-UI-89), so its text carries `lang="en"`; the frame is translated.
 * A plain user (R-UI-067: no system role, at most a reader in every project) gets the administrator's guide as a closed
 * disclosure at the end (Q-UI-324): it is about running trove and managing projects, which they do not do.
 */
@Component({
  selector: 'trove-help-overview',
  imports: [NgTemplateOutlet, RouterLink, ErrorNotice, Disclosure],
  template: `
    <h1 i18n="@@help.title">Documentation</h1>
    <p class="help-lead" i18n="@@help.lead">
      The manual of the trove version this installation runs, for the people who use it and the people who run it.
    </p>
    @if (contents.error(); as e) {
      <trove-error-notice [error]="error(e)" (retry)="contents.reload()" />
    } @else if (value(); as c) {
      <ng-template #cards let-s>
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
      </ng-template>
      <div class="help-sections">
        @for (s of c.sections; track s.id) {
          @if (s.id === 'admin' && plain()) {
            <trove-disclosure class="help-section" [heading]="s.title" [hint]="adminHint" lang="en">
              <ng-container *ngTemplateOutlet="cards; context: { $implicit: s }" />
            </trove-disclosure>
          } @else {
            <section class="help-section" [attr.aria-labelledby]="'help-section-' + s.id">
              <h2 [id]="'help-section-' + s.id" lang="en">{{ s.title }}</h2>
              <ng-container *ngTemplateOutlet="cards; context: { $implicit: s }" />
            </section>
          }
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
  private readonly session = inject(SessionStore);

  /** No system role and at most a reader in every project: the administrator's guide is closed (Q-UI-324). */
  protected readonly plain = computed(
    () => !this.session.isAdministrator() && this.session.projects().every((m) => m.role === 'reader'),
  );
  protected readonly adminHint = $localize`:@@help.admin-hint:For the people who run trove and manage its projects.`;

  protected readonly contents = resource({ loader: () => this.api.contents() });
  protected readonly value = computed(() => valueOf(this.contents));
  protected readonly error = asApiError;
}
