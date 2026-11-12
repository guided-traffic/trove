import {
  VulnerabilityBlockReason,
  VulnerabilityDismissalReason,
  VulnerabilityFinding,
  VulnerabilityKind,
  VulnerabilityResolvedReason,
  VulnerabilitySeverity,
  VulnerabilityStatus,
  VulnerabilityVersionBlockReason,
} from '../../api/vulnerabilities-api';

/*
 * The words of the vulnerability screens, in one place so that the report, the detail, the package tab and the
 * dialogs say the same thing (R-VULN-022, R-VULN-023). A severity is a word and an icon, never a colour alone
 * (R-UI-044); the colour is a class (`sev-*`) that only reinforces them.
 */

export function severityLabel(s: VulnerabilitySeverity): string {
  switch (s) {
    case 'critical':
      return $localize`:@@vuln.severity.critical:Critical`;
    case 'high':
      return $localize`:@@vuln.severity.high:High`;
    case 'medium':
      return $localize`:@@vuln.severity.medium:Medium`;
    case 'low':
      return $localize`:@@vuln.severity.low:Low`;
    case 'none':
      return $localize`:@@vuln.severity.none:None`;
    default:
      return $localize`:@@vuln.severity.unknown:Unknown`;
  }
}

/** The icon ligature of a severity; the shapes differ, so the level reads without the colour. */
export function severityIcon(s: VulnerabilitySeverity): string {
  switch (s) {
    case 'critical':
      return 'error';
    case 'high':
      return 'report';
    case 'medium':
      return 'warning';
    case 'low':
      return 'info';
    case 'none':
      return 'remove_circle_outline';
    default:
      return 'help_outline';
  }
}

export function statusLabel(s: VulnerabilityStatus): string {
  switch (s) {
    case 'needs_triage':
      return $localize`:@@vuln.status.needs-triage:Needs triage`;
    case 'confirmed':
      return $localize`:@@vuln.status.confirmed:Confirmed`;
    case 'blocked':
      return $localize`:@@vuln.status.blocked:Blocked`;
    case 'dismissed':
      return $localize`:@@vuln.status.dismissed:Dismissed`;
    default:
      return $localize`:@@vuln.status.resolved:Resolved`;
  }
}

/** The reason of a block; `missing_data` is a version's only: the ecosystem's advisory data is not complete and onMissingData is block (R-VULN-015). */
export function blockReasonLabel(r: VulnerabilityBlockReason | VulnerabilityVersionBlockReason): string {
  switch (r) {
    case 'malicious':
      return $localize`:@@vuln.block-reason.malicious:malicious package`;
    case 'missing_data':
      return $localize`:@@vuln.block-reason.missing-data:advisory data not yet available`;
    default:
      return $localize`:@@vuln.block-reason.triage:blocked in triage`;
  }
}

export function kindLabel(k: VulnerabilityKind): string {
  return k === 'malicious'
    ? $localize`:@@vuln.kind.malicious:Malicious package`
    : $localize`:@@vuln.kind.vulnerability:Vulnerability`;
}

export function dismissalLabel(r: VulnerabilityDismissalReason): string {
  switch (r) {
    case 'not_exploitable':
      return $localize`:@@vuln.dismissal.not-exploitable:Not exploitable`;
    case 'false_positive':
      return $localize`:@@vuln.dismissal.false-positive:False positive`;
    case 'risk_accepted':
      return $localize`:@@vuln.dismissal.risk-accepted:Risk accepted`;
    case 'mitigated':
      return $localize`:@@vuln.dismissal.mitigated:Mitigated elsewhere`;
    default:
      return $localize`:@@vuln.dismissal.test-only:Test code only`;
  }
}

export function resolvedLabel(r: VulnerabilityResolvedReason): string {
  switch (r) {
    case 'no_longer_affected':
      return $localize`:@@vuln.resolved.no-longer-affected:The version is no longer affected`;
    case 'advisory_withdrawn':
      return $localize`:@@vuln.resolved.withdrawn:The advisory was withdrawn`;
    case 'version_removed':
      return $localize`:@@vuln.resolved.removed:The version was removed`;
    default:
      return $localize`:@@vuln.resolved.scanning-disabled:Scanning was switched off`;
  }
}

/**
 * What the status column and the status text of a finding say: a version whose download is refused shows as
 * *Blocked* with the reason; otherwise the status. `blocked` is the API's computed value (R-VULN-015), so the
 * report agrees with the download.
 */
export function statusText(f: Pick<VulnerabilityFinding, 'status' | 'blocked' | 'blockReason'>): string {
  if (f.blocked) {
    const reason = f.blockReason ? ` (${blockReasonLabel(f.blockReason)})` : '';
    return `${statusLabel('blocked')}${reason}`;
  }
  return statusLabel(f.status);
}

/** The class that tints a severity word: red for the two top levels, amber for medium, muted below. */
export function severityClass(s: VulnerabilitySeverity): string {
  return `sev-${s}`;
}
