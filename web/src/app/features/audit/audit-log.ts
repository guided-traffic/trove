import { DatePipe } from '@angular/common';
import {
  Component,
  computed,
  effect,
  inject,
  input,
  resource,
  signal,
  untracked,
} from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { Router, RouterLink } from '@angular/router';
import { AUDIT_CATEGORIES, AuditApi, AuditEvent, AuditFilters } from '../../api/audit-api';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { FilterCategory, FilterPanel, FilterToggle } from '../../shared/ui/filter-panel';
import { PagedResource, asApiError, valueOf } from '../../shared/paged-resource';
import { KNOWN_ACTIONS, auditActionLabel, categoryLabel, outcomeLabel } from './audit-labels';

/** The page size of the table (the server default and a good fit for one screen). */
export const AUDIT_PAGE_SIZE = 100;

const HOUR_MS = 3_600_000;
const PRESETS: Readonly<Record<string, number>> = {
  '1h': HOUR_MS,
  '24h': 24 * HOUR_MS,
  '7d': 7 * 24 * HOUR_MS,
  '30d': 30 * 24 * HOUR_MS,
};

/**
 * The audit log (R-UI-037): the events of a time range, newest first, with the filters the API
 * offers and "Load more" over the keyset cursor. Times follow the user's locale, with a switch to
 * UTC. A row opens the full event in a panel with links to the actor and the target; the export
 * links download the current filter as CSV or JSON Lines. Filters, the open event and the time
 * zone live in the query parameters (R-UI-003).
 */
@Component({
  selector: 'trove-audit-log',
  imports: [
    DatePipe,
    MatButtonModule,
    RouterLink,
    CopyButton,
    DataTable,
    ErrorNotice,
    FilterCategory,
    FilterPanel,
    FilterToggle,
  ],
  templateUrl: './audit-log.html',
  styleUrl: './audit-log.scss',
})
export class AuditLog {
  protected readonly valueOf = valueOf;
  private readonly api = inject(AuditApi);
  private readonly router = inject(Router);

  /** Query parameters, bound by the router's component input binding. */
  readonly range = input<string | undefined>();
  readonly from = input<string | undefined>();
  readonly to = input<string | undefined>();
  readonly actor = input<string | undefined>();
  readonly action = input<string | undefined>();
  readonly category = input<string | undefined>();
  readonly repository = input<string | undefined>();
  readonly outcome = input<string | undefined>();
  readonly event = input<string | undefined>();
  readonly utc = input<string | undefined>();

  protected readonly list = new PagedResource<AuditEvent>();
  protected readonly now = signal(Date.now());
  protected readonly categories = AUDIT_CATEGORIES;
  protected readonly knownActions = KNOWN_ACTIONS;

  // The form controls hold their text until "Apply" puts it into the URL.
  protected readonly draft = signal<Readonly<Record<string, string>>>({});
  protected readonly presetDraft = signal('24h');
  protected readonly panelOpen = signal(false);
  protected readonly applyLabel = $localize`:@@repo.filter.apply:Search`;
  protected readonly caption = $localize`:@@audit.caption:Audit events, newest first`;
  /** No sort control: the log is always newest first, and the API takes no sort parameter. */
  protected readonly columns: readonly TableColumn[] = [
    {
      id: 'time',
      label: $localize`:@@audit.col.time:Time`,
      help: $localize`:@@audit.help.time:Events are listed newest first. Narrow the time range in the filters to reach older ones.`,
    },
    { id: 'actor', label: $localize`:@@audit.col.actor:Actor` },
    { id: 'action', label: $localize`:@@audit.col.action:Action` },
    { id: 'target', label: $localize`:@@audit.col.target:Target` },
    {
      id: 'outcome',
      label: $localize`:@@audit.col.outcome:Outcome`,
      help: $localize`:@@audit.help.outcome:Success: the action was carried out. Failure: it was allowed but did not work. Denied: the caller was not permitted.`,
    },
    { id: 'source', label: $localize`:@@audit.col.source:Source address` },
  ];

  protected readonly preset = computed(() => this.range() ?? '24h');
  protected readonly timeZone = computed(() => (this.utc() === '1' ? 'UTC' : undefined));

