import { Component, computed, inject, input } from '@angular/core';
import { ProjectContext } from '../../core/project-context';
import { SessionStore } from '../../core/session-store';

/**
 * The reason behind a disabled or missing control (R-UI-005). Renders nothing while the caller holds the capability. The API
 * decides in any case; this only tells the user why a control is not offered. Give `id` to the control's `aria-describedby`.
 *
 * On a screen of a project (`project` given) the reason names whom to ask, the project's Owners by name, and no capability
 * (Q-UI-318): a member can ask a person, not a permission. Elsewhere, on the administrator's screens, it names the capability:
 * "Read only: changing this needs the permission X".
 */
@Component({
  selector: 'trove-needs',
  template: `
    @if (missing()) {
      <p class="needs" role="note">
        <span class="material-icons" aria-hidden="true">lock</span>
        @if (askText(); as ask) {
          <span>{{ ask }}</span>
        } @else {
          <span
            ><ng-container i18n="@@needs.text">Read only: changing this needs the permission</ng-container>{{ ' ' }}<code translate="no">{{ capability() }}</code></span
          >
        }
      </p>
    }
  `,
  styles: `
    .needs {
      display: flex;
      align-items: center;
      gap: var(--trove-space-2);
      margin-block: var(--trove-space-2);
      color: var(--trove-text-muted);
    }
    .material-icons {
      font-size: 1.125rem;
    }
  `,
})
export class Needs {
  readonly capability = input.required<string>();
  /** The key of the project whose screen this is; its Owners are named instead of the capability. */
  readonly project = input<string | null | undefined>(null);

  private readonly session = inject(SessionStore);
  private readonly projects = inject(ProjectContext);
  protected readonly missing = computed(() => !this.session.has(this.capability()));
  protected readonly askText = computed<string | null>(() => {
    const key = this.project();
    if (!key) return null;
    const names = (this.projects.projects().find((p) => p.key === key)?.owners ?? []).map((o) => o.replace(/^(user|group):/, ''));
    if (names.length === 0) return $localize`:@@needs.ask-owner:Read only. An Owner of ${key}:project: can change this.`;
    const owners = names.join(', ');
    return $localize`:@@needs.ask-owners:Read only. Ask ${owners}:owners:, an Owner of ${key}:project:, to change this.`;
  });
}
