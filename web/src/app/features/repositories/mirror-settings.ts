import { Component, computed, input, model } from '@angular/core';
import { FieldProblem } from '../../api/problem';
import { FieldWidth } from '../../shared/access/fields';
import { FormatDescriptor } from '../../api/repositories-api';
import { fieldMessage, problemsAt } from '../../shared/field-errors';
import { JsonObject } from '../../shared/merge-patch';
import { MirrorDefaults, RemoteMode, RepositoryForm, lines } from './repository-model';
import { defaultPreset, normalizedUrl } from './upstream-model';
import { controlIdOf, numberOrText } from './settings-schema';

/** The id of the section; a server error at a pointer the section does not render lands here. */
export const MIRROR_SECTION_ID = 'f-remote-mirror';

/** The members of `remote.mirror` the section has a control for (R-REPO-050). */
const KNOWN_MEMBERS = [
  'paused',
  'intervalSeconds',
  'fullPassIntervalSeconds',
  'discovery',
  'concurrency',
  'maxVersionsPerPackage',
  'includePrereleases',
  'maxBytes',
];

/**
 * The control a field error of the mirror section belongs to (`/remote/mode`,
 * `/remote/mirror/intervalSeconds`), or null for a pointer that is not the section's.
 */
export function mirrorTarget(pointer: string): string | null {
  if (pointer === '/remote/mode') return 'f-remote-mode';
  if (pointer === '/remote/mirror' || pointer === '/remote') return MIRROR_SECTION_ID;
  const name = pointer.startsWith('/remote/mirror/') ? pointer.split('/')[3] : undefined;
  if (name === undefined) return null;
  return KNOWN_MEMBERS.includes(name) ? controlIdOf(`/remote/mirror/${name}`) : MIRROR_SECTION_ID;
}

/** The label a summary shows for a field error of the section, or null. */
export function mirrorPointerLabel(pointer: string): string | null {
  if (pointer === '/remote/mode') return $localize`:@@mirror.field.mode:Mode`;
  const name = pointer.startsWith('/remote/mirror/') ? pointer.split('/')[3] : undefined;
  if (name === undefined) return null;
  return mirrorLabels()[name] ?? $localize`:@@mirror.heading:Mirror`;
}

function mirrorLabels(): Record<string, string> {
  return {
    paused: $localize`:@@mirror.paused:Paused`,
    intervalSeconds: $localize`:@@mirror.interval:Pass interval in seconds`,
    fullPassIntervalSeconds: $localize`:@@mirror.full-interval:Full pass interval in seconds`,
    discovery: $localize`:@@mirror.discovery:Discovery`,
    concurrency: $localize`:@@mirror.concurrency:Concurrent fetches`,
    maxVersionsPerPackage: $localize`:@@mirror.max-versions:Versions per package`,
    includePrereleases: $localize`:@@mirror.prereleases:Include prerelease versions`,
    maxBytes: $localize`:@@mirror.max-bytes:Storage limit in bytes`,
  };
}

/**
 * Whether an include pattern bounds a mirror (R-REPO-052, Q-REPO-132): an exact name, or a literal
 * prefix before the first wildcard that holds more than the scope sigils `@` and `/`. The server
 * decides; the section only warns early with the same rule.
 */
export function boundsMirror(pattern: string): boolean {
  let prefix = '';
  for (let i = 0; i < pattern.length; i++) {
    const c = pattern.charAt(i);
    if (c === '\\') {
      i++;
      prefix += pattern.charAt(i);
    } else if (c === '*' || c === '?') {
      break;
    } else {
      prefix += c;
    }
  }
  return prefix.replace(/^[@/]+|[@/]+$/g, '') !== '';
}

interface NumberMember {
  readonly name: string;
  readonly label: string;
  readonly hint: string;
  /** The field width of its kind (Q-UI-335): an interval in seconds, a count, a byte count. */
  readonly width: Extract<FieldWidth, 'number' | 'duration' | 'bytes'>;
}

/**
 * The **Mirror** section of a remote repository of a format with `mirror: true` (R-UI-021,
 * R-REPO-050, Q-UI-104): the mode and the members of `remote.mirror`, in the creation form's Advanced settings
 * and in the settings page's section Mirror, which give it its heading. The members show in mode `mirror` only, each with its
 * default as its value (R-UI-084). A member equal to its default is not written (Q-UI-80). A project Owner (`restricted`) sees the section
 * read-only except **Pause** and **Turn off** (Q-REPO-142). Server field errors (`/remote/mode`,
 * `/remote/mirror/...`) appear at their controls.
 */
