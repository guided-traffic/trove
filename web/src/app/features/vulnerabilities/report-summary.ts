import { DecimalPipe } from '@angular/common';
import { Component, LOCALE_ID, computed, inject, input, resource } from '@angular/core';
import { RouterLink } from '@angular/router';
import { OPEN_STATUSES, VulnerabilitiesApi, VulnerabilitySourceState, VulnerabilitySummary } from '../../api/vulnerabilities-api';
import { SessionStore } from '../../core/session-store';
import { VULN_CONTEXT } from './vuln-context';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { BAR_SEVERITIES, BarQueries, SeverityBar } from '../../shared/vulnerability/severity-bar';
import { relativeTime } from '../packages/format-time';

/** One count of the header: a link that applies its filter (R-VULN-022). */
export interface SummaryCount {
  readonly id: string;
  readonly label: string;
  readonly icon: string;
  readonly cls: string;
  readonly count: number;
  /** The query of the link; the scope (project, repository, format) is kept, every other filter is replaced. */
  readonly query: Readonly<Record<string, string | readonly string[]>>;
}

/** A line of the banner about the advisory data of one ecosystem. */
export interface DataNotice {
  readonly key: string;
  readonly text: string;
}

/**
 * The header of the report (R-VULN-022): the open findings by severity as the severity bar whose segments are filter links
 * (R-UI-092, Q-UI-376), the counts of malicious findings, of findings to triage and of blocked versions as filter links, a banner when an ecosystem's
 * advisory data is stale, was never imported completely or its sync fails, and the coverage line. The counts are the
 * API's, narrowed to what the caller may read; the failing state needs the sources, which need `settings:read`, so a
 * caller without it sees stale and pending only.
 */
@Component({
  selector: 'trove-vuln-summary',
  imports: [DecimalPipe, RouterLink, ErrorNotice, SeverityBar],
  template: `
    @if (failure(); as f) {
      <trove-error-notice [error]="f" (retry)="summary.reload()" />
    }
    @for (n of notices(); track n.key) {
      <p class="banner-warning" role="note">
        <span class="material-icons inline warn" aria-hidden="true">warning</span>
        <span
          >{{ n.text }}
          @if (canSources()) {
            <a routerLink="/admin/advisory-sources" i18n="@@vuln.banner.sources">Open the advisory sources</a>
          }
        </span>
      </p>
    }
    @if (summary.hasValue() && bySeverity(); as severities) {
      <trove-severity-bar class="vuln-bar" [counts]="severities" [queries]="barQueries()" />
    }
    @if (counts().length > 0) {
      <ul class="vuln-counts" aria-label="Open findings by state" i18n-aria-label="@@vuln.counts.state-label">
        @for (c of counts(); track c.id) {
          <li>
            <a class="vuln-count" [class]="c.cls" [routerLink]="[]" [queryParams]="c.query">
              <span class="vuln-count-number">{{ c.count | number }}</span>
              {{ ' ' }}
              <span class="vuln-count-label">
                <span class="material-icons" aria-hidden="true">{{ c.icon }}</span>
                <span>{{ c.label }}</span>
              </span>
            </a>
          </li>
        }
      </ul>
    }
    @if (summary.hasValue() && coverageText(); as line) {
      <p class="vuln-coverage">{{ line }}</p>
    }
  `,
})
export class ReportSummary {
  private readonly api = inject(VulnerabilitiesApi);
  private readonly session = inject(SessionStore);
  private readonly locale = inject(LOCALE_ID);
  protected readonly ctx = inject(VULN_CONTEXT);

  /** The projects whose findings are counted (Q-UI-129). */
  readonly project = input<readonly string[]>([]);
  readonly repository = input<readonly string[]>([]);
  readonly format = input<readonly string[]>([]);
  /** Counts up when the screen changed findings, so that the counts are read again. */
  readonly refresh = input(0);

  protected readonly canSources = computed(() => this.session.has('settings:read'));

  protected readonly summary = resource({
    params: () => ({ project: this.project(), repository: this.repository(), format: this.format(), refresh: this.refresh() }),
    loader: ({ params }) => this.api.summary({ project: params.project, repository: params.repository, format: params.format }),
  });
  protected readonly sources = resource({
    params: () => (this.canSources() ? { refresh: this.refresh() } : undefined),
    loader: () => this.api.sources(),
  });

