import { CdkMenu, CdkMenuItem, CdkMenuTrigger } from '@angular/cdk/menu';
import { repositoryCommands } from '../../core/project-links';
import { Component, computed, inject, input, resource, signal } from '@angular/core';
import { RouterLink } from '@angular/router';
import { EndpointsApi, ExposureRule, ruleEtag } from '../../api/endpoints-api';
import { ApiError } from '../../api/problem';
import { RepositoriesApi, refOf } from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { CopyButton } from '../../shared/copy-button';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { Tooltip } from '../../shared/ui/tooltip';
import { ErrorNotice } from '../../shared/error-notice';
import { TextField } from '../../shared/access/fields';
import { Disclosure } from '../../shared/ui/disclosure';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { endpointAddress, patternSummary, versionSummary } from './endpoint-model';
import { PageHeader } from '../../shared/ui/page-header';
import { EmptyState } from '../../shared/ui/empty-state';

/**
 * One endpoint (R-EP-017): its configuration read-only and marked as coming from the
 * configuration file, a warning when the mode is `all` (the rules have no effect there), and the
 * rule list in the shared data table (Q-UI-331): what each rule exposes as columns, an enable switch per row (a `PATCH` with the
 * row's ETag), a copy button for the client address of the rule's repository on this endpoint, and a menu with edit and delete.
 */
@Component({
  selector: 'trove-endpoint-detail',
  imports: [
    CdkMenu,
    CdkMenuItem,
    CdkMenuTrigger,
    CopyButton,
    DataTable,
    EmptyState,
    PageHeader,
    RouterLink,
    ErrorNotice,
    Disclosure,
    TextField,
    Tooltip,
  ],
  templateUrl: './endpoint-detail.html',
  styleUrl: './endpoint-detail.scss',
})
export class EndpointDetail {
  protected readonly valueOf = valueOf;
  protected readonly repositoryLink = repositoryCommands;
  private readonly api = inject(EndpointsApi);
  private readonly repositories = inject(RepositoriesApi);
  private readonly session = inject(SessionStore);
  private readonly notifier = inject(Notifier);
  private readonly confirm = inject(Confirm);

  /** The route parameter `:endpoint`. */
  readonly endpoint = input.required<string>();

  protected readonly detail = resource({
    params: () => this.endpoint(),
    loader: ({ params }) => this.api.get(params),
  });
  protected readonly rules = resource({
    params: () => this.endpoint(),
    loader: ({ params }) => this.api.rules(params),
  });
  protected readonly formats = resource({ loader: () => this.repositories.formats() });
  protected readonly repos = resource({ loader: () => this.repositories.listAll() });

  protected readonly busy = signal<string | null>(null);
  protected readonly actionFailure = signal<ApiError | null>(null);

  protected readonly canWrite = computed(() => this.session.has('endpoints:write'));
  protected readonly failure = computed(() => {
    const e = this.detail.error();
    return e ? asApiError(e) : null;
  });
  protected readonly rulesFailure = computed(() => {
    const e = this.rules.error();
    return e ? asApiError(e) : null;
  });
  protected readonly modeAll = computed(
    () => valueOf(this.detail)?.configuration?.exposure === 'all',
  );
  protected readonly inert = computed(() => valueOf(this.detail)?.inert ?? false);

  protected addressOf(rule: ExposureRule): { url: string; served: boolean } | null {
    const configuration = valueOf(this.detail)?.configuration;
    if (!configuration) return null;
    const summary = (valueOf(this.repos) ?? []).find((r) => refOf(r) === rule.repository);
    const format = (valueOf(this.formats) ?? []).find((f) => f.name === summary?.format);
    return endpointAddress(configuration, format, rule.repository);
  }

