import { DatePipe } from '@angular/common';
import { Component, LOCALE_ID, computed, inject, input, model, signal } from '@angular/core';
import { ApiError, FieldProblem } from '../../api/problem';
import {
  FormatDescriptor,
  RepositoriesApi,
  UpstreamPreset,
  UpstreamTestResult,
} from '../../api/repositories-api';
import { ErrorNotice } from '../../shared/error-notice';
import { fieldMessage, problemsAt } from '../../shared/field-errors';
import { asApiError } from '../../shared/paged-resource';
import { RepositoryForm } from './repository-model';
import {
  UpstreamAuthMode,
  UpstreamForm,
  UpstreamHost,
  credentialHosts,
  effectiveUrl,
  normalizedUrl,
  shownHosts,
  upstreamDefaultsOf,
  upstreamTestBody,
} from './upstream-model';
import { controlIdOf, numberOrText } from './settings-schema';

/** The id of the section; a server error at a pointer of the upstream settings that has no control lands here. */
export const UPSTREAM_SECTION_ID = 'f-remote-upstream';

const MEMBER_POINTERS = [
  '/remote/url',
  '/remote/authMode',
  '/remote/username',
  '/remote/password',
  '/remote/extraHosts',
  '/remote/cache/metadataTtlSeconds',
  '/remote/cache/notFoundTtlSeconds',
  '/remote/availability/mode',
];

/** The pointers whose controls sit in the region Upstream details: an error at one of them opens it (R-UI-083). */
const DETAIL_POINTERS = ['/remote/extraHosts', '/remote/cache', '/remote/availability'];

/**
 * The control a field error of the upstream settings belongs to (`/remote/url`, `/remote/password/value`),
 * or null for a pointer that is not theirs (the mode and the mirror members are the Mirror section's).
 */
export function upstreamTarget(pointer: string): string | null {
  if (pointer === '/remote' || pointer.startsWith('/remote/mirror') || pointer === '/remote/mode') {
    return null;
  }
  if (!pointer.startsWith('/remote/')) return null;
  const known = MEMBER_POINTERS.find((m) => pointer === m || pointer.startsWith(`${m}/`));
  return known ? controlIdOf(known) : UPSTREAM_SECTION_ID;
}

/** The label a summary shows for a field error of the upstream settings, or null. */
export function upstreamPointerLabel(pointer: string): string | null {
  if (upstreamTarget(pointer) === null) return null;
  if (pointer.startsWith('/remote/url')) return $localize`:@@upstream.url:Upstream URL`;
  if (pointer.startsWith('/remote/authMode')) return $localize`:@@upstream.auth:Authentication`;
  if (pointer.startsWith('/remote/username')) return $localize`:@@upstream.username:Username`;
  if (pointer.startsWith('/remote/password')) return $localize`:@@upstream.password:Password or token`;
  if (pointer.startsWith('/remote/extraHosts')) return $localize`:@@upstream.hosts:Further hosts`;
  if (pointer.startsWith('/remote/cache/metadataTtlSeconds')) {
    return $localize`:@@upstream.metadata-ttl:Metadata cache period in seconds`;
  }
  if (pointer.startsWith('/remote/cache/notFoundTtlSeconds')) {
    return $localize`:@@upstream.notfound-ttl:Negative cache period in seconds`;
  }
  if (pointer.startsWith('/remote/availability')) return $localize`:@@upstream.offline:Offline`;
  return $localize`:@@upstream.heading:Upstream`;
}

/**
 * The upstream of a remote repository (R-UI-082, R-UI-084, R-UI-085, R-REPO-024, R-REPO-033, R-REPO-062, R-REPO-064): a button per
 * well-known registry of the format, the URL starting with the default one, the credentials (the password is write-only: sent
 * when typed, never read back into the field) with the hosts that receive them named under it, **Test connection** right there,
 * and the closed region **Upstream details** with the further hosts, the cache periods as values and, on the settings page,
 * Offline. A test that finds the registry's token service on a host the repository may not reach offers to allow that host.
 * What the section writes is `upstreamOf` of upstream-model.ts: on a create only what differs from the defaults (Q-UI-80).
 */