@Component({
  selector: 'trove-mirror-settings',
  imports: [],
  template: `
    <div class="mirror" [id]="sectionId" tabindex="-1">
      <p class="hint" i18n="@@mirror.intro">
        A remote repository fetches and stores what clients ask for. In mode mirror it also stores,
        in background passes, what the upstream offers within the include patterns and bounds below.
      </p>
      @for (e of problems('/remote/mirror'); track $index) {
        <p class="field-error">{{ message(e) }}</p>
      }
      @if (restricted()) {
        <p class="hint" role="note" id="mirror-restricted">
          <span class="material-icons inline" aria-hidden="true">lock</span>
          <ng-container i18n="@@mirror.restricted"
            >Only an administrator of the installation can switch a remote repository to mirror or
            change these settings. You can pause a mirror and turn it off.</ng-container
          >
        </p>
      }

      <fieldset class="mode" [attr.aria-describedby]="restricted() ? 'mirror-restricted' : null">
        <legend i18n="@@mirror.field.mode">Mode</legend>
        <label class="mode-option">
          <input
            id="f-remote-mode"
            type="radio"
            name="remote-mode"
            value="pullThrough"
            [checked]="mode() === 'pullThrough'"
            [disabled]="restricted()"
            [attr.aria-invalid]="problems('/remote/mode').length > 0 ? 'true' : null"
            [attr.aria-describedby]="problems('/remote/mode').length > 0 ? 'e-remote-mode' : null"
            (change)="setMode('pullThrough')"
          />
          <span class="mode-title" i18n="@@mirror.mode.pull-through">Pull-through</span>
          <span class="hint" i18n="@@mirror.mode.pull-through.hint"
            >Fetches and stores what clients ask for. This is the default.</span
          >
        </label>
        <label class="mode-option">
          <input
            type="radio"
            name="remote-mode"
            value="mirror"
            [checked]="mode() === 'mirror'"
            [disabled]="restricted()"
            (change)="setMode('mirror')"
          />
          <span class="mode-title" i18n="@@mirror.mode.mirror">Mirror</span>
          <span class="hint" i18n="@@mirror.mode.mirror.hint"
            >Also stores, in background passes, every package of the upstream that the include
            patterns and bounds admit.</span
          >
        </label>
        @for (e of problems('/remote/mode'); track $index) {
          <p class="field-error" id="e-remote-mode">
            <span class="material-icons inline" aria-hidden="true">error</span>
            {{ message(e) }}
          </p>
        }
      </fieldset>

      @if (restricted() && mode() === 'mirror') {
        <div class="turn-off">
          <button class="btn btn-default" type="button" (click)="setMode('pullThrough')">
            <ng-container i18n="@@mirror.turn-off">Turn off</ng-container>
          </button>
          <span class="hint" i18n="@@mirror.turn-off.hint"
            >Back to pull-through. A running pass stops before its next package; stored content
            stays.</span
          >
        </div>
      }

      @if (mode() === 'mirror') {
        @if (unbounded() === 'refused') {
          <p class="banner-warning" role="note" id="mirror-unbounded">
            <span class="material-icons inline warn" aria-hidden="true">warning</span>
            <span i18n="@@mirror.unbounded.refused"
              >A mirror of a well-known public registry needs a bound. Give include patterns
              that each start with a literal part (such as <code translate="no">&#64;types/*</code>),
              or a storage limit. Without either the server refuses to save it.</span
            >
          </p>
        } @else if (unbounded() === 'warned') {
          <p class="banner-warning" role="note" id="mirror-unbounded">
            <span class="material-icons inline warn" aria-hidden="true">warning</span>
            <span i18n="@@mirror.unbounded.warned"
              >The mirror has neither include patterns that start with a literal part nor a storage
              limit: it stores everything the upstream offers. It is saved with a warning.</span
            >
          </p>
        }

        <div class="form-grid">
          <div class="field">
            <label class="check-row" for="f-remote-mirror-paused">
              <input
                id="f-remote-mirror-paused"
                type="checkbox"
                [checked]="paused()"
                [attr.aria-invalid]="invalid('paused')"
                aria-describedby="d-mirror-paused"
                (change)="set('paused', $any($event.target).checked)"
              />
              <span i18n="@@mirror.paused">Paused</span>
            </label>
            <p id="d-mirror-paused" class="hint" i18n="@@mirror.paused.hint">
              No pass starts, and a running pass stops before its next package.
            </p>
            @for (e of problems('/remote/mirror/paused'); track $index) {
              <p class="field-error">{{ message(e) }}</p>
            }
          </div>

          @for (n of numbers(); track n.name) {
            <div class="field">
              <label class="field-label" [for]="idOf(n.name)">{{ n.label }}</label>
              <input
                class="text-input"
                [class]="'field-w-' + n.width"
                type="text"
                inputmode="numeric"
                autocomplete="off"
                spellcheck="false"
                [id]="idOf(n.name)"
                [value]="shown(n.name)"
                [disabled]="restricted()"
                [attr.aria-invalid]="invalid(n.name)"
                [attr.aria-describedby]="'d-' + idOf(n.name)"
                (input)="setNumber(n.name, $any($event.target).value)"
                (blur)="showDefaultAgain(n.name, $event)"
              />
              <p class="hint" [id]="'d-' + idOf(n.name)">{{ n.hint }}</p>
              @for (e of problems('/remote/mirror/' + n.name); track $index) {
                <p class="field-error">{{ message(e) }}</p>
              }
            </div>
          }

          <div class="field">
            <label class="field-label" for="f-remote-mirror-discovery" i18n="@@mirror.discovery"
              >Discovery</label
            >
            <select
              id="f-remote-mirror-discovery"
              class="select-input field-w-name"
              [disabled]="restricted()"
              [attr.aria-invalid]="invalid('discovery')"
              aria-describedby="d-mirror-discovery"
              (change)="set('discovery', $any($event.target).value)"
            >
              @for (d of discoveries; track d) {
                <option [value]="d" [selected]="discovery() === d">{{ discoveryLabel(d) }}</option>
              }
            </select>
            <p id="d-mirror-discovery" class="hint" i18n="@@mirror.discovery.hint">
              How a pass finds the names of the upstream. Automatic tries the feed, then the
              listing, then the names list.
            </p>
            @for (e of problems('/remote/mirror/discovery'); track $index) {
              <p class="field-error">{{ message(e) }}</p>
            }
          </div>

          <div class="field">
            <label class="check-row" for="f-remote-mirror-includePrereleases">
              <input
                id="f-remote-mirror-includePrereleases"
                type="checkbox"
                [checked]="prereleases()"
                [disabled]="restricted()"
                [attr.aria-invalid]="invalid('includePrereleases')"
                aria-describedby="d-mirror-prereleases"
                (change)="set('includePrereleases', $any($event.target).checked)"
              />
              <span i18n="@@mirror.prereleases">Include prerelease versions</span>
            </label>
            <p id="d-mirror-prereleases" class="hint" i18n="@@mirror.prereleases.hint">
              Whether a pass stores prerelease versions.
            </p>
            @for (e of problems('/remote/mirror/includePrereleases'); track $index) {
              <p class="field-error">{{ message(e) }}</p>
            }
          </div>
        </div>
      }
    </div>
  `,
  styles: `
    .mirror {
      margin: 0;
    }
    .mode {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-2);
      max-inline-size: 36rem;
      margin: 0 0 var(--trove-space-4);
      padding: 0;
      border: 0;

      legend {
        margin-block-end: var(--trove-space-2);
        font-weight: 600;
      }
    }
    .mode-option {
      display: grid;
      grid-template-columns: auto 1fr;
      column-gap: var(--trove-space-2);
      padding: var(--trove-space-3);
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-l);

      input {
        grid-row: 1 / span 2;
      }
      .hint {
        grid-column: 2;
        margin: 0;
      }
    }
    .mode-title {
      font-weight: 600;
    }
    .turn-off {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--trove-space-3);
      margin-block-end: var(--trove-space-4);
    }
  `,
})
export class MirrorSettings {
  /** The form of the repository; the section edits `remoteMode` and `mirror`. */
  readonly form = model.required<RepositoryForm>();
  readonly format = input.required<FormatDescriptor>();
  /**
   * The upstream URL the user entered or the repository is stored with (Q-UI-122); empty or null means
   * the format's well-known upstream, which is what a create without a URL takes.
   */
  readonly upstreamUrl = input<string | null>(null);
  /** A project Owner: read-only except Pause and Turn off (Q-REPO-142). */
  readonly restricted = input(false);
  /** The server's defaults of `remote.mirror` (R-REPO-063): the values of the fields that are not set. */
  readonly defaults = input<MirrorDefaults>(null);
  /** Server field problems by pointer (`ApiError.fieldErrors`). */
  readonly fieldErrors = input<ReadonlyMap<string, FieldProblem>>(new Map());

