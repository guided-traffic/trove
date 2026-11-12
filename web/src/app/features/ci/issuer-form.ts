import { Component, DestroyRef, computed, effect, inject, input, output, signal, untracked } from '@angular/core';
import { Checkbox } from '../../shared/ui/checkbox';
import { Disclosure } from '../../shared/ui/disclosure';
import { CI_NAME_PATTERN, CiApi, CiIssuer, CiIssuerCreate, CiIssuerInput } from '../../api/ci-api';
import { ApiError } from '../../api/problem';
import { Versioned } from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { TextField } from '../../shared/access/fields';
import { ConflictResolver } from '../../shared/conflict-dialog';
import { DraftStore } from '../../shared/drafts';
import { ErrorNotice } from '../../shared/error-notice';
import { fieldErrorAt } from '../../shared/field-errors';
import { asApiError } from '../../shared/paged-resource';
import { changedMembers, overwriteBody } from './ci-common';
import { splitValues } from './ci-model';

const ALGORITHMS = ['RS256', 'RS384', 'RS512', 'PS256', 'PS384', 'PS512', 'ES256', 'ES384', 'EdDSA'] as const;

/** The lines of a list field: one entry per line, empty lines dropped. */
const toLines = (text: string): string[] => splitValues(text.replace(/,/g, '\n'));

/**
 * The settings of a CI issuer (R-UI-035, R-CI-001, R-CI-002): the create form (with the discovery and
 * key check of `validate`) and the editor of an issuer that API created. An issuer declared in the
 * system configuration is shown read-only with the configuration named as its source (Q-CI-16); so is
 * every issuer for a caller without `ci:write`. The save sends the full representation with the loaded
 * `ETag`; a 412 opens the conflict dialog. The 422 codes of the validation show next to the field they
 * name and as the notice of the form.
 */