@Component({
  selector: 'trove-upstream-settings',
  imports: [DatePipe, ErrorNotice],
  template: `
    <section class="upstream" [id]="sectionId" tabindex="-1" aria-labelledby="upstream-heading">
      <h2 id="upstream-heading" i18n="@@upstream.heading">Upstream</h2>
      <p class="hint" i18n="@@upstream.intro.registry">The registry this repository fetches from.</p>

      <div class="upstream-grid">
        @if (presets().length > 0) {
          <div class="field" role="group" aria-labelledby="upstream-presets-label">
            <span id="upstream-presets-label" class="field-label" i18n="@@upstream.presets">Well-known registries</span>
            <div class="presets">
              @for (p of presets(); track p.id) {
                <button
                  class="btn btn-default preset"
                  type="button"
                  translate="no"
                  [attr.aria-pressed]="pressed(p)"
                  (click)="choose(p)"
                >
                  {{ p.displayName }}
                </button>
              }
            </div>
          </div>
        }

        <div class="field">
          <label class="field-label" for="f-remote-url" i18n="@@upstream.url">Upstream URL</label>
          <input
            id="f-remote-url"
            class="text-input field-w-full"
            type="text"
            inputmode="url"
            autocomplete="off"
            spellcheck="false"
            [value]="upstream().url"
            [attr.aria-invalid]="urlError() ? 'true' : invalid('/remote/url')"
            aria-describedby="d-remote-url"
            (input)="set({ url: $any($event.target).value })"
            (blur)="leaveUrl()"
          />
          <p id="d-remote-url" class="hint">
            @if (presets().length > 0) {
              <ng-container i18n="@@upstream.url.hint-presets">Choose a well-known registry or enter the address of another.</ng-container>
            } @else {
              <ng-container i18n="@@upstream.url.hint-required"
                >This format has no well-known upstream: enter the address of the registry.</ng-container
              >
            }
          </p>
          @if (urlError()) {
            <p class="field-error" i18n="@@upstream.url.required">Enter the address of the registry, such as https://registry.example.com/.</p>
          }
          @for (e of problems('/remote/url'); track $index) {
            <p class="field-error">{{ message(e) }}</p>
          }
        </div>

        <div class="field">
          <label class="field-label" for="f-remote-authMode" i18n="@@upstream.auth">Authentication</label>
          <select
            id="f-remote-authMode"
            class="select-input field-w-name"
            [attr.aria-invalid]="invalid('/remote/authMode')"
            (change)="set({ authMode: $any($event.target).value })"
          >
            @for (m of authModes; track m) {
              <option [value]="m" [selected]="upstream().authMode === m">{{ authLabel(m) }}</option>
            }
          </select>
          @for (e of problems('/remote/authMode'); track $index) {
            <p class="field-error">{{ message(e) }}</p>
          }
        </div>

        @if (upstream().authMode === 'basic') {
          <div class="field">
            <label class="field-label" for="f-remote-username" i18n="@@upstream.username">Username</label>
            <input
              id="f-remote-username"
              class="text-input field-w-name"
              type="text"
              autocomplete="off"
              spellcheck="false"
              [value]="upstream().username"
              [attr.aria-invalid]="invalid('/remote/username')"
              (input)="set({ username: $any($event.target).value })"
            />
            @for (e of problems('/remote/username'); track $index) {
              <p class="field-error">{{ message(e) }}</p>
            }
          </div>
        }

        @if (upstream().authMode !== 'none') {
          <div class="field">
            <label class="field-label" for="f-remote-password" i18n="@@upstream.password">Password or token</label>
            <input
              id="f-remote-password"
              class="text-input field-w-name"
              type="password"
              autocomplete="new-password"
              spellcheck="false"
              [value]="upstream().password"
              [attr.aria-invalid]="invalid('/remote/password')"
              aria-describedby="d-remote-password d-remote-credential-hosts"
              (input)="set({ password: $any($event.target).value })"
            />
            <p id="d-remote-password" class="hint">
              @switch (upstream().passwordStored) {
                @case ('sealed') {
                  <ng-container i18n="@@upstream.password.stored"
                    >A password is stored and is not shown. Leave the field empty to keep it; type one to replace it.</ng-container
                  >
                }
                @case ('reference') {
                  <ng-container i18n="@@upstream.password.reference"
                    >A reference to a secret file or variable is stored. Leave the field empty to keep it; type a password to replace it.</ng-container
                  >
                }
                @default {
                  <ng-container i18n="@@upstream.password.hint"
                    >Sealed when you save, then never shown again. With bearer authentication this is the token.</ng-container
                  >
                }
              }
            </p>
            <p id="d-remote-credential-hosts" class="hint credential-hosts">{{ credentialLine() }}</p>
            @if (upstream().passwordStored !== 'none') {
              <label class="check-row" for="f-remote-clearPassword">
                <input
                  id="f-remote-clearPassword"
                  type="checkbox"
                  [checked]="upstream().clearPassword"
                  (change)="set({ clearPassword: $any($event.target).checked })"
                />
                <span i18n="@@upstream.password.clear">Remove the stored password</span>
              </label>
            }
            @for (e of problems('/remote/password'); track $index) {
              <p class="field-error">{{ message(e) }}</p>
            }
          </div>
        }
      </div>

      <div class="test">
        <button class="btn btn-default" type="button" [disabled]="testing()" (click)="test()">
          <ng-container i18n="@@upstream.test">Test connection</ng-container>
        </button>
        @if (upstream().passwordStored !== 'none' && upstream().password === '') {
          <span class="hint" i18n="@@upstream.test.stored"
            >The test does not use the stored password; type it to test with it.</span
          >
        }
      </div>
      <div class="test-result" role="status" aria-live="polite">
        @if (testing()) {
          <span i18n="@@upstream.test.running">Testing the connection.</span>
        }
        @if (failure(); as f) {
          <trove-error-notice [error]="f" />
        }
        @if (outcome(); as o) {
          <p class="verdict" [class.ok]="o.good" [class.warn]="!o.good" data-testid="upstream-test-result">
            <span class="material-icons inline" aria-hidden="true">{{ o.good ? 'check_circle' : 'warning' }}</span>
            <strong>{{ o.summary }}</strong>
          </p>
          @if (foreignTokenHost(); as host) {
            <div class="allow">
              <p i18n="@@upstream.test.foreign">
                The registry asks for a login at <code translate="no">{{ host }}</code>, which this repository may not reach.
                Allow it as a further host if it is the registry's own token service.
              </p>
              <button class="btn btn-default" type="button" data-testid="upstream-allow-host" (click)="allow(host)">
                <ng-container i18n="@@upstream.test.allow">Allow</ng-container>&ngsp;<span translate="no">{{ host }}</span>
              </button>
            </div>
          }
          <dl class="facts">
            @if (o.result.httpStatus !== null) {
              <dt i18n="@@upstream.test.status">HTTP status</dt>
              <dd>{{ o.result.httpStatus }}</dd>
            }
            @if (o.result.authenticated !== null) {
              <dt i18n="@@upstream.test.credentials">Credentials</dt>
              <dd>{{ o.result.authenticated ? accepted : refused }}</dd>
            }
            @if (o.result.tokenService; as ts) {
              <dt i18n="@@upstream.test.token-service">Token service</dt>
              <dd>
                <span translate="no">{{ ts.host }}</span>,&ngsp;{{ tokenServiceState(ts) }}
              </dd>
            }
            <dt i18n="@@upstream.test.duration">Duration</dt>
            <dd>{{ o.result.durationMs }} ms</dd>
            @if (o.result.tls; as tls) {
              <dt i18n="@@upstream.test.tls">Certificate</dt>
              <dd>
                <span translate="no">{{ tls.peerSubject }}</span>,&ngsp;<ng-container i18n="@@upstream.test.valid-until">valid until</ng-container>&ngsp;<time [attr.datetime]="tls.notAfter">{{ tls.notAfter | date: 'mediumDate' }}</time>
              </dd>
            }
            @if (o.result.error; as err) {
              <dt i18n="@@upstream.test.error">Reason</dt>
              <dd>
                {{ err.message }} <code translate="no">{{ err.code }}</code>
              </dd>
            }
          </dl>
        }
      </div>

      <button
        class="details-toggle"
        type="button"
        aria-controls="upstream-details"
        [attr.aria-expanded]="detailsOpen()"
        (click)="detailsOpened.set(!detailsOpen())"
      >
        <span class="material-icons chevron" aria-hidden="true">chevron_right</span>
        <ng-container i18n="@@upstream.details">Upstream details</ng-container>
      </button>
      <div id="upstream-details" class="collapse-region" [class.open]="detailsOpen()">
        <div>
          <div class="upstream-grid details">
            <div class="field" role="group" aria-labelledby="upstream-hosts-label" [id]="hostsId">
              <span id="upstream-hosts-label" class="field-label" i18n="@@upstream.hosts">Further hosts</span>
              <p class="hint" i18n="@@upstream.hosts.hint">
                Hosts the upstream's documents and its token service may lead to. Only a host that receives the credential gets it.
              </p>
              @for (h of hosts(); track $index; let i = $index) {
                <div class="host-row">
                  <label class="visually-hidden" [for]="'f-remote-extraHosts-' + i" i18n="@@upstream.hosts.pattern">Host pattern</label>
                  <input
                    class="text-input field-w-name"
                    type="text"
                    autocomplete="off"
                    spellcheck="false"
                    translate="no"
                    [id]="'f-remote-extraHosts-' + i"
                    [value]="h.pattern"
                    [attr.aria-invalid]="invalid('/remote/extraHosts/' + i)"
                    (input)="setHost(i, { pattern: $any($event.target).value })"
                  />
                  <label class="check-row" [for]="'f-remote-extraHosts-' + i + '-send'">
                    <input
                      type="checkbox"
                      [id]="'f-remote-extraHosts-' + i + '-send'"
                      [checked]="h.sendCredentials"
                      (change)="setHost(i, { sendCredentials: $any($event.target).checked })"
                    />
                    <span i18n="@@upstream.hosts.send">Receives the credential</span>
                  </label>
                  <button class="btn btn-tertiary" type="button" (click)="removeHost(i)">
                    <ng-container i18n="@@upstream.hosts.remove">Remove</ng-container><span class="visually-hidden">&ngsp;{{ h.pattern }}</span>
                  </button>
                </div>
                @for (e of problems('/remote/extraHosts/' + i); track $index) {
                  <p class="field-error">{{ message(e) }}</p>
                }
              } @empty {
                <p class="hint" i18n="@@upstream.hosts.none">No further host.</p>
              }
              <div>
                <button class="btn btn-default" type="button" (click)="addHost()">
                  <ng-container i18n="@@upstream.hosts.add">Add host</ng-container>
                </button>
              </div>
              @for (e of problems('/remote/extraHosts'); track $index) {
                <p class="field-error">{{ message(e) }}</p>
              }
            </div>

            <div class="field">
              <label class="field-label" for="f-remote-cache-metadataTtlSeconds" i18n="@@upstream.metadata-ttl"
                >Metadata cache period in seconds</label
              >
              <input
                id="f-remote-cache-metadataTtlSeconds"
                class="text-input field-w-duration"
                type="text"
                inputmode="numeric"
                autocomplete="off"
                spellcheck="false"
                [value]="shown(upstream().metadataTtl, proposed().metadataTtlSeconds)"
                [attr.aria-invalid]="invalid('/remote/cache/metadataTtlSeconds')"
                aria-describedby="d-remote-metadata-ttl"
                (input)="set({ metadataTtl: number($any($event.target).value) })"
                (blur)="leavePeriod('metadataTtl')"
              />
              <p id="d-remote-metadata-ttl" class="hint" i18n="@@upstream.metadata-ttl.hint-value">
                How long fetched metadata is served without asking the upstream again.
              </p>
              @for (e of problems('/remote/cache/metadataTtlSeconds'); track $index) {
                <p class="field-error">{{ message(e) }}</p>
              }
            </div>

            <div class="field">
              <label class="field-label" for="f-remote-cache-notFoundTtlSeconds" i18n="@@upstream.notfound-ttl"
                >Negative cache period in seconds</label
              >
              <input
                id="f-remote-cache-notFoundTtlSeconds"
                class="text-input field-w-duration"
                type="text"
                inputmode="numeric"
                autocomplete="off"
                spellcheck="false"
                [value]="shown(upstream().notFoundTtl, proposed().notFoundTtlSeconds)"
                [attr.aria-invalid]="invalid('/remote/cache/notFoundTtlSeconds')"
                aria-describedby="d-remote-notfound-ttl"
                (input)="set({ notFoundTtl: number($any($event.target).value) })"
                (blur)="leavePeriod('notFoundTtl')"
              />
              <p id="d-remote-notfound-ttl" class="hint" i18n="@@upstream.notfound-ttl.hint-value">
                How long a "not found" answer of the upstream is remembered.
              </p>
              @for (e of problems('/remote/cache/notFoundTtlSeconds'); track $index) {
                <p class="field-error">{{ message(e) }}</p>
              }
            </div>

            @if (edit()) {
              <div class="field">
                <label class="check-row" for="f-remote-availability-mode">
                  <input
                    id="f-remote-availability-mode"
                    type="checkbox"
                    [checked]="upstream().offline"
                    [attr.aria-invalid]="invalid('/remote/availability/mode')"
                    aria-describedby="d-remote-offline"
                    (change)="set({ offline: $any($event.target).checked })"
                  />
                  <span i18n="@@upstream.offline">Offline</span>
                </label>
                <p id="d-remote-offline" class="hint" i18n="@@upstream.offline.hint">
                  The upstream is not contacted: clients get what is already stored.
                </p>
                @for (e of problems('/remote/availability/mode'); track $index) {
                  <p class="field-error">{{ message(e) }}</p>
                }
              </div>
            }
          </div>
        </div>
      </div>
    </section>
  `,
  styles: `
    .upstream {
      margin-block-start: var(--trove-space-6);
    }
    .upstream > .hint {
      margin-block: 0 var(--trove-space-4);
    }
    .test-result {
      max-inline-size: var(--trove-settings-measure);
    }
    .upstream-grid {
      display: grid;
      grid-template-columns: minmax(0, var(--trove-settings-measure));
      gap: var(--trove-space-4);
    }
    .upstream-grid .field {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-1);
    }
    .upstream-grid.details {
      padding-block: var(--trove-space-2) var(--trove-space-4);
    }
    .presets {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-2);
    }
    .preset[aria-pressed='true'] {
      background: var(--trove-accent-container);
      color: var(--trove-on-accent-container);
      border-color: var(--trove-accent);
      font-weight: 600;
    }
    .host-row {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--trove-space-1) var(--trove-space-4);
      margin-block-end: var(--trove-space-2);
    }
    .host-row .text-input {
      flex: 0 1 var(--trove-field-width-name);
    }
    .test {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--trove-space-3);
      margin-block: var(--trove-space-4) var(--trove-space-2);
    }
    .verdict {
      margin-block: var(--trove-space-2);
    }
    .allow {
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      gap: var(--trove-space-2);
      margin-block-end: var(--trove-space-2);
    }
    .allow p {
      margin: 0;
      overflow-wrap: anywhere;
    }
    .allow .btn {
      max-inline-size: 100%;
      white-space: normal;
      overflow-wrap: anywhere;
      text-align: start;
    }
    /* The look of a closed section's toggle (<trove-disclosure>), so that every fold of a form reads the same. */
    .details-toggle {
      display: inline-flex;
      align-items: center;
      gap: var(--trove-space-1);
      margin-block-start: var(--trove-space-3);
      padding: 0;
      border: 0;
      background: none;
      color: var(--trove-heading);
      font: inherit;
      font-weight: 600;
      cursor: pointer;
    }
    .details-toggle:focus-visible {
      outline: var(--trove-focus-ring);
      outline-offset: 2px;
    }
    .details-toggle .chevron {
      font-size: 1.25rem;
      transition: transform var(--trove-motion-duration) var(--trove-motion-easing);
    }
    .details-toggle[aria-expanded='true'] .chevron {
      transform: rotate(90deg);
    }
    #upstream-details .details {
      padding-inline-start: calc(1.25rem + var(--trove-space-1));
    }
  `,
})
export class UpstreamSettings {
  private readonly api = inject(RepositoriesApi);
  private readonly locale = inject(LOCALE_ID);

