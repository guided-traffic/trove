import { Component, computed, effect, inject, input, signal, untracked } from '@angular/core';
import { TabContent, TabGroup, TabPage } from '../../shared/ui/tabs';
import { Router, RouterLink } from '@angular/router';
import { CiApi, CiIssuer } from '../../api/ci-api';
import { ApiError } from '../../api/problem';
import { Notifier } from '../../core/notifier';
import { SessionStore } from '../../core/session-store';
import { Confirm } from '../../shared/confirm-dialog';
import { asApiError } from '../../shared/paged-resource';
import { ciRoutes } from './ci-common';
import { IssuanceList } from './issuance-list';
import { IssuerForm } from './issuer-form';
import { IssuerKeys } from './issuer-keys';
import { MappingList } from './mapping-list';
import { ErrorNotice } from '../../shared/error-notice';
import { Disclosure } from '../../shared/ui/disclosure';
import { PageHeader } from '../../shared/ui/page-header';

/** The tabs of the issuer page, in this order; the query parameter `tab` names the open one. */
export const ISSUER_TABS = ['settings', 'keys', 'mappings', 'issuances'] as const;
export type IssuerTab = (typeof ISSUER_TABS)[number];

/**
 * One CI issuer (R-UI-035): its settings (an editor for an issuer that API created, read-only with the
 * configuration named as the source for one declared in the Helm values), its signing keys, its mappings
 * in evaluation order and the tokens issued through it. The tab shows in the address (`?tab=`), so a link
 * opens it. Deleting asks for the name and says what goes with the issuer.
 */
@Component({
  selector: 'trove-ci-issuer-detail',
  imports: [PageHeader, TabContent, TabGroup, TabPage, RouterLink, ErrorNotice, IssuanceList, IssuerForm, IssuerKeys, MappingList, Disclosure],
  template: `
    <trove-page-header [heading]="issuer()" [name]="true">
      <span meta class="badge" i18n="@@ci.issuer.heading">CI issuer</span>
      <a actions class="btn btn-default" [routerLink]="testLink()" i18n="@@ci.issuer.test">Test with claims</a>
    </trove-page-header>

    @if (error(); as e) {
      <trove-error-notice [error]="e" (retry)="load()" />
    }
    @if (current(); as i) {
      <p class="muted" translate="no">{{ i.issuer }}</p>
      <trove-tab-group [selectedIndex]="index()" (selectedIndexChange)="select($event)">
        <trove-tab label="Settings" i18n-label="@@ci.issuer.tab.settings">
          <ng-template troveTabContent>
            <div class="settings">
              <trove-ci-issuer-form [issuer]="i" [etag]="etag()" (saved)="saved($event.value, $event.etag)" />
              @if (session.has('ci:write') && deletable()) {
                <trove-disclosure
                  i18n-heading="@@ci.issuer.advanced"
                  heading="Advanced"
                  i18n-hint="@@ci.issuer.advanced-hint"
                  hint="Delete this issuer."
                >
                  <p class="muted" i18n="@@ci.issuer.delete-note">Deleting the issuer revokes the tokens issued through it and deletes its mappings.</p>
                  <button class="btn btn-danger" type="button" [disabled]="busy()" (click)="remove()" i18n="@@ci.issuer.delete">Delete issuer</button>
                </trove-disclosure>
              }
            </div>
          </ng-template>
        </trove-tab>
        <trove-tab label="Keys" i18n-label="@@ci.issuer.tab.keys">
          <ng-template troveTabContent>
            <section aria-labelledby="ci-keys">
              <h2 id="ci-keys" i18n="@@ci.issuer.keys">Signing keys</h2>
              <trove-ci-issuer-keys [issuer]="i.name" />
            </section>
          </ng-template>
        </trove-tab>
        <trove-tab label="Mappings" i18n-label="@@ci.issuer.tab.mappings">
          <ng-template troveTabContent>
            <section aria-labelledby="ci-mappings">
              <h2 id="ci-mappings" i18n="@@ci.issuer.mappings">Mappings</h2>
              <trove-ci-mapping-list [issuer]="i.name" />
            </section>
          </ng-template>
        </trove-tab>
        <trove-tab label="Issuances" i18n-label="@@ci.issuer.tab.issuances">
          <ng-template troveTabContent>
            <section aria-labelledby="ci-issuances">
              <h2 id="ci-issuances" i18n="@@ci.issuer.issuances">Issued tokens</h2>
              <trove-ci-issuance-list [issuer]="i.name" />
            </section>
          </ng-template>
        </trove-tab>
      </trove-tab-group>
    }
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .settings {
      max-inline-size: var(--trove-settings-measure);
    }
  `,
})
export class IssuerDetail {
  /** Route parameter `:issuer`. */
  readonly issuer = input.required<string>();
  /** Query parameter `tab`. */
  readonly tab = input<string | undefined>();

