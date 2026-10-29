import { Injectable, signal } from '@angular/core';

/**
 * The highest authorization epoch this tab has seen on one of its own permission changes
 * (`Trove-Authz-Epoch`, R-UI-042, R-API-052). The interceptor sends it back as
 * `Trove-Authz-Min-Epoch` so that the next screen shows the change on whichever replica answers.
 * It lives in memory only, never in browser storage, and is cleared on 401, on sign-out and when
 * the server answers `authz_epoch_unknown`.
 */
@Injectable({ providedIn: 'root' })
export class AuthzEpoch {
  private readonly _value = signal<number | null>(null);

  readonly value = this._value.asReadonly();

  /** Keeps the highest epoch seen; a smaller one is ignored. */
  note(epoch: number): void {
    if (!Number.isSafeInteger(epoch) || epoch < 1) return;
    const current = this._value();
    if (current === null || epoch > current) this._value.set(epoch);
  }

  clear(): void {
    this._value.set(null);
  }
}
