import { DatePipe, DecimalPipe, NgTemplateOutlet } from '@angular/common';
import {
  Component,
  DestroyRef,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
} from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatMenuModule } from '@angular/material/menu';
import { RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { JobReference } from '../../api/jobs-api';
import { ApiError } from '../../api/problem';
import { MirrorState, MirrorStatus, RepositoriesApi } from '../../api/repositories-api';
import { ErrorNotice } from '../../shared/error-notice';
import { jobStateText } from '../../shared/job-progress';
import { asApiError } from '../../shared/paged-resource';
import { formatSize } from '../packages/format-size';

/** The status is read again this often while a pass is in progress (R-UI-021, Q-UI-119). */
export const MIRROR_POLL_INTERVAL_MS = 5_000;

type Tone = 'ok' | 'warn' | 'bad' | 'neutral' | 'primary';

interface StateInfo {
  readonly label: string;
  readonly icon: string;
  readonly tone: Tone;
  readonly sentence: string;
}

/** What the panel says about the outcome of a run request (Q-UI-121). */
type RunNotice =
  | { readonly kind: 'started'; readonly job: JobReference; readonly full: boolean }
  | { readonly kind: 'running'; readonly job: JobReference | null }
  | { readonly kind: 'off' | 'paused' | 'not-applicable' };

/** The words of a state; the colour is a second signal, never the only one (R-UI-044). */
export function stateInfo(status: MirrorStatus): StateInfo {
  switch (status.state) {
    case 'off':
      return {
        label: $localize`:@@mirror.state.off:Off`,
        icon: 'power_settings_new',
        tone: 'neutral',
        sentence: $localize`:@@mirror.state.off.text:The repository is in pull-through mode: no pass runs.`,
      };
    case 'paused':
      return {
        label: $localize`:@@mirror.state.paused:Paused`,
        icon: 'pause_circle',
        tone: 'neutral',
        sentence: $localize`:@@mirror.state.paused.text:No pass starts. Resume the mirror to continue.`,
      };
    case 'running':
      return {
        label: $localize`:@@mirror.state.running:Running`,
        icon: 'sync',
        tone: 'primary',
        sentence: $localize`:@@mirror.state.running.text:A pass is in progress.`,
      };
    case 'waiting':
      return {
        label: $localize`:@@mirror.state.waiting:Waiting`,
        icon: 'hourglass_empty',
        tone: 'neutral',
        sentence: $localize`:@@mirror.state.waiting.text:A pass is in progress and waits for its next run, after a long rate-limit wait, a failure or a release.`,
      };
    case 'bound_reached':
      return {
        label: $localize`:@@mirror.state.bound:Bound reached`,
        icon: 'warning',
        tone: 'warn',
        sentence: $localize`:@@mirror.state.bound.text:A storage bound stops the additions of the mirror. Raise the bound or make room.`,
      };
    case 'failing':
      return {
        label: $localize`:@@mirror.state.failing:Failing`,
        icon: 'error',
        tone: 'bad',
        sentence: $localize`:@@mirror.state.failing.text:The last passes failed ${status.errors.consecutiveFailures}:count: times in a row.`,
      };
    default:
      return {
        label: $localize`:@@mirror.state.scheduled:Scheduled`,
        icon: status.lastCompletePass?.outcome === 'complete' ? 'check_circle' : 'schedule',
        tone: status.lastCompletePass?.outcome === 'complete' ? 'ok' : 'neutral',
        sentence: $localize`:@@mirror.state.scheduled.text:No pass is in progress; the next one starts when it is due.`,
      };
  }
}

/** The polling rule: a pass is in progress. */
export function mirrorActive(state: MirrorState): boolean {
  return state === 'running' || state === 'waiting';
}

function outcomeText(outcome: string): string {
  switch (outcome) {
    case 'complete':
      return $localize`:@@mirror.outcome.complete:Complete`;
    case 'partial':
      return $localize`:@@mirror.outcome.partial:Partial: some packages failed`;
    case 'bound_reached':
      return $localize`:@@mirror.outcome.bound:Stopped at a storage bound`;
    case 'offline':
      return $localize`:@@mirror.outcome.offline:Not run: the repository is offline`;
    case 'stopped':
      return $localize`:@@mirror.outcome.stopped:Stopped before the end`;
    default:
      return outcome;
  }
}

function kindText(kind: string): string {
  return kind === 'full'
    ? $localize`:@@mirror.kind.full:Full`
    : $localize`:@@mirror.kind.incremental:Incremental`;
}

function methodText(method: string | null): string {
  switch (method) {
    case 'feed':
      return $localize`:@@mirror.discovery.feed:Change feed`;
    case 'listing':
      return $localize`:@@mirror.discovery.listing:Listing`;
    case 'names':
      return $localize`:@@mirror.discovery.names:Names list`;
    default:
      return '–';
  }
}

function boundText(kind: string): string {
  switch (kind) {
    case 'bytes':
      return $localize`:@@mirror.bound.bytes:the storage limit of the mirror`;
    case 'quota':
      return $localize`:@@mirror.bound.quota:a quota of the repository`;
    default:
      return $localize`:@@mirror.bound.capacity:the capacity of the storage`;
  }
}

/**
 * The status of a remote repository's mirror (R-UI-021, R-REPO-056, Q-UI-104, Q-UI-105): the state in
 * words, the pass in progress, the last complete pass, the next pass, the last error, the rate-limit
 * waits and the bound, read from `GET /api/v1/repositories/{key}/mirror`. While a pass is in progress
 * the status is read again every few seconds; the polling ends when the panel is left. **Pause** and
 * **Resume** are the parent's patch of the document (`setPaused`); **Run a pass now** and, from its
 * menu, **Run a full pass** post `…/mirror/run` here.
 */
@Component({
  selector: 'trove-mirror-status',
  imports: [DatePipe, DecimalPipe, NgTemplateOutlet, MatButtonModule, MatMenuModule, RouterLink, ErrorNotice],
  template: `
    <section class="status" aria-labelledby="mirror-status-heading">
      <h2 id="mirror-status-heading" i18n="@@mirror.status.heading">Mirror status</h2>

      @if (failure(); as f) {
        <trove-error-notice [error]="f" (retry)="load()" />
      } @else if (status(); as s) {
        @if (info(); as i) {
          <p class="state" data-testid="mirror-state" [attr.data-state]="s.state">
            <span class="chip" [class.ok]="i.tone === 'ok'" [class.warn]="i.tone === 'warn'" [class.bad]="i.tone === 'bad'" [class.primary]="i.tone === 'primary'">
              <span class="material-icons inline" aria-hidden="true">{{ i.icon }}</span>
              <strong>{{ i.label }}</strong>
            </span>
            <span class="sentence">{{ i.sentence }}</span>
          </p>
        }

        @if (canChange()) {
          <div class="buttons">
            @if (s.state === 'paused') {
              <button mat-stroked-button type="button" [disabled]="busy()" (click)="setPaused.emit(false)">
                <ng-container i18n="@@mirror.resume">Resume</ng-container>
              </button>
            } @else {
              <button mat-stroked-button type="button" [disabled]="busy() || s.state === 'off'" (click)="setPaused.emit(true)">
                <ng-container i18n="@@mirror.pause">Pause</ng-container>
              </button>
            }
            <span class="split">
              <button mat-flat-button type="button" [disabled]="requesting()" (click)="run(false)">
                <ng-container i18n="@@mirror.run">Run a pass now</ng-container>
              </button>
              <button
                mat-flat-button
                type="button"
                class="more"
                [disabled]="requesting()"
                [matMenuTriggerFor]="runMenu"
                aria-label="More ways to run a pass"
                i18n-aria-label="@@mirror.run.more"
              >
                <span class="material-icons" aria-hidden="true">arrow_drop_down</span>
              </button>
              <mat-menu #runMenu="matMenu">
                <button mat-menu-item type="button" (click)="run(true)">
                  <ng-container i18n="@@mirror.run.full">Run a full pass</ng-container>
                </button>
              </mat-menu>
            </span>
          </div>
        }

        <div class="notices" role="status" aria-live="polite">
          @if (notice(); as n) {
            @switch (n.kind) {
              @case ('started') {
                <p>
                  <span class="material-icons inline" aria-hidden="true">play_circle</span>
                  @if (n.full) {
                    <ng-container i18n="@@mirror.run.started-full">A full pass is queued.</ng-container>
                  } @else {
                    <ng-container i18n="@@mirror.run.started">A pass is queued.</ng-container>
                  }&ngsp;
                  <ng-container *ngTemplateOutlet="jobLink; context: { job: n.job }" />
                </p>
              }
              @case ('running') {
                <p>
                  <span class="material-icons inline" aria-hidden="true">sync</span>
                  <ng-container i18n="@@mirror.run.running">A pass is already queued or running.</ng-container>&ngsp;
                  @if (n.job; as job) {
                    <ng-container *ngTemplateOutlet="jobLink; context: { job }" />
                  }
                </p>
              }
              @case ('off') {
                <p class="field-error">
                  <span class="material-icons inline" aria-hidden="true">error</span>
                  <ng-container i18n="@@mirror.run.off">The repository is in pull-through mode; there is no mirror to run. Choose mode mirror and save first.</ng-container>
                </p>
              }
              @case ('paused') {
                <p class="field-error">
                  <span class="material-icons inline" aria-hidden="true">error</span>
                  <ng-container i18n="@@mirror.run.paused">The mirror is paused. Resume it to run a pass.</ng-container>
                </p>
              }
              @case ('not-applicable') {
                <p class="field-error">
                  <span class="material-icons inline" aria-hidden="true">error</span>
                  <ng-container i18n="@@mirror.run.not-applicable">Only a remote repository has a mirror.</ng-container>
                </p>
              }
            }
          }
          @if (runFailure(); as f) {
            <trove-error-notice [error]="f" />
          }
        </div>

        @if (s.pass; as p) {
          <h3 i18n="@@mirror.pass.heading">Pass in progress</h3>
          <dl class="facts" data-testid="mirror-pass">
            <dt i18n="@@mirror.pass.kind">Kind</dt>
            <dd>{{ kind(p.kind) }}</dd>
            <dt i18n="@@mirror.pass.method">Discovery</dt>
            <dd>{{ method(p.method) }}</dd>
            <dt i18n="@@mirror.pass.started">Started</dt>
            <dd><time [attr.datetime]="p.startedAt">{{ p.startedAt | date: 'medium' }}</time></dd>
            <dt i18n="@@mirror.pass.names">Names discovered</dt>
            <dd>{{ p.namesDiscovered | number }}</dd>
            <dt i18n="@@mirror.pass.done">Packages done</dt>
            <dd>{{ p.packagesDone | number }}</dd>
            <dt i18n="@@mirror.pass.pending">Packages pending</dt>
            <dd>{{ p.packagesPending | number }}</dd>
            <dt i18n="@@mirror.pass.failed">Packages failed</dt>
            <dd>{{ p.packagesFailed | number }}</dd>
            <dt i18n="@@mirror.pass.items">Items stored</dt>
            <dd>{{ p.itemsStored | number }}</dd>
            <dt i18n="@@mirror.pass.bytes">Bytes stored</dt>
            <dd [attr.title]="(p.bytesStored | number) + ' B'">{{ size(p.bytesStored) }}</dd>
            <dt i18n="@@mirror.pass.discovery-done">Discovery</dt>
            <dd>
              @if (p.discoveryDone) {
                <ng-container i18n="@@mirror.pass.discovery-finished">finished</ng-container>
              } @else {
                <ng-container i18n="@@mirror.pass.discovery-running">still running</ng-container>
              }
            </dd>
            <dt i18n="@@mirror.pass.job">Job</dt>
            <dd>
              @if (p.job; as job) {
                <ng-container *ngTemplateOutlet="jobLink; context: { job }" />
              } @else {
                <span i18n="@@mirror.pass.no-job">The job has been removed.</span>
              }
            </dd>
          </dl>
        }

        <h3 i18n="@@mirror.last.heading">Last complete pass</h3>
        @if (s.lastCompletePass; as l) {
          <dl class="facts" data-testid="mirror-last-pass">
            <dt i18n="@@mirror.last.outcome">Outcome</dt>
            <dd>{{ outcome(l.outcome) }}</dd>
            <dt i18n="@@mirror.pass.kind">Kind</dt>
            <dd>{{ kind(l.kind) }}</dd>
            <dt i18n="@@mirror.pass.method">Discovery</dt>
            <dd>{{ method(l.method) }}</dd>
            <dt i18n="@@mirror.last.started">Started</dt>
            <dd><time [attr.datetime]="l.startedAt">{{ l.startedAt | date: 'medium' }}</time></dd>
            <dt i18n="@@mirror.last.finished">Finished</dt>
            <dd><time [attr.datetime]="l.finishedAt">{{ l.finishedAt | date: 'medium' }}</time></dd>
            <dt i18n="@@mirror.last.packages">Packages</dt>
            <dd>{{ l.packages | number }}</dd>
            <dt i18n="@@mirror.pass.failed">Packages failed</dt>
            <dd>{{ l.packagesFailed | number }}</dd>
            <dt i18n="@@mirror.pass.items">Items stored</dt>
            <dd>{{ l.itemsStored | number }}</dd>
            <dt i18n="@@mirror.pass.bytes">Bytes stored</dt>
            <dd [attr.title]="(l.bytesStored | number) + ' B'">{{ size(l.bytesStored) }}</dd>
            <dt i18n="@@mirror.last.discovery">Discovery saw everything</dt>
            <dd>
              @if (l.discoveryComplete) {
                <ng-container i18n="@@mirror.last.yes">Yes</ng-container>
              } @else {
                <span class="warn">
                  <span class="material-icons inline" aria-hidden="true">warning</span>
                  <ng-container i18n="@@mirror.last.no">No</ng-container>
                </span>
                @if (l.discoveryNote) {
                  : {{ l.discoveryNote }}
                }
              }
            </dd>
          </dl>
        } @else {
          <p class="hint" i18n="@@mirror.last.none">No pass has completed yet.</p>
        }

        <h3 i18n="@@mirror.next.heading">Next pass</h3>
        <dl class="facts" data-testid="mirror-next">
          <dt i18n="@@mirror.next.at">Due</dt>
          <dd>
            @if (s.state === 'paused' || s.state === 'off') {
              <ng-container i18n="@@mirror.next.none">No pass is planned.</ng-container>
            } @else if (s.nextPassAt; as at) {
              <time [attr.datetime]="at">{{ at | date: 'medium' }}</time>
            } @else {
              <ng-container i18n="@@mirror.next.dispatch">At the next dispatch, within a minute.</ng-container>
            }
          </dd>
          <dt i18n="@@mirror.next.full">Last full pass</dt>
          <dd>
            @if (s.lastFullPassAt; as at) {
              <time [attr.datetime]="at">{{ at | date: 'medium' }}</time>
            } @else {
              –
            }
          </dd>
        </dl>

        <h3 i18n="@@mirror.error.heading">Last error</h3>
        @if (s.errors.lastCode || s.errors.lastMessage) {
          <dl class="facts" data-testid="mirror-error">
            <dt i18n="@@mirror.error.code">Code</dt>
            <dd><code translate="no">{{ s.errors.lastCode ?? '–' }}</code></dd>
            <dt i18n="@@mirror.error.message">Message</dt>
            <dd>{{ s.errors.lastMessage ?? '–' }}</dd>
            <dt i18n="@@mirror.error.at">Time</dt>
            <dd>
              @if (s.errors.lastAt; as at) {
                <time [attr.datetime]="at">{{ at | date: 'medium' }}</time>
              } @else {
                –
              }
            </dd>
            <dt i18n="@@mirror.error.failures">Failures in a row</dt>
            <dd>{{ s.errors.consecutiveFailures }}</dd>
          </dl>
        } @else {
          <p class="hint" i18n="@@mirror.error.none">No error.</p>
        }

        <h3 i18n="@@mirror.rate.heading">Rate-limit waits</h3>
        <dl class="facts" data-testid="mirror-rate">
          <dt i18n="@@mirror.rate.waits">Waits</dt>
          <dd>{{ s.rateLimit.waits | number }}</dd>
          <dt i18n="@@mirror.rate.seconds">Seconds waited</dt>
          <dd>{{ s.rateLimit.waitSeconds | number }}</dd>
          <dt i18n="@@mirror.rate.last">Last answer</dt>
          <dd>
            @if (s.rateLimit.lastStatus !== null) {
              HTTP {{ s.rateLimit.lastStatus }}
              @if (s.rateLimit.lastAt; as at) {
                <ng-container i18n="@@mirror.rate.at">at</ng-container>&ngsp;<time [attr.datetime]="at">{{ at | date: 'medium' }}</time>
              }
            } @else {
              –
            }
          </dd>
          @if (s.rateLimit.until; as until) {
            <dt i18n="@@mirror.rate.until">Waiting until</dt>
            <dd><time [attr.datetime]="until">{{ until | date: 'medium' }}</time></dd>
          }
        </dl>

        <h3 i18n="@@mirror.bound.heading">Bound</h3>
        <dl class="facts" data-testid="mirror-bound">
          <dt i18n="@@mirror.bound.max">Storage limit</dt>
          <dd>
            @if (s.bound.maxBytes !== null) {
              <span [attr.title]="(s.bound.maxBytes | number) + ' B'">{{ size(s.bound.maxBytes) }}</span>
            } @else {
              <ng-container i18n="@@mirror.bound.unlimited">No limit of its own</ng-container>
            }
          </dd>
          <dt i18n="@@mirror.bound.used">Stored</dt>
          <dd [attr.title]="(s.bound.usedBytes | number) + ' B'">{{ size(s.bound.usedBytes) }}</dd>
          <dt i18n="@@mirror.bound.reached">Reached</dt>
          <dd>
            @if (s.bound.reached; as kind) {
              <span class="warn">
                <span class="material-icons inline" aria-hidden="true">warning</span>
                <ng-container i18n="@@mirror.bound.reached-by">Yes, by</ng-container>&ngsp;{{ boundKind(kind) }}
              </span>
              @if (s.bound.at; as at) {
                &ngsp;<ng-container i18n="@@mirror.rate.at">at</ng-container>&ngsp;<time [attr.datetime]="at">{{ at | date: 'medium' }}</time>
              }
            } @else {
              <ng-container i18n="@@mirror.bound.no">No</ng-container>
            }
          </dd>
        </dl>
      } @else {
        <p role="status" i18n="@@mirror.status.loading">Loading the status.</p>
      }
    </section>

    <ng-template #jobLink let-job="job">
      <a routerLink="/admin/system/jobs" [queryParams]="{ kind: 'mirror-pass' }">
        <ng-container i18n="@@mirror.job.link">Open the mirror jobs</ng-container>
      </a>
      (<span i18n="@@mirror.job.is">job</span>&ngsp;<code translate="no">{{ job.id }}</code>,&ngsp;{{ jobState(job) }})
    </ng-template>
  `,
  styles: `
    .status {
      margin-block-end: var(--trove-space-6);
    }
    h3 {
      margin-block: var(--trove-space-4) var(--trove-space-2);
      font: var(--mat-sys-title-small);
    }
    .state {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--trove-space-3);
      margin-block: var(--trove-space-2);
    }
    .chip {
      display: inline-flex;
      align-items: center;
      padding: var(--trove-space-1) var(--trove-space-3);
      border: 1px solid currentColor;
      border-radius: var(--mat-sys-corner-full);
    }
    .chip.primary {
      color: var(--mat-sys-primary);
    }
    .buttons {
      display: flex;
      flex-wrap: wrap;
      gap: var(--trove-space-3);
      margin-block: var(--trove-space-3);
    }
    .split {
      display: inline-flex;
      gap: 2px;
    }
    .notices p {
      margin-block: var(--trove-space-2);
    }
  `,
})
export class MirrorStatusPanel {
  private readonly api = inject(RepositoriesApi);
  private readonly destroyed = inject(DestroyRef);

  readonly key = input.required<string>();
  /** May pause, resume and request a pass: the same permission as saving the repository (Q-UI-120). */
  readonly canChange = input(false);
  /** A pause or resume is being written. */
  readonly busy = input(false);
  /** Changes when the parent changed the document (a pause, a resume, a save): the status is read again. */
  readonly refresh = input(0);
  readonly intervalMs = input(MIRROR_POLL_INTERVAL_MS);
  /** The user asked to pause (`true`) or resume (`false`); the parent patches the document with its `If-Match`. */
  readonly setPaused = output<boolean>();

  protected readonly status = signal<MirrorStatus | null>(null);
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly requesting = signal(false);
  protected readonly notice = signal<RunNotice | null>(null);
  protected readonly runFailure = signal<ApiError | null>(null);
  protected readonly info = computed(() => {
    const s = this.status();
    return s ? stateInfo(s) : null;
  });

  private generation = 0;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor() {
    this.destroyed.onDestroy(() => this.stop());
    effect(() => {
      this.key();
      this.refresh();
      untracked(() => this.load());
    });
  }

  protected kind = kindText;
  protected method = methodText;
  protected outcome = outcomeText;
  protected boundKind = boundText;
  protected size = formatSize;
  protected jobState(job: JobReference): string {
    return jobStateText(job.state);
  }

  protected load(): void {
    this.stop();
    this.failure.set(null);
    void this.poll(++this.generation);
  }

  private stop(): void {
    this.generation++;
    clearTimeout(this.timer);
  }

  private async poll(run: number): Promise<void> {
    try {
      const status = await this.api.mirrorStatus(this.key());
      if (run !== this.generation) return;
      this.status.set(status);
      if (mirrorActive(status.state)) {
        this.timer = setTimeout(() => void this.poll(run), this.intervalMs());
      }
    } catch (e) {
      if (run === this.generation) this.failure.set(asApiError(e));
    }
  }

  protected async run(full: boolean): Promise<void> {
    if (this.requesting()) return;
    this.requesting.set(true);
    this.notice.set(null);
    this.runFailure.set(null);
    try {
      const job = await this.api.runMirror(this.key(), full, newIdempotencyKey());
      this.notice.set({ kind: 'started', job, full });
    } catch (e) {
      this.runError(asApiError(e));
    } finally {
      this.requesting.set(false);
    }
    this.load();
  }

  /** The refusals of the run operation are answers, not failures (Q-UI-121). */
  private runError(error: ApiError): void {
    switch (error.code) {
      case 'mirror_pass_running': {
        const job = error.problem?.['job'] as JobReference | undefined;
        this.notice.set({ kind: 'running', job: job ?? null });
        return;
      }
      case 'mirror_off':
        this.notice.set({ kind: 'off' });
        return;
      case 'mirror_paused':
        this.notice.set({ kind: 'paused' });
        return;
      case 'not_applicable':
        this.notice.set({ kind: 'not-applicable' });
        return;
      default:
        this.runFailure.set(error);
    }
  }
}
