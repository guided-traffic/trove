import { Injectable, signal } from '@angular/core';

export interface NoticeAction {
  readonly label: string;
  readonly run: () => void;
}

export interface Notice {
  readonly id: number;
  /** `status` is announced politely, `alert` immediately (R-UI-042). */
  readonly kind: 'status' | 'alert';
  readonly text: string;
  readonly action?: NoticeAction;
}

/** Time a confirmation without an action stays on screen. */
export const STATUS_LIFETIME_MS = 8000;

/**
 * Feedback outside forms (R-UI-042): confirmations through a polite live region, errors through
 * an alert region. Notices with an action stay until dismissed; so do errors.
 */
@Injectable({ providedIn: 'root' })
export class Notifier {
  private nextId = 1;
  private readonly _notices = signal<readonly Notice[]>([]);

  readonly notices = this._notices.asReadonly();

  status(text: string, action?: NoticeAction): number {
    const id = this.add({ kind: 'status', text, action });
    if (!action) setTimeout(() => this.dismiss(id), STATUS_LIFETIME_MS);
    return id;
  }

  alert(text: string, action?: NoticeAction): number {
    return this.add({ kind: 'alert', text, action });
  }

  dismiss(id: number): void {
    this._notices.update((list) => list.filter((n) => n.id !== id));
  }

  private add(partial: Omit<Notice, 'id'>): number {
    const id = this.nextId++;
    this._notices.update((list) => [...list, { ...partial, id }]);
    return id;
  }
}
