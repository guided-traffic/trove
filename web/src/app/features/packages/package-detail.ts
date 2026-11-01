import { DatePipe, DecimalPipe } from '@angular/common';
import { Component, computed, effect, inject, input, resource, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatTabsModule } from '@angular/material/tabs';
import { Router, RouterLink } from '@angular/router';
import { PackageVersionSummary, PackagesApi } from '../../api/packages-api';
import { collectAll } from '../../api/pagination';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { formatSize } from './format-size';
import { InstallPanel } from './install-panel';
import { EXTERNAL_REL, packageCommands, safeHttpUrl } from './package-links';
import { PackageMemory } from './package-memory';
import { PackageReadmeView } from './package-readme';

export type DetailTab = 'readme' | 'metadata' | 'files' | 'repositories' | 'versions';

/** A link of the metadata tab: a URL when its scheme is http or https, else the text alone (R-UI-007). */
interface MetadataLink {
  readonly label: string;
  readonly text: string;
  readonly href: string | null;
}

interface DependencyGroup {
  readonly label: string;
  readonly entries: readonly (readonly [string, string])[];
}

/** The dependencies of a NuGet package for one target framework (`null`: declared for all). */
interface FrameworkGroup {
  readonly framework: string | null;
  readonly entries: readonly (readonly [string, string])[];
}

function stringRecord(value: unknown): [string, string][] {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return [];
  return Object.entries(value).flatMap(([k, v]): [string, string][] =>
    typeof v === 'string' ? [[k, v]] : [],
  );
}

function strings(value: unknown): string[] {
  if (typeof value === 'string') return value.trim() === '' ? [] : [value];
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string') : [];
}

/**
 * The `dependencyGroups` member of NuGet metadata (formats/nuget.md R-NUGET-007): a list of groups
 * with an optional `targetFramework` and `dependencies` of `id` and `range`. Anything else is skipped.
 */
function frameworkGroups(value: unknown): FrameworkGroup[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((g): FrameworkGroup[] => {
    if (typeof g !== 'object' || g === null) return [];
    const group = g as Record<string, unknown>;
    const deps = Array.isArray(group['dependencies']) ? group['dependencies'] : [];
    const entries = deps.flatMap((d): [string, string][] => {
      if (typeof d !== 'object' || d === null) return [];
      const dep = d as Record<string, unknown>;
      const id = dep['id'];
      const range = dep['range'] ?? dep['version'];
      return typeof id === 'string' ? [[id, typeof range === 'string' && range !== '' ? range : '*']] : [];
    });
    const fw = group['targetFramework'];
    return [{ framework: typeof fw === 'string' && fw !== '' ? fw : null, entries }];
  });
}

/**
 * Package details and versions (R-UI-007). `/packages/{format}/{name…}` resolves through the
 * lookup; the version is the `version` query parameter, by default the latest stable version the
 * caller can see (the latest pre-release when no stable one exists). The tab is the `tab` query
 * parameter. The README renders on the server (R-UI-008) and is fetched only when the version says
 * it has one. The install panel sits above the tabs on a narrow content area and beside them on a
 * wide one (Q-UI-3).
 */
@Component({
  selector: 'trove-package-detail',
  imports: [
    DatePipe,
    DecimalPipe,
    MatButtonModule,
    MatTabsModule,
    RouterLink,
    CopyButton,
    DataTable,
    ErrorNotice,
    InstallPanel,
    PackageReadmeView,
  ],
  templateUrl: './package-detail.html',
  styleUrl: './package-detail.scss',
})
export class PackageDetail {
  private readonly api = inject(PackagesApi);
  private readonly router = inject(Router);
  private readonly memory = inject(PackageMemory);

  /** Route parameters of the matcher, bound by component input binding. */
  readonly format = input.required<string>();
  readonly name = input.required<string>();
  /** Query parameters. */
  readonly version = input<string | undefined>();
  readonly tab = input<string | undefined>();
  readonly prerelease = input<string | undefined>();

  protected readonly summary = resource({
    params: () => ({ format: this.format(), name: this.name() }),
    loader: ({ params }) => this.api.lookup(params.format, params.name),
  });
  protected readonly versions = resource({
    params: () => valueOf(this.summary)?.id,
    loader: ({ params }) => collectAll(this.api.versions(params, { sort: '-version' })),
  });

