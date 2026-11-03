import { Component, computed, inject, input, resource } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { Bastion, BastionsApi } from '../../api/bastions-api';
import { GuidesApi } from '../../api/guides-api';
import { ClientSetupApi } from '../../api/packages-api';
import { FormatDescriptor, RepositoriesApi, RepositorySummary } from '../../api/repositories-api';
import { SessionStore } from '../../core/session-store';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { reportedBastions } from '../setup/bastion-targets';
import { GuideView } from './guide-view';

/**
 * The guides area (R-UI-049): package type, client, and a repository or a class, then the guide the
 * server renders for that choice. Everything it offers comes from `GET /api/v1/formats`: a format
 * with clients has a guide (kit check 8), so a new format appears here without a new screen. The
 * choices live in the query, so the setup page and the repository screen link to it prepared.
 */
@Component({
  selector: 'trove-guide-page',
  imports: [RouterLink, CopyButton, ErrorNotice, GuideView],
  templateUrl: './guide-page.html',
  styleUrl: './guide-page.scss',
})
export class GuidePage {
  private readonly api = inject(GuidesApi);
  private readonly repositories = inject(RepositoriesApi);
  private readonly router = inject(Router);
  private readonly setupApi = inject(ClientSetupApi);
  private readonly bastionsApi = inject(BastionsApi);
  private readonly session = inject(SessionStore);

  /** Query parameters, bound by the router's component input binding. */
  readonly format = input<string | undefined>();
  readonly client = input<string | undefined>();
  readonly repository = input<string | undefined>();
  readonly class = input<string | undefined>();
  /** A Bastion registration to read the guide for (R-BAS-007, R-UI-049); the client setup page links here with it. */
  readonly bastion = input<string | undefined>();

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

  /** The registrations that have reported a base URL; none for a caller who cannot read them (Q-BAS-96). */
  protected readonly bastions = resource({ loader: () => reportedBastions(this.bastionsApi, this.session) });
  protected readonly bastionOptions = computed<readonly Bastion[]>(() => valueOf(this.bastions) ?? []);
  protected readonly activeBastion = computed<string | null>(() => this.bastion()?.trim() || null);
  protected readonly showTarget = computed(() => this.bastionOptions().length > 0 || this.activeBastion() !== null);
  /**
   * What a Bastion target shows (Q-BAS-96): the configure and install snippets of the client setup for the
   * repository on the Bastion's address. The format's guide describes this installation, its publishing and
   * its tags, so it is not shown for a target that is read only.
   */
  protected readonly bastionSetup = resource({
    params: () => {
      const bastion = this.activeBastion();
      const repository = this.activeRepository()?.key;
      const client = this.activeClient();
      if (!bastion || !repository || !client) return undefined;
      return { bastion, repository, client, credentials: this.session.state() === 'anonymous' ? 'none' : 'env' } as const;
    },
    loader: ({ params }) =>
      this.setupApi.get(params.repository, { client: params.client, credentials: params.credentials, bastion: params.bastion }),
  });
  protected readonly bastionSnippets = computed(() => {
    const s = valueOf(this.bastionSetup);
    return s ? s.snippets.filter((sn) => sn.tab !== 'publish') : [];
  });
  protected readonly copyLabel = $localize`:@@guide.copy:Copy snippet`;

  protected readonly answer = resource({
    params: () => {
      if (this.activeBastion()) return undefined;
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
    const e = this.answer.error() ?? this.bastionSetup.error() ?? this.formats.error() ?? this.allRepositories.error();
    return e ? asApiError(e) : null;
  });
  protected readonly setupQuery = computed(() => ({
    format: this.activeFormat()?.name ?? null,
    client: this.activeClient(),
    repository: this.activeRepository()?.key ?? null,
    bastion: this.activeBastion(),
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
