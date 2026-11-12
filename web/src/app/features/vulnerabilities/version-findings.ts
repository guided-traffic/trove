import { DecimalPipe } from '@angular/common';
import { Component, computed, effect, inject, input, untracked } from '@angular/core';
import { RouterLink } from '@angular/router';
import {
  FindingFilters,
  OPEN_STATUSES,
  VulnerabilitiesApi,
  VulnerabilityFinding,
  VulnerabilitySeverity,
  VulnerabilitySeverityCounts,
} from '../../api/vulnerabilities-api';
import { PagedResource } from '../../shared/paged-resource';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { fiveCounts } from '../../shared/vulnerability/grade-words';
import { BarSeverity } from '../../shared/vulnerability/severity-bar';
import { severityLabel } from './vuln-labels';
import { FindingStatus, SeverityBadge } from './vuln-parts';

/** The findings of a version on the package page come 25 at a time (Q-UI-379). */
export const VERSION_FINDINGS_PAGE = 25;

/** The severities a segment of the bar stands for: Low with None (Q-VULN-153). */
export function severitiesOf(s: BarSeverity): VulnerabilitySeverity[] {
  return s === 'low' ? ['low', 'none'] : [s];
}

/**
 * The open findings of one version in one repository on the package page's Vulnerabilities tab (R-VULN-023, Q-UI-379): asked of
 * the findings API, which pages, orders by severity and narrows to what the reader may read (R-VULN-019, R-VULN-020), 25 at a time,
 * the worst first, narrowed to one severity by the card's bar. How many there are comes from the grade's counts, which the API
 * counts in full; the version report's own list stops at 1,000 per repository. The table is the shared data table, which stacks
 * on a narrow screen.
 */
@Component({
  selector: 'trove-version-findings',
  preserveWhitespaces: true,
  imports: [DecimalPipe, RouterLink, DataTable, FindingStatus, SeverityBadge],
  template: `
    <p class="vuln-findings-total">
      @if (severity(); as s) {
        <span i18n="@@pkg.vuln.findings-filtered">{{ shown() | number }} of {{ total() | number }} open findings: {{ severityName(s) }}.</span>
        <a [routerLink]="[]" [queryParams]="{ severity: null }" queryParamsHandling="merge" i18n="@@pkg.vuln.findings-all">Show all</a>
      } @else {
        <span i18n="@@pkg.vuln.findings-total">{{ total() | number }} open findings, the worst first.</span>
      }
    </p>
    <trove-data-table
      [caption]="caption()"
      [columns]="columns"
      [loaded]="list.loaded()"
      [loading]="list.loading()"
      [error]="list.error()"
      [empty]="list.items().length === 0"
      [emptyText]="emptyText"
      [hasMore]="list.hasMore()"
      (more)="list.more()"
      (retry)="list.reload()"
    >
      @for (f of list.items(); track f.id) {
        <tr>
          <td><trove-severity [severity]="f.severity" [score]="f.cvss?.score ?? null" /></td>
          <th scope="row">
            <a [routerLink]="['/projects', f.project, 'vulnerabilities', 'findings', f.id]" translate="no">{{ f.advisory }}</a>
            <span class="secondary">{{ f.summary }}</span>
          </th>
          <td><trove-finding-status [finding]="f" /></td>
          <td translate="no" class="wrap">{{ f.fixedVersions.length > 0 ? f.fixedVersions.join(', ') : '–' }}</td>
        </tr>
      }
    </trove-data-table>
  `,
})
export class VersionFindings {
  private readonly api = inject(VulnerabilitiesApi);

  /** The repository's qualified name. */
  readonly repository = input.required<string>();
  readonly packageName = input.required<string>();
  /** The versions the findings name: the version, or an image tag's manifest digests (R-VULN-037). */
  readonly versions = input.required<readonly string[]>();
  /** The open findings by severity, the grade's (R-VULN-039): the total. */
  readonly counts = input.required<VulnerabilitySeverityCounts>();
  /** The severity the card's bar narrowed to, or null for all. */
  readonly severity = input<BarSeverity | null>(null);

  protected readonly list = new PagedResource<VulnerabilityFinding>(VERSION_FINDINGS_PAGE);
  protected readonly columns: readonly TableColumn[] = [
    { id: 'severity', label: $localize`:@@vuln.col.severity:Severity`, width: '7rem' },
    { id: 'identifier', label: $localize`:@@vuln.col.identifier:Identifier`, width: '14rem' },
    { id: 'status', label: $localize`:@@vuln.col.status:Status` },
    { id: 'fixed', label: $localize`:@@pkg.vuln.col-fixed:Fixed in` },
  ];
  protected readonly emptyText = $localize`:@@pkg.vuln.findings-none:No open finding of this severity.`;
  protected readonly caption = computed(() => $localize`:@@pkg.vuln.caption-in:Open findings of this version in ${this.repository()}:repository:`);

  protected readonly total = computed(() => {
    const c = fiveCounts(this.counts());
    return c.critical + c.high + c.medium + c.low + c.unknown;
  });
  protected readonly shown = computed(() => {
    const s = this.severity();
    return s ? fiveCounts(this.counts())[s] : this.total();
  });

  /**
   * The query of the list, equal by value: the page hands the versions over as a new array on every change detection, and a new
   * array must not restart the list (and lose what "Load more" added).
   */
  private readonly filters = computed<FindingFilters>(
    () => ({
      repository: [this.repository()],
      package: [this.packageName()],
      version: [...this.versions()],
      status: OPEN_STATUSES,
      sort: '-severity',
      ...(this.severity() ? { severity: severitiesOf(this.severity()!) } : {}),
    }),
    { equal: (a, b) => JSON.stringify(a) === JSON.stringify(b) },
  );

  constructor() {
    effect(() => {
      const filters = this.filters();
      untracked(() => void this.list.start(this.api.findings(filters)));
    });
  }

  protected severityName(s: BarSeverity): string {
    return severityLabel(s);
  }
}
