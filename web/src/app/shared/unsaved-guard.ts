import { inject } from '@angular/core';
import { CanDeactivateFn } from '@angular/router';
import { Confirm } from './confirm-dialog';

/** Implemented by an editor so that leaving with unsaved changes asks first (R-UI-021). */
export interface HasUnsavedChanges {
  hasUnsavedChanges(): boolean;
}

/** Route guard: asks before an editor with unsaved changes is left. */
export const unsavedChangesGuard: CanDeactivateFn<HasUnsavedChanges> = async (component) => {
  if (!component.hasUnsavedChanges()) return true;
  const answer = await inject(Confirm).ask({
    title: $localize`:@@unsaved.title:Discard unsaved changes?`,
    lines: [
      $localize`:@@unsaved.text:You have changes that are not saved. If you leave this page, they are lost.`,
    ],
    confirmLabel: $localize`:@@unsaved.discard:Discard and leave`,
  });
  return answer !== null;
};
