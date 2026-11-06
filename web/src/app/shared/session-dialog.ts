import { Component, inject } from '@angular/core';
import { DIALOG_DATA, DialogClose, DialogRef, DialogTitle } from './ui/dialog';
import { Router } from '@angular/router';
// >>> M1 ui-b
import { DraftStore } from './drafts';
// <<< M1 ui-b

export interface SessionDialogData {
  /** `ended`: the session is gone. `reauth`: the action needs a recent sign-in (R-AUTH-011). */
  readonly kind: 'ended' | 'reauth';
}

/**
 * The two session dialogs of R-UI-004 and R-UI-042. Both lead to the sign-in screen with the
 * current route as `returnTo`; the draft handling of editors (copy as JSON, restore after
 * re-authentication) is the editors' part.
 */
@Component({
  selector: 'trove-session-dialog',
  imports: [DialogTitle, DialogClose],
  template: `
    @if (data.kind === 'ended') {
      <h2 troveDialogTitle i18n="@@session.ended.title">Your session has ended</h2>
      <div class="dialog-body" i18n="@@session.ended.text">
        You were signed out. Sign in again to continue where you left off.
      </div>
    } @else {
      <h2 troveDialogTitle i18n="@@session.reauth.title">Confirm it is you</h2>
      <div class="dialog-body" i18n="@@session.reauth.text">
        This action needs a recent sign-in. Sign in again, then repeat the action.
      </div>
    }
    <div class="dialog-actions">
      <!-- >>> M1 ui-b -->
      @if (data.kind === 'ended' && draft !== null) {
        <button class="btn btn-tertiary" type="button" (click)="copyDraft()" i18n="@@session.copy-draft">
          Copy draft as JSON
        </button>
      }
      <!-- <<< M1 ui-b -->
      <button class="btn btn-tertiary" type="button" troveDialogClose i18n="@@session.later">Not now</button>
      <button class="btn btn-confirm" type="button" (click)="signIn()" i18n="@@session.sign-in-again">
        Sign in again
      </button>
    </div>
  `,
})
export class SessionDialog {
  protected readonly data = inject<SessionDialogData>(DIALOG_DATA);
  private readonly ref = inject(DialogRef<unknown, SessionDialog>);
  private readonly router = inject(Router);
  // >>> M1 ui-b
  /** The unsaved draft of an open editor (R-UI-004: "Copy draft as JSON"), or null. */
  protected readonly draft = inject(DraftStore).snapshot();

  protected async copyDraft(): Promise<void> {
    if (this.draft !== null) await navigator.clipboard.writeText(this.draft);
  }
  // <<< M1 ui-b

  protected signIn(): void {
    const returnTo = this.router.url;
    this.ref.close();
    void this.router.navigate(['/login'], {
      queryParams: this.data.kind === 'reauth' ? { returnTo, reauth: 1 } : { returnTo },
    });
  }
}
