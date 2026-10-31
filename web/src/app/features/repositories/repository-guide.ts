import { Component, computed, inject, input, resource, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { FormatClient, GuidesApi } from '../../api/guides-api';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { GuideView } from '../guides/guide-view';

/**
 * "How to use" on the repository screen (R-UI-049): the guide of this repository for the client the
 * reader picks, with links to the client setup and to the guides area prepared for the same choice.
 * The clients are the format's (`GET /api/v1/formats`); a format without clients shows nothing.
 */
@Component({
  selector: 'trove-repository-guide',
  imports: [RouterLink, ErrorNotice, GuideView],
  template: `
    @if (clients().length === 0) {
      <p i18n="@@repo.guide.none">There is no guide for this package type.</p>
    } @else {
      <fieldset class="clients">
        <legend i18n="@@repo.guide.client">Client</legend>
        @for (c of clients(); track c.id) {
          <label class="choice">
            <input type="radio" name="repo-guide-client" [value]="c.id" [checked]="c.id === activeClient()" (change)="picked.set(c.id)" />
            <span translate="no">{{ c.displayName }}</span>
          </label>
        }
      </fieldset>
      <p class="links">
        <a routerLink="/setup" [queryParams]="query()" i18n="@@repo.guide.setup">Open the client setup</a>
        <a routerLink="/guides" [queryParams]="query()" i18n="@@repo.guide.open">Open in the guides</a>
      </p>
      @if (failure(); as f) {
        <trove-error-notice [error]="f" (retry)="answer.reload()" />
      } @else if (guide(); as g) {
        <trove-guide-view [guide]="g" prefix="repo-guide" />
      } @else {
        <p role="status" i18n="@@repo.guide.loading">Loading the guide.</p>
      }
    }
  `,
  styles: `
    .clients {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-2) var(--trove-space-4);
      margin: 0 0 var(--trove-space-3);
      padding: var(--trove-space-2) var(--trove-space-3);
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-m);
    }
    .choice {
      display: flex;
      align-items: center;
      gap: var(--trove-space-2);
    }
    .links {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-4);
      margin-block: 0 var(--trove-space-4);
    }
  `,
})
export class RepositoryGuide {
  private readonly api = inject(GuidesApi);

  readonly key = input.required<string>();
  readonly format = input.required<string>();
  readonly clients = input.required<readonly FormatClient[]>();

  protected readonly picked = signal<string | null>(null);
  protected readonly activeClient = computed<string | null>(() => {
    const ids = this.clients().map((c) => c.id);
    const p = this.picked();
    return p && ids.includes(p) ? p : (ids[0] ?? null);
  });
  protected readonly answer = resource({
    params: () => {
      const client = this.activeClient();
      return client ? { key: this.key(), client } : undefined;
    },
    loader: ({ params }) => this.api.repository(params.key, params.client),
  });
  protected readonly guide = computed(() => valueOf(this.answer));
  protected readonly failure = computed(() => {
    const e = this.answer.error();
    return e ? asApiError(e) : null;
  });
  protected readonly query = computed(() => ({ format: this.format(), client: this.activeClient(), repository: this.key() }));
}
