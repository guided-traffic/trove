import { DecimalPipe } from '@angular/common';
import { Component, computed, inject, input, output, resource, signal } from '@angular/core';
import { SEVERITIES, VulnerabilitiesApi, VulnerabilitySeverity, VulnerabilityTrend } from '../../api/vulnerabilities-api';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { severityIcon, severityLabel } from './vuln-labels';

export type TrendRange = '30' | '90' | '365';
export const TREND_RANGES: readonly TrendRange[] = ['30', '90', '365'];

const WIDTH = 640;
const HEIGHT = 200;
const LEFT = 40;
const TOP = 8;
const BOTTOM = 24;

/** Bottom to top of a column: the worst level sits on the axis, so its height reads without adding up. */
const STACK: readonly VulnerabilitySeverity[] = ['critical', 'high', 'medium', 'low', 'none', 'unknown'];

export interface TrendSeries {
  readonly severity: VulnerabilitySeverity;
  /** The `d` of one path: one rectangle per day. */
  readonly path: string;
  readonly total: number;
}

export interface TrendGeometry {
  readonly series: readonly TrendSeries[];
  readonly yMax: number;
  readonly ticks: readonly { readonly value: number; readonly y: number }[];
  readonly width: number;
  readonly height: number;
  readonly left: number;
  readonly right: number;
}

/** A round upper bound for the axis: 5, 10, 20, 50, 100, … never below the largest total. */
export function niceMax(value: number): number {
  if (value <= 5) return 5;
  const power = 10 ** Math.floor(Math.log10(value));
  for (const m of [1, 2, 5, 10]) if (value <= m * power) return m * power;
  return 10 * power;
}

/** The bars of the stacked chart in viewBox units (exported for the specs). */
export function trendGeometry(trend: VulnerabilityTrend): TrendGeometry {
  const n = trend.points.length;
  const totals = trend.points.map((p) => SEVERITIES.reduce((sum, s) => sum + p.counts[s], 0));
  const yMax = niceMax(Math.max(0, ...totals));
  const plotW = WIDTH - LEFT - 4;
  const plotH = HEIGHT - TOP - BOTTOM;
  const slot = n === 0 ? plotW : plotW / n;
  const barW = Math.max(0.5, slot - (slot > 4 ? 1 : 0));
  const yOf = (v: number): number => TOP + plotH - (v / yMax) * plotH;
  const series = STACK.map((severity): TrendSeries => {
    let d = '';
    let total = 0;
    trend.points.forEach((p, i) => {
      const below = STACK.slice(0, STACK.indexOf(severity)).reduce((sum, s) => sum + p.counts[s], 0);
      const v = p.counts[severity];
      total += v;
      if (v <= 0) return;
      const x = LEFT + i * slot;
      const top = yOf(below + v);
      const h = yOf(below) - top;
      d += `M${x.toFixed(2)} ${top.toFixed(2)}h${barW.toFixed(2)}v${h.toFixed(2)}h${(-barW).toFixed(2)}z`;
    });
    return { severity, path: d, total };
  });
  const ticks = [0, yMax / 2, yMax].map((value) => ({ value, y: yOf(value) }));
  return { series, yMax, ticks, width: WIDTH, height: HEIGHT, left: LEFT, right: WIDTH - 4 };
}

/**
 * The trend of the open findings by severity (R-VULN-022): a stacked column per day, drawn here as an SVG (no chart
 * library, Q-VULN-53), over 30, 90 or 365 days, with a "Show as table" alternative that carries every number. The
 * chart is an image with a text alternative; the table is the accessible form of the data.
 */