  /** The form of the repository; the section edits `upstream`. */
  readonly form = model.required<RepositoryForm>();
  readonly format = input.required<FormatDescriptor>();
  /** The project the upstream is tested in: the test runs inside a project (`POST /projects/{project}/upstream-tests`). */
  readonly project = input.required<string>();
  /** Server field problems by pointer (`ApiError.fieldErrors`). */
  readonly fieldErrors = input<ReadonlyMap<string, FieldProblem>>(new Map());
  /** The `repository` document of `getRepositoryDefaults` (R-REPO-063) when the form read it: its remote's periods are the defaults shown. */
  readonly defaults = input<Readonly<Record<string, unknown>> | null>(null);
  /** The settings page of a stored repository: Offline is offered (R-UI-082, R-UI-083). */
  readonly edit = input(false);
  /** The form was sent: a missing URL shows its error whether or not the field was left (R-UI-086). */
  readonly submitted = input(false);

  protected readonly sectionId = UPSTREAM_SECTION_ID;
  protected readonly hostsId = controlIdOf('/remote/extraHosts');
  protected readonly authModes: readonly UpstreamAuthMode[] = ['none', 'basic', 'bearer'];
  protected readonly accepted = $localize`:@@upstream.test.accepted:accepted`;
  protected readonly refused = $localize`:@@upstream.test.refused:refused`;

