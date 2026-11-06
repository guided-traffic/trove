import { AriaDescriber } from '@angular/cdk/a11y';
import { Overlay, OverlayRef } from '@angular/cdk/overlay';
import { ComponentPortal } from '@angular/cdk/portal';
import {
  Component,
  Directive,
  ElementRef,
  OnDestroy,
  effect,
  inject,
  input,
  signal,
} from '@angular/core';

/** The bubble of a tooltip: the reference's small dark label (`--trove-inverse-surface`). */
@Component({
  selector: 'trove-tooltip',
  // Hidden from assistive technology: the element's description (below) carries the text once.
  host: { 'aria-hidden': 'true' },
  template: `{{ text() }}`,
  styles: `
    :host {
      display: block;
      box-sizing: border-box;
      max-inline-size: 20rem;
      padding: var(--trove-space-1) var(--trove-space-2);
      border-radius: var(--trove-radius-m);
      background: var(--trove-inverse-surface);
      color: var(--trove-on-inverse-surface);
      font: var(--trove-type-body-small);
      overflow-wrap: anywhere;
    }
  `,
})
export class TooltipBubble {
  readonly text = signal('');
}

/**
 * A tooltip for an element, shown under the pointer and on keyboard focus, hidden by Escape, by leaving, and by blur
 * (WCAG 1.4.13: dismissible, hoverable, persistent while it is pointed at). The element is described by it
 * (a hidden message the CDK links with `aria-describedby`); an element whose name already says everything needs no tooltip.
 */
@Directive({
  selector: '[troveTooltip]',
  host: {
    '(mouseenter)': 'show()',
    '(mouseleave)': 'hideSoon()',
    '(focus)': 'show()',
    '(blur)': 'hide()',
    '(keydown.escape)': 'hide()',
  },
})
export class Tooltip implements OnDestroy {
  readonly troveTooltip = input.required<string>();

  private readonly overlay = inject(Overlay);
  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef);
  private readonly describer = inject(AriaDescriber);
  private ref: OverlayRef | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private described: string | null = null;

  constructor() {
    // The element is always described by the text (a hidden message the CDK keeps), so a screen reader announces it on
    // focus whether or not the bubble is showing.
    effect(() => {
      const text = this.troveTooltip();
      const el = this.host.nativeElement;
      if (this.described !== null) this.describer.removeDescription(el, this.described);
      this.describer.describe(el, text);
      this.described = text;
    });
  }

  protected show(): void {
    clearTimeout(this.timer);
    if (this.ref) return;
    const ref = this.overlay.create({
      positionStrategy: this.overlay
        .position()
        .flexibleConnectedTo(this.host)
        .withPositions([
          { originX: 'center', originY: 'bottom', overlayX: 'center', overlayY: 'top', offsetY: 4 },
          { originX: 'center', originY: 'top', overlayX: 'center', overlayY: 'bottom', offsetY: -4 },
        ])
        .withPush(true),
      scrollStrategy: this.overlay.scrollStrategies.close(),
    });
    const bubble = ref.attach(new ComponentPortal(TooltipBubble)).instance;
    bubble.text.set(this.troveTooltip());
    // Hoverable: moving from the element onto the tooltip keeps it open.
    ref.overlayElement.addEventListener('mouseenter', () => clearTimeout(this.timer));
    ref.overlayElement.addEventListener('mouseleave', () => this.hideSoon());
    this.ref = ref;
  }

  protected hideSoon(): void {
    clearTimeout(this.timer);
    this.timer = setTimeout(() => this.hide(), 100);
  }

  protected hide(): void {
    clearTimeout(this.timer);
    this.ref?.dispose();
    this.ref = null;
  }

  ngOnDestroy(): void {
    this.hide();
    if (this.described !== null) this.describer.removeDescription(this.host.nativeElement, this.described);
  }
}
