import { DatePipe } from '@angular/common';
import { Component, computed, inject, input, model, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { ApiError, FieldProblem } from '../../api/problem';
import {
  FormatDescriptor,
  RepositoriesApi,
  UpstreamTestResult,
} from '../../api/repositories-api';
import { ErrorNotice } from '../../shared/error-notice';
import { fieldMessage, problemsAt } from '../../shared/field-errors';
import { asApiError } from '../../shared/paged-resource';
import {
  RepositoryForm,
  UpstreamAuthMode,
  UpstreamForm,
  upstreamDefaultsOf,
  upstreamTestBody,
} from './repository-model';
import { controlIdOf, numberOrText } from './settings-schema';

/** The id of the section; a server error at a pointer of the upstream settings that has no control lands here. */
export const UPSTREAM_SECTION_ID = 'f-remote-upstream';

const MEMBER_POINTERS = [
  '/remote/url',
  '/remote/authMode',
  '/remote/username',
  '/remote/password',
  '/remote/cache/metadataTtlSeconds',
  '/remote/cache/notFoundTtlSeconds',
  '/remote/availability/mode',
];

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
 * The upstream settings of a remote repository (R-UI-021, R-REPO-024, R-REPO-033, F-54): the URL with the
 * format's preset, the credentials (the password is write-only: it is sent when typed and never read
 * back into the field), the metadata and negative cache periods with the format's defaults, the offline
 * switch (INV-11) and **Test connection**, which probes the unsaved section (`POST /api/v1/upstream-tests`)
 * and shows the result inline. A member left empty or equal to its default is not written (Q-UI-80).
 */
@Component({
  selector: 'trove-upstream-settings',
  imports: [DatePipe, MatButtonModule, ErrorNotice],
  template: `
    <section class="upstream" [id]="sectionId" tabindex="-1" aria-labelledby="upstream-heading">
      <h2 id="upstream-heading" i18n="@@upstream.heading">Upstream</h2>
      <p class="hint" i18n="@@upstream.intro">
        The registry this repository fetches from. Credentials go to this host only and are never
        shown again once saved.
      </p>

      <div class="form-grid">
        <div class="field">
          <label class="field-label" for="f-remote-url" i18n="@@upstream.url">Upstream URL</label>
          <input
            id="f-remote-url"
            class="text-input"
            type="text"
            inputmode="url"
            autocomplete="off"
            spellcheck="false"
            [value]="upstream().url"
            [attr.aria-invalid]="invalid('/remote/url')"
            aria-describedby="d-remote-url"
            (input)="set({ url: $any($event.target).value })"
          />
          <p id="d-remote-url" class="hint">
            @if (preset(); as p) {
              <ng-container i18n="@@upstream.url.hint-preset">The format's well-known upstream is</ng-container>&ngsp;<code translate="no">{{ p }}</code>.&ngsp;<ng-container i18n="@@upstream.url.hint-empty">Empty means that upstream.</ng-container>
            } @else {
              <ng-container i18n="@@upstream.url.hint-required"
                >This format has no well-known upstream: enter the address of the registry.</ng-container
              >
            }
          </p>
          @if (preset() && upstream().url.trim() !== '' && !isPreset()) {
            <button mat-button type="button" (click)="set({ url: preset()! })">
              <ng-container i18n="@@upstream.url.use-preset">Use the well-known upstream</ng-container>
            </button>
          }
          @for (e of problems('/remote/url'); track $index) {
            <p class="field-error">{{ message(e) }}</p>
          }
        </div>

        <div class="field">
          <label class="field-label" for="f-remote-authMode" i18n="@@upstream.auth">Authentication</label>
          <select
            id="f-remote-authMode"
            class="select-input"
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
              class="text-input"
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
            <label class="field-label" for="f-remote-password" i18n="@@upstream.password"
              >Password or token</label
            >
            <input
              id="f-remote-password"
              class="text-input"
              type="password"
              autocomplete="new-password"
              spellcheck="false"
              [value]="upstream().password"
              [attr.aria-invalid]="invalid('/remote/password')"
              aria-describedby="d-remote-password"
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

        <div class="field">
          <label class="field-label" for="f-remote-cache-metadataTtlSeconds" i18n="@@upstream.metadata-ttl"
            >Metadata cache period in seconds</label
          >
          <input
            id="f-remote-cache-metadataTtlSeconds"
            class="text-input"
            type="text"
            inputmode="numeric"
            autocomplete="off"
            spellcheck="false"
            [value]="shown(upstream().metadataTtl)"
            [attr.aria-invalid]="invalid('/remote/cache/metadataTtlSeconds')"
            aria-describedby="d-remote-metadata-ttl"
            (input)="set({ metadataTtl: number($any($event.target).value) })"
          />
          <p id="d-remote-metadata-ttl" class="hint">{{ metadataHint() }}</p>
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
            class="text-input"
            type="text"
            inputmode="numeric"
            autocomplete="off"
            spellcheck="false"
            [value]="shown(upstream().notFoundTtl)"
            [attr.aria-invalid]="invalid('/remote/cache/notFoundTtlSeconds')"
            aria-describedby="d-remote-notfound-ttl"
            (input)="set({ notFoundTtl: number($any($event.target).value) })"
          />
          <p id="d-remote-notfound-ttl" class="hint">{{ notFoundHint() }}</p>
          @for (e of problems('/remote/cache/notFoundTtlSeconds'); track $index) {
            <p class="field-error">{{ message(e) }}</p>
          }
        </div>

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
      </div>

      <div class="test">
        <button mat-stroked-button type="button" [disabled]="testing()" (click)="test()">
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
          <dl class="facts">
            @if (o.result.httpStatus !== null) {
              <dt i18n="@@upstream.test.status">HTTP status</dt>
              <dd>{{ o.result.httpStatus }}</dd>
            }
            @if (o.result.authenticated !== null) {
              <dt i18n="@@upstream.test.credentials">Credentials</dt>
              <dd>{{ o.result.authenticated ? accepted : refused }}</dd>
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
    </section>
  `,
  styles: `
    .upstream {
      margin-block-start: var(--trove-space-6);
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
  `,
})
export class UpstreamSettings {
  private readonly api = inject(RepositoriesApi);

