import { Component, input } from '@angular/core';
import { RouterLink } from '@angular/router';
import { VulnerabilityFinding, VulnerabilityVersionReport } from '../../api/vulnerabilities-api';
import { GradeCard } from '../../shared/vulnerability/grade-card';
import { BAR_SEVERITIES, BarQueries, BarSeverity } from '../../shared/vulnerability/severity-bar';
import { VersionFindings } from './version-findings';
import { blockReasonLabel } from './vuln-labels';

/** The query of each segment of the card's bar: the tab's severity filter, merged into the page's address (Q-UI-379). */
const BAR_QUERIES = Object.fromEntries(BAR_SEVERITIES.map((s) => [s, { severity: s }])) as unknown as BarQueries;

/** The severity filter of the address, one of the bar's five; anything else is no filter. */
export function barSeverityOf(raw: string | null | undefined): BarSeverity | null {
  return (BAR_SEVERITIES as readonly string[]).includes(raw ?? '') ? (raw as BarSeverity) : null;
}

/**
 * The open findings of a version report for the tab's label (R-VULN-023), from the grades' counts of every repository: the
 * listed findings stop at 1,000 per repository, the counts do not.
 */
export function openCount(report: VulnerabilityVersionReport | null): number {
  return (report?.repositories ?? []).reduce(
    (sum, r) => sum + r.grade.open.critical + r.grade.open.high + r.grade.open.medium + r.grade.open.low + r.grade.open.none + r.grade.open.unknown,
    0,
  );
}

/** The repositories a version report names as blocking the download (the header's "Blocked in …"). */
export function blockedRepositories(report: VulnerabilityVersionReport | null): string[] {
  return (report?.repositories ?? []).filter((r) => r.blocked).map((r) => r.repository);
}

/**
 * The *Vulnerabilities* tab of the package page (R-VULN-023): per readable repository that holds the selected version its grade
 * card (R-UI-093: the grade, its rule, the counts as a bar, when it was checked; N/A with why it is not evaluated), a block with
 * its reason (*malicious package*, or *blocked in triage* with the finding that blocks), and the open findings with severity,
 * identifier, status and fixed versions, each linking to the finding, 25 at a time from the findings API and narrowed by the bar's
 * segments (`<trove-version-findings>`, Q-UI-379). The report is the API's (`GET …/versions/{version}/vulnerabilities`); the page
 * shows nothing it does not carry.
 */
@Component({
  selector: 'trove-package-vulnerabilities',
  preserveWhitespaces: true,
  imports: [RouterLink, GradeCard, VersionFindings],
  template: `
    @if (report(); as r) {
      @if (r.repositories.length === 0) {
        <p class="empty" i18n="@@pkg.vuln.none-repositories">No repository you can read holds this version.</p>
      }
      @for (repo of r.repositories; track repo.repository) {
        <section class="vuln-repo" [attr.aria-labelledby]="'vuln-repo-' + $index">
          <h2 [id]="'vuln-repo-' + $index" translate="no">{{ repo.repository }}</h2>
          <trove-grade-card
            class="vuln-grade"
            [grade]="repo.grade"
            [packageName]="packageName()"
            [findingVersions]="findingVersions(repo, r.version)"
            [image]="repo.images !== undefined"
            [barQueries]="barQueries"
            [activeSeverity]="severity()"
          />
          @if (repo.blocked) {
            <p class="banner-warning" role="note">
              <span class="material-icons inline warn" aria-hidden="true">block</span>
              <span>
                <strong i18n="@@pkg.vuln.blocked">Blocked.</strong>
                <ng-container i18n="@@pkg.vuln.blocked-reason">Downloads of this version are refused. Reason:</ng-container>
                {{ repo.blockReason ? reason(repo.blockReason) : '' }}.
                @for (f of blocking(repo.findings); track f.id) {
                  <a [routerLink]="['/projects', f.project, 'vulnerabilities', 'findings', f.id]" translate="no">{{ f.advisory }}</a>
                }
              </span>
            </p>
          }
          @if (repo.grade.grade !== 'N/A' && openCount({ packageId: r.packageId, version: r.version, repositories: [repo] }) > 0) {
            <trove-version-findings
              [repository]="repo.repository"
              [packageName]="packageName()"
              [versions]="findingVersions(repo, r.version)"
              [counts]="repo.grade.open"
              [severity]="severity()"
            />
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
  /** The package's name, for the link of the grade card to the findings. */
  readonly packageName = input('');
  /** The severity the card's bar narrowed the findings to (the address's `severity`), or null. */
  readonly severity = input<BarSeverity | null>(null);

  protected reason = blockReasonLabel;
  protected readonly openCount = openCount;
  protected readonly barQueries = BAR_QUERIES;

  /** The versions the findings of the version name in a repository: the version, or an image's manifest digests (R-VULN-037). */
  protected findingVersions(repo: VulnerabilityVersionReport['repositories'][number], version: string): readonly string[] {
    return repo.images?.length ? repo.images.map((m) => m.digest) : [version];
  }

  /** The findings that block the download: the ones whose own block is computed. */
  protected blocking(findings: readonly VulnerabilityFinding[]): VulnerabilityFinding[] {
    return findings.filter((f) => f.blocked);
  }
}
