import { Component, computed, input } from '@angular/core';
import { VulnerabilityFinding, VulnerabilitySeverity } from '../../api/vulnerabilities-api';
import { blockReasonLabel, severityClass, severityIcon, severityLabel, statusLabel } from './vuln-labels';

/**
 * A severity as an icon and a word, with the score when there is one (R-VULN-022, R-UI-044): the colour only
 * reinforces them, and the shapes of the icons differ.
 */
@Component({
  selector: 'trove-severity',
  template: `
    <span class="sev" [class]="cls()">
      <span class="material-icons" aria-hidden="true">{{ icon() }}</span>
      <span>{{ label() }}</span>
      @if (score() !== null) {
        {{ ' ' }}<span class="sev-score">{{ score() }}</span>
      }
    </span>
  `,
})
export class SeverityBadge {
  readonly severity = input.required<VulnerabilitySeverity>();
  readonly score = input<number | null>(null);

  protected readonly label = computed(() => severityLabel(this.severity()));
  protected readonly icon = computed(() => severityIcon(this.severity()));
  protected readonly cls = computed(() => `sev ${severityClass(this.severity())}`);
}

/**
 * The status of a finding: a block shows as *Blocked* with its reason (*malicious package*, *blocked in
 * triage*), anything else as its status (R-VULN-022). An icon and a word; a positive state (resolved) is the only
 * green.
 */
@Component({
  selector: 'trove-finding-status',
  template: `
    <span class="fstatus" [class]="cls()">
      <span class="material-icons" aria-hidden="true">{{ icon() }}</span>
      <span>{{ label() }}</span>
      @if (reason(); as r) {
        {{ ' ' }}<span class="fstatus-reason">({{ r }})</span>
      }
    </span>
  `,
})
export class FindingStatus {
  readonly finding = input.required<Pick<VulnerabilityFinding, 'status' | 'blocked' | 'blockReason'>>();

  protected readonly blocked = computed(() => this.finding().blocked || this.finding().status === 'blocked');
  protected readonly label = computed(() => (this.blocked() ? statusLabel('blocked') : statusLabel(this.finding().status)));
  protected readonly reason = computed(() => {
    const f = this.finding();
    if (!this.blocked()) return null;
    return f.blockReason ? blockReasonLabel(f.blockReason) : f.status === 'blocked' ? blockReasonLabel('triage') : null;
  });
  protected readonly icon = computed(() => {
    if (this.blocked()) return 'block';
    switch (this.finding().status) {
      case 'needs_triage':
        return 'pending';
      case 'confirmed':
        return 'fact_check';
      case 'dismissed':
        return 'visibility_off';
      default:
        return 'check_circle';
    }
  });
  protected readonly cls = computed(() => {
    if (this.blocked()) return 'fstatus fstatus-blocked';
    return this.finding().status === 'resolved' ? 'fstatus ok' : 'fstatus';
  });
}
