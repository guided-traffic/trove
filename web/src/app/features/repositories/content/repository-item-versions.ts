import { DatePipe } from '@angular/common';
import { Component, computed, effect, inject, input, resource, signal, untracked } from '@angular/core';
import { RouterLink } from '@angular/router';
import { PackageSummary, PackageVersionSummary, PackagesApi } from '../../../api/packages-api';
import { splitRef } from '../../../api/repositories-api';
import { VulnerabilitiesApi, VulnerabilitySeverityCounts } from '../../../api/vulnerabilities-api';
import { SessionStore } from '../../../core/session-store';
import { CopyButton } from '../../../shared/copy-button';
import { PagedResource } from '../../../shared/paged-resource';
import { DataTable, TableColumn } from '../../../shared/ui/data-table';
import { formatSize } from '../../packages/format-size';
import { InstallCommand } from '../../packages/install-command';
import { packageCommands } from '../../packages/package-links';
import { ContentShape } from './content-shape';
import { VulnCounts } from './vuln-counts';

/** How many versions an opened item lists at a time. */
const VERSIONS_PAGE = 20;

/**
 * The versions of one item of a repository's content page, opened in its row (R-UI-052): for an image its tags with digest, size and
 * pushed time, for a package its versions; the open findings of each, and the install or pull command for the version chosen in the
 * list. The command is the server's install snippet (R-API-030) for this repository, so it carries this installation's real address.
 */
@Component({
  selector: 'trove-repository-item-versions',
  imports: [DatePipe, RouterLink, CopyButton, DataTable, InstallCommand, VulnCounts],
  template: `
    <div class="command-panel">
      <p class="command-title">
        {{ shape().commandLabel }}
        @if (selected(); as v) {
          &ngsp;<code translate="no">{{ pkg().name }} {{ v }}</code>
        }
      </p>
      <trove-install-command
        [format]="pkg().format"
        [name]="pkg().name"
        [version]="selected()"
        [repository]="repository()"
      />
    </div>

    <trove-data-table
      [caption]="caption()"
      [columns]="columns()"
      [loaded]="versions.loaded()"
      [loading]="versions.loading()"
      [error]="versions.error()"
      [empty]="versions.items().length === 0"
      [emptyText]="shape().emptyText"
      [hasMore]="versions.hasMore()"
      (more)="versions.more()"
      (retry)="versions.more()"
    >
      @for (v of versions.items(); track v.version) {
        <tr [attr.aria-current]="v.version === selected() ? 'true' : null">
          <th scope="row">
            <a [routerLink]="detailLink()" [queryParams]="{ version: v.version }" translate="no">{{ v.version }}</a>
            @if (v.prerelease) {
              <span class="badge" i18n="@@repo.content.prerelease">pre-release</span>
            }
          </th>
          @if (shape().digest) {
            <td>
              @if (v.digest; as d) {
                <code class="ellipsis" translate="no" [attr.title]="d">{{ shortDigest(d) }}</code>
                <trove-copy-button [value]="d" [label]="copyDigestLabel" />
              } @else {
                –
              }
            </td>
          }
          <td class="num">{{ size(v.sizeBytes) }}</td>
          <td>
            <time [attr.datetime]="v.publishedAt" [attr.title]="v.publishedAt | date: 'long'">{{ v.publishedAt | date: 'medium' }}</time>
          </td>
          @if (scanned()) {
            <td><trove-vuln-counts [counts]="countsOf(v.version)" /></td>
          }
          <td class="actions">
            <button
              type="button"
              class="btn btn-tertiary"
              [attr.aria-pressed]="v.version === selected()"
              (click)="select(v.version)"
            >
              <span class="visually-hidden">{{ v.version }}: </span>
              <span i18n="@@repo.content.show-command">Show command</span>
            </button>
          </td>
        </tr>
      }
    </trove-data-table>
  `,
  styles: `
    :host {
      display: block;
      padding: var(--trove-space-3) 0 var(--trove-space-3) var(--trove-space-6);
    }
    .command-panel {
      margin-block-end: var(--trove-space-3);
      padding: var(--trove-space-3);
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-m);
    }
    .command-title {
      margin: 0 0 var(--trove-space-2);
      font-weight: 600;
    }
    .command-title code {
      font-weight: 400;
    }
    code.ellipsis {
      display: inline-block;
      max-inline-size: 24ch;
      overflow: hidden;
      text-overflow: ellipsis;
      vertical-align: middle;
      white-space: nowrap;
    }
    .num {
      text-align: end;
      font-variant-numeric: tabular-nums;
    }
    .actions {
      text-align: end;
      white-space: nowrap;
    }
  `,
})
export class RepositoryItemVersions {
  private readonly api = inject(PackagesApi);
  private readonly vulnerabilities = inject(VulnerabilitiesApi);
  private readonly session = inject(SessionStore);

