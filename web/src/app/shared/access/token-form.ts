import { Component, computed, effect, input, output, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatCheckboxModule } from '@angular/material/checkbox';
import { ApiError } from '../../api/problem';
import { CreateTokenRequest } from '../../api/tokens-api';
import { SelectField, SelectOption, TextField } from './fields';
import { fieldMessage } from '../field-errors';

export const TOKEN_ACTIONS = ['read', 'annotate', 'deploy', 'overwrite', 'delete', 'manage'] as const;
type TokenAction = (typeof TOKEN_ACTIONS)[number];

const DAY = 86_400;

/** Lifetime choices; the numbers are seconds, `default` leaves the server's default, `0` never expires. */
const PRESET_SECONDS: Readonly<Record<string, number>> = {
  d7: 7 * DAY,
  d30: 30 * DAY,
  d90: 90 * DAY,
  d365: 365 * DAY,
};

/** The non-secret part of the form, for a draft kept across re-authentication (R-UI-042). */
export interface TokenDraft {
  readonly description: string;
  readonly lifetime: string;
  readonly customDate: string;
  readonly restricted: boolean;
  readonly repositories: string;
  readonly packages: string;
  readonly actions: readonly string[];
}

/**
 * The create form of a personal or service-account token (R-UI-029, R-UI-026): description,
 * lifetime (presets, a custom date, or "never" where the owner may have a non-expiring token) and
 * scope (everything the owner may do, or repositories and actions; grammar of R-AUTH-024). It
 * validates what it can before the request and shows server field errors next to the fields. The
 * idempotency key is the parent's, generated when the form opens.
 */
@Component({
  selector: 'trove-token-form',
  imports: [MatButtonModule, MatCheckboxModule, SelectField, TextField],
  template: `
    <form class="form" (submit)="submit($event)" novalidate>
      <trove-text-field
        i18n-label="@@token-form.description"
        label="Description"
        name="description"
        [(value)]="description"
        [required]="true"
        [error]="fieldError('/description', 'description')"
        i18n-hint="@@token-form.description-hint"
        hint="What the token is for, for example the machine or the job."
      />
      <trove-select-field
        i18n-label="@@token-form.lifetime"
        label="Lifetime"
        name="lifetime"
        [options]="lifetimeOptions()"
        [(value)]="lifetime"
        [error]="fieldError('/ttlSeconds', 'lifetime')"
      />
      @if (lifetime() === 'custom') {
        <trove-text-field
          i18n-label="@@token-form.expires"
          label="Expires on (UTC)"
          name="expires"
          type="date"
          [(value)]="customDate"
          [required]="true"
          [error]="fieldError('/expiresAt', 'customDate')"
        />
      }

      <fieldset class="scope">
        <legend i18n="@@token-form.scope">Scope</legend>
        <label class="radio">
          <input type="radio" name="scope" [checked]="!restricted()" (change)="restricted.set(false)" />
          <span i18n="@@token-form.scope-all">All permissions of the owner</span>
        </label>
        <label class="radio">
          <input type="radio" name="scope" [checked]="restricted()" (change)="restricted.set(true)" />
          <span i18n="@@token-form.scope-restricted">Only the repositories and actions below</span>
        </label>
        @if (restricted()) {
          <trove-text-field
            i18n-label="@@token-form.repositories"
            label="Repositories"
            name="repositories"
            type="textarea"
            [(value)]="repositories"
            [required]="true"
            [error]="fieldError('/scope/repositories', 'repositories')"
            i18n-hint="@@token-form.repositories-hint"
            hint="One repository key or pattern per line, for example npm-internal or npm-team-*."
          />
          <trove-text-field
            i18n-label="@@token-form.packages"
            label="Package patterns (optional)"
            name="packages"
            type="textarea"
            [(value)]="packages"
            i18n-hint="@@token-form.packages-hint"
            hint="One pattern per line, for example @team-a/*. Empty means every package."
          />
          <fieldset class="actions">
            <legend i18n="@@token-form.actions">Actions</legend>
            @for (a of actionList; track a) {
              <mat-checkbox [checked]="actions().includes(a)" (change)="toggle(a, $event.checked)">{{ a }}</mat-checkbox>
            }
            @if (fieldError('/scope/actions', 'actions'); as text) {
              <p class="field-error">{{ text }}</p>
            }
          </fieldset>
        }
      </fieldset>

      <div class="row">
        <button mat-flat-button type="submit" [disabled]="busy()">{{ submitLabel() }}</button>
        <button mat-button type="button" (click)="cancelled.emit()" i18n="@@token-form.cancel">
          Cancel
        </button>
      </div>
    </form>
  `,
  styles: `
    .form {
      display: flex;
      flex-direction: column;
      align-items: flex-start;
      max-inline-size: 36rem;
    }
    fieldset {
      margin: 0 0 var(--trove-space-3);
      padding: var(--trove-space-2) var(--trove-space-3);
      border: 1px solid var(--mat-sys-outline-variant);
      border-radius: var(--mat-sys-corner-small);
      inline-size: 100%;
      box-sizing: border-box;
    }
    .actions {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-1) var(--trove-space-3);
    }
    .radio {
      display: flex;
      align-items: center;
      gap: var(--trove-space-2);
      min-block-size: 24px;
      margin-block-end: var(--trove-space-1);
    }
    .row {
      display: flex;
      gap: var(--trove-space-2);
    }
    .field-error {
      flex-basis: 100%;
      margin: 0;
      color: var(--trove-status-bad);
      font: var(--mat-sys-body-small);
    }
  `,
})
export class TokenForm {
  /** Offers "Never expires" (service-account tokens may be non-expiring, R-AUTH-026). */
  readonly allowNonExpiring = input(false);
  readonly submitLabel = input.required<string>();
  readonly busy = input(false);
  /** The last failed submit; its field errors appear next to the fields. */
  readonly failure = input<ApiError | null>(null);
  /** A draft restored after re-authentication. */
  readonly restore = input<TokenDraft | null>(null);

