import { Directive, ElementRef, inject } from '@angular/core';
import { CdkMenuTrigger } from '@angular/cdk/menu';

/**
 * Makes a click on the trigger of a submenu open it and never close it. The CDK menu opens a submenu when the pointer
 * enters its trigger and toggles it on click, so a pointer user who clicks the item (the pointer is already on it) closes
 * the submenu that the hover just opened. A touch user, for whom nothing hovered, still opens it with the first tap.
 * The listener runs in the capture phase of the trigger itself, before the toggle of the CDK's own host listener; it
 * moves the focus into the open submenu, as the toggle would have. Escape and the left arrow close it as before.
 */
@Directive({
  selector: '[troveOpenSubmenuOnClick]',
})
export class OpenSubmenuOnClick {
  private readonly trigger = inject(CdkMenuTrigger);
  private readonly element = inject<ElementRef<HTMLElement>>(ElementRef).nativeElement;

  constructor() {
    this.element.addEventListener('click', (event) => this.keepOpen(event), { capture: true });
  }

  private keepOpen(event: Event): void {
    if (!this.trigger.isOpen()) {
      return;
    }
    event.stopImmediatePropagation();
    const id = this.element.getAttribute('aria-controls');
    const menu = id ? this.element.ownerDocument.getElementById(id) : null;
    menu?.querySelector<HTMLElement>('[role^="menuitem"]')?.focus();
  }
}