@Component({
  selector: 'trove-ci-issuer-form',
  imports: [Checkbox, ErrorNotice, Disclosure, TextField],
  template: `
    @if (configured()) {
      <p class="source-note" role="note">
        <span class="material-icons" aria-hidden="true">lock</span>
        <span i18n="@@ci.form.configuration-source">
          This issuer is declared in the installation's configuration (<code translate="no">ci.issuers</code> of the Helm values). Its settings are read-only here; change them there. Its mappings are managed here as for any issuer.
        </span>
      </p>
      @if (issuer()?.removedFromConfiguration) {
        <p class="source-note warn" role="note">
          <span class="material-icons" aria-hidden="true">warning</span>
          <span i18n="@@ci.form.removed">
            The issuer is no longer declared in the configuration. It is disabled and kept, so its mappings and issued tokens stay; you can delete it now.
          </span>
        </p>
      }
    }

    @if (readonly()) {
      @if (issuer(); as i) {
        <div class="readonly-fields">
          <trove-text-field i18n-label="@@ci.form.name" label="Name" name="ro-name" [value]="i.name" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.kind" label="Kind" name="ro-kind" [value]="i.kind" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.issuer" label="Issuer URL" name="ro-issuer" [value]="i.issuer" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.description" label="Description" name="ro-description" [value]="i.description || '–'" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.enabled" label="Enabled" name="ro-enabled" [value]="i.enabled ? yes : no" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.audiences" label="Accepted audiences" name="ro-audiences" [value]="i.allowedAudiences.join(', ')" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.exclusive" label="Only these audiences" name="ro-exclusive" [value]="i.exclusiveAudience ? yes : no" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.algorithms" label="Signing algorithms" name="ro-algorithms" [value]="i.allowedAlgorithms.join(', ')" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.public" label="Public issuer" name="ro-public" [value]="i.publicIssuer ? yes : no" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.pin-claims" label="Pin claims" name="ro-pinClaims" [value]="i.pinClaims.join(', ')" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.required-claims" label="Required claims" name="ro-requiredClaims" [value]="i.requiredClaims.join(', ')" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.skew" label="Clock skew (seconds)" name="ro-skew" [value]="'' + i.clockSkewSeconds" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.max-ttl" label="Maximum token lifetime (seconds)" name="ro-maxTtl" [value]="i.maxTtlSeconds === null || i.maxTtlSeconds === undefined ? '–' : '' + i.maxTtlSeconds" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.discovery" label="Discovery document" name="ro-discovery" [value]="i.discoveryUrl ?? '–'" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.jwks" label="Key set URL" name="ro-jwks" [value]="i.jwksUri ?? '–'" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.ca" label="CA bundle" name="ro-ca" [value]="i.caBundleRef ?? '–'" [readonly]="true" />
          <trove-text-field i18n-label="@@ci.form.proxy" label="Outbound proxy" name="ro-proxy" [value]="i.useOutboundProxy ? yes : no" [readonly]="true" />
        </div>
      }
    } @else {
      @if (error(); as e) {
        <trove-error-notice [error]="e" />
      }
      <form class="form-grid" (submit)="submit($event)" novalidate>
        @if (isNew()) {
          <trove-text-field
            i18n-label="@@ci.form.name"
            label="Name"
            name="name"
            [(value)]="name"
            [required]="true"
            [error]="fieldError('/name') ?? nameProblem()"
            i18n-hint="@@ci.form.name-hint"
            hint="Lower-case letters, digits, dot, underscore and hyphen. It cannot be changed later."
          />
          <p class="muted" i18n="@@ci.form.kind-note">Kind: GitLab. GitHub Actions and generic OpenID Connect issuers are not available yet.</p>
        }
        <trove-text-field
          i18n-label="@@ci.form.issuer"
          label="Issuer URL"
          name="issuer"
          type="text"
          [(value)]="url"
          [required]="true"
          [readonly]="!isNew()"
          [error]="fieldError('/issuer') ?? urlProblem()"
          i18n-hint="@@ci.form.issuer-hint"
          hint="The base URL of the GitLab instance, https only. It must match the iss claim exactly."
        />
        <trove-text-field i18n-label="@@ci.form.description" label="Description" name="description" [(value)]="description" [error]="fieldError('/description')" />
        <trove-checkbox [checked]="enabled()" (checkedChange)="enabled.set($event)" i18n="@@ci.form.enabled-check">Enabled (disabling revokes the tokens issued through this issuer)</trove-checkbox>
        <trove-text-field
          i18n-label="@@ci.form.audiences-lines"
          label="Accepted audiences"
          name="audiences"
          type="textarea"
          [(value)]="audiences"
          [error]="fieldError('/allowedAudiences')"
          i18n-hint="@@ci.form.audiences-hint"
          hint="One per line. Empty: the base URL of this server and npm:host."
        />
        <trove-checkbox [checked]="publicIssuer()" (checkedChange)="publicIssuer.set($event)" i18n="@@ci.form.public-check">Public issuer (anyone can obtain tokens from it, such as GitLab.com): mappings must pin a project or namespace ID</trove-checkbox>
        @if (fieldError('/publicIssuer'); as text) {
          <p class="field-error">{{ text }}</p>
        }
        <trove-text-field
          i18n-label="@@ci.form.pin-lines"
          label="Pin claims"
          name="pinClaims"
          type="textarea"
          [(value)]="pinClaims"
          [error]="fieldError('/pinClaims')"
          i18n-hint="@@ci.form.pin-hint"
          hint="One per line. Immutable ID claims only. Empty: project_id, namespace_id, job_project_id and job_namespace_id."
        />

        <trove-disclosure
          i18n-heading="@@ci.form.advanced"
          heading="More settings"
          i18n-hint="@@ci.form.advanced-hint"
          hint="Discovery, algorithms, claims, lifetimes and the connection to the issuer."
        >
          <trove-text-field i18n-label="@@ci.form.discovery" label="Discovery document" name="discoveryUrl" [(value)]="discoveryUrl" [error]="fieldError('/discoveryUrl')" i18n-hint="@@ci.form.discovery-hint" hint="Empty: the issuer URL followed by /.well-known/openid-configuration" />
          <trove-text-field i18n-label="@@ci.form.jwks" label="Key set URL" name="jwksUri" [(value)]="jwksUri" [error]="fieldError('/jwksUri')" i18n-hint="@@ci.form.jwks-hint" hint="Empty: the jwks_uri of the discovery document" />
          <fieldset class="algorithms">
            <legend i18n="@@ci.form.algorithms">Signing algorithms</legend>
            @for (a of algorithms; track a) {
              <trove-checkbox [checked]="algorithmSet().includes(a)" (checkedChange)="toggleAlgorithm(a, $event)">{{ a }}</trove-checkbox>
            }
            @if (fieldError('/allowedAlgorithms'); as text) {
              <p class="field-error">{{ text }}</p>
            }
          </fieldset>
          <trove-checkbox [checked]="exclusiveAudience()" (checkedChange)="exclusiveAudience.set($event)" i18n="@@ci.form.exclusive-check">Accept only tokens whose audience is in the list above</trove-checkbox>
          <trove-text-field i18n-label="@@ci.form.required-lines" label="Required claims" name="requiredClaims" type="textarea" [(value)]="requiredClaims" [error]="fieldError('/requiredClaims')" i18n-hint="@@ci.form.required-hint" hint="One per line. Empty: the preset of the kind." />
          <trove-text-field i18n-label="@@ci.form.skew" label="Clock skew (seconds)" name="clockSkewSeconds" type="number" [(value)]="clockSkew" [error]="fieldError('/clockSkewSeconds')" i18n-hint="@@ci.form.skew-hint" hint="0 to 300" />
          <trove-text-field i18n-label="@@ci.form.max-ttl" label="Maximum token lifetime (seconds)" name="maxTtlSeconds" type="number" [(value)]="maxTtl" [error]="fieldError('/maxTtlSeconds')" i18n-hint="@@ci.form.max-ttl-hint" hint="Empty: only the global maximum applies" />
          <trove-checkbox [checked]="singleUse()" (checkedChange)="singleUse.set($event)" i18n="@@ci.form.single-use">Reject a second exchange of the same ID token</trove-checkbox>
          <trove-checkbox [checked]="useOutboundProxy()" (checkedChange)="useOutboundProxy.set($event)" i18n="@@ci.form.proxy-check">Fetch discovery and keys through the outbound proxy</trove-checkbox>
          <trove-text-field i18n-label="@@ci.form.ca" label="CA bundle" name="caBundleRef" [(value)]="caBundleRef" [error]="fieldError('/caBundleRef')" i18n-hint="@@ci.form.ca-hint" hint="A reference such as file:/path or env:NAME, for an instance behind a private CA" />
        </trove-disclosure>

        @if (isNew()) {
          <trove-checkbox [checked]="validate()" (checkedChange)="validate.set($event)" i18n="@@ci.form.validate">Check the discovery document and the keys before saving</trove-checkbox>
        }
        <div class="row-actions">
          <button class="btn btn-confirm" type="submit" [disabled]="busy()">
            @if (isNew()) {
              <ng-container i18n="@@ci.form.create">Create issuer</ng-container>
            } @else {
              <ng-container i18n="@@ci.form.save-changes">Save changes</ng-container>
            }
          </button>
        </div>
      </form>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .source-note {
      display: flex;
      gap: var(--trove-space-2);
      align-items: flex-start;
      margin-block: var(--trove-space-2);
      color: var(--trove-text-muted);
    }
    .source-note.warn .material-icons {
      color: var(--trove-status-warn);
    }
    .algorithms {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-1) var(--trove-space-3);
      margin: var(--trove-space-2) 0;
      border: 0;
      padding: 0;
    }
    .algorithms legend {
      font-weight: 600;
    }
  `,
})
export class IssuerForm {
  /** The issuer to edit; null creates a new one. */
  readonly issuer = input<CiIssuer | null>(null);
  /** The `ETag` the issuer was loaded with. */
  readonly etag = input('');
  readonly saved = output<Versioned<CiIssuer>>();

