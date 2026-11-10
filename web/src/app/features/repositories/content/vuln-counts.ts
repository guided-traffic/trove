import { Component, computed, input } from '@angular/core';
import { SEVERITIES, VulnerabilitySeverityCounts } from '../../../api/vulnerabilities-api';
import { highestOpen } from '../../vulnerabilities/package-vulnerabilities';
import { severityLabel } from '../../vulnerabilities/vuln-labels';
import { SeverityBadge } from '../../vulnerabilities/vuln-parts';

/**
 * The open findings of a package or a version on the content page of a repository (R-UI-052): the highest severity as a badge and,
 * after it, how many findings are open. No finding reads as a positive status; `undefined` means the page has no counts (an
 * anonymous caller, an installation without scanning data) and shows a dash.
 */
@Component({
  selector: 'trove-vuln-counts',
  imports: [SeverityBadge],
  template: `
    @if (counts()) {
      @if (highest(); as h) {
        <span class="repo-findings">
          <trove-severity [severity]="h" />
          <span class="repo-findings-total" aria-hidden="true">{{ total() }}</span>
          <span class="visually-hidden">{{ totalLabel() }}</span>
        </span>
      } @else {
        <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span><span i18n="@@repo.content.no-findings">None open</span></span>
      }
    } @else {
      <span class="muted">–</span>
    }
  `,
  styles: `
    .repo-findings {
      display: inline-flex;
      align-items: center;
      gap: var(--trove-space-1);
    }
    .repo-findings-total {
      font-variant-numeric: tabular-nums;
    }
    .muted {
      color: var(--trove-text-muted);
    }
  `,
})
export class VulnCounts {
  readonly counts = input<VulnerabilitySeverityCounts | undefined>();

  protected readonly highest = computed(() => highestOpen(this.counts()));
  protected readonly total = computed(() => {
    const c = this.counts();
    return c ? SEVERITIES.reduce((sum, s) => sum + c[s], 0) : 0;
  });
  protected readonly totalLabel = computed(
    () => $localize`:@@repo.content.findings:${this.total()}:count: open findings, highest ${severityLabel(this.highest() ?? 'unknown')}:severity:`,
  );
}