  readonly submitted = output<CreateTokenRequest>();
  readonly cancelled = output<void>();
  /** The current non-secret form content, for the draft kept across re-authentication. */
  readonly draft = output<TokenDraft>();

  protected readonly actionList = TOKEN_ACTIONS;
  protected readonly description = signal('');
  protected readonly lifetime = signal('default');
  protected readonly customDate = signal('');
  protected readonly restricted = signal(false);
  protected readonly repositories = signal('');
  protected readonly packages = signal('');
  protected readonly actions = signal<readonly TokenAction[]>(['read']);
  private readonly localErrors = signal<Readonly<Record<string, string>>>({});

  protected readonly lifetimeOptions = computed<readonly SelectOption[]>(() => [
    { value: 'default', label: $localize`:@@token-form.life.default:Server default` },
    { value: 'd7', label: $localize`:@@token-form.life.d7:7 days` },
    { value: 'd30', label: $localize`:@@token-form.life.d30:30 days` },
    { value: 'd90', label: $localize`:@@token-form.life.d90:90 days` },
    { value: 'd365', label: $localize`:@@token-form.life.d365:1 year` },
    { value: 'custom', label: $localize`:@@token-form.life.custom:Custom date` },
    ...(this.allowNonExpiring()
      ? [{ value: 'never', label: $localize`:@@token-form.life.never:Never expires` }]
      : []),
  ]);

  constructor() {
    effect(() => {
      const d = this.restore();
      if (!d) return;
      untracked(() => {
        this.description.set(d.description);
        this.lifetime.set(d.lifetime);
        this.customDate.set(d.customDate);
        this.restricted.set(d.restricted);
        this.repositories.set(d.repositories);
        this.packages.set(d.packages);
        this.actions.set(d.actions.filter((a): a is TokenAction => TOKEN_ACTIONS.includes(a as TokenAction)));
      });
    });
  }

  /** The text for a server field error at `pointer` or a local one under `key`. */
  protected fieldError(pointer: string, key: string): string | null {
    const local = this.localErrors()[key];
    if (local) return local;
    const server = this.failure()?.fieldErrors.get(pointer);
    if (!server) return null;
    return fieldMessage(server);
  }

  protected toggle(action: TokenAction, on: boolean): void {
    this.actions.update((list) => (on ? [...new Set([...list, action])] : list.filter((a) => a !== action)));
  }

  /** Emits the current content as a draft; the parent keeps it when re-authentication interrupts. */
  currentDraft(): TokenDraft {
    return {
      description: this.description(),
      lifetime: this.lifetime(),
      customDate: this.customDate(),
      restricted: this.restricted(),
      repositories: this.repositories(),
      packages: this.packages(),
      actions: this.actions(),
    };
  }

  protected submit(event: Event): void {
    event.preventDefault();
    this.draft.emit(this.currentDraft());
    const errors: Record<string, string> = {};
    const description = this.description().trim();
    if (!description) {
      errors['description'] = $localize`:@@token-form.err.description:Enter a description.`;
    }
    const lifetime = this.lifetime();
    if (lifetime === 'custom' && !/^\d{4}-\d{2}-\d{2}$/.test(this.customDate())) {
      errors['customDate'] = $localize`:@@token-form.err.date:Pick the expiry date.`;
    }
    const repositories = lines(this.repositories());
    if (this.restricted()) {
      if (repositories.length === 0) {
        errors['repositories'] = $localize`:@@token-form.err.repositories:Name at least one repository.`;
      }
      if (this.actions().length === 0) {
        errors['actions'] = $localize`:@@token-form.err.actions:Choose at least one action.`;
      }
    }
    this.localErrors.set(errors);
    if (Object.keys(errors).length > 0) return;

    const request: {
      description: string;
      selfRotatable: boolean;
      ttlSeconds?: number;
      expiresAt?: string;
      scope?: CreateTokenRequest['scope'];
    } = { description, selfRotatable: false };
    if (lifetime === 'never') request.ttlSeconds = 0;
    else if (lifetime === 'custom') request.expiresAt = `${this.customDate()}T23:59:59Z`;
    else if (lifetime in PRESET_SECONDS) request.ttlSeconds = PRESET_SECONDS[lifetime];
    if (this.restricted()) {
      const include = lines(this.packages());
      request.scope = {
        repositories: [
          {
            repositories,
            ...(include.length > 0 ? { packages: { include } } : {}),
            actions: [...this.actions()],
          },
        ],
      };
    } else {
      request.scope = { inherit: true };
    }
    this.submitted.emit(request);
  }
}

function lines(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l !== '');
}