  protected readonly testing = signal(false);
  private readonly result = signal<UpstreamTestResult | null>(null);
  protected readonly failure = signal<ApiError | null>(null);
  /** The user opened or closed Upstream details; a field error inside it opens it whatever this says. */
  protected readonly detailsOpened = signal(false);
  private readonly urlLeft = signal(false);

  protected readonly upstream = computed(() => this.form().upstream);
  protected readonly proposed = computed(() => upstreamDefaultsOf(this.format(), this.remoteDefaults()));
  protected readonly presets = computed<readonly UpstreamPreset[]>(() => this.format().upstreamPresets);
  protected readonly hosts = computed(() => shownHosts(this.upstream(), this.proposed()));

  private readonly remoteDefaults = computed(() => {
    const r = this.defaults()?.['remote'];
    return typeof r === 'object' && r !== null ? (r as Readonly<Record<string, unknown>>) : null;
  });

  protected readonly detailsOpen = computed(
    () =>
      this.detailsOpened() ||
      [...this.fieldErrors().keys()].some((p) => DETAIL_POINTERS.some((d) => p === d || p.startsWith(`${d}/`))),
  );

  /** A format without presets needs the URL: once the field was left empty, it says what to type (R-UI-086). */
  protected readonly urlError = computed(
    () => (this.urlLeft() || this.submitted()) && this.presets().length === 0 && this.upstream().url.trim() === '',
  );

