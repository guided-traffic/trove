import { Component, computed, input } from '@angular/core';
import { stateView } from './backups-model';

/**
 * The state of a backup run as the standard status badge (R-UI-048): an icon and the word, in the tone of
 * `stateView`. `showHint` adds the sentence that says what an `interrupted` or stopped run means.
 */
@Component({
  selector: 'trove-backup-state',
  template: `
    <span class="badge" [class]="view().tone">
      <span class="material-icons" aria-hidden="true">{{ view().icon }}</span>
      {{ view().text }}
    </span>
    @if (showHint() && view().hint; as hint) {
      <span class="hint">{{ hint }}</span>
    }
  `,
  styles: `
    .hint {
      display: block;
      margin-block-start: var(--trove-space-1);
      font-size: 0.8125rem;
      color: var(--trove-text-muted);
      white-space: normal;
    }
  `,
})
export class BackupState {
  readonly state = input.required<string>();
  readonly leaseExpired = input(false);
  readonly showHint = input(false);

  protected readonly view = computed(() => stateView(this.state(), this.leaseExpired()));
}