  protected readonly failure = computed(() => {
    const e = this.summary.error();
    return e ? asApiError(e) : null;
  });

  private readonly scope = computed<Record<string, readonly string[]>>(() => {
    const out: Record<string, readonly string[]> = {};
    if (this.project().length > 0) out['project'] = this.project();
    if (this.repository().length > 0) out['repository'] = this.repository();
    if (this.format().length > 0) out['format'] = this.format();
    return out;
  });

  /** The open findings by severity, for the bar. */
  protected readonly bySeverity = computed(() => valueOf(this.summary)?.bySeverity ?? null);

  /** The filter of each segment of the bar: its severity among the open findings, Low with None (Q-VULN-153). */
  protected readonly barQueries = computed<BarQueries>(() => {
    const scope = this.scope();
    const open = [...OPEN_STATUSES];
    const out = {} as Record<(typeof BAR_SEVERITIES)[number], Record<string, readonly string[]>>;
    for (const sev of BAR_SEVERITIES) out[sev] = { ...scope, severity: sev === 'low' ? ['low', 'none'] : [sev], status: open };
    return out;
  });

  protected readonly counts = computed<readonly SummaryCount[]>(() => {
    const s = valueOf(this.summary);
    if (!s) return [];
    const scope = this.scope();
    const open = [...OPEN_STATUSES];
    return [
      {
        id: 'malicious',
        label: $localize`:@@vuln.counts.malicious:Malicious`,
        icon: 'bug_report',
        cls: 'count-kind',
        count: s.byKind.malicious,
        query: { ...scope, kind: 'malicious', status: open },
      },
      {
        id: 'needs-triage',
        label: $localize`:@@vuln.counts.needs-triage:Needs triage`,
        icon: 'pending',
        cls: 'count-kind',
        count: s.needsTriage,
        query: { ...scope, status: ['needs_triage'] },
      },
      {
        id: 'blocked',
        label: $localize`:@@vuln.counts.blocked:Blocked versions`,
        icon: 'block',
        cls: 'count-kind',
        count: s.blockedVersions,
        query: { ...scope, blocked: 'true' },
      },
    ];
  });

  /** The notices of the banner: stale, never imported completely, failing (R-VULN-022). */
  protected readonly notices = computed<readonly DataNotice[]>(() => {
    const s = valueOf(this.summary);
    if (!s) return [];
    return dataNotices(s, valueOf(this.sources) ?? [], this.locale);
  });

  protected readonly coverageText = computed(() => {
    const c = valueOf(this.summary)?.coverage;
    if (!c) return null;
    return $localize`:@@vuln.coverage:Coverage of the repositories you can read: ${c.evaluated}:scanned: scanned, ${c.internal}:internal: internal names only, ${c.pendingData}:pending: waiting for advisory data, ${c.notCovered}:notCovered: not covered.`;
  });
}

/** Builds the banner's lines from the summary's data ages and the sources' failures (exported for the specs). */
export function dataNotices(
  summary: VulnerabilitySummary,
  sources: readonly VulnerabilitySourceState[],
  locale: string,
  now: number = Date.now(),
): DataNotice[] {
  const out: DataNotice[] = [];
  for (const d of summary.data) {
    if (d.state === 'empty') {
      out.push({
        key: `${d.ecosystem}:empty`,
        text: $localize`:@@vuln.banner.pending:The advisory data of ${d.ecosystem}:ecosystem: has not been imported completely yet. Versions of ${d.ecosystem}:ecosystemAgain: are not evaluated until it has.`,
      });
    } else if (d.stale) {
      const age = d.syncedAt ? relativeTime(d.syncedAt, locale, now) : '–';
      out.push({
        key: `${d.ecosystem}:stale`,
        text: $localize`:@@vuln.banner.stale:The advisory data of ${d.ecosystem}:ecosystem: is stale, it was last synchronised ${age}:age:.`,
      });
    }
  }
  for (const s of sources) {
    if (s.failures > 0 && s.source === 'osv') {
      const name = s.ecosystem || s.source;
      out.push({
        key: `${name}:failing`,
        text: $localize`:@@vuln.banner.failing:The last ${s.failures}:failures: synchronisations of ${name}:ecosystem: failed.`,
      });
    }
  }
  return out;
}