  /** "The credential is sent to registry.gitlab.com and gitlab.com." (R-UI-082) */
  protected readonly credentialLine = computed(() => {
    const hosts = credentialHosts(this.upstream(), this.proposed());
    if (hosts.length === 0) return '';
    const list = new Intl.ListFormat(this.locale, { type: 'conjunction' }).format(hosts);
    return $localize`:@@upstream.credential-hosts:The credential is sent to ${list}:hosts:.`;
  });

  /** The verdict in words, so that the state is not carried by colour alone (R-UI-044). */
  protected readonly outcome = computed(() => {
    const result = this.result();
    if (!result) return null;
    const good =
      result.reachable &&
      result.error === null &&
      result.authenticated !== false &&
      (result.httpStatus === null || result.httpStatus < 400 || result.tokenService !== null);
    let summary: string;
    if (!result.reachable) {
      summary = $localize`:@@upstream.test.unreachable:The upstream could not be reached.`;
    } else if (result.error?.code === 'foreign_host_denied') {
      summary = $localize`:@@upstream.test.token-foreign:The upstream answered, and its login is on a host this repository may not reach.`;
    } else if (result.authenticated === false) {
      summary = $localize`:@@upstream.test.denied:The upstream answered and refused the credentials.`;
    } else if (result.authenticated === true && good) {
      summary = $localize`:@@upstream.test.ok-credentials:The upstream answered and accepted the credentials.`;
    } else if (good) {
      summary = $localize`:@@upstream.test.ok:The upstream answered.`;
    } else {
      summary = $localize`:@@upstream.test.answered-error:The upstream answered with an error.`;
    }
    return { result, good, summary };
  });