  private readonly api = inject(CiApi);
  private readonly conflicts = inject(ConflictResolver);
  private readonly drafts = inject(DraftStore);
  private readonly notifier = inject(Notifier);
  private readonly session = inject(SessionStore);

  protected readonly algorithms = ALGORITHMS;
  protected readonly yes = $localize`:@@ci.yes:yes`;
  protected readonly no = $localize`:@@ci.no:no`;

  protected readonly name = signal('');
  protected readonly url = signal('https://');
  protected readonly description = signal('');
  protected readonly enabled = signal(true);
  protected readonly audiences = signal('');
  protected readonly exclusiveAudience = signal(true);
  protected readonly publicIssuer = signal(true);
  protected readonly pinClaims = signal('');
  protected readonly requiredClaims = signal('');
  protected readonly algorithmSet = signal<readonly string[]>(['RS256']);
  protected readonly discoveryUrl = signal('');
  protected readonly jwksUri = signal('');
  protected readonly clockSkew = signal('60');
  protected readonly maxTtl = signal('');
  protected readonly singleUse = signal(false);
  protected readonly useOutboundProxy = signal(false);
  protected readonly caBundleRef = signal('');
  protected readonly validate = signal(true);

  protected readonly busy = signal(false);
  protected readonly attempted = signal(false);
  protected readonly error = signal<ApiError | null>(null);