  /** The form of the repository; the section edits `upstream`. */
  readonly form = model.required<RepositoryForm>();
  readonly format = input.required<FormatDescriptor>();
  /** Server field problems by pointer (`ApiError.fieldErrors`). */
  readonly fieldErrors = input<ReadonlyMap<string, FieldProblem>>(new Map());

  protected readonly sectionId = UPSTREAM_SECTION_ID;
  protected readonly authModes: readonly UpstreamAuthMode[] = ['none', 'basic', 'bearer'];
  protected readonly accepted = $localize`:@@upstream.test.accepted:accepted`;
  protected readonly refused = $localize`:@@upstream.test.refused:refused`;

  protected readonly testing = signal(false);
  private readonly result = signal<UpstreamTestResult | null>(null);
  protected readonly failure = signal<ApiError | null>(null);

  protected readonly upstream = computed(() => this.form().upstream);
  protected readonly defaults = computed(() => upstreamDefaultsOf(this.format()));
  protected readonly preset = computed(() => this.defaults().url);
  protected readonly isPreset = computed(
    () => this.preset() !== null && this.upstream().url.trim() === this.preset(),
  );

  /** The verdict in words, so that the state is not carried by colour alone (R-UI-044). */
  protected readonly outcome = computed(() => {
    const result = this.result();
    if (!result) return null;
    const good =
      result.reachable &&
      result.error === null &&
      result.authenticated !== false &&
      (result.httpStatus ?? 200) < 400;
    const summary = !result.reachable
      ? $localize`:@@upstream.test.unreachable:The upstream could not be reached.`
      : result.authenticated === false
        ? $localize`:@@upstream.test.denied:The upstream answered and refused the credentials.`
        : good
          ? $localize`:@@upstream.test.ok:The upstream answered.`
          : $localize`:@@upstream.test.answered-error:The upstream answered with an error.`;
    return { result, good, summary };
  });

  protected readonly metadataHint = computed(() => {
    const d = this.defaults().metadataTtlSeconds;
    return d === null
      ? $localize`:@@upstream.metadata-ttl.hint-none:How long fetched metadata is served without asking the upstream again. Empty: the format's default.`
      : $localize`:@@upstream.metadata-ttl.hint:How long fetched metadata is served without asking the upstream again. Default: ${d}:seconds:.`;
  });
  protected readonly notFoundHint = computed(() => {
    const d = this.defaults().notFoundTtlSeconds;
    return d === null
      ? $localize`:@@upstream.notfound-ttl.hint-none:How long "not found" answers are remembered. Empty: the format's default.`
      : $localize`:@@upstream.notfound-ttl.hint:How long "not found" answers are remembered. Default: ${d}:seconds:.`;
  });

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

  protected shown(value: number | string | null): string {
    return value === null ? '' : String(value);
  }

  protected number(text: string): number | string | null {
    const v = numberOrText(text);
    return v === undefined ? null : (v as number | string);
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
      this.result.set(await this.api.testUpstream(upstreamTestBody(this.upstream(), this.format())));
    } catch (e) {
      this.failure.set(asApiError(e));
    } finally {
      this.testing.set(false);
    }
  }
}
