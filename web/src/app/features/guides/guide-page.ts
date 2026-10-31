import { Component, computed, inject, input, resource } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { GuidesApi } from '../../api/guides-api';
import { FormatDescriptor, RepositoriesApi, RepositorySummary } from '../../api/repositories-api';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { GuideView } from './guide-view';

/**
 * The guides area (R-UI-049): package type, client, and a repository or a class, then the guide the
 * server renders for that choice. Everything it offers comes from `GET /api/v1/formats`: a format
 * with clients has a guide (kit check 8), so a new format appears here without a new screen. The
 * choices live in the query, so the setup page and the repository screen link to it prepared.
 */
@Component({
  selector: 'trove-guide-page',
  imports: [RouterLink, ErrorNotice, GuideView],
  templateUrl: './guide-page.html',
  styleUrl: './guide-page.scss',
})
export class GuidePage {
  private readonly api = inject(GuidesApi);
  private readonly repositories = inject(RepositoriesApi);
  private readonly router = inject(Router);

  /** Query parameters, bound by the router's component input binding. */
  readonly format = input<string | undefined>();
  readonly client = input<string | undefined>();
  readonly repository = input<string | undefined>();
  readonly class = input<string | undefined>();

  protected readonly formats = resource({ loader: () => this.repositories.formats() });
  protected readonly allRepositories = resource({ loader: () => this.repositories.listAll() });

  protected readonly formatOptions = computed<readonly FormatDescriptor[]>(() =>
    (valueOf(this.formats) ?? []).filter((f) => f.clients.length > 0),
  );
  protected readonly activeFormat = computed<FormatDescriptor | null>(() => {
    const options = this.formatOptions();
    return options.find((f) => f.name === this.format()) ?? options[0] ?? null;
  });
  protected readonly clients = computed(() => this.activeFormat()?.clients ?? []);
  protected readonly activeClient = computed<string | null>(() => {
    const ids = this.clients().map((c) => c.id);
    const given = this.client();
    return given && ids.includes(given) ? given : (ids[0] ?? null);
  });
  protected readonly repositoryOptions = computed<readonly RepositorySummary[]>(() =>
    (valueOf(this.allRepositories) ?? [])
      .filter((r) => r.format === this.activeFormat()?.name && !r.disabled)
      .sort((a, b) => Number(b.class === 'virtual') - Number(a.class === 'virtual') || a.key.localeCompare(b.key)),
  );
  protected readonly activeRepository = computed<RepositorySummary | null>(
    () => this.repositoryOptions().find((r) => r.key === this.repository()) ?? null,
  );
  protected readonly classes = computed(() => this.activeFormat()?.classes ?? []);
  /** The class of the chosen repository; without one the class asked for, else virtual, the recommended address. */
  protected readonly activeClass = computed<string | null>(() => {
    const repo = this.activeRepository();
    if (repo) return repo.class;
    const classes = this.classes();
    const given = this.class();
    if (given && (classes as readonly string[]).includes(given)) return given;
    return classes.includes('virtual') ? 'virtual' : (classes[0] ?? null);
  });

  protected readonly answer = resource({
    params: () => {
      const format = this.activeFormat()?.name;
      const client = this.activeClient();
      const cls = this.activeClass();
      if (!format || !client || !cls) return undefined;
      return { format, client, cls, repository: this.activeRepository()?.key ?? null };
    },
    loader: ({ params }) =>
      params.repository
        ? this.api.repository(params.repository, params.client)
        : this.api.format(params.format, params.cls, params.client),
  });
  protected readonly guide = computed(() => valueOf(this.answer));
  protected readonly loadFailure = computed(() => {
    const e = this.answer.error() ?? this.formats.error() ?? this.allRepositories.error();
    return e ? asApiError(e) : null;
  });
  protected readonly setupQuery = computed(() => ({
    format: this.activeFormat()?.name ?? null,
    client: this.activeClient(),
    repository: this.activeRepository()?.key ?? null,
  }));

  protected classText(cls: string): string {
    switch (cls) {
      case 'local':
        return $localize`:@@repo.class.local:Local`;
      case 'remote':
        return $localize`:@@repo.class.remote:Remote`;
      case 'virtual':
        return $localize`:@@repo.class.virtual:Virtual`;
      default:
        return cls;
    }
  }

  protected pick(change: Record<string, string | null>): void {
    void this.router.navigate([], { queryParams: change, queryParamsHandling: 'merge', replaceUrl: true });
  }

  protected pickFormat(format: string): void {
    this.pick({ format, client: null, repository: null, class: null });
  }
}