  protected readonly rulesCaption = $localize`:@@ep.rules.caption:Exposure rules`;
  /**
   * Each width is the column's least (Q-UI-308); the rule takes what they leave. They sum to 63 rem; below 64 rem of the list
   * the versions and the pre-releases give their room (`narrow`) and the rest fits 48 rem, where the rows stack (Q-UI-331).
   */
  protected readonly ruleColumns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@ep.col.rule:Rule`, width: '9rem', prefer: '9rem' },
    { id: 'repository', label: $localize`:@@ep.rule.col.repository:Repository`, width: '9rem' },
    { id: 'patterns', label: $localize`:@@ep.rule.col.patterns:Packages and paths`, width: '8rem' },
    { id: 'versions', label: $localize`:@@ep.rule.col.versions:Versions`, width: '6rem', narrow: true },
    { id: 'prereleases', label: $localize`:@@ep.rule.col.prereleases:Pre-releases`, width: '6rem', narrow: true },
    { id: 'enabled', label: $localize`:@@ep.rule.col.enabled:Enabled`, width: '6.5rem', stackLabel: false },
    { id: 'actions', label: $localize`:@@repo.col.actions:Actions`, hideLabel: true, width: '4.5rem' },
  ];
  protected readonly notServed = $localize`:@@ep.address.not-served:This endpoint does not serve this address.`;

  /** The copy button's name and tooltip carry the address itself (Q-UI-330, Q-UI-331). */
  protected copyValueLabel(address: string): string {
    return $localize`:@@repo.copy-address-value:Copy client address ${address}:address:`;
  }

  protected actionsLabel(name: string): string {
    return $localize`:@@ep.rule.actions-of:Actions for ${name}:name:`;
  }

  /** The menu's "Copy client address": the same as the row's copy button. */
  protected async copyAddress(address: string): Promise<void> {
    try {
      await navigator.clipboard.writeText(address);
      this.notifier.status($localize`:@@copy.done:Copied to the clipboard.`);
    } catch {
      this.notifier.alert($localize`:@@copy.failed:The clipboard is not available. Select the text and copy it.`);
    }
  }

  protected hostsText(c: { hosts: readonly string[]; wildcardHost?: string | null }): string {
    return [...c.hosts, ...(c.wildcardHost ? [c.wildcardHost] : [])].join(', ');
  }

  protected credentialsText(c: { auth: { tokenTypes: readonly string[]; boundTokensOnly?: boolean; anonymous?: boolean } }): string {
    return `${c.auth.tokenTypes.join(', ')}${c.auth.boundTokensOnly ? ' (bound only)' : ''}${c.auth.anonymous ? ', anonymous' : ''}`;
  }

  protected rateLimitText(c: { rateLimit?: { class: string; requestsPerSecond: number; burst: number; key: string } | null }): string {
    const r = c.rateLimit;
    return r ? `${r.class}: ${r.requestsPerSecond}/s, burst ${r.burst}, per ${r.key}` : '–';
  }

  protected summary = patternSummary;
  protected versions = versionSummary;

  /** The switch of a row: a one-member merge patch with the row's ETag (Q-EP-18). */
  protected async toggle(rule: ExposureRule, input: HTMLInputElement): Promise<void> {
    const enabled = input.checked;
    this.busy.set(rule.name);
    this.actionFailure.set(null);
    try {
      await this.api.patchRule(this.endpoint(), rule.name, { enabled }, ruleEtag(rule));
      this.notifier.status(
        enabled
          ? $localize`:@@ep.rule.enabled:Rule ${rule.name}:name: enabled.`
          : $localize`:@@ep.rule.disabled:Rule ${rule.name}:name: disabled.`,
      );
    } catch (e) {
      input.checked = rule.enabled;
      const error = asApiError(e);
      if (error.status === 412) {
        this.notifier.alert(
          $localize`:@@ep.rule.changed:Rule ${rule.name}:name: was changed by someone else. The table shows the current state.`,
        );
      } else {
        this.actionFailure.set(error);
      }
    } finally {
      this.busy.set(null);
      this.rules.reload();
      this.detail.reload();
    }
  }

  protected async remove(rule: ExposureRule): Promise<void> {
    const answer = await this.confirm.ask({
      title: $localize`:@@ep.rule.delete.title:Delete rule ${rule.name}:name:?`,
      lines: [
        $localize`:@@ep.rule.delete.text:Content that only this rule exposes stops being retrievable on this endpoint.`,
      ],
      confirmLabel: $localize`:@@ep.rule.delete.confirm:Delete rule`,
    });
    if (answer === null) return;
    this.actionFailure.set(null);
    try {
      await this.api.deleteRule(this.endpoint(), rule.name, ruleEtag(rule));
      this.notifier.status($localize`:@@ep.rule.deleted:Rule ${rule.name}:name: deleted.`);
    } catch (e) {
      const error = asApiError(e);
      if (error.status === 412) {
        this.notifier.alert(
          $localize`:@@ep.rule.changed:Rule ${rule.name}:name: was changed by someone else. The table shows the current state.`,
        );
      } else {
        this.actionFailure.set(error);
      }
    } finally {
      this.rules.reload();
      this.detail.reload();
    }
  }

  protected toggleLabel(name: string): string {
    return $localize`:@@ep.rule.toggle:Enabled: ${name}:name:`;
  }

}
