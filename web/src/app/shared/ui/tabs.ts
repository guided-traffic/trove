import { NgTemplateOutlet } from '@angular/common';
import {
  Component,
  Directive,
  TemplateRef,
  computed,
  contentChild,
  contentChildren,
  input,
  model,
  viewChild,
} from '@angular/core';
import { Tab as AriaTab, TabList, TabPanel, Tabs } from '@angular/aria/tabs';

/** Marks the lazy content of a tab: `<ng-template troveTabContent>` is created when the tab is selected and gone when it is left. */
@Directive({ selector: 'ng-template[troveTabContent]' })
export class TabContent {}

/**
 * One tab of a `trove-tab-group`: its label and its content. The content is created with the screen, as the tabs it
 * replaces did, and only the selected tab's content is in the page, so a form in another tab keeps what was typed.
 */
@Component({
  selector: 'trove-tab',
  template: `<ng-template><ng-content /></ng-template>`,
})
export class TabPage {
  readonly label = input.required<string>();
  /** A count shown in the strip as a neutral badge after the label (R-UI-062); `null` shows none. */
  readonly count = input<number | null>(null);
  readonly eager = viewChild.required(TemplateRef);
  readonly lazy = contentChild(TabContent, { read: TemplateRef });
}

/**
 * Tabs that switch a view in place (Q-UI-126). The strip is the reference's tab strip (`.tab-nav`, `.tab-link` of
 * `_controls.scss`); the roles, the arrow keys, Home and End and the roving tab stop are `@angular/aria`'s
 * WAI-ARIA tabs pattern, and the selected tab's panel is a `tabpanel` named by its tab. Tabs that are links to
 * routes are plain `.tab-nav` links instead.
 *
 *   <trove-tab-group [(selectedIndex)]="index"><trove-tab label="General">…</trove-tab></trove-tab-group>
 */
@Component({
  selector: 'trove-tab-group',
  imports: [NgTemplateOutlet, Tabs, TabList, AriaTab, TabPanel],
  template: `
    <div ngTabs>
      <div
        ngTabList
        class="tab-nav"
        [selectedTab]="active().toString()"
        (selectedTabChange)="select($event)"
      >
        @for (t of tabs(); track t; let i = $index) {
          <button ngTab class="tab-link" type="button" [value]="i.toString()">
            {{ t.label() }}
            @if (t.count() !== null) {
              <span class="badge">{{ t.count() }}</span>
            }
          </button>
        }
      </div>
      @if (current(); as t) {
        <div ngTabPanel [value]="active().toString()">
          <ng-container [ngTemplateOutlet]="t.lazy() ?? t.eager()" />
        </div>
      }
    </div>
  `,
  styles: `
    :host {
      display: block;
    }
    .tab-link .badge {
      margin-inline-start: var(--trove-space-1);
    }
  `,
})
export class TabGroup {
  readonly selectedIndex = model(0);
  protected readonly tabs = contentChildren(TabPage, { descendants: true });

  protected readonly active = computed(() =>
    Math.max(0, Math.min(this.selectedIndex(), this.tabs().length - 1)),
  );
  protected readonly current = computed(() => this.tabs()[this.active()] ?? null);

  protected select(value: string | undefined): void {
    if (value !== undefined) this.selectedIndex.set(Number(value));
  }
}
