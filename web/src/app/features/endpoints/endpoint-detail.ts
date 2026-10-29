import { Component, computed, inject, input, resource, signal } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { RouterLink } from '@angular/router';
import { EndpointsApi, ExposureRule, ruleEtag } from '../../api/endpoints-api';
import { ApiError } from '../../api/problem';
import { RepositoriesApi } from '../../api/repositories-api';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { endpointAddress, patternSummary, versionSummary } from './endpoint-model';

/**
 * One endpoint (R-EP-017): its configuration read-only and marked as coming from the
 * configuration file, a warning when the mode is `all` (the rules have no effect there), and the
 * rule table with an enable switch per row (a `PATCH` with the row's ETag), the client address of
 * each exposed repository on this endpoint, and delete.
 */
@Component({
  selector: 'trove-endpoint-detail',
  imports: [MatButtonModule, RouterLink, CopyButton, ErrorNotice],
  templateUrl: './endpoint-detail.html',
  styleUrl: './endpoint-detail.scss',
})
export class EndpointDetail {
  protected readonly valueOf = valueOf;
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
    const summary = (valueOf(this.repos) ?? []).find((r) => r.key === rule.repository);
    const format = (valueOf(this.formats) ?? []).find((f) => f.name === summary?.format);
    return endpointAddress(configuration, format, rule.repository);
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

  protected readonly copyAddress = $localize`:@@ep.copy-address:Copy client address`;
}
