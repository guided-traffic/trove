import { Component, computed, input, model } from '@angular/core';
import { Schema } from '../../api/types';
import { Tooltip } from './tooltip';

/** A visibility level (R-AUTHZ-033), from the most restrictive. */
export type VisibilityLevel = Schema<'VisibilityLevel'>;

export const VISIBILITY_LEVELS: readonly VisibilityLevel[] = ['private', 'internal', 'public'];

/** What the control is choosing the level of; it decides the sentence under each option (Q-UI-228). */
export type VisibilitySubject = 'new-projects' | 'project' | 'artifacts' | 'wiki';

/** The icon of each level, the ones the owner's GitLab uses: a lock, a shield, a globe (Q-UI-230). */
const ICONS: Readonly<Record<VisibilityLevel, string>> = { private: 'lock', internal: 'shield', public: 'public' };

/** The name of a level as the UI writes it. */
export function levelName(level: VisibilityLevel): string {
  switch (level) {
    case 'private':
      return $localize`:@@visibility.private:Private`;
    case 'internal':
      return $localize`:@@visibility.internal:Internal`;
    case 'public':
      return $localize`:@@visibility.public:Public`;
  }
}

/** The reason Public is off while the installation admits no request without a credential (R-UI-053). */
export function publicUnavailableReason(): Partial<Record<VisibilityLevel, string>> {
  return {
    public: $localize`:@@visibility.public-unavailable:The installation's configuration admits no request without a credential (endpoints.default.auth.anonymous).`,
  };
}

/** The lower of two levels. */
export function lowerLevel(a: VisibilityLevel, b: VisibilityLevel): VisibilityLevel {
  return VISIBILITY_LEVELS.indexOf(a) <= VISIBILITY_LEVELS.indexOf(b) ? a : b;
}

/** `true` when `a` is more open than `b`. */
export function isMoreOpen(a: VisibilityLevel, b: VisibilityLevel): boolean {
  return VISIBILITY_LEVELS.indexOf(a) > VISIBILITY_LEVELS.indexOf(b);
}

/**
 * The reasons for the options a repository cannot take because its project is less open (R-UI-055): every level above `parent`
 * is off. `null` (the parent's level is not known) restricts nothing; the server decides.
 */
export function aboveProjectReasons(parent: VisibilityLevel | null): Partial<Record<VisibilityLevel, string>> {
  const out: Partial<Record<VisibilityLevel, string>> = {};
  if (parent === null) return out;
  const p = levelName(parent).toLowerCase();
  for (const level of VISIBILITY_LEVELS) {
    if (isMoreOpen(level, parent)) {
      const l = levelName(level).toLowerCase();
      out[level] = $localize`:@@visibility.above-project:The project is ${p}:project:, so this repository cannot be ${l}:level:.`;
    }
  }
  return out;
}

function sentence(subject: VisibilitySubject, level: VisibilityLevel): string {
  switch (subject) {
    case 'new-projects':
      switch (level) {
        case 'private':
          return $localize`:@@visibility.new-projects.private:Only members and the principals a policy names read new projects.`;
        case 'internal':
          return $localize`:@@visibility.new-projects.internal:Every signed-in user who is not external reads new projects.`;
        case 'public':
          return $localize`:@@visibility.new-projects.public:Anyone, also without signing in, reads new projects.`;
      }
      break;
    case 'project':
      switch (level) {
        case 'private':
          return $localize`:@@visibility.project.private:Only members and the principals a policy names read the project's repositories.`;
        case 'internal':
          return $localize`:@@visibility.project.internal:Every signed-in user who is not external can find the project and read what its repositories make internal.`;
        case 'public':
          return $localize`:@@visibility.project.public:Anyone, also without signing in, can find the project and read what its repositories make public.`;
      }
      break;
    case 'artifacts':
      switch (level) {
        case 'private':
          return $localize`:@@visibility.artifacts.private:Only members of the project and the principals a policy names.`;
        case 'internal':
          return $localize`:@@visibility.artifacts.internal:Every signed-in user who is not external.`;
        case 'public':
          return $localize`:@@visibility.artifacts.public:Anyone, also without signing in.`;
      }
      break;
    case 'wiki':
      switch (level) {
        case 'private':
          return $localize`:@@visibility.wiki.private:Only members of the project and the principals a policy names.`;
        case 'internal':
          return $localize`:@@visibility.wiki.internal:Every signed-in user who is not external.`;
        case 'public':
          return $localize`:@@visibility.wiki.public:Anyone, also without signing in.`;
      }
  }
}