  /** The host of a token service the repository may not reach, which the user may allow (R-UI-082, Q-UI-346). */
  protected readonly foreignTokenHost = computed(() => {
    const r = this.result();
    return r?.error?.code === 'foreign_host_denied' && r.tokenService && !r.tokenService.allowed ? r.tokenService.host : null;
  });

  /** A preset is pressed while the URL the repository would use is its URL, compared as the server stores URLs (Q-UI-345). */
  protected pressed(p: UpstreamPreset): boolean {
    const url = effectiveUrl(this.upstream(), this.proposed());
    return url !== null && normalizedUrl(url.trim()) === normalizedUrl(p.url);
  }

  protected choose(p: UpstreamPreset): void {
    this.set({ url: p.url });
  }

  /** An emptied URL of a format with presets shows the default preset's again when it is left (R-UI-084). */
  protected leaveUrl(): void {
    this.urlLeft.set(true);
    const def = this.proposed().url;
    if (this.upstream().url.trim() === '' && def !== null) this.set({ url: def });
  }

  protected tokenServiceState(ts: NonNullable<UpstreamTestResult['tokenService']>): string {
    if (!ts.allowed) return $localize`:@@upstream.test.token-not-allowed:not allowed for this repository`;
    return ts.receivesCredential
      ? $localize`:@@upstream.test.token-credential:allowed, receives the credential`
      : $localize`:@@upstream.test.token-no-credential:allowed, receives no credential`;
  }

