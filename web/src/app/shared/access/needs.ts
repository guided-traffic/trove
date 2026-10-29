import { Component, computed, inject, input } from '@angular/core';
import { SessionStore } from '../../core/session-store';

/**
 * The reason behind a disabled or missing control (R-UI-005): "Read only: changing this needs the
 * permission X". Renders nothing while the caller holds the capability. The API decides in any
 * case; this only tells the user why a control is not offered. Give `id` to the control's
 * `aria-describedby`.
 */
@Component({
  selector: 'trove-needs',
  template: `
    @if (missing()) {
      <p class="needs" role="note">
        <span class="material-icons" aria-hidden="true">lock</span>
        <span
          ><ng-container i18n="@@needs.text">Read only: changing this needs the permission</ng-container>{{ ' ' }}<code translate="no">{{ capability() }}</code></span
        >
      </p>
    }
  `,
  styles: `
    .needs {
      display: flex;
      align-items: center;
      gap: var(--trove-space-2);
      margin-block: var(--trove-space-2);
      color: var(--mat-sys-on-surface-variant);
    }
    .material-icons {
      font-size: 1.125rem;
    }
  `,
})
export class Needs {
  readonly capability = input.required<string>();

  private readonly session = inject(SessionStore);
  protected readonly missing = computed(() => !this.session.has(this.capability()));
}
