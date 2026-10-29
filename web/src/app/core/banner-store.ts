import { Injectable, signal } from '@angular/core';

/**
 * Holds the text of the system banner (R-UI-002). M0 has no API behind it; the store exists so
 * the shell's banner area has one place to read from once `GET /api/v1/system/banner` is wired.
 */
@Injectable({ providedIn: 'root' })
export class BannerStore {
  private readonly _text = signal<string | null>(null);

  /** The banner text, or null when no banner is set. */
  readonly text = this._text.asReadonly();

  set(text: string | null): void {
    const trimmed = text?.trim() ?? '';
    this._text.set(trimmed === '' ? null : trimmed);
  }
}
