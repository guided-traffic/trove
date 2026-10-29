import { BreakpointObserver } from '@angular/cdk/layout';
import { Injectable, inject } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { map } from 'rxjs';

/** Viewport width from which the side bar is pinned (Q-UI-2, R-UI-002). */
export const PINNED_MIN_WIDTH_PX = 1024;

/** Layout state of the shell as signals. */
@Injectable({ providedIn: 'root' })
export class Layout {
  private readonly breakpoints = inject(BreakpointObserver);

  /** True when the viewport is wide enough for the pinned side bar; false means drawer. */
  readonly pinned = toSignal(
    this.breakpoints
      .observe(`(min-width: ${PINNED_MIN_WIDTH_PX}px)`)
      .pipe(map((state) => state.matches)),
    { initialValue: false },
  );
}