  protected authLabel(mode: UpstreamAuthMode): string {
    switch (mode) {
      case 'basic':
        return $localize`:@@upstream.auth.basic:Username and password`;
      case 'bearer':
        return $localize`:@@upstream.auth.bearer:Bearer token`;
      default:
        return $localize`:@@upstream.auth.none:None`;
    }
  }

  protected problems(pointer: string): readonly FieldProblem[] {
    return problemsAt(this.fieldErrors(), pointer);
  }

  protected invalid(pointer: string): 'true' | null {
    return this.problems(pointer).length > 0 ? 'true' : null;
  }

  protected message(problem: FieldProblem): string {
    return fieldMessage(problem);
  }

  /**
   * A period shows its default while it holds none: the default is a value in the field (R-UI-084). A field the user empties
   * stays empty while it is edited (the form holds '', which is written as the default) and shows the default again once left.
   */
  protected shown(value: number | string | null, fallback: number | null): string {
    const v = value ?? fallback;
    return v === null ? '' : String(v);
  }

  protected number(text: string): number | string {
    const v = numberOrText(text);
    return v === undefined ? '' : (v as number | string);
  }

  protected leavePeriod(member: 'metadataTtl' | 'notFoundTtl'): void {
    if (this.upstream()[member] === '') this.set({ [member]: null });
  }

  protected setHost(i: number, change: Partial<UpstreamHost>): void {
    this.setHosts(this.hosts().map((h, j) => (j === i ? { ...h, ...change } : h)));
  }

  protected removeHost(i: number): void {
    this.setHosts(this.hosts().filter((_, j) => j !== i));
  }

  protected addHost(): void {
    this.setHosts([...this.hosts(), { pattern: '', sendCredentials: false }]);
  }

  /**
   * Allow a token service the test found on a host the repository may not reach (Q-UI-346): the host becomes a further host,
   * receiving the credential when the repository has one, and the test runs again. The click is the user's consent to the host
   * the upstream named (H-610).
   */
  protected async allow(host: string): Promise<void> {
    const rest = this.hosts().filter((h) => h.pattern.trim().toLowerCase() !== host);
    this.setHosts([...rest, { pattern: host, sendCredentials: this.upstream().authMode !== 'none' }]);
    await this.test();
  }

  private setHosts(hosts: readonly UpstreamHost[]): void {
    this.set({ extraHosts: hosts });
  }

  /** A change of the settings makes an earlier test result stale: it goes. */
  protected set(change: Partial<UpstreamForm>): void {
    this.result.set(null);
    this.failure.set(null);
    this.form.update((f) => ({ ...f, upstream: { ...f.upstream, ...change } }));
  }

  protected async test(): Promise<void> {
    if (this.testing()) return;
    this.testing.set(true);
    this.result.set(null);
    this.failure.set(null);
    try {
      this.result.set(await this.api.testUpstream(this.project(), upstreamTestBody(this.upstream(), this.format())));
    } catch (e) {
      this.failure.set(asApiError(e));
    } finally {
      this.testing.set(false);
    }
  }
}
