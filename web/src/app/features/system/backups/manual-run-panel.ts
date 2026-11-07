import { Component, computed, input } from '@angular/core';
import { BackupSummary } from '../../../api/backups-api';
import { CopyButton } from '../../../shared/copy-button';
import { manualRunCommand } from './backups-model';

/**
 * "Start a backup now" (R-UI-048): no button starts a backup, because the management API has no write operation (Q-API-10).
 * The panel shows the `kubectl` command that creates a Job from the CronJob, filled from `manualRun`, with a copy button. The
 * Unix time in the Job name is taken when the panel is created, so the name is new each time the screen opens; copying
 * twice from one visit gives the same name, which Kubernetes refuses the second time (the first Job exists).
 */
@Component({
  selector: 'trove-manual-run-panel',
  imports: [CopyButton],
  template: `
    <section class="card" aria-labelledby="bk-manual">
      <h2 id="bk-manual" i18n="@@backups.manual.heading">Start a backup now</h2>
      <p i18n="@@backups.manual.intro">
        No button on this screen starts a backup. Run this command with access to the cluster:
      </p>
      <div class="command">
        <pre class="code-block" translate="no" tabindex="0">{{ command() }}</pre>
        <trove-copy-button
          [value]="command()"
          [label]="copyLabel"
        />
      </div>
      @if (placeholders()) {
        <p class="hint" i18n="@@backups.manual.placeholders">
          The installation does not say its namespace or its CronJob name. Replace the placeholders in angle brackets.
        </p>
      }
      <ul>
        <li i18n="@@backups.manual.appears">The run appears in the list below once its pod has started.</li>
        <li i18n="@@backups.manual.trigger">
          It records the trigger schedule, like a scheduled run, and is told apart by its pod name, which is the name of the Job.
        </li>
        <li i18n="@@backups.manual.one-at-a-time">
          One backup runs at a time. A run started while another is active ends without writing.
        </li>
      </ul>
    </section>
  `,
  styles: `
    .command {
      display: flex;
      align-items: flex-start;
      gap: var(--trove-space-2);

      pre {
        flex: 1;
        min-inline-size: 0;
        margin: 0;
        overflow-x: auto;
      }
    }
  `,
})
export class ManualRunPanel {
  readonly manualRun = input.required<BackupSummary['manualRun']>();

  protected readonly copyLabel = $localize`:@@backups.manual.copy:Copy the command`;
  private readonly openedAt = Math.floor(Date.now() / 1000);

  protected readonly command = computed(() => manualRunCommand(this.manualRun(), this.openedAt));
  protected readonly placeholders = computed(
    () => this.manualRun().namespace === null || this.manualRun().cronJob === null,
  );
}
