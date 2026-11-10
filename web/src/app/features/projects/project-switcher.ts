import { Component, ElementRef, computed, inject, input, signal, viewChild } from '@angular/core';
import { RouterLink } from '@angular/router';
import { Project } from '../../api/projects-api';
import { ProjectContext } from '../../core/project-context';

/** How many matches the switcher lists at once; a longer list is narrowed by typing. */
const SHOWN = 20;

/**
 * The project switcher on top of a project's sidebar (Q-UI-129): the project the screens are in, and a panel with a search
 * field, the recently visited projects and the matches. It is a disclosure, not a menu: the panel holds a text field, so
 * the button announces `aria-expanded`, Escape closes the panel and returns the focus to the button, and a choice is a
 * link to the project (its packages). `label` names the project in the button.
 */
@Component({
  selector: 'trove-project-switcher',
  imports: [RouterLink],
  host: {
    '(document:click)': 'outside($event)',
    '(keydown.escape)': 'escape($event)',
  },
  template: `
    <button
      #trigger
      type="button"
      class="switcher-button"
      [attr.aria-expanded]="open()"
      aria-controls="project-switcher-panel"
      (click)="toggle()"
    >
      <span class="switcher-mark" aria-hidden="true" translate="no">{{ initial() }}</span>
      <span class="switcher-text">
        <span class="switcher-name" translate="no">{{ label() }}</span>
        <span class="visually-hidden">&ngsp;<ng-container i18n="@@switcher.button">Switch project</ng-container></span>
      </span>
      <span class="material-icons switcher-chevron" aria-hidden="true">unfold_more</span>
    </button>
    @if (open()) {
      <div id="project-switcher-panel" animate.enter="dropdown-in" animate.leave="dropdown-out" class="switcher-panel" role="group" aria-labelledby="project-switcher-title">
        <p id="project-switcher-title" class="visually-hidden" i18n="@@switcher.title">Projects</p>
        <input
          #search
          type="search"
          class="switcher-search"
          autocomplete="off"
          spellcheck="false"
          i18n-aria-label="@@switcher.search"
          aria-label="Find a project"
          i18n-placeholder="@@switcher.search"
          placeholder="Find a project"
          [value]="query()"
          (input)="query.set($any($event.target).value)"
        />
        @if (recent().length > 0 && query().trim() === '') {
          <p class="switcher-heading" i18n="@@switcher.recent">Recently visited</p>
          <ul class="switcher-list">
            @for (p of recent(); track p.key) {
              <li>
                <a class="switcher-item" [routerLink]="['/projects', p.key]" (click)="close(false)">
                  <span class="switcher-name" translate="no">{{ p.displayName }}</span>
                  <span class="switcher-key" translate="no">{{ p.key }}</span>
                </a>
              </li>
            }
          </ul>
        }
        <p class="switcher-heading" i18n="@@switcher.all">Your projects</p>
        @if (matches().length === 0) {
          <p class="switcher-empty" role="status" i18n="@@switcher.none">No project matches.</p>
        } @else {
          <ul class="switcher-list">
            @for (p of matches(); track p.key) {
              <li>
                <a
                  class="switcher-item"
                  [routerLink]="['/projects', p.key]"
                  [attr.aria-current]="p.key === current() ? 'true' : null"
                  (click)="close(false)"
                >
                  <span class="switcher-name" translate="no">{{ p.displayName }}</span>
                  <span class="switcher-key" translate="no">{{ p.key }}</span>
                </a>
              </li>
            }
          </ul>
        }
        <a class="switcher-all" routerLink="/projects" (click)="close(false)" i18n="@@switcher.overview">All projects</a>
      </div>
    }
  `,
})
export class ProjectSwitcher {
  /** The name shown in the button: the project's display name, else its key. */
  readonly label = input.required<string>();

  private readonly context = inject(ProjectContext);
  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly trigger = viewChild.required<ElementRef<HTMLButtonElement>>('trigger');

  protected readonly open = signal(false);
  protected readonly query = signal('');
  protected readonly current = this.context.key;
  protected readonly initial = computed(() => (this.label().trim().charAt(0) || this.current()?.charAt(0)) ?? '');

  /** The recently visited projects that still exist, newest first, without the current one. */
  protected readonly recent = computed<readonly Project[]>(() => {
    const known = new Map(this.context.projects().map((p) => [p.key, p]));
    return this.context
      .recent()
      .filter((k) => k !== this.current())
      .flatMap((k) => {
        const p = known.get(k);
        return p ? [p] : [];
      });
  });
  protected readonly matches = computed<readonly Project[]>(() => {
    const q = this.query().trim().toLowerCase();
    return this.context
      .projects()
      .filter((p) => !q || p.key.includes(q) || p.displayName.toLowerCase().includes(q))
      .slice(0, SHOWN);
  });

  protected toggle(): void {
    if (this.open()) this.close(false);
    else {
      this.query.set('');
      this.open.set(true);
      // The field is created by the change that opens the panel; focus it once it is there.
      queueMicrotask(() => this.host.nativeElement.querySelector<HTMLInputElement>('.switcher-search')?.focus());
    }
  }

  protected close(refocus: boolean): void {
    this.open.set(false);
    if (refocus) this.trigger().nativeElement.focus();
  }

  protected escape(event: Event): void {
    if (!this.open()) return;
    event.stopPropagation();
    this.close(true);
  }

  protected outside(event: Event): void {
    if (this.open() && !this.host.nativeElement.contains(event.target as Node)) this.open.set(false);
  }
}
