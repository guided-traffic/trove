import { BreakpointObserver } from '@angular/cdk/layout';
import { Injectable, inject } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { map } from 'rxjs';

/** Below this width the field gives way to a button that opens the search as a sheet (Q-UI-403): 36 rem. */
export const SHEET_BELOW = '(max-width: 35.99rem)';

/** Whether the top bar is too narrow for the field (R-UI-095); a spec provides its own value. */
@Injectable({ providedIn: 'root' })
export class SearchLayout {
  readonly narrow = toSignal(inject(BreakpointObserver).observe(SHEET_BELOW).pipe(map((s) => s.matches)), {
    initialValue: false,
  });
}