  protected readonly sectionId = MIRROR_SECTION_ID;
  protected readonly discoveries = ['auto', 'feed', 'listing', 'names'] as const;

  protected readonly mode = computed<RemoteMode>(() => this.form().remoteMode);
  protected readonly paused = computed(() => this.valueOf('paused') === true);
  protected readonly prereleases = computed(() => this.valueOf('includePrereleases') !== false);
  protected readonly discovery = computed(() => {
    const v = this.valueOf('discovery');
    return typeof v === 'string' ? v : 'auto';
  });

  /** A member as it is set, else as the server's default has it. */
  private valueOf(name: string): unknown {
    const set = this.form().mirror[name];
    return set === undefined || set === null ? this.defaults()?.[name] : set;
  }

  /** The numeric members with their texts, built where `$localize` is available. */
  protected readonly numbers = computed<readonly NumberMember[]>(() => {
    const labels = mirrorLabels();
    return [
      {
        name: 'intervalSeconds',
        width: 'duration',
        label: labels['intervalSeconds']!,
        hint: $localize`:@@mirror.interval.hint:From the start of one pass to the start of the next. 300 to 604800.`,
      },
      {
        name: 'fullPassIntervalSeconds',
        width: 'duration',
        label: labels['fullPassIntervalSeconds']!,
        hint: $localize`:@@mirror.full-interval.hint:Age of the last full pass after which the next pass is full. 0 means only when an incremental pass cannot run; otherwise 86400 to 7776000.`,
      },
      {
        name: 'concurrency',
        width: 'number',
        label: labels['concurrency']!,
        hint: $localize`:@@mirror.concurrency.hint:The most background fetches one pass runs at once. 1 to 64.`,
      },
      {
        name: 'maxVersionsPerPackage',
        width: 'number',
        label: labels['maxVersionsPerPackage']!,
        hint: $localize`:@@mirror.max-versions.hint:The newest versions of a package that a pass stores. 0 means all. 0 to 10000.`,
      },
      {
        name: 'maxBytes',
        width: 'bytes',
        label: labels['maxBytes']!,
        hint: $localize`:@@mirror.max-bytes.hint:Bytes of stored content beyond which a pass adds nothing. At least 1048576. Empty means no limit of its own.`,
      },
    ];
  });

