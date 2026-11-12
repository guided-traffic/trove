import { DatePipe } from '@angular/common';
import { Component, computed, inject, signal } from '@angular/core';
import { Role, RoleBinding, RoleName, RolesApi } from '../../api/authz-api';
import { collectAll } from '../../api/pagination';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/field-errors';
import { Needs } from '../../shared/access/needs';
import { asApiError } from '../../shared/paged-resource';
import { ErrorNotice } from '../../shared/error-notice';
import { PageHeader } from '../../shared/ui/page-header';
import { DataTable, TableColumn } from '../../shared/ui/data-table';

/**
 * System roles and their bindings (R-UI-024, R-AUTHZ-022). The five roles are fixed (R-AUTHZ-010);
 * each shows its capabilities and who holds it, and offers bind and unbind with `roles:write`.
 * The last administrator binding cannot be removed: the button is disabled with the reason before
 * the API would answer 409 `last_admin` (R-UI-005). A binding declared in the system configuration
 * (`source: configuration`, R-AUTHZ-010, Q-AUTHZ-170) has no unbind control and a hint where it is
 * changed, before the API would answer 409 `role_binding_managed_by_configuration`. A write
 * answers `Trove-Authz-Epoch`; the HTTP layer sends it as `Trove-Authz-Min-Epoch` on the reads
 * that follow (R-AUTHZ-032), and the caller's own capabilities are read again because a binding
 * may have changed them.
 */
@Component({
  selector: 'trove-role-bindings',
  imports: [DataTable, PageHeader, DatePipe, ErrorNotice, Needs, TextField],
  templateUrl: './role-bindings.html',
  styleUrl: '../../shared/access/access.scss',
  styles: `
    /* A role's capabilities as a list in columns, one capability a line (UI-A-47). */
    .caps-heading {
      margin: var(--trove-space-2) 0 var(--trove-space-1);
      font-size: var(--trove-text-size);
      font-weight: 600;
    }
    .caps {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(14rem, 1fr));
      gap: var(--trove-space-1) var(--trove-space-4);
      margin: 0 0 var(--trove-space-3);
      padding: 0;
      list-style: none;
    }
  `,
})
export class RoleBindings {
  private readonly api = inject(RolesApi);
  private readonly confirmer = inject(Confirm);
  private readonly notifier = inject(Notifier);
  protected readonly session = inject(SessionStore);

  protected readonly roles = signal<readonly Role[]>([]);
  protected readonly bindings = signal<readonly RoleBinding[]>([]);
  protected readonly loaded = signal(false);
  protected readonly loadError = signal<ApiError | null>(null);
  protected readonly writeError = signal<ApiError | null>(null);
  protected readonly busy = signal(false);
  /** The principal reference typed per role. */
  protected readonly drafts = signal<Readonly<Record<string, string>>>({});

  protected readonly canWrite = computed(() => this.session.has('roles:write'));
  /** The holders of a role in the shared data table, which stacks below 48 rem (no sideways scroll at 390 px). */
  protected readonly bindingColumns = computed<readonly TableColumn[]>(() => [
    { id: 'principal', label: $localize`:@@roles.principal:Principal`, width: '10rem' },
    { id: 'granted', label: $localize`:@@roles.granted-at:Granted`, width: '10rem' },
    { id: 'grantedBy', label: $localize`:@@roles.granted-by:Granted by`, width: '8rem' },
    { id: 'source', label: $localize`:@@roles.source:Source`, width: '6rem' },
    ...(this.canWrite() ? [{ id: 'actions', label: $localize`:@@roles.actions:Actions`, hideLabel: true }] : []),
  ]);
  protected holdersCaption(role: string): string {
    return $localize`:@@roles.holders-caption:Principals that hold the role ${role}:role:`;
  }
  private readonly adminHolders = computed(() => this.bindings().filter((b) => b.role === 'admin'));

  constructor() {
    void this.load();
  }

  protected async load(): Promise<void> {
    this.loadError.set(null);
    try {
      this.roles.set(await this.api.roles());
      this.bindings.set(await collectAll(this.api.bindings({})));
      this.loaded.set(true);
    } catch (e) {
      this.loadError.set(asApiError(e));
    }
  }

  protected holders(role: RoleName): readonly RoleBinding[] {
    return this.bindings().filter((b) => b.role === role);
  }

  protected isLastAdmin(b: RoleBinding): boolean {
    return b.role === 'admin' && this.adminHolders().length <= 1;
  }

  /** A binding `authz.role_bindings` declares: the API refuses to remove it (R-AUTHZ-010, Q-AUTHZ-170). */
  protected fromConfiguration(b: RoleBinding): boolean {
    return b.source === 'configuration';
  }

  protected hasConfigured(role: RoleName): boolean {
    return this.holders(role).some((b) => this.fromConfiguration(b));
  }

  protected draft(role: RoleName): string {
    return this.drafts()[role] ?? '';
  }

  protected setDraft(role: RoleName, value: string): void {
    this.drafts.update((d) => ({ ...d, [role]: value }));
  }

  protected fieldError(): string | null {
    return fieldErrorAt(this.writeError(), '/principal');
  }

  protected async bind(event: Event, role: RoleName): Promise<void> {
    event.preventDefault();
    const principal = this.draft(role).trim();
    if (!principal) return;
    await this.write(async () => {
      await this.api.bind(role, principal);
      this.setDraft(role, '');
    }, $localize`:@@roles.bound:Role bound.`);
  }

  protected async unbind(b: RoleBinding): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@roles.unbind-title:Unbind this role?`,
      lines: [$localize`:@@roles.unbind-text:${b.role}:role: will no longer apply to ${b.principal}:principal:.`],
      confirmLabel: $localize`:@@roles.unbind:Unbind`,
    });
    if (answer !== null) {
      await this.write(() => this.api.unbind(b.role, b.principal), $localize`:@@roles.unbound:Role unbound.`);
    }
  }

  private async write(run: () => Promise<void>, done: string): Promise<void> {
    this.busy.set(true);
    this.writeError.set(null);
    try {
      await run();
      this.notifier.status(done);
      await this.load();
      await this.session.refreshCapabilities();
    } catch (e) {
      this.writeError.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