  /** The API filter for the current URL; a preset is resolved against `now`. */
  protected readonly filters = computed<AuditFilters & { from: string; to: string }>(() => {
    const now = this.now();
    const preset = this.preset();
    const span = PRESETS[preset];
    const from =
      span !== undefined
        ? new Date(now - span).toISOString()
        : toIso(this.from(), new Date(now - 24 * HOUR_MS).toISOString());
    const to =
      span !== undefined
        ? new Date(now).toISOString()
        : toIso(this.to(), new Date(now).toISOString());
    return {
      from,
      to,
      actor: this.actor(),
      action: this.action(),
      category: this.category(),
      repository: this.repository(),
      outcome: this.outcome(),
    };
  });
  protected readonly filtered = computed(
    () =>
      !!(
        this.actor() ||
        this.action() ||
        this.category() ||
        this.repository() ||
        this.outcome() ||
        this.range()
      ),
  );
  /** The number of filters in the URL, the time range included when it is not the default. */
  protected readonly activeCount = computed(
    () =>
      [this.actor(), this.action(), this.category(), this.repository(), this.outcome(), this.range()].filter(
        (v) => !!v,
      ).length,
  );
  protected readonly csvUrl = computed(() => this.api.exportUrl(this.filters(), 'csv'));
  protected readonly jsonlUrl = computed(() => this.api.exportUrl(this.filters(), 'jsonl'));

  protected readonly selected = resource({
    params: () => this.event() ?? null,
    loader: async ({ params }) => {
      if (params === null) return null;
      return this.list.items().find((e) => e.id === params) ?? (await this.api.event(params));
    },
  });
  protected readonly selectedFailure = computed(() => {
    const e = this.selected.error();
    return e ? asApiError(e) : null;
  });
  protected readonly detailsJson = computed(() =>
    JSON.stringify(valueOf(this.selected)?.details ?? {}, null, 2),
  );

  constructor() {
    effect(() => {
      const filters = this.filters();
      // A preset moves with the clock; the explicit refresh and a filter change reload.
      untracked(() => void this.list.start(this.api.events(filters), AUDIT_PAGE_SIZE));
    });
    effect(() => {
      // An applied filter is never hidden: the panel opens with it.
      if (this.activeCount() > 0) untracked(() => this.panelOpen.set(true));
    });
    effect(() => {
      this.draft.set({
        actor: this.actor() ?? '',
        action: this.action() ?? '',
        category: this.category() ?? '',
        repository: this.repository() ?? '',
        outcome: this.outcome() ?? '',
        from: this.from() ?? '',
        to: this.to() ?? '',
      });
      this.presetDraft.set(this.preset());
    });
  }

  protected setDraft(name: string, value: string): void {
    this.draft.update((d) => ({ ...d, [name]: value }));
  }

  protected apply(): void {
    const d = this.draft();
    const preset = this.presetDraft();
    this.now.set(Date.now());
    void this.router.navigate([], {
      queryParams: {
        range: preset === '24h' ? null : preset,
        from: preset === 'custom' ? d['from'] || null : null,
        to: preset === 'custom' ? d['to'] || null : null,
        actor: d['actor']?.trim() || null,
        action: d['action']?.trim() || null,
        category: d['category'] || null,
        repository: d['repository']?.trim() || null,
        outcome: d['outcome'] || null,
        event: null,
      },
      queryParamsHandling: 'merge',
    });
  }

  protected clear(): void {
    this.now.set(Date.now());
    void this.router.navigate([], {
      queryParams: {
        range: null,
        from: null,
        to: null,
        actor: null,
        action: null,
        category: null,
        repository: null,
        outcome: null,
        event: null,
      },
      queryParamsHandling: 'merge',
    });
  }

  protected refresh(): void {
    this.now.set(Date.now());
  }

  protected toggleUtc(utc: boolean): void {
    void this.router.navigate([], {
      queryParams: { utc: utc ? '1' : null },
      queryParamsHandling: 'merge',
    });
  }

  protected open(id: string): void {
    void this.router.navigate([], { queryParams: { event: id }, queryParamsHandling: 'merge' });
  }

  protected close(): void {
    void this.router.navigate([], { queryParams: { event: null }, queryParamsHandling: 'merge' });
  }

  protected actionLabel = auditActionLabel;
  protected categoryText = categoryLabel;
  protected outcomeText = outcomeLabel;

  protected actorName(e: AuditEvent): string {
    return e.actor.name ?? e.actor.id ?? e.actor.type;
  }

  /** The admin screen of the actor, where one exists. */
  protected actorLink(e: AuditEvent): string[] | null {
    const name = e.actor.name;
    if (!name) return null;
    if (e.actor.type === 'user') return ['/admin/users', name];
    if (e.actor.type === 'service_account') return ['/admin/service-accounts', name];
    return null;
  }

  protected targetText(e: AuditEvent): string {
    if (e.repository) return e.path ? `${e.repository}/${e.path}` : e.repository;
    if (e.resource.type)
      return e.resource.id ? `${e.resource.type} ${e.resource.id}` : e.resource.type;
    return '–';
  }

  protected readonly copyId = $localize`:@@audit.copy-id:Copy the event ID`;
  protected readonly copyTrace = $localize`:@@audit.copy-trace:Copy the trace ID`;
}

/** A `datetime-local` value in the browser's zone as an RFC 3339 instant; the fallback for an empty or bad value. */
function toIso(value: string | undefined, fallback: string): string {
  if (!value) return fallback;
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? fallback : d.toISOString();
}