  protected readonly includePrereleases = computed(() => this.prerelease() === 'true');
  protected readonly all = computed<readonly PackageVersionSummary[]>(() => valueOf(this.versions) ?? []);
  /** The latest stable version, else the latest pre-release (the list is newest first). */
  protected readonly defaultVersion = computed(
    () => (this.all().find((v) => !v.prerelease) ?? this.all()[0])?.version ?? null,
  );
  protected readonly selected = computed(() => this.version() ?? this.defaultVersion());
  protected readonly options = computed(() =>
    this.all().filter(
      (v) => this.includePrereleases() || !v.prerelease || v.version === this.selected(),
    ),
  );

  protected readonly details = resource({
    params: () => {
      const id = valueOf(this.summary)?.id;
      const version = this.selected();
      return id && version ? { id, version } : undefined;
    },
    loader: ({ params }) => this.api.version(params.id, params.version),
  });
  protected readonly detail = computed(() => valueOf(this.details));
  protected readonly readme = resource({
    params: () => {
      const d = valueOf(this.details);
      return d?.hasReadme ? { id: d.packageId, version: d.version } : undefined;
    },
    loader: ({ params }) => this.api.readme(params.id, params.version),
  });

  protected readonly loadFailure = computed(() => {
    const e = this.summary.error() ?? this.versions.error() ?? this.details.error();
    return e ? asApiError(e) : null;
  });
  protected readonly readmeFailure = computed(() => {
    const e = this.readme.error();
    return e ? asApiError(e) : null;
  });

  protected readonly tabs = computed<readonly { id: DetailTab; label: string }[]>(() => [
    ...(this.detail()?.hasReadme ? [{ id: 'readme' as const, label: $localize`:@@pkg.tab.readme:README` }] : []),
    { id: 'metadata', label: $localize`:@@pkg.tab.metadata:Metadata` },
    { id: 'files', label: $localize`:@@pkg.tab.files:Files` },
    { id: 'repositories', label: $localize`:@@pkg.tab.repositories:Repositories` },
    { id: 'versions', label: $localize`:@@pkg.tab.versions:All versions` },
  ]);
  protected readonly activeIndex = computed(() => {
    const i = this.tabs().findIndex((t) => t.id === this.tab());
    return i < 0 ? 0 : i;
  });

  protected readonly distTags = computed(() => stringRecord(this.detail()?.metadata['distTags']));
  protected readonly dependencyGroups = computed<readonly DependencyGroup[]>(() => {
    const m = this.detail()?.metadata ?? {};
    return [
      { label: $localize`:@@pkg.meta.dependencies:Dependencies`, entries: stringRecord(m['dependencies']) },
      { label: $localize`:@@pkg.meta.dev-dependencies:Development dependencies`, entries: stringRecord(m['devDependencies']) },
      { label: $localize`:@@pkg.meta.peer-dependencies:Peer dependencies`, entries: stringRecord(m['peerDependencies']) },
      { label: $localize`:@@pkg.meta.optional-dependencies:Optional dependencies`, entries: stringRecord(m['optionalDependencies']) },
    ].filter((g) => g.entries.length > 0);
  });
  protected readonly frameworkGroups = computed(() => frameworkGroups(this.detail()?.metadata['dependencyGroups']));
  /** A NuGet version the publisher unlisted (R-NUGET-006): it restores when pinned and is hidden from search. */
  protected readonly unlisted = computed(() => this.detail()?.metadata['listed'] === false);
  protected readonly engines = computed(() => stringRecord(this.detail()?.metadata['engines']));
  protected readonly authors = computed(() => strings(this.detail()?.metadata['authors']));
  /** The plain members of a NuGet version (Q-NUGET-48): text, a name list or a version, shown as given. */
  protected readonly nugetFacts = computed<readonly [string, string][]>(() => {
    const m = this.detail()?.metadata ?? {};
    const text = (v: unknown): string => (typeof v === 'string' ? v.trim() : '');
    const types = strings(m['packageTypes']).join(', ');
    const entries: [string, string][] = [
      [$localize`:@@pkg.meta.min-client:Minimum client version`, text(m['minClientVersion'])],
      [$localize`:@@pkg.meta.package-types:Package types`, types],
      [$localize`:@@pkg.meta.language:Language`, text(m['language'])],
      [$localize`:@@pkg.meta.copyright:Copyright`, text(m['copyright'])],
      [$localize`:@@pkg.meta.release-notes:Release notes`, text(m['releaseNotes'])],
    ];
    return entries.filter((e) => e[1] !== '');
  });
  protected readonly deprecated = computed(() => {
    const d = this.detail()?.metadata['deprecated'];
    return typeof d === 'string' && d !== '' ? d : null;
  });
  protected readonly links = computed<readonly MetadataLink[]>(() => {
    const m = this.detail()?.metadata ?? {};
    const entries: [string, unknown][] = [
      [$localize`:@@pkg.meta.homepage:Homepage`, m['homepage']],
      [$localize`:@@pkg.meta.license-url:License`, m['licenseUrl']],
      [$localize`:@@pkg.meta.source:Source repository`, m['repositoryUrl']],
      [$localize`:@@pkg.meta.bugs:Issue tracker`, m['bugsUrl']],
    ];
    return entries.flatMap(([label, value]): MetadataLink[] =>
      typeof value === 'string' && value.trim() !== ''
        ? [{ label, text: value, href: safeHttpUrl(value) }]
        : [],
    );
  });
  protected readonly hasMetadata = computed(
    () =>
      this.distTags().length > 0 ||
      this.dependencyGroups().length > 0 ||
      this.frameworkGroups().length > 0 ||
      this.engines().length > 0 ||
      this.authors().length > 0 ||
      this.nugetFacts().length > 0 ||
      this.links().length > 0 ||
      this.deprecated() !== null,
  );

