import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { DatePipe } from '@angular/common';
import { MatButtonModule } from '@angular/material/button';
import { RouterLink } from '@angular/router';
import { RoleBinding, RoleName, RolesApi } from '../../api/authz-api';
import { collectAll } from '../../api/pagination';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { ErrorNotice } from '../error-notice';
import { Confirm } from '../confirm-dialog';
import { SelectField } from './fields';
import { asApiError } from '../paged-resource';

/** The five fixed system roles (R-AUTHZ-010). The API refuses any other. */
export const ROLE_NAMES: readonly RoleName[] = [
  'admin',
  'security-admin',
  'auditor',
  'repository-admin',
  'webhook-admin',
];

/**
 * The system roles bound to one principal (R-UI-024, R-AUTHZ-022), with bind and unbind where the
 * caller holds `roles:write`. The last-administrator guard (R-UI-005) is shown before the API
 * refuses it: when the principal is the only holder of `admin`, "Unbind" on that row is disabled
 * and says why. Every write answers `Trove-Authz-Epoch`; the HTTP layer sends it on the reads
 * that follow, so the list below shows the change at once (R-AUTHZ-032), and the caller's own
 * capabilities are read again because the caller may have changed their own.
 */
@Component({
  selector: 'trove-principal-roles',
  imports: [DatePipe, MatButtonModule, RouterLink, ErrorNotice, SelectField],
  template: `
    <section class="card" [attr.aria-labelledby]="headingId">
      <h2 [id]="headingId" i18n="@@principal-roles.heading">System roles</h2>
      @if (error(); as e) {
        <trove-error-notice [error]="e" (retry)="load()" />
      }
      @if (loaded()) {
        @if (bindings().length === 0) {
          <p i18n="@@principal-roles.none">No system role is bound to this principal.</p>
        } @else {
          <div class="scroll">
            <table class="data">
              <caption class="visually-hidden" i18n="@@principal-roles.caption">System roles of this principal</caption>
              <thead>
                <tr>
                  <th scope="col" i18n="@@principal-roles.role">Role</th>
                  <th scope="col" i18n="@@principal-roles.granted">Granted</th>
                  @if (canWrite()) {
                    <th scope="col"><span class="visually-hidden" i18n="@@principal-roles.actions">Actions</span></th>
                  }
                </tr>
              </thead>
              <tbody>
                @for (b of bindings(); track b.role) {
                  <tr>
                    <th scope="row" translate="no">{{ b.role }}</th>
                    <td>{{ b.grantedAt | date: 'medium' }}</td>
                    @if (canWrite()) {
                      <td class="actions">
                        <button
                          mat-button
                          type="button"
                          [disabled]="lastAdmin(b)"
                          [attr.aria-describedby]="lastAdmin(b) ? headingId + '-guard' : null"
                          (click)="unbind(b)"
                        >
                          <ng-container i18n="@@principal-roles.unbind">Unbind</ng-container>
                          <span class="visually-hidden" translate="no"> {{ b.role }}</span>
                        </button>
                      </td>
                    }
                  </tr>
                }
              </tbody>
            </table>
          </div>
          @if (hasGuard()) {
            <p class="muted" [id]="headingId + '-guard'" i18n="@@principal-roles.guard">
              The last administrator binding cannot be removed.
            </p>
          }
        }
        @if (canWrite() && free().length > 0) {
          <div class="bind">
            <trove-select-field
              i18n-label="@@principal-roles.bind-label"
              label="Role to bind"
              name="role"
              [options]="freeOptions()"
              [(value)]="chosen"
            />
            <button mat-flat-button type="button" [disabled]="busy()" (click)="bind()" i18n="@@principal-roles.bind">
              Bind role
            </button>
          </div>
        }
        <p><a routerLink="/admin/roles" i18n="@@principal-roles.link">All system roles</a></p>
      }
    </section>
  `,
  styleUrl: './access.scss',
})
export class PrincipalRoles {
  /** A principal reference: `user:<name>`, `group:<name>` or `sa:<name>`. */
  readonly principal = input.required<string>();

  private readonly api = inject(RolesApi);
  private readonly session = inject(SessionStore);
  private readonly confirmer = inject(Confirm);
  private readonly notifier = inject(Notifier);

  protected readonly headingId = `roles-${Math.random().toString(36).slice(2, 8)}`;
  protected readonly bindings = signal<readonly RoleBinding[]>([]);
  private readonly admins = signal<readonly RoleBinding[]>([]);
  protected readonly loaded = signal(false);
  protected readonly error = signal<ApiError | null>(null);
  protected readonly busy = signal(false);
  protected readonly chosen = signal('');

  protected readonly canWrite = computed(() => this.session.has('roles:write'));
  protected readonly free = computed(() => {
    const bound = new Set(this.bindings().map((b) => b.role));
    return ROLE_NAMES.filter((r) => !bound.has(r));
  });
  protected readonly freeOptions = computed(() => this.free().map((r) => ({ value: r, label: r })));
  protected readonly hasGuard = computed(() => this.bindings().some((b) => this.lastAdmin(b)));

  constructor() {
    effect(() => {
      this.principal();
      untracked(() => void this.load());
    });
    effect(() => {
      const options = this.free();
      if (!options.includes(this.chosen() as RoleName)) this.chosen.set(options[0] ?? '');
    });
  }

  /** The only `admin` binding that exists belongs to this principal. */
  protected lastAdmin(b: RoleBinding): boolean {
    const list = this.admins();
    return b.role === 'admin' && list.length <= 1 && list.every((a) => a.principal === b.principal);
  }

  protected async load(): Promise<void> {
    this.error.set(null);
    try {
      const principal = this.principal();
      this.bindings.set(await collectAll(this.api.bindings({ principal })));
      this.admins.set(await collectAll(this.api.bindings({ role: 'admin' })));
      this.loaded.set(true);
    } catch (e) {
      this.error.set(asApiError(e));
    }
  }

  protected async bind(): Promise<void> {
    const role = this.chosen() as RoleName;
    if (!role) return;
    await this.write(
      () => this.api.bind(role, this.principal()),
      $localize`:@@principal-roles.bound:Role bound.`,
    );
  }

  protected async unbind(b: RoleBinding): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@principal-roles.unbind-title:Unbind this role?`,
      lines: [$localize`:@@principal-roles.unbind-text:${b.role}:role: will no longer apply to ${b.principal}:principal:.`],
      confirmLabel: $localize`:@@principal-roles.unbind:Unbind`,
    });
    if (answer === null) return;
    await this.write(
      () => this.api.unbind(b.role, b.principal),
      $localize`:@@principal-roles.unbound:Role unbound.`,
    );
  }

  private async write(run: () => Promise<unknown>, done: string): Promise<void> {
    this.busy.set(true);
    this.error.set(null);
    try {
      await run();
      this.notifier.status(done);
      // The epoch of the write is on its way back in the HTTP layer; these reads carry it.
      await this.load();
      await this.session.refreshCapabilities();
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