let nextGroupId = 1;

/**
 * The control that chooses a visibility level (Q-UI-228, docs/developer/ui-style-guide.md#forms): a radio group of Private,
 * Internal and Public, each with its icon and one muted sentence, as the owner's GitLab has it. An option that cannot be
 * chosen stays visible, muted, with its reason under the sentence (`reasons`); the radio is disabled and the reason is
 * part of the option's description. Native radios, so the arrow keys move and select, and Tab enters and leaves the group once.
 *
 *   <trove-visibility-level label="Artifacts" subject="artifacts" [(value)]="artifacts" [reasons]="{ public: 'The project is internal.' }" />
 */
@Component({
  selector: 'trove-visibility-level',
  template: `
    <fieldset class="levels" [disabled]="disabled()">
      <legend class="field-label">{{ label() }}</legend>
      @if (unsetNote(); as note) {
        <p class="muted unset" role="status">{{ note }}</p>
      }
      @for (option of options(); track option.level) {
        <label class="level" [class.unavailable]="option.reason !== null">
          <input
            type="radio"
            [name]="group"
            [value]="option.level"
            [checked]="unsetNote() === null && value() === option.level"
            [disabled]="option.reason !== null"
            [attr.aria-describedby]="option.describedBy"
            (change)="value.set(option.level)"
          />
          <span class="text">
            <span class="name"><span class="material-icons inline" aria-hidden="true">{{ option.icon }}</span>{{ option.name }}</span>
            <span class="muted" [id]="option.sentenceId">{{ option.sentence }}</span>
            @if (option.reason; as reason) {
              <span class="muted reason" [id]="option.reasonId">{{ reason }}</span>
            }
          </span>
        </label>
      }
    </fieldset>
  `,
  styles: `
    :host {
      display: block;
    }
    .levels {
      min-inline-size: 0;
      margin: 0;
      padding: 0;
      border: 0;
    }
    legend {
      padding: 0;
      margin-block-end: var(--trove-space-2);
    }
    .level {
      display: flex;
      align-items: flex-start;
      gap: var(--trove-space-2);
      margin-block-end: var(--trove-space-3);
    }
    .text {
      display: flex;
      flex-direction: column;
    }
    .name {
      font-weight: 600;
    }
    .muted {
      color: var(--trove-text-muted);
      font-size: var(--trove-text-muted-size);
    }
    .unavailable .name {
      color: var(--trove-text-muted);
      font-weight: 400;
    }
    fieldset:disabled .name {
      color: var(--trove-text-muted);
    }
  `,
})
export class VisibilityLevelControl {
  readonly label = input.required<string>();
  readonly subject = input<VisibilitySubject>('project');
  readonly value = model.required<VisibilityLevel>();
  /** While set, nothing is chosen yet: no option is checked and the note says what is applied instead (the server's default). */
  readonly unsetNote = input<string | null>(null);
  /** The reason an option cannot be chosen, by level; an option without an entry can. */
  readonly reasons = input<Partial<Record<VisibilityLevel, string>>>({});
  /** The whole group cannot be changed (a reader without the right to write). */
  readonly disabled = input(false);

  protected readonly group = `trove-visibility-level-${nextGroupId++}`;

  protected readonly options = computed(() =>
    VISIBILITY_LEVELS.map((level) => {
      const reason = this.reasons()[level] ?? null;
      const sentenceId = `${this.group}-${level}`;
      const reasonId = `${this.group}-${level}-reason`;
      return {
        level,
        icon: ICONS[level],
        name: levelName(level),
        sentence: sentence(this.subject(), level),
        reason,
        sentenceId,
        reasonId,
        describedBy: reason === null ? sentenceId : `${sentenceId} ${reasonId}`,
      };
    }),
  );
}

/**
 * The level's icon before the name of a project or a repository (Q-UI-230): the level's name is its accessible label and its
 * tooltip. Rendered inline, so it sits in front of the name in a list cell or a header.
 */
@Component({
  selector: 'trove-visibility-icon',
  imports: [Tooltip],
  template: `<span class="material-icons inline" role="img" [attr.aria-label]="name()" [troveTooltip]="name()">{{ icon() }}</span>`,
})
export class VisibilityIcon {
  readonly level = input.required<VisibilityLevel>();
  protected readonly name = computed(() => levelName(this.level()));
  protected readonly icon = computed(() => ICONS[this.level()]);
}
