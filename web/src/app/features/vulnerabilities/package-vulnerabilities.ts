import { DatePipe } from '@angular/common';
import { Component, input } from '@angular/core';
import { RouterLink } from '@angular/router';
import {
  SEVERITIES,
  VulnerabilityFinding,
  VulnerabilityPackageReport,
  VulnerabilitySeverity,
  VulnerabilityVersionReport,
} from '../../api/vulnerabilities-api';
import { blockReasonLabel, evaluationLabel, isOpen } from './vuln-labels';
import { FindingStatus, SeverityBadge } from './vuln-parts';

/** The open findings of a version report, across the repositories (R-VULN-023: the count on the tab). */
export function openFindings(report: VulnerabilityVersionReport | null): VulnerabilityFinding[] {
  return (report?.repositories ?? []).flatMap((r) => r.findings).filter((f) => isOpen(f.status));
}

/** The repositories a version report names as blocking the download (the header's "Blocked in …"). */
export function blockedRepositories(report: VulnerabilityVersionReport | null): string[] {
  return (report?.repositories ?? []).filter((r) => r.blocked).map((r) => r.repository);
}

/** The highest severity with an open finding in the counts of one version, or null (the badge of "All versions"). */
export function highestOpen(open: VulnerabilityPackageReport['versions'][number]['open'] | undefined): VulnerabilitySeverity | null {
  if (!open) return null;
  return SEVERITIES.find((s) => open[s] > 0) ?? null;
}

/**
 * The *Vulnerabilities* tab of the package page (R-VULN-023): per readable repository that holds the selected version its
 * evaluation state, a block with its reason (*malicious package*, or *blocked in triage* with the finding that blocks), and
 * the findings with severity, identifier, status and fixed versions, each linking to the finding. The report is the API's
 * (`GET …/versions/{version}/vulnerabilities`); the page shows nothing it does not carry.
 */
@Component({
  selector: 'trove-package-vulnerabilities',
  preserveWhitespaces: true,
  imports: [DatePipe, RouterLink, FindingStatus, SeverityBadge],
  template: `
    @if (report(); as r) {
      @if (r.repositories.length === 0) {
        <p class="empty" i18n="@@pkg.vuln.none-repositories">No repository you can read holds this version.</p>
      }
      @for (repo of r.repositories; track repo.repository) {
        <section class="vuln-repo" [attr.aria-labelledby]="'vuln-repo-' + $index">
          <h2 [id]="'vuln-repo-' + $index" translate="no">{{ repo.repository }}</h2>
          <p class="vuln-evaluation">
            <span class="material-icons inline" aria-hidden="true">{{ repo.evaluation === 'evaluated' ? 'verified_user' : 'info' }}</span>
            {{ evaluation(repo.evaluation) }}
            @if (repo.evaluation === 'evaluated' && repo.syncedAt) {
              <span class="secondary-inline">
                <ng-container i18n="@@pkg.vuln.data-from">Advisory data from</ng-container>
                <time [attr.datetime]="repo.syncedAt">{{ repo.syncedAt | date: 'medium' }}</time>
              </span>
            }
          </p>
          @if (repo.blocked) {
            <p class="banner-warning" role="note">
              <span class="material-icons inline warn" aria-hidden="true">block</span>
              <span>
                <strong i18n="@@pkg.vuln.blocked">Blocked.</strong>
                <ng-container i18n="@@pkg.vuln.blocked-reason">Downloads of this version are refused. Reason:</ng-container>
                {{ repo.blockReason ? reason(repo.blockReason) : '' }}.
                @for (f of blocking(repo.findings); track f.id) {
                  <a [routerLink]="['/vulnerabilities', 'findings', f.id]" translate="no">{{ f.advisory }}</a>
                }
              </span>
            </p>
          }
          @if (repo.evaluation === 'evaluated' && repo.findings.length === 0) {
            <p class="vuln-clean">
              <span class="material-icons inline ok" aria-hidden="true">check_circle</span>
              <ng-container i18n="@@pkg.vuln.clean">No known vulnerabilities.</ng-container>
            </p>
          }
          @if (repo.findings.length > 0) {
            <div class="scroll" tabindex="0" role="region" [attr.aria-label]="repo.repository">
              <table class="data">
                <caption class="visually-hidden" i18n="@@pkg.vuln.caption">Findings of this version</caption>
                <thead>
                  <tr>
                    <th scope="col" i18n="@@vuln.col.severity">Severity</th>
                    <th scope="col" i18n="@@vuln.col.identifier">Identifier</th>
                    <th scope="col" i18n="@@vuln.col.status">Status</th>
                    <th scope="col" i18n="@@pkg.vuln.col-fixed">Fixed in</th>
                  </tr>
                </thead>
                <tbody>
                  @for (f of repo.findings; track f.id) {
                    <tr>
                      <td><trove-severity [severity]="f.severity" [score]="f.cvss?.score ?? null" /></td>
                      <th scope="row">
                        <a [routerLink]="['/vulnerabilities', 'findings', f.id]" translate="no">{{ f.advisory }}</a>
                        <span class="secondary">{{ f.summary }}</span>
                      </th>
                      <td><trove-finding-status [finding]="f" /></td>
                      <td translate="no">{{ f.fixedVersions.length > 0 ? f.fixedVersions.join(', ') : '–' }}</td>
                    </tr>
                  }
                </tbody>
              </table>
            </div>
          }
        </section>
      }
    } @else {
      <p class="empty" i18n="@@pkg.vuln.unavailable">The vulnerability report of this version is not available.</p>
    }
  `,
})
export class PackageVulnerabilities {
  /** The report of the selected version, or null while it is not available. */
  readonly report = input<VulnerabilityVersionReport | null>(null);

  protected evaluation = evaluationLabel;
  protected reason = blockReasonLabel;

  /** The findings that block the download: the ones whose own block is computed. */
  protected blocking(findings: readonly VulnerabilityFinding[]): VulnerabilityFinding[] {
    return findings.filter((f) => f.blocked);
  }
}