  protected readonly isNew = computed(() => this.issuer() === null);
  protected readonly configured = computed(() => this.issuer()?.source === 'configuration');
  protected readonly readonly = computed(() => !this.isNew() && (this.configured() || !this.session.has('ci:write')));

  protected readonly nameProblem = computed(() =>
    this.attempted() && !CI_NAME_PATTERN.test(this.name().trim())
      ? $localize`:@@ci.form.name-invalid:Use two to 64 characters: lower-case letters, digits, dot, underscore or hyphen, starting with a letter or digit.`
      : null,
  );
  protected readonly urlProblem = computed(() =>
    this.attempted() && !/^https:\/\/[^/\s]+/.test(this.url().trim())
      ? $localize`:@@ci.form.url-invalid:The issuer URL must be an https URL.`
      : null,
  );

  /** True when the form holds entries that are not stored yet. */
  private dirty(): boolean {
    if (this.readonly()) return false;
    const stored = this.issuer();
    if (stored === null) return this.name() !== '' || this.description() !== '' || this.url() !== 'https://';
    return Object.keys(changedMembers(this.body(), stored)).length > 0;
  }

  constructor() {
    // The entries of an open, changed form: kept across a re-authentication and offered as "Copy draft as JSON" (R-UI-042).
    this.drafts.track(() => (this.dirty() ? { ...this.body(), validate: this.validate() } : null), inject(DestroyRef));
    effect(() => {
      const i = this.issuer();
      untracked(() => {
        if (i) this.load(i);
        this.restoreDraft();
      });
    });
  }

  /** The entries kept across a re-authentication (R-UI-042) fill the form again. */
  private restoreDraft(): void {
    if (this.readonly()) return;
    const d = this.drafts.take<CiIssuerInput & { validate?: boolean }>();
    if (!d || typeof d !== 'object') return;
    this.name.set(d.name);
    this.url.set(d.issuer);
    this.description.set(d.description ?? '');
    this.enabled.set(d.enabled ?? true);
    this.audiences.set((d.allowedAudiences ?? []).join('\n'));
    this.exclusiveAudience.set(d.exclusiveAudience ?? true);
    this.publicIssuer.set(d.publicIssuer ?? true);
    this.pinClaims.set((d.pinClaims ?? []).join('\n'));
    this.requiredClaims.set((d.requiredClaims ?? []).join('\n'));
    this.algorithmSet.set(d.allowedAlgorithms ?? ['RS256']);
    this.discoveryUrl.set(d.discoveryUrl ?? '');
    this.jwksUri.set(d.jwksUri ?? '');
    this.clockSkew.set(String(d.clockSkewSeconds ?? 60));
    this.maxTtl.set(d.maxTtlSeconds ? String(d.maxTtlSeconds) : '');
    this.singleUse.set(d.singleUseSubjectTokens ?? false);
    this.useOutboundProxy.set(d.useOutboundProxy ?? false);
    this.caBundleRef.set(d.caBundleRef ?? '');
    this.validate.set(d.validate ?? true);
    this.notifier.status($localize`:@@ci.form.draft-restored:Your entries were restored. Save the issuer again.`);
  }