@Component({
  selector: 'trove-vuln-trend',
  imports: [DecimalPipe, ErrorNotice],
  template: `
    <section class="vuln-trend" aria-labelledby="vuln-trend-title">
      <div class="vuln-trend-head">
        <h2 id="vuln-trend-title" i18n="@@vuln.trend.title">Open findings over time</h2>
        <div class="vuln-range" role="group" aria-label="Period" i18n-aria-label="@@vuln.trend.period">
          @for (r of ranges; track r) {
            <button class="btn btn-default" type="button" [attr.aria-pressed]="range() === r" (click)="rangeChange.emit(r)">
              {{ rangeLabel(r) }}
            </button>
          }
        </div>
        <button class="btn btn-tertiary" type="button" [attr.aria-expanded]="asTable()" aria-controls="vuln-trend-body" (click)="asTable.set(!asTable())">
          @if (asTable()) {
            <ng-container i18n="@@vuln.trend.show-chart">Show as chart</ng-container>
          } @else {
            <ng-container i18n="@@vuln.trend.show-table">Show as table</ng-container>
          }
        </button>
      </div>
      <div id="vuln-trend-body">
        @if (failure(); as f) {
          <trove-error-notice [error]="f" (retry)="trend.reload()" />
        } @else if (!trend.hasValue()) {
          <p role="status" i18n="@@vuln.trend.loading">Loading the trend.</p>
        } @else if (asTable()) {
          <div class="trend-table-scroll" tabindex="0" role="region" aria-label="Open findings per day" i18n-aria-label="@@vuln.trend.table-label">
            <table class="data trend-table">
              <thead>
                <tr>
                  <th scope="col" i18n="@@vuln.trend.col-date">Date</th>
                  @for (s of stack; track s) {
                    <th scope="col" class="num">{{ label(s) }}</th>
                  }
                </tr>
              </thead>
              <tbody>
                @for (p of points(); track p.date) {
                  <tr>
                    <th scope="row">
                      <time [attr.datetime]="p.date">{{ p.date }}</time>
                    </th>
                    @for (s of stack; track s) {
                      <td class="num">{{ p.counts[s] | number }}</td>
                    }
                  </tr>
                }
              </tbody>
            </table>
          </div>
        } @else if (geometry(); as g) {
          <svg class="trend-chart" [attr.viewBox]="'0 0 ' + g.width + ' ' + g.height" role="img" aria-labelledby="vuln-trend-title vuln-trend-desc">
            <desc id="vuln-trend-desc">{{ description() }}</desc>
            @for (t of g.ticks; track t.value) {
              <line class="trend-grid" [attr.x1]="g.left" [attr.x2]="g.right" [attr.y1]="t.y" [attr.y2]="t.y" />
              <text class="trend-tick" [attr.x]="g.left - 6" [attr.y]="t.y + 4" text-anchor="end">{{ t.value }}</text>
            }
            @for (s of g.series; track s.severity) {
              @if (s.path) {
                <path [class]="'trend-series trend-' + s.severity" [attr.d]="s.path" />
              }
            }
          </svg>
          <ul class="trend-legend" aria-label="Legend" i18n-aria-label="@@vuln.trend.legend">
            @for (s of stack; track s) {
              <li>
                <span class="trend-swatch" [class]="'trend-swatch trend-' + s" aria-hidden="true"></span>
                <span class="material-icons" aria-hidden="true">{{ icon(s) }}</span>
                <span>{{ label(s) }}</span>
              </li>
            }
          </ul>
          <p class="hint">{{ basis() }}</p>
        }
      </div>
    </section>
  `,
})
export class ReportTrend {
  private readonly api = inject(VulnerabilitiesApi);

  /** The project whose findings are counted (Q-UI-129). */
  readonly project = input.required<string>();
  readonly repository = input<readonly string[]>([]);
  readonly format = input<readonly string[]>([]);
  readonly range = input<TrendRange>('90');
  readonly refresh = input(0);
  readonly rangeChange = output<TrendRange>();

  protected readonly ranges = TREND_RANGES;
  protected readonly stack = STACK;
  protected readonly asTable = signal(false);

  protected readonly trend = resource({
    params: () => ({ project: this.project(), repository: this.repository(), format: this.format(), range: this.range(), refresh: this.refresh() }),
    loader: ({ params }) => {
      const to = new Date();
      const from = new Date(to.getTime() - (Number(params.range) - 1) * 86_400_000);
      const day = (d: Date): string => d.toISOString().slice(0, 10);
      return this.api.trend({ from: day(from), to: day(to), project: params.project, repository: params.repository, format: params.format });
    },
  });
  protected readonly failure = computed(() => {
    const e = this.trend.error();
    return e ? asApiError(e) : null;
  });
  protected readonly basis = computed(() => valueOf(this.trend)?.basis ?? '');
  protected readonly points = computed(() => valueOf(this.trend)?.points ?? []);
  protected readonly geometry = computed(() => {
    const t = valueOf(this.trend);
    return t ? trendGeometry(t) : null;
  });
  /** The text alternative of the image: the span and the numbers of the last day. */
  protected readonly description = computed(() => {
    const t = valueOf(this.trend);
    const last = t?.points[t.points.length - 1];
    if (!t || !last) return $localize`:@@vuln.trend.empty:No data in this period.`;
    const parts = STACK.map((s) => `${severityLabel(s)} ${last.counts[s]}`).join(', ');
    return $localize`:@@vuln.trend.description:Stacked columns of the open findings per day from ${t.from}:from: to ${t.to}:to:. On ${t.to}:last: there were ${parts}:counts:.`;
  });

  protected label(s: VulnerabilitySeverity): string {
    return severityLabel(s);
  }

  protected icon(s: VulnerabilitySeverity): string {
    return severityIcon(s);
  }

  protected rangeLabel(r: TrendRange): string {
    switch (r) {
      case '30':
        return $localize`:@@vuln.trend.range-30:30 days`;
      case '90':
        return $localize`:@@vuln.trend.range-90:90 days`;
      default:
        return $localize`:@@vuln.trend.range-365:365 days`;
    }
  }
}