  readonly pkg = input.required<PackageSummary>();
  /** The qualified name of the repository whose versions are shown. */
  readonly repository = input.required<string>();
  readonly shape = input.required<ContentShape>();
  /** The format is matched against advisories: the column of findings is shown. */
  readonly scanned = input(false);

  protected readonly versions = new PagedResource<PackageVersionSummary>(VERSIONS_PAGE);
  private readonly chosen = signal<string | null>(null);
  /** The version the command is for: the one the reader chose, else the package's latest. */
  protected readonly selected = computed(() => this.chosen() ?? this.pkg().latestVersion);

  /** The open findings per version in this repository, in one request for the item (R-VULN-020). */
  protected readonly report = resource({
    params: () => (this.scanned() && this.session.state() !== 'anonymous' ? { id: this.pkg().id, repository: this.repository() } : undefined),
    loader: ({ params }) => this.vulnerabilities.packageReport(params.id, [params.repository]),
  });
  private readonly countsByVersion = computed(() => {
    const report = this.report.hasValue() ? this.report.value() : undefined;
    return new Map((report?.versions ?? []).map((v) => [v.version, v.open] as const));
  });

  protected readonly caption = computed(() => `${this.shape().versionsLabel}: ${this.pkg().name}`);
  protected readonly copyDigestLabel = $localize`:@@repo.content.copy-digest:Copy digest`;
  protected readonly detailLink = computed(() => packageCommands(splitRef(this.repository()).project, this.pkg().format, this.pkg().name));
  protected readonly columns = computed<readonly TableColumn[]>(() => {
    const s = this.shape();
    const columns: TableColumn[] = [{ id: 'version', label: s.versionLabel }];
    if (s.digest) columns.push({ id: 'digest', label: $localize`:@@repo.content.col.digest:Digest` });
    columns.push(
      { id: 'size', label: $localize`:@@repo.content.col.size:Size`, numeric: true },
      { id: 'published', label: s.publishedLabel },
    );
    if (this.scanned()) columns.push({ id: 'vulnerabilities', label: $localize`:@@repo.content.col.vulnerabilities:Vulnerabilities` });
    columns.push({ id: 'actions', label: $localize`:@@repo.col.actions:Actions`, hideLabel: true });
    return columns;
  });

  constructor() {
    effect(() => {
      const id = this.pkg().id;
      const repository = this.repository();
      const digest = this.shape().digest;
      untracked(() => {
        this.chosen.set(null);
        void this.versions.start(
          this.api.versions(id, { repository: [repository], sort: '-publishedAt', include: digest ? ['digest'] : [] }),
        );
      });
    });
  }

  protected countsOf(version: string): VulnerabilitySeverityCounts | undefined {
    return this.countsByVersion().get(version);
  }

  protected select(version: string): void {
    this.chosen.set(version);
  }

  protected size(bytes: number): string {
    return formatSize(bytes);
  }

  /** `sha256:abc…` keeps the algorithm and a stretch a person can compare; the copy button has the whole digest. */
  protected shortDigest(digest: string): string {
    return digest.length > 19 ? `${digest.slice(0, 19)}…` : digest;
  }
}