  protected readonly externalRel = EXTERNAL_REL;
  protected readonly commands = packageCommands;
  protected readonly size = formatSize;
  protected readonly fileColumns: readonly TableColumn[] = [
    { id: 'path', label: $localize`:@@pkg.file.path:File` },
    { id: 'size', label: $localize`:@@pkg.file.size:Size`, numeric: true },
    {
      id: 'sha256',
      label: $localize`:@@pkg.file.sha256:SHA-256`,
      help: $localize`:@@pkg.help.sha256:The checksum of the file's content. Compare it with the output of sha256sum on the downloaded file.`,
    },
    { id: 'download', label: $localize`:@@pkg.file.download:Download`, hideLabel: true },
  ];
  protected readonly versionColumns: readonly TableColumn[] = [
    { id: 'version', label: $localize`:@@pkg.ver.version:Version` },
    { id: 'published', label: $localize`:@@pkg.ver.published:Published` },
    { id: 'size', label: $localize`:@@pkg.ver.size:Size`, numeric: true },
    { id: 'repositories', label: $localize`:@@pkg.ver.repositories:Repositories` },
    { id: 'downloads', label: $localize`:@@pkg.ver.downloads:Downloads`, numeric: true },
  ];
  protected readonly repositoryColumns: readonly TableColumn[] = [
    { id: 'repository', label: $localize`:@@pkg.repo.repository:Repository` },
    { id: 'class', label: $localize`:@@pkg.repo.class:Class` },
    { id: 'published', label: $localize`:@@pkg.repo.published:Published` },
    { id: 'browse', label: $localize`:@@pkg.repo.browse:Browse`, hideLabel: true },
  ];
  protected readonly tableCaptionFiles = $localize`:@@pkg.file.caption:Files of this version`;
  protected readonly tableCaptionVersions = $localize`:@@pkg.ver.caption:All versions`;
  protected readonly tableCaptionRepositories = $localize`:@@pkg.repo.caption:Repositories that hold this version`;
  protected readonly copySha = $localize`:@@pkg.file.copy-sha:Copy SHA-256`;
  protected readonly classLabels = {
    local: $localize`:@@repo.class.local:Local`,
    remote: $localize`:@@repo.class.remote:Remote`,
    virtual: $localize`:@@repo.class.virtual:Virtual`,
  };

  constructor() {
    // Opening the page records it in "recently viewed" (R-UI-007).
    effect(() => {
      const s = valueOf(this.summary);
      if (s) untracked(() => this.memory.addRecent({ format: s.format, name: s.name }));
    });
  }

  protected selectTab(index: number): void {
    const id = this.tabs()[index]?.id;
    if (id && id !== this.tabs()[this.activeIndex()]?.id) void this.navigate({ tab: id });
  }

  protected chooseVersion(event: Event): void {
    const value = (event.target as HTMLInputElement).value.trim();
    if (this.all().some((v) => v.version === value)) void this.navigate({ version: value });
  }

  protected togglePrereleases(event: Event): void {
    const on = (event.target as HTMLInputElement).checked;
    void this.navigate({ prerelease: on ? 'true' : null });
  }

  private navigate(queryParams: Record<string, string | null>): Promise<boolean> {
    return this.router.navigate([], { queryParams, queryParamsHandling: 'merge' });
  }
}
