import { BackupRun, BackupSummary, BACKUP_ACTIVE_STATES } from '../../../api/backups-api';

/** How a status badge looks: the class of `.badge`, the Material icon and the word (R-UI-048: icon and text). */
export interface StateView {
  readonly tone: 'ok' | 'warn' | 'bad' | 'info' | '';
  readonly icon: string;
  readonly text: string;
  /** A sentence under the badge where the state alone does not say what it means. */
  readonly hint: string | null;
}

/**
 * The badge of a run (R-UI-048, Q-BAK-37). Green only for a run that ended well; a run in progress is blue; `interrupted`
 * is neutral because a drain is not a failure (R-BAK-012); a run whose lease has passed reads as stopped, in a warning
 * colour, because its process is gone but the row does not say so yet (R-BAK-010).
 */
export function stateView(state: string, leaseExpired: boolean): StateView {
  if (leaseExpired && BACKUP_ACTIVE_STATES.includes(state)) {
    return {
      tone: 'warn',
      icon: 'report_problem',
      text: $localize`:@@backups.state.stopped:Stopped`,
      hint: $localize`:@@backups.state.stopped-hint:Stopped without finishing; the next run marks it abandoned.`,
    };
  }
  switch (state) {
    case 'starting':
      return active($localize`:@@backups.state.starting:Starting`);
    case 'snapshot':
      return active($localize`:@@backups.state.snapshot:Snapshot`);
    case 'copying':
      return active($localize`:@@backups.state.copying:Copying`);
    case 'finalizing':
      return active($localize`:@@backups.state.finalizing:Finalizing`);
    case 'pruning':
      return active($localize`:@@backups.state.pruning:Pruning`);
    case 'completed':
      return { tone: 'ok', icon: 'check_circle', text: $localize`:@@backups.state.completed:Completed`, hint: null };
    case 'completed_with_warnings':
      return {
        tone: 'warn',
        icon: 'warning',
        text: $localize`:@@backups.state.completed-with-warnings:Completed with warnings`,
        hint: null,
      };
    case 'failed':
      return { tone: 'bad', icon: 'error', text: $localize`:@@backups.state.failed:Failed`, hint: null };
    case 'interrupted':
      return {
        tone: '',
        icon: 'pause_circle',
        text: $localize`:@@backups.state.interrupted:Interrupted`,
        hint: $localize`:@@backups.state.interrupted-hint:Interrupted by a drain or eviction; the Job retries it.`,
      };
    case 'abandoned':
      return { tone: 'bad', icon: 'error', text: $localize`:@@backups.state.abandoned:Abandoned`, hint: null };
    default:
      return { tone: '', icon: 'help', text: state, hint: null };
  }
}

function active(text: string): StateView {
  return { tone: 'info', icon: 'autorenew', text, hint: null };
}

/** The translated name of an operation. */
export function operationText(operation: string): string {
  switch (operation) {
    case 'create':
      return $localize`:@@backups.op.create:Create`;
    case 'delete':
      return $localize`:@@backups.op.delete:Delete`;
    case 'verify':
      return $localize`:@@backups.op.verify:Verify`;
    default:
      return operation;
  }
}

/** The translated name of a phase of a running create (R-BAK-009). */
export function phaseText(phase: string | undefined): string {
  switch (phase) {
    case 'starting':
      return $localize`:@@backups.phase.starting:starting`;
    case 'snapshot':
      return $localize`:@@backups.phase.snapshot:snapshot`;
    case 'copying':
      return $localize`:@@backups.phase.copying:copying`;
    case 'finalizing':
      return $localize`:@@backups.phase.finalizing:finalizing`;
    case 'pruning':
      return $localize`:@@backups.phase.pruning:pruning`;
    default:
      return phase ?? '–';
  }
}

/** A length of time as `1 d 4 h`, `2 h 5 min`, `7 min` or `12 s`; the units are abbreviations that need no translation. */
export function durationText(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  const d = Math.floor(s / 86_400);
  const h = Math.floor((s % 86_400) / 3_600);
  const m = Math.floor((s % 3_600) / 60);
  if (d > 0) return h > 0 ? `${d} d ${h} h` : `${d} d`;
  if (h > 0) return m > 0 ? `${h} h ${m} min` : `${h} h`;
  if (m > 0) return `${m} min`;
  return `${s} s`;
}

/** The seconds a run took: to its end, or to `now` while it runs; `null` for a timestamp that does not parse. */
export function runSeconds(run: BackupRun, now: number): number | null {
  const start = Date.parse(run.startedAt);
  const end = run.finishedAt === null ? now : Date.parse(run.finishedAt);
  return Number.isNaN(start) || Number.isNaN(end) ? null : (end - start) / 1000;
}

/** A number the result carries under `key`, or `null` when the run's result has none (a run that failed has no result). */
export function resultNumber(run: BackupRun, key: string): number | null {
  const v = run.result?.[key];
  return typeof v === 'number' ? v : null;
}

/** The warnings of a run's result (R-BAK-017: a sweep stopped by a foreign key, a damaged blob, an unregistered table). */
export function resultWarnings(run: BackupRun): readonly string[] {
  const v = run.result?.['warnings'];
  return Array.isArray(v) ? v.filter((w): w is string => typeof w === 'string') : [];
}

/** The `code` of a stored problem object, or `null`. */
export function errorCode(error: Readonly<Record<string, unknown>> | null): string | null {
  const c = error?.['code'];
  return typeof c === 'string' ? c : null;
}

/** A Job name is a DNS label of at most 63 characters ([Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/)). */
export const JOB_NAME_MAX = 63;

const MANUAL_INFIX = '-manual-';

/**
 * The command of the "Start a backup now" panel (R-UI-048): `kubectl -n <namespace> create job --from=cronjob/<name>
 * <name>-manual-<unix seconds>`. A CronJob name may have 52 characters, so a Job name built from it can exceed 63; the
 * browser then shortens the CronJob part of the Job name (not the `--from` reference), dropping a trailing separator that
 * would make the name end in a non-alphanumeric character. A value `manualRun` does not carry shows as a placeholder.
 */
export function manualRunCommand(manualRun: BackupSummary['manualRun'], unixSeconds: number): string {
  const namespace = manualRun.namespace ?? '<namespace>';
  const cronJob = manualRun.cronJob ?? '<cronjob>';
  const suffix = `${MANUAL_INFIX}${Math.floor(unixSeconds)}`;
  const room = Math.max(1, JOB_NAME_MAX - suffix.length);
  const base = cronJob.length > room ? cronJob.slice(0, room).replace(/[^a-z0-9]+$/, '') : cronJob;
  return `kubectl -n ${namespace} create job --from=cronjob/${cronJob} ${base}${suffix}`;
}
