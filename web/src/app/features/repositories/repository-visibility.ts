import { Component, computed, effect, input, output, signal, untracked } from '@angular/core';
import { RepositoryVisibility } from '../../api/repositories-api';
import {
  VisibilityLevel,
  VisibilityLevelControl,
  aboveProjectReasons,
  lowerLevel,
  isMoreOpen,
} from '../../shared/ui/visibility-level';
import { ApiError } from '../../api/problem';
import { ErrorNotice } from '../../shared/error-notice';

/**
 * The block Visibility of a repository's Access tab (R-UI-055, Q-UI-231): the radio group for the artifacts and, for a format with
 * wiki content, one for the wiki, with its own Save changes. An option above the project's level is disabled with the reason; a
 * wiki option above the artifacts' level is disabled with the reason, and choosing an artifacts level below the wiki's lowers the
 * wiki's choice with it. The block sends only the parts that changed; the screen applies the answer.
 */
@Component({
  selector: 'trove-repository-visibility',
  imports: [ErrorNotice, VisibilityLevelControl],
  template: `
    <section class="visibility" aria-labelledby="repo-visibility">
      <h2 id="repo-visibility" i18n="@@repo.visibility.heading">Visibility</h2>
      <form (submit)="submit($event)" novalidate>
        <div class="groups">
          <trove-visibility-level
            [label]="artifactsLabel()"
            subject="artifacts"
            [value]="artifacts()"
            (valueChange)="setArtifacts($event)"
            [reasons]="artifactReasons()"
            [disabled]="!canWrite()"
          />
          @if (hasWiki()) {
            <trove-visibility-level
              i18n-label="@@repo.visibility.wiki"
              label="Wiki"
              subject="wiki"
              [value]="wiki()"
              (valueChange)="wiki.set($event)"
              [reasons]="wikiReasons()"
              [disabled]="!canWrite()"
            />
          }
        </div>
        @if (hasWiki()) {
          <p class="muted" i18n="@@repo.visibility.wiki-note">
            Artifacts: who can install, download and list the packages. Wiki: who can read the README and the documentation.
          </p>
        }
        @for (w of warnings(); track w) {
          <p class="banner-warning" role="status">
            <span class="material-icons inline warn" aria-hidden="true">warning</span>
            <span>{{ w }}</span>
          </p>
        }
        @if (failure(); as f) {
          <trove-error-notice [error]="f" />
        }
        @if (canWrite()) {
          <button class="btn btn-confirm" type="submit" [disabled]="saving() || !dirty()">
            <ng-container i18n="@@repo.visibility.save">Save changes</ng-container>
            <span class="visually-hidden" i18n="@@repo.visibility.save-for"> (visibility)</span>
          </button>
        } @else {
          <p class="muted" i18n="@@repo.visibility.read-only">
            Only the project's Owner and holders of repositories:write-config change the visibility.
          </p>
        }
      </form>
    </section>
  `,
  styles: `
    .visibility {
      margin-block-end: var(--trove-space-5);
    }
    .groups {
      display: flex;
      flex-direction: column;
      gap: var(--trove-space-4);
    }
    .muted {
      color: var(--trove-text-muted);
      font-size: var(--trove-text-muted-size);
    }
  `,
})
export class RepositoryVisibilityBlock {
  /** The stored levels. */
  readonly stored = input.required<RepositoryVisibility>();
  /** The project's level; `null` while it is unknown, which restricts nothing (the server decides). */
  readonly projectLevel = input<VisibilityLevel | null>(null);
  /** The format has wiki content (`wiki` of `GET /api/v1/formats`). */
  readonly hasWiki = input(false);
  readonly canWrite = input(false);
  readonly saving = input(false);
  readonly failure = input<ApiError | null>(null);
  /** The sentences of the answer's warnings, shown at the block. */
  readonly warnings = input<readonly string[]>([]);
  /** Only the parts that differ from the stored levels. */
  readonly save = output<Partial<RepositoryVisibility>>();

  protected readonly artifacts = signal<VisibilityLevel>('private');
  protected readonly wiki = signal<VisibilityLevel>('private');

  protected readonly artifactsLabel = computed(() =>
    this.hasWiki() ? $localize`:@@repo.visibility.artifacts:Artifacts` : $localize`:@@repo.visibility.single:Visibility`,
  );

  protected readonly artifactReasons = computed(() => aboveProjectReasons(this.projectLevel()));

  protected readonly wikiReasons = computed(() => {
    const out = { ...aboveProjectReasons(this.projectLevel()) };
    for (const level of ['internal', 'public'] as const) {
      if (isMoreOpen(level, this.artifacts())) {
        out[level] = $localize`:@@repo.visibility.wiki-above-artifacts:The wiki cannot be more open than the artifacts.`;
      }
    }
    return out;
  });

  protected readonly dirty = computed(
    () => this.artifacts() !== this.stored().artifacts || this.wikiValue() !== this.stored().wiki,
  );

  constructor() {
    effect(() => {
      const s = this.stored();
      untracked(() => {
        this.artifacts.set(s.artifacts);
        this.wiki.set(s.wiki);
      });
    });
  }

  /** A format without wiki content keeps the wiki at the artifacts' level, so the one group says everything. */
  private wikiValue(): VisibilityLevel {
    return this.hasWiki() ? this.wiki() : lowerLevel(this.stored().wiki, this.artifacts());
  }

  protected setArtifacts(level: VisibilityLevel): void {
    this.artifacts.set(level);
    this.wiki.update((w) => lowerLevel(w, level));
  }

  protected submit(event: Event): void {
    event.preventDefault();
    if (!this.dirty() || this.saving()) return;
    const change: { artifacts?: VisibilityLevel; wiki?: VisibilityLevel } = {};
    if (this.artifacts() !== this.stored().artifacts) change.artifacts = this.artifacts();
    if (this.wikiValue() !== this.stored().wiki) change.wiki = this.wikiValue();
    this.save.emit(change);
  }
}