  private load(i: CiIssuer): void {
    this.name.set(i.name);
    this.url.set(i.issuer);
    this.description.set(i.description);
    this.enabled.set(i.enabled);
    this.audiences.set(i.allowedAudiences.join('\n'));
    this.exclusiveAudience.set(i.exclusiveAudience);
    this.publicIssuer.set(i.publicIssuer);
    this.pinClaims.set(i.pinClaims.join('\n'));
    this.requiredClaims.set(i.requiredClaims.join('\n'));
    this.algorithmSet.set(i.allowedAlgorithms);
    this.discoveryUrl.set(i.discoveryUrl ?? '');
    this.jwksUri.set(i.jwksUri ?? '');
    this.clockSkew.set(String(i.clockSkewSeconds));
    this.maxTtl.set(i.maxTtlSeconds ? String(i.maxTtlSeconds) : '');
    this.singleUse.set(i.singleUseSubjectTokens);
    this.useOutboundProxy.set(i.useOutboundProxy);
    this.caBundleRef.set(i.caBundleRef ?? '');
  }

  protected fieldError(pointer: string): string | null {
    return fieldErrorAt(this.error(), pointer);
  }

  protected toggleAlgorithm(a: string, on: boolean): void {
    this.algorithmSet.update((set) => (on ? [...set, a] : set.filter((x) => x !== a)));
  }

  private body(): CiIssuerInput {
    const skew = Number.parseInt(this.clockSkew(), 10);
    const ttl = Number.parseInt(this.maxTtl(), 10);
    const audiences = toLines(this.audiences());
    const required = toLines(this.requiredClaims());
    const pins = toLines(this.pinClaims());
    return {
      name: this.name().trim(),
      kind: 'gitlab',
      issuer: this.url().trim(),
      description: this.description().trim(),
      enabled: this.enabled(),
      discoveryUrl: this.discoveryUrl().trim() || null,
      jwksUri: this.jwksUri().trim() || null,
      ...(audiences.length > 0 ? { allowedAudiences: audiences } : {}),
      exclusiveAudience: this.exclusiveAudience(),
      allowedAlgorithms: this.algorithmSet() as CiIssuerInput['allowedAlgorithms'],
      allowedTyp: this.issuer()?.allowedTyp ?? ['JWT'],
      allowMissingTyp: this.issuer()?.allowMissingTyp ?? true,
      clockSkewSeconds: Number.isFinite(skew) ? skew : 60,
      ...(required.length > 0 ? { requiredClaims: required } : {}),
      publicIssuer: this.publicIssuer(),
      ...(pins.length > 0 ? { pinClaims: pins } : {}),
      singleUseSubjectTokens: this.singleUse(),
      maxTtlSeconds: Number.isFinite(ttl) && ttl > 0 ? ttl : null,
      caBundleRef: this.caBundleRef().trim() || null,
      useOutboundProxy: this.useOutboundProxy(),
      jobTokenFallback: { enabled: false },
    };
  }

  protected async submit(event: Event): Promise<void> {
    event.preventDefault();
    this.attempted.set(true);
    if (this.nameProblem() || this.urlProblem()) return;
    this.busy.set(true);
    this.error.set(null);
    try {
      const stored = this.issuer();
      if (stored === null) {
        const input: CiIssuerCreate = { ...this.body(), validate: this.validate() };
        const created = await this.api.createIssuer(input);
        this.notifier.status($localize`:@@ci.form.created:CI issuer created.`);
        this.saved.emit(created);
        return;
      }
      const body = this.body();
      const patch = changedMembers(body, stored);
      if (Object.keys(patch).length === 0) return;
      const result = await this.conflicts.save<CiIssuer>({
        etag: this.etag(),
        patch,
        send: (etag, current) =>
          this.api.replaceIssuer(
            stored.name,
            current === null ? body : overwriteBody(body, patch, current, stored, ['allowedAudiences', 'requiredClaims', 'pinClaims']),
            etag,
          ),
        reload: () => this.api.getIssuer(stored.name),
      });
      if (result.outcome === 'dismissed') return;
      if (result.outcome === 'saved') this.notifier.status($localize`:@@ci.form.saved:CI issuer saved.`);
      this.saved.emit(result.current);
    } catch (e) {
      const error = asApiError(e);
      if (error.code === 'reauthentication_required') this.drafts.keep({ ...this.body(), validate: this.validate() });
      this.error.set(error);
    } finally {
      this.busy.set(false);
    }
  }
}
