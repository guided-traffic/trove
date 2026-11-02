import { Component, DestroyRef, LOCALE_ID, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { RouterLink } from '@angular/router';
import { CiApi, CiEvaluateResult, CiIssuerChoice, CiLeafResult, CiMapping } from '../../api/ci-api';
import { ApiError } from '../../api/problem';
import { formatDuration } from '../../shared/access/duration';
import { SelectField, SelectOption } from '../../shared/access/fields';
import { asApiError } from '../../shared/paged-resource';
import { CiError, CiWarnings, ciRoutes } from './ci-common';
import { claimsSatisfying, parseClaimsInput, sampleClaims, summarizeGrants, tierLabel } from './ci-model';

const pretty = (v: unknown): string => JSON.stringify(v, null, 2);

/**
 * The claim tester (R-UI-036, R-CI-019): paste the claims of a job as JSON, or a compact ID token, and
 * see which mapping would match and why not the others. A pasted token is decoded here, header and
 * payload only and without any signature check, and at once replaced by its pretty-printed claims: the
 * token is dropped and never kept, and the request carries the claims alone. If the token's `exp` lies
 * in the future the screen says it is still a credential. The claims live in this component's signals;
 * nothing is written to browser storage and everything is gone when the route is left. With a project
 * the dry run covers the project's tier only (Q-CI-26).
 */
@Component({
  selector: 'trove-ci-claim-tester',
  imports: [MatButtonModule, RouterLink, CiError, CiWarnings, SelectField],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@ci.test.heading">Claim tester</h1>
      <a mat-button [routerLink]="back()" [queryParams]="backQuery()" i18n="@@ci.test.back">Back</a>
    </header>
    <p class="subject" translate="no">{{ project() ? project() + ' · ' : '' }}{{ issuerName() }}</p>
    <p i18n="@@ci.test.hint">
      Check which mapping a job would match. This is a dry run: it issues no token and records nothing. Only the claims are sent to the server, never a token.
    </p>

    @if (project() && !issuer()) {
      <trove-select-field i18n-label="@@ci.test.issuer" label="Issuer" name="issuer" [options]="issuerOptions()" [value]="issuerChoice()" (valueChange)="issuerChoice.set($event)" />
    }
    @if (loadError(); as e) {
      <trove-ci-error [error]="e" />
    }

    <section class="card" aria-labelledby="ci-t-input">
      <h2 id="ci-t-input" i18n="@@ci.test.input">Claims</h2>
      <label class="block-label" for="ci-t-claims" i18n="@@ci.test.label">Claims as JSON, or paste an ID token</label>
      <textarea
        id="ci-t-claims"
        class="claims-input"
        rows="16"
        spellcheck="false"
        autocomplete="off"
        [value]="text()"
        [attr.aria-invalid]="inputError() ? 'true' : null"
        [attr.aria-describedby]="describedBy()"
        (input)="edit($any($event.target).value)"
      ></textarea>
      @if (inputError(); as message) {
        <p class="field-error" id="ci-t-error">{{ message }}</p>
      }
      @if (fromToken()) {
        <p class="hint" role="status" id="ci-t-token-note">
          <span class="material-icons" aria-hidden="true">info</span>
          <span i18n="@@ci.test.token-decoded">The pasted token was decoded in your browser and discarded. Only its claims are used, and the signature was not checked.</span>
        </p>
      }
      @if (stillValid()) {
        <p class="hint warn" role="alert" id="ci-t-token-valid">
          <span class="material-icons" aria-hidden="true">warning</span>
          <span i18n="@@ci.test.token-valid">The token's expiry lies in the future, so it is still a valid credential until then. It is not kept here, but remove it from wherever you copied it from, and revoke the job if it was not a test.</span>
        </p>
      }
      <div class="row-actions">
        <button mat-flat-button type="button" [disabled]="busy()" (click)="evaluate()" i18n="@@ci.test.evaluate">Evaluate</button>
        <button mat-stroked-button type="button" (click)="insertSample()" i18n="@@ci.test.sample">Insert sample GitLab claims</button>
      </div>
    </section>

    @if (error(); as e) {
      <trove-ci-error [error]="e" />
    }

    @if (result(); as r) {
      <section class="card" aria-labelledby="ci-t-result" aria-live="polite">
        <h2 id="ci-t-result" i18n="@@ci.test.result">Result</h2>
        @if (r.selected.length === 0) {
          <p class="verdict bad" role="status">
            <span class="material-icons" aria-hidden="true">cancel</span>
            <strong i18n="@@ci.test.no-match">No mapping matches; the exchange would fail.</strong>
          </p>
        } @else if (r.failure) {
          <p class="verdict bad" role="status">
            <span class="material-icons" aria-hidden="true">cancel</span>
            <strong i18n="@@ci.test.would-fail">A mapping matches, but the exchange would still fail.</strong>
          </p>
        } @else {
          <p class="verdict ok" role="status">
            <span class="material-icons" aria-hidden="true">check_circle</span>
            <strong i18n="@@ci.test.match">Matched:</strong>&ngsp;<span translate="no">{{ r.selected.join(', ') }}</span>
          </p>
        }
        @if (r.failure) {
          <p><code translate="no">{{ r.failure }}</code>@if (r.detail) { <span>: {{ r.detail }}</span> }</p>
        }
        @if (r.crossProject) {
          <p class="muted" i18n="@@ci.test.cross">The claims describe a pipeline of another project than the one it runs for (a fork or a merge request from one).</p>
        }

        <h3 i18n="@@ci.test.candidates">Mappings looked at</h3>
        @for (entry of r.trace; track entry.tier + '/' + entry.mapping) {
          <div class="candidate" [class.picked]="entry.mapping === mapping()">
            <p class="candidate-head">
              <strong translate="no">{{ entry.mapping }}</strong>
              <span class="muted">{{ tier(entry.tier) }}</span>
              @if (entry.matched) {
                <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@ci.test.matched">Matched</ng-container></span>
                @if (!entry.contributes) {
                  <span class="warn"><span class="material-icons" aria-hidden="true">warning</span> <ng-container i18n="@@ci.test.not-contributing">but gives nothing: another project's pipeline is refused by this mapping</ng-container></span>
                }
              } @else {
                <span class="bad"><span class="material-icons" aria-hidden="true">cancel</span> <ng-container i18n="@@ci.test.not-matched">Did not match</ng-container></span>
              }
              @if (entry.mapping === mapping()) {
                <span class="badge" i18n="@@ci.test.selected-mapping">The mapping you came from</span>
              }
            </p>
            @if (entry.detail && !entry.matched) {
              <p class="muted" translate="no">{{ entry.detail }}</p>
            }
            @if ((entry.leaves ?? []).length > 0) {
              <div class="scroll">
                <table class="data">
                  <caption class="visually-hidden">{{ leavesCaption(entry.mapping) }}</caption>
                  <thead>
                    <tr>
                      <th scope="col" i18n="@@ci.test.col.condition">Condition</th>
                      <th scope="col" i18n="@@ci.test.col.expected">Expected</th>
                      <th scope="col" i18n="@@ci.test.col.actual">Actual</th>
                      <th scope="col" i18n="@@ci.test.col.result">Result</th>
                    </tr>
                  </thead>
                  <tbody>
                    @for (l of entry.leaves; track l.pointer) {
                      <tr [class.failed]="!l.passed">
                        <th scope="row" translate="no"><code>{{ l.claim }}</code> {{ l.operator }}</th>
                        <td translate="no">{{ l.expected.join(', ') }}</td>
                        <td translate="no">{{ actual(l) }}</td>
                        <td>
                          @if (l.passed) {
                            <span class="ok"><span class="material-icons" aria-hidden="true">check_circle</span> <ng-container i18n="@@ci.test.pass">Passed</ng-container></span>
                          } @else {
                            <span class="bad"><span class="material-icons" aria-hidden="true">cancel</span> <ng-container i18n="@@ci.test.fail">Failed</ng-container></span>
                          }
                        </td>
                      </tr>
                    }
                  </tbody>
                </table>
              </div>
            }
          </div>
        }

        @if (r.grants; as g) {
          <h3 i18n="@@ci.test.grants">Resulting grants</h3>
          <p class="muted" i18n="@@ci.test.grants-hint">Templates are resolved with the claims above.</p>
          <ul class="grant-list" translate="no">
            @for (line of grantLines(g); track $index) {
              <li>{{ line }}</li>
            }
          </ul>
        }
        @if (r.selected.length > 0 && !r.failure) {
          <h3 i18n="@@ci.test.token">The token that would be issued</h3>
          <dl class="facts">
            <dt i18n="@@ci.test.token-type">Type</dt>
            <dd><code translate="no">trv_cit_…</code> <ng-container i18n="@@ci.test.token-type-text">(CI token)</ng-container></dd>
            <dt i18n="@@ci.test.token-scope">Scope</dt>
            <dd translate="no">{{ r.scope || '–' }}</dd>
            <dt i18n="@@ci.test.token-lifetime">Lifetime</dt>
            <dd>{{ r.ttlSeconds ? lifetime(r.ttlSeconds) : '–' }}</dd>
          </dl>
        }
        <trove-ci-warnings [warnings]="r.warnings ?? []" />
      </section>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .block-label {
      display: block;
      margin-block: var(--trove-space-2) var(--trove-space-1);
      font: var(--mat-sys-label-large);
    }
    .claims-input {
      box-sizing: border-box;
      inline-size: 100%;
      padding: var(--trove-space-2);
      border: 1px solid var(--trove-border-strong);
      border-radius: var(--trove-radius-s);
      background: var(--trove-surface-raised);
      color: var(--mat-sys-on-surface);
      font-family: var(--trove-font-mono);
      font-size: 0.8125rem;
    }
    .hint {
      display: flex;
      gap: var(--trove-space-2);
      align-items: flex-start;
      margin-block: var(--trove-space-2);
      padding: var(--trove-space-2) var(--trove-space-3);
      border: 1px solid var(--trove-border-strong);
      border-radius: var(--trove-radius-s);
    }
    .hint.warn {
      border-color: var(--trove-status-warn);
    }
    .verdict {
      display: flex;
      gap: var(--trove-space-2);
      align-items: center;
      font: var(--mat-sys-title-medium);
    }
    h3 {
      margin: var(--trove-space-4) 0 var(--trove-space-2);
      font: var(--mat-sys-title-small);
    }
    .candidate {
      margin-block: var(--trove-space-2);
      padding: var(--trove-space-2) var(--trove-space-3);
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-m);
    }
    .candidate.picked {
      border-color: var(--trove-accent);
    }
    .candidate-head {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-3);
      align-items: center;
      margin: 0;
    }
    tr.failed th,
    tr.failed td {
      background: color-mix(in srgb, var(--trove-status-bad) 8%, transparent);
    }
    .grant-list {
      margin: 0;
      padding-inline-start: var(--trove-space-4);
    }
  `,
})
export class ClaimTester {
  /** Route parameter `:issuer`, or the query parameter of a project's tester. */
  readonly issuer = input<string | undefined>();
  /** Route parameter `:key`: the project whose tier is tested. */
  readonly key = input<string | undefined>();
  /** Query parameter `mapping`: the mapping the tester was opened from; its candidate is marked and the claims start out matching it. */
  readonly mapping = input<string | undefined>();

  private readonly api = inject(CiApi);
  private readonly locale = inject(LOCALE_ID);

  protected readonly project = computed(() => this.key() ?? null);
  protected readonly issuerChoice = signal('');
  protected readonly issuerName = computed(() => this.issuer() || this.issuerChoice());
  protected readonly issuerOptions = signal<readonly SelectOption[]>([]);
  private choices: readonly CiIssuerChoice[] = [];
  private issuerUrl = '';
  private audience = '';

  /** The claims text. It is a signal of this component only: no store, no browser storage (R-UI-036). */
  protected readonly text = signal('');
  protected readonly fromToken = signal(false);
  protected readonly stillValid = signal(false);
  protected readonly inputError = signal<string | null>(null);
  protected readonly result = signal<CiEvaluateResult | null>(null);
  protected readonly error = signal<ApiError | null>(null);
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly busy = signal(false);

  protected readonly tier = tierLabel;
  protected readonly describedBy = computed(() =>
    [this.inputError() ? 'ci-t-error' : null, this.fromToken() ? 'ci-t-token-note' : null].filter(Boolean).join(' ') || null,
  );

  constructor() {
    const ref = inject(DestroyRef);
    // The claims are gone when the route is left (R-UI-036).
    ref.onDestroy(() => {
      this.text.set('');
      this.result.set(null);
    });
    effect(() => {
      this.issuer();
      this.key();
      this.mapping();
      this.issuerChoice();
      untracked(() => void this.prepare());
    });
  }

  protected back(): string[] {
    return this.mapping() && this.issuerName()
      ? ciRoutes.mapping(this.project(), this.issuerName(), this.mapping()!)
      : ciRoutes.overview(this.project(), this.issuerName() || null);
  }

  protected backQuery(): Record<string, string> | null {
    return this.mapping() ? ciRoutes.mappingQuery(this.project(), this.issuerName()) : null;
  }

  /** Reads what the sample needs (the issuer's URL and audience) and, when a mapping is named, the mapping, so the claims start out matching it. */
  private async prepare(): Promise<void> {
    this.loadError.set(null);
    const project = this.project();
    try {
      if (project) {
        this.choices = await this.api.projectIssuers(project);
        this.issuerOptions.set([{ value: '', label: '' }, ...this.choices.map((c) => ({ value: c.name, label: c.name }))]);
        const chosen = this.choices.find((c) => c.name === this.issuerName());
        this.issuerUrl = chosen?.issuer ?? '';
        this.audience = document.location.origin;
      } else if (this.issuer()) {
        const i = (await this.api.getIssuer(this.issuer()!)).value;
        this.issuerUrl = i.issuer;
        this.audience = i.allowedAudiences[0] ?? '';
      }
      if (this.mapping() && this.issuerName() && this.text() === '') {
        const m: CiMapping = (await this.api.getMapping(this.issuerName(), this.mapping()!, project)).value;
        this.text.set(pretty(claimsSatisfying(this.sample(), m.match)));
      }
    } catch (e) {
      this.loadError.set(asApiError(e));
    }
  }

  private sample(): Record<string, unknown> {
    return sampleClaims(this.issuerUrl, this.audience, Math.floor(Date.now() / 1000));
  }

  protected insertSample(): void {
    this.fromToken.set(false);
    this.stillValid.set(false);
    this.inputError.set(null);
    this.text.set(pretty(this.sample()));
  }

  /**
   * Every change of the text. A compact token is decoded and replaced by its claims in the same step, so
   * the token itself is never held (R-UI-036); anything else stays as typed.
   */
  protected edit(value: string): void {
    const parsed = parseClaimsInput(value, Math.floor(Date.now() / 1000));
    if (parsed.kind === 'jwt') {
      this.text.set(pretty(parsed.claims));
      this.fromToken.set(true);
      this.stillValid.set(parsed.stillValid);
      this.inputError.set(null);
      return;
    }
    this.text.set(value);
    this.fromToken.set(false);
    this.stillValid.set(false);
    this.inputError.set(null);
  }

  private problemText(reason: 'empty' | 'not-json' | 'not-object' | 'bad-token'): string {
    switch (reason) {
      case 'empty':
        return $localize`:@@ci.test.err.empty:Enter the claims as JSON, or paste an ID token.`;
      case 'not-object':
        return $localize`:@@ci.test.err.not-object:The claims must be a JSON object.`;
      case 'bad-token':
        return $localize`:@@ci.test.err.bad-token:This looks like a token, but its header or payload is not valid.`;
      default:
        return $localize`:@@ci.test.err.not-json:This is neither a JSON object nor an ID token.`;
    }
  }

  protected async evaluate(): Promise<void> {
    const parsed = parseClaimsInput(this.text(), Math.floor(Date.now() / 1000));
    if (parsed.kind === 'error') {
      this.inputError.set(this.problemText(parsed.reason));
      return;
    }
    if (!this.issuerName()) {
      this.inputError.set($localize`:@@ci.test.err.issuer:Choose an issuer first.`);
      return;
    }
    this.busy.set(true);
    this.error.set(null);
    this.result.set(null);
    try {
      // The request holds the claims and nothing else: not the token, not its signature.
      this.result.set(await this.api.evaluate(this.issuerName(), { subjectKind: 'id_token', claims: parsed.claims }, this.project()));
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected grantLines = summarizeGrants;
  protected lifetime = (seconds: number): string => formatDuration(seconds, this.locale);
  protected actual = (l: CiLeafResult): string => (l.absent ? $localize`:@@ci.test.absent:(absent)` : l.actual.join(', '));
  protected leavesCaption = (name: string): string => $localize`:@@ci.test.leaves-caption:Conditions of ${name}:name:`;
}