  private readonly api = inject(CiApi);
  private readonly router = inject(Router);
  private readonly confirmer = inject(Confirm);
  private readonly notifier = inject(Notifier);
  protected readonly session = inject(SessionStore);

  protected readonly current = signal<CiIssuer | null>(null);
  protected readonly etag = signal('');
  protected readonly error = signal<ApiError | null>(null);
  protected readonly busy = signal(false);

  protected readonly index = computed(() => {
    const i = ISSUER_TABS.indexOf((this.tab() ?? 'mappings') as IssuerTab);
    return i < 0 ? 2 : i;
  });
  /** An issuer of the configuration is deletable only once it is no longer declared (R-CI-001). */
  protected readonly deletable = computed(() => {
    const i = this.current();
    return i !== null && (i.source !== 'configuration' || i.removedFromConfiguration);
  });

  constructor() {
    effect(() => {
      this.issuer();
      untracked(() => void this.load());
    });
  }

  protected testLink(): string[] {
    return ciRoutes.test(null, this.issuer());
  }

  protected async load(): Promise<void> {
    this.error.set(null);
    try {
      const loaded = await this.api.getIssuer(this.issuer());
      this.current.set(loaded.value);
      this.etag.set(loaded.etag);
    } catch (e) {
      this.current.set(null);
      this.error.set(asApiError(e));
    }
  }

  protected saved(value: CiIssuer, etag: string): void {
    this.current.set(value);
    this.etag.set(etag);
  }

  protected select(index: number): void {
    void this.router.navigate([], { queryParams: { tab: ISSUER_TABS[index] }, replaceUrl: true });
  }

  protected async remove(): Promise<void> {
    const i = this.current();
    if (!i) return;
    const count = i.mappingCount ?? 0;
    const answer = await this.confirmer.ask({
      title: $localize`:@@ci.issuer.delete-title:Delete issuer ${i.name}:name:?`,
      lines: [
        $localize`:@@ci.issuer.delete-text:The tokens issued through this issuer are revoked at once and no job of the instance can exchange a token any more.`,
        ...(count > 0
          ? [$localize`:@@ci.issuer.delete-mappings:Its ${count}:count: mappings and their principals are deleted with it.`]
          : []),
      ],
      confirmLabel: $localize`:@@ci.issuer.delete-confirm:Delete issuer`,
      typeToConfirm: i.name,
    });
    if (answer === null) return;
    this.busy.set(true);
    try {
      await this.api.deleteIssuer(i.name, count > 0);
      this.notifier.status($localize`:@@ci.issuer.deleted:CI issuer deleted.`);
      await this.router.navigate(['/admin/ci']);
    } catch (e) {
      this.error.set(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }
}

/** The form of a new issuer (R-UI-035): after the save the issuer's page opens. */
@Component({
  selector: 'trove-ci-issuer-create',
  imports: [PageHeader, IssuerForm],
  template: `
    <trove-page-header heading="New CI issuer" i18n-heading="@@ci.create.heading" />
    <div class="settings">
      <trove-ci-issuer-form (saved)="created($event.value.name)" />
    </div>
  `,
  styleUrl: '../../shared/access/access.scss',
  styles: `
    .settings {
      max-inline-size: var(--trove-settings-measure);
    }
  `,
})
export class IssuerCreate {
  private readonly router = inject(Router);

  protected created(name: string): void {
    void this.router.navigate(['/admin/ci', name], { queryParams: { tab: 'keys' } });
  }
}
