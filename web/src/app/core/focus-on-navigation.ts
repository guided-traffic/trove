import { DOCUMENT } from '@angular/common';
import { Injectable, Injector, afterNextRender, effect, inject } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { NavigationEnd, Router } from '@angular/router';
import { filter } from 'rxjs';

/**
 * Moves focus to the new screen's `h1` after every navigation (WCAG 2.4.3, R-UI-044), or to the
 * main region when the screen has none. The first navigation is left alone so a fresh load does
 * not steal focus from the skip link.
 */
@Injectable({ providedIn: 'root' })
export class FocusOnNavigation {
  private readonly router = inject(Router);
  private readonly document = inject(DOCUMENT);
  private readonly injector = inject(Injector);
  private first = true;

  private readonly ends = toSignal(
    this.router.events.pipe(filter((e): e is NavigationEnd => e instanceof NavigationEnd)),
  );

  /** Starts watching; call once from the root component. */
  start(): void {
    effect(
      () => {
        if (!this.ends()) return;
        if (this.first) {
          this.first = false;
          return;
        }
        afterNextRender(() => this.focusHeading(), { injector: this.injector });
      },
      { injector: this.injector },
    );
  }

  private focusHeading(): void {
    const main = this.document.getElementById('main');
    const target = main?.querySelector<HTMLElement>('h1') ?? main;
    target?.focus();
  }
}
