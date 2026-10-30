import { DOCUMENT } from '@angular/common';
import {
  Component,
  ElementRef,
  Injector,
  afterNextRender,
  effect,
  inject,
  input,
  model,
  output,
  untracked,
  viewChild,
} from '@angular/core';
import { MatButtonModule } from '@angular/material/button';

let nextPanelId = 1;

/**
 * The button above a list that opens and closes its `<trove-filter-panel>`. It carries
 * `aria-expanded` and the number of active filters, as text as well as a badge.
 */
@Component({
  selector: 'trove-filter-toggle',
  imports: [MatButtonModule],
  template: `
    <button
      mat-stroked-button
      type="button"
      class="filter-toggle"
      [attr.aria-expanded]="open()"
      [attr.aria-controls]="open() ? controls() : null"
      (click)="open.set(!open())"
    >
      <span class="material-icons" aria-hidden="true">filter_list</span>
      <span i18n="@@filters.toggle">Filters</span>
      @if (activeCount() > 0) {
        <span class="filter-count" [attr.aria-label]="countLabel()">{{ activeCount() }}</span>
      }
    </button>
  `,
  host: { class: 'trove-filter-toggle' },
})
export class FilterToggle {
  readonly open = model(false);
  readonly activeCount = input(0);
  /** The id of the panel this button controls (`FilterPanel.panelId`). */
  readonly controls = input<string | null>(null);

  protected countLabel(): string {
    return $localize`:@@filters.active-count:${this.activeCount()}:count: filters active`;
  }
}

/**
 * One collapsible category of the filter panel: a heading button with a chevron, and the fields
 * of that filter below it. A category with an active value starts open so that no applied filter
 * is hidden.
 */
@Component({
  selector: 'trove-filter-category',
  template: `
    <section class="category" [class.open]="expanded()">
      <h3 class="category-heading">
        <button
          type="button"
          class="category-toggle"
          [attr.aria-expanded]="expanded()"
          [attr.aria-controls]="bodyId"
          (click)="expanded.set(!expanded())"
        >
          <span class="category-label">{{ label() }}</span>
          @if (active()) {
            <span class="category-dot" aria-hidden="true"></span>
            <span class="visually-hidden" i18n="@@filters.category-active">(active)</span>
          }
          <span class="material-icons chevron" aria-hidden="true">{{ expanded() ? 'expand_less' : 'chevron_right' }}</span>
        </button>
      </h3>
      <div class="category-body" [id]="bodyId" [hidden]="!expanded()">
        <ng-content />
      </div>
    </section>
  `,
  host: { class: 'trove-filter-category' },
})
export class FilterCategory {
  readonly label = input.required<string>();
  /** True when the filter of this category has a value. */
  readonly active = input(false);
  readonly expanded = model(false);

  protected readonly bodyId = `trove-filter-body-${nextPanelId++}`;
}

/**
 * The right-hand filter panel of a list screen: a heading, "Clear all", a close button, the
 * filter categories as content and an apply button that submits the form. It is a `search`
 * landmark. Opening moves focus to the heading; closing returns it to the element that had it
 * (the toggle). Escape closes it.
 *
 * The panel does not own filter values: the screen keeps them as signals (or in the query
 * parameters) and reacts to `apply` and `clearAll`. Every control in it must have a working
 * backend behind it; the panel adds no filter of its own.
 */
@Component({
  selector: 'trove-filter-panel',
  imports: [MatButtonModule],
  template: `
    @if (open()) {
      <form
        class="panel"
        role="search"
        [id]="panelId"
        [attr.aria-labelledby]="panelId + '-title'"
        (submit)="submit($event)"
        (keydown.escape)="close()"
      >
        <header class="panel-head">
          <h2 #heading [id]="panelId + '-title'" tabindex="-1" i18n="@@filters.title">Filters</h2>
          <button mat-button type="button" class="clear-all" [disabled]="activeCount() === 0" (click)="clearAll.emit()" i18n="@@filters.clear-all">Clear all</button>
          <button mat-icon-button type="button" class="close" (click)="close()" i18n-aria-label="@@filters.close" aria-label="Close filters">
            <span class="material-icons" aria-hidden="true">close</span>
          </button>
        </header>
        <div class="panel-body">
          <ng-content />
        </div>
        <footer class="panel-foot">
          <button mat-flat-button type="submit">{{ applyLabel() }}</button>
        </footer>
      </form>
    }
  `,
  host: { class: 'trove-filter-panel' },
})
export class FilterPanel {
  private readonly document = inject(DOCUMENT);
  private readonly injector = inject(Injector);
  private readonly heading = viewChild<ElementRef<HTMLElement>>('heading');

  readonly open = model(false);
  readonly activeCount = input(0);
  readonly applyLabel = input($localize`:@@filters.apply:Apply filters`);
  readonly apply = output<void>();
  readonly clearAll = output<void>();

  /** The id of the form; `<trove-filter-toggle [controls]>` points at it. */
  readonly panelId = `trove-filter-panel-${nextPanelId++}`;

  private opener: HTMLElement | null = null;
  private previous: boolean | null = null;

  constructor() {
    effect(() => {
      const open = this.open();
      untracked(() => {
        const before = this.previous;
        this.previous = open;
        if (before === null || before === open) return;
        if (open) {
          const active = this.document.activeElement;
          this.opener = active instanceof HTMLElement ? active : null;
          afterNextRender(() => this.heading()?.nativeElement.focus(), { injector: this.injector });
        } else {
          const opener = this.opener;
          this.opener = null;
          if (opener?.isConnected) opener.focus();
        }
      });
    });
  }

  protected submit(event: Event): void {
    event.preventDefault();
    this.apply.emit();
  }

  protected close(): void {
    this.open.set(false);
  }
}