  /**
   * Whether the mirror is bounded (R-REPO-052): `refused` when the upstream is one of the format's presets,
   * each a public registry the server refuses to mirror without a bound (Q-REPO-323), `warned` when it will
   * save with a warning, else none.
   */
  protected readonly unbounded = computed<'refused' | 'warned' | null>(() => {
    const f = this.form();
    if (f.mirror['maxBytes'] !== undefined && f.mirror['maxBytes'] !== null && f.mirror['maxBytes'] !== '') {
      return null;
    }
    const includes = lines(f.includePatterns);
    if (includes.length > 0 && includes.every(boundsMirror)) return null;
    const entered = this.upstreamUrl()?.trim();
    const url = entered ? entered : (defaultPreset(this.format())?.url ?? null);
    if (url === null) return 'warned';
    const preset = this.format().upstreamPresets.some((p) => normalizedUrl(p.url) === normalizedUrl(url));
    return preset ? 'refused' : 'warned';
  });

  protected idOf(name: string): string {
    return controlIdOf(`/remote/mirror/${name}`);
  }

  protected problems(pointer: string): readonly FieldProblem[] {
    return problemsAt(this.fieldErrors(), pointer);
  }

  protected invalid(name: string): 'true' | null {
    return this.problems(`/remote/mirror/${name}`).length > 0 ? 'true' : null;
  }

  protected message(problem: FieldProblem): string {
    return fieldMessage(problem);
  }

  /**
   * The value in the field: the one set, else the server's default (R-UI-084, R-REPO-063); `maxBytes` has none and stays empty, and
   * so does every member while the defaults are not known.
   */
  protected shown(name: string): string {
    const v = this.valueOf(name);
    return v === undefined || v === null ? '' : String(v);
  }

  protected discoveryLabel(d: string): string {
    switch (d) {
      case 'feed':
        return $localize`:@@mirror.discovery.feed:Change feed`;
      case 'listing':
        return $localize`:@@mirror.discovery.listing:Listing`;
      case 'names':
        return $localize`:@@mirror.discovery.names:Names list`;
      default:
        return $localize`:@@mirror.discovery.auto:Automatic`;
    }
  }

  protected setMode(remoteMode: RemoteMode): void {
    this.form.update((f) => ({ ...f, remoteMode }));
  }

  protected set(name: string, value: unknown): void {
    this.form.update((f) => {
      const mirror: JsonObject = { ...f.mirror };
      if (value === undefined) delete mirror[name];
      else mirror[name] = value;
      return { ...f, mirror };
    });
  }

  protected setNumber(name: string, text: string): void {
    this.set(name, numberOrText(text));
  }

  /** A field the user emptied shows its default again when it is left (R-UI-084); the member stays unwritten. */
  protected showDefaultAgain(name: string, event: Event): void {
    const field = event.target as HTMLInputElement;
    if (field.value.trim() === '') field.value = this.shown(name);
  }
}
