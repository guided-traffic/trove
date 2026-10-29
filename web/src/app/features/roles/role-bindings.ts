import { DatePipe } from '@angular/common';
import { Component, computed, inject, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { Role, RoleBinding, RoleName, RolesApi } from '../../api/authz-api';
import { collectAll } from '../../api/pagination';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirmer } from '../../shared/access/confirm-dialog';
import { TextField } from '../../shared/access/fields';
import { fieldErrorAt } from '../../shared/access/field-problem';
import { Needs } from '../../shared/access/needs';
import { toApiError } from '../../shared/access/paged-signal';
import { ErrorNotice } from '../../shared/error-notice';

/**
 * System roles and their bindings (R-UI-024, R-AUTHZ-022). The five roles are fixed (R-AUTHZ-010);
 * each shows its capabilities and who holds it, and offers bind and unbind with `roles:write`.
 * The last administrator binding cannot be removed: the button is disabled with the reason before
 * the API would answer 409 `last_admin` (R-UI-005). A write answers `Trove-Authz-Epoch`; the HTTP
 * layer sends it as `Trove-Authz-Min-Epoch` on the reads that follow (R-AUTHZ-032), and the
 * caller's own capabilities are read again because a binding may have changed them.
 */
@Component({
  selector: 'trove-role-bindings',
  imports: [DatePipe, MatButtonModule, ErrorNotice, Needs, TextField],
  templateUrl: './role-bindings.html',
  styleUrl: '../../shared/access/access.scss',
})
export class RoleBindings {
  private readonly api = inject(RolesApi);
  private readonly confirmer = inject(Confirmer);
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
      this.loadError.set(toApiError(e));
    }
  }

  protected holders(role: RoleName): readonly RoleBinding[] {
    return this.bindings().filter((b) => b.role === role);
  }

  protected isLastAdmin(b: RoleBinding): boolean {
    return b.role === 'admin' && this.adminHolders().length <= 1;
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
      text: $localize`:@@roles.unbind-text:${b.role}:role: will no longer apply to ${b.principal}:principal:.`,
      confirm: $localize`:@@roles.unbind:Unbind`,
    });
    if (answer.confirmed) {
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
      this.writeError.set(toApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}
