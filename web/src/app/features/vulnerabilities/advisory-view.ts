import { DatePipe } from '@angular/common';
import { Component, computed, input } from '@angular/core';
import { VulnerabilityAdvisory } from '../../api/vulnerabilities-api';
import { EXTERNAL_REL, safeHttpUrl } from '../packages/package-links';
import { kindLabel } from './vuln-labels';
import { SeverityBadge } from './vuln-parts';

/** One affected entry reduced to a line: `name: introduced X, fixed Y` per range, or the listed versions. */
export function affectedLines(advisory: Pick<VulnerabilityAdvisory, 'affected'>): string[] {
  return advisory.affected.map((a) => {
    const parts: string[] = [];
    for (const r of a.ranges) {
      const events = r.events.map((e) => Object.entries(e).map(([k, v]) => `${k.replace('_', ' ')} ${v}`).join(' ')).join(', ');
      parts.push(`${r.type}: ${events}`);
    }
    if (a.versions.length > 0) parts.push(a.versions.join(', '));
    return `${a.name}: ${parts.join('; ')}`;
  });
}

/** A reference of an advisory: a link only for an http or https address, else its text (R-VULN-022). */
export interface ReferenceLink {
  readonly type: string;
  readonly text: string;
  readonly href: string | null;
}

export function referenceLinks(advisory: Pick<VulnerabilityAdvisory, 'references'>): ReferenceLink[] {
  return advisory.references.map((r) => ({ type: r.type ?? '', text: r.url, href: safeHttpUrl(r.url) }));
}

/**
 * An advisory as the finding and the advisory page show it (R-VULN-022): identifier and aliases, severity with its
 * vector, the known-exploited date, CWEs, affected ranges, fixed versions, the references as external links, and the
 * source and license line of R-VULN-003.
 *
 * The advisory's `details` are Markdown from an untrusted source (R-VULN-006). The API carries no rendered form, so the
 * text is shown as plain text, escaped by the template and with its line breaks kept: no HTML is built from it and no
 * link in it is made live (Q-VULN-52). A reference is a link only for an http or https address, with
 * `rel="nofollow noopener noreferrer"` and no `target`.
 */
@Component({
  selector: 'trove-advisory',
  preserveWhitespaces: true,
  imports: [DatePipe, SeverityBadge],
  template: `
    @let a = advisory();
    <section class="advisory" [attr.aria-label]="a.id">
      <dl class="facts">
        <dt i18n="@@vuln.adv.identifier">Identifier</dt>
        <dd translate="no">{{ a.id }}</dd>
        @if (a.aliases.length > 0) {
          <dt i18n="@@vuln.adv.aliases">Aliases</dt>
          <dd translate="no">{{ a.aliases.join(', ') }}</dd>
        }
        <dt i18n="@@vuln.adv.kind">Kind</dt>
        <dd>{{ kind() }}</dd>
        <dt i18n="@@vuln.adv.severity">Severity</dt>
        <dd>
          <trove-severity [severity]="a.severity" [score]="a.cvss?.score ?? null" />
          @if (a.cvss; as c) {
            <span class="secondary" translate="no">CVSS {{ c.version }} {{ c.vector }}</span>
          }
        </dd>
        @if (a.knownExploited) {
          <dt i18n="@@vuln.adv.exploited">Known exploited</dt>
          <dd>
            <span class="material-icons inline bad" aria-hidden="true">local_fire_department</span>
            <ng-container i18n="@@vuln.adv.exploited-yes">Yes, on the CISA known-exploited list</ng-container>
            @if (a.knownExploitedAddedAt) {
              <ng-container i18n="@@vuln.adv.exploited-since">since {{ a.knownExploitedAddedAt }}</ng-container>
            }
          </dd>
        }
        @if (a.cwe.length > 0) {
          <dt i18n="@@vuln.adv.cwe">Weaknesses</dt>
          <dd translate="no">{{ a.cwe.join(', ') }}</dd>
        }
        @if (a.published) {
          <dt i18n="@@vuln.adv.published">Published</dt>
          <dd>
            <time [attr.datetime]="a.published">{{ a.published | date: 'medium' }}</time>
          </dd>
        }
        <dt i18n="@@vuln.adv.modified">Modified</dt>
        <dd>
          <time [attr.datetime]="a.modified">{{ a.modified | date: 'medium' }}</time>
        </dd>
        @if (a.withdrawn) {
          <dt i18n="@@vuln.adv.withdrawn">Withdrawn</dt>
          <dd>
            <time [attr.datetime]="a.withdrawn">{{ a.withdrawn | date: 'medium' }}</time>
          </dd>
        }
        <dt i18n="@@vuln.adv.fixed">Fixed in</dt>
        <dd translate="no">{{ a.fixedVersions.length > 0 ? a.fixedVersions.join(', ') : '–' }}</dd>
      </dl>

      <h3 translate="no">{{ a.summary }}</h3>
      @if (a.details) {
        <div class="advisory-text" translate="no">{{ a.details }}</div>
      }

      @if (affected().length > 0) {
        <h3 i18n="@@vuln.adv.affected">Affected</h3>
        <ul class="plain" translate="no">
          @for (line of affected(); track $index) {
            <li>{{ line }}</li>
          }
        </ul>
      }

      @if (references().length > 0) {
        <h3 i18n="@@vuln.adv.references">References</h3>
        <ul class="plain">
          @for (r of references(); track $index) {
            <li translate="no">
              @if (r.type) {
                <span class="secondary-inline">{{ r.type }}</span>
              }
              @if (r.href; as href) {
                <a [href]="href" [attr.rel]="rel" target="_blank">{{ r.text }}</a>
                <span class="material-icons inline" aria-hidden="true">open_in_new</span>
                <span class="visually-hidden" i18n="@@vuln.adv.external">(opens another site)</span>
              } @else {
                {{ r.text }}
              }
            </li>
          }
        </ul>
      }

      <p class="vuln-source">
        <ng-container i18n="@@vuln.adv.source">Source:</ng-container>
        <span translate="no">{{ a.source.name }}</span>
        @if (a.source.license) {
          <ng-container i18n="@@vuln.adv.license">, licensed under</ng-container>
          <span translate="no">{{ a.source.license }}</span>
        }
        @if (sourceHref(); as href) {
          (<a [href]="href" [attr.rel]="rel" target="_blank" translate="no">{{ a.source.url }}</a>)
        }
      </p>
    </section>
  `,
})
export class AdvisoryView {
  readonly advisory = input.required<VulnerabilityAdvisory>();

  protected readonly rel = EXTERNAL_REL;
  protected readonly kind = computed(() => kindLabel(this.advisory().kind));
  protected readonly affected = computed(() => affectedLines(this.advisory()));
  protected readonly references = computed(() => referenceLinks(this.advisory()));
  protected readonly sourceHref = computed(() => safeHttpUrl(this.advisory().source.url));
}
