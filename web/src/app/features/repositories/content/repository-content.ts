import { CdkMenu, CdkMenuItem, CdkMenuTrigger } from '@angular/cdk/menu';
import { VisibilityIcon, levelName } from '../../../shared/ui/visibility-level';
import { Component, computed, effect, inject, input, resource, untracked } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import {
  FormatDescriptor,
  RepositoriesApi,
  Repository,
  RepositoryConsumerView,
  isConsumerView,
  repoRef,
} from '../../../api/repositories-api';
import { ServerAddress } from '../../../core/server-address';
import { SessionStore } from '../../../core/session-store';
import { ErrorNotice } from '../../../shared/error-notice';
import { asApiError, valueOf } from '../../../shared/paged-resource';
import { PageHeader } from '../../../shared/ui/page-header';
import { AddressField } from '../../../shared/address-field';
import { Disclosure } from '../../../shared/ui/disclosure';
import { RecentRepositories } from '../recent-repositories';
import { clientAddress } from '../repository-model';
import { classLabel } from '../repository-wizard';
import { contentShapeOf } from './content-shape';
import { RepositoryFiles } from './repository-files';
import { ItemSort, RepositoryItems } from './repository-items';
import { FormatIconView } from '../../../shared/format-icon';

const SORTS: readonly ItemSort[] = ['name', '-name', 'updatedAt', '-updatedAt', 'grade', '-grade'];

/**
 * The content page of a repository, `/projects/<project>/repositories/<key>` (R-UI-052, Q-UI-216): what the repository holds, on
 * the logical level, in one shell for every format. A package format lists its images or packages and opens their versions in the
 * row; a generic repository lists its files. A virtual repository shows what it resolves, a remote one what it has cached. The
 * second tab is the client setup of this repository (R-UI-016), on the page. The repository's settings are a separate route
 * (`…/settings`). The header (Q-UI-307, Q-UI-313) shows the client address and offers **Set up client**; the other views of
 * the repository are in its menu, and where it fetches from or resolves from is its Details.
 */
@Component({
  selector: 'trove-repository-content',
  imports: [FormatIconView, VisibilityIcon,
    CdkMenu,
    CdkMenuItem,
    CdkMenuTrigger,
    RouterLink,
    ErrorNotice,
    AddressField,
    Disclosure,
    PageHeader,
    RepositoryFiles,
    RepositoryItems,
  ],
  templateUrl: './repository-content.html',
  styleUrl: './repository-content.scss',
})
export class RepositoryContent {
  private readonly api = inject(RepositoriesApi);
  private readonly router = inject(Router);
  private readonly session = inject(SessionStore);
  private readonly recent = inject(RecentRepositories);
  private readonly serverAddress = inject(ServerAddress);
  private readonly baseUrl = resource({ loader: () => this.serverAddress.baseUrl() });

  /** Route parameters, bound by the router's component input binding. */
  readonly project = input.required<string>();
  readonly key = input.required<string>();
  /** Query parameters (R-UI-003): the open tab, the filter, the sort and the folder of a generic repository. */
  readonly q = input<string | undefined>();
  readonly sort = input<string | undefined>();
  readonly path = input<string | undefined>();

  protected readonly ref = computed(() => repoRef(this.project(), this.key()));
  protected readonly loaded = resource({
    params: () => this.ref(),
    loader: ({ params }) => this.api.get(params),
  });
  protected readonly formats = resource({ loader: () => this.api.formats() });
  protected readonly repository = computed<Repository | RepositoryConsumerView | undefined>(() => valueOf(this.loaded)?.value);
  protected readonly failure = computed(() => {
    const e = this.loaded.error();
    return e ? asApiError(e) : null;
  });
  protected readonly descriptor = computed<FormatDescriptor | undefined>(() =>
    (valueOf(this.formats) ?? []).find((f) => f.name === this.repository()?.format),
  );
  protected readonly levelText = levelName;
  protected readonly formatName = computed(() => this.descriptor()?.displayName ?? this.repository()?.format ?? '');
  protected readonly isGeneric = computed(() => this.repository()?.format === 'generic');
  protected readonly scanned = computed(() => this.descriptor()?.vulnerabilityScanning ?? false);
  protected readonly shape = computed(() => contentShapeOf(this.repository()?.format ?? '', this.repository()?.class ?? ''));
  protected readonly classText = computed(() => classLabel(this.repository()?.class ?? 'local'));
  /** The settings exist for whoever can read the configuration: the capability, or an Owner of the project. */
  protected readonly canConfigure = computed(
    () => this.session.has('repositories:read-config') || this.session.canWriteRepositories(this.project()),
  );
  protected readonly canReadFindings = computed(() => this.session.state() !== 'anonymous');
  /** What the header says about the repository: its description, or what it is. */
  protected readonly sentence = computed(() => {
    const r = this.repository();
    if (!r) return '';
    if (r.description) return r.description;
    switch (r.class) {
      case 'remote':
        return $localize`:@@repo.content.sentence.remote:A cache of another registry. It lists what clients have fetched through it.`;
      case 'virtual':
        return $localize`:@@repo.content.sentence.virtual:Combines other repositories behind one address. It lists what they resolve to.`;
      default:
        return $localize`:@@repo.content.sentence.local2:Holds what is published to it.`;
    }
  });
  /** The members a virtual repository resolves from, in order; only the full document has them. */
  protected readonly members = computed<readonly string[]>(() => {
    const r = this.repository();
    return r && !isConsumerView(r) && r.class === 'virtual' ? (r.virtual?.members ?? []) : [];
  });
  /** The address of a remote's upstream; only the full document has it. */
  protected readonly upstream = computed<string | null>(() => {
    const r = this.repository();
    return r && !isConsumerView(r) && r.class === 'remote' ? (r.remote?.url ?? null) : null;
  });
  protected readonly disabled = computed(() => {
    const r = this.repository();
    return r !== undefined && !isConsumerView(r) && (r.disabled ?? false);
  });

  /**
   * The client addresses of the header (Q-UI-313): the consumer view carries them; the full document does not, so the address is
   * built from the server's base URL and the format, as the settings page does.
   */
  protected readonly addresses = computed<readonly string[]>(() => {
    const r = this.repository();
    if (!r) return [];
    if (isConsumerView(r)) return r.addresses;
    const a = clientAddress(valueOf(this.baseUrl), this.descriptor(), this.ref());
    return a ? [a] : [];
  });
  /**
   * The header shows the client address, the first of the repository's addresses (R-UI-066). The API lists the address of
   * the format's protocol first and the raw file address after it, once, where the two differ (R-REPO-002, `ConsumerView` of
   * internal/repository/document.go), so a further address is the one for downloads by path; Details shows it with that
   * label, so that no field stands without a name (Q-UI-320).
   */
  protected readonly headerAddresses = computed(() => this.addresses().slice(0, 1));
  protected readonly otherAddresses = computed(() =>
    this.addresses()
      .slice(1)
      .map((value) => {
        const label = $localize`:@@repo.content.files-address:Address for downloads by path (curl, wget)`;
        return { value, label, copyLabel: $localize`:@@repo.content.copy-files-address:Copy address for downloads by path` };
      }),
  );
  protected readonly actionsLabel = $localize`:@@repo.content.more:More actions`;
  protected readonly detailsHeading = $localize`:@@repo.content.details:Details`;
  protected readonly detailsText = $localize`:@@repo.content.details-text2:Further addresses, and where this repository fetches from or what it resolves from.`;

  protected readonly filter = computed(() => this.q() ?? '');
  protected readonly order = computed<ItemSort>(() => {
    const s = this.sort();
    return (SORTS as readonly string[]).includes(s ?? '') ? (s as ItemSort) : 'name';
  });
  protected readonly folder = computed(() => this.path() ?? '');

  constructor() {
    effect(() => {
      const ref = this.ref();
      untracked(() => this.recent.add(ref));
    });
  }

  protected settingsLink(): string[] {
    return ['/projects', this.project(), 'repositories', this.key(), 'settings'];
  }

  protected setFilter(q: string): void {
    this.navigate({ q: q || null });
  }

  protected setSort(sort: ItemSort): void {
    this.navigate({ sort: sort === 'name' ? null : sort });
  }

  protected setPath(path: string): void {
    this.navigate({ path: path || null });
  }

  private navigate(change: Readonly<Record<string, string | null>>): void {
    void this.router.navigate([], { queryParams: change, queryParamsHandling: 'merge', replaceUrl: true });
  }
}
