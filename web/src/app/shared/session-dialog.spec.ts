import { DestroyRef } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { MatDialog } from '@angular/material/dialog';
import { Router, provideRouter } from '@angular/router';
import { DraftStore } from './access/drafts';
import { settle } from './access/testing';
import { SessionDialog } from './session-dialog';
import { seriousViolations } from '../testing/axe';
import { App } from '../app';
import { provideFakeApis } from '../api/testing/fakes';
import { provideHttpClient } from '@angular/common/http';

async function openDialog(kind: 'ended' | 'reauth') {
  TestBed.resetTestingModule();
  await TestBed.configureTestingModule({
    imports: [App],
    providers: [provideRouter([{ path: '**', children: [] }]), provideHttpClient(), ...provideFakeApis()],
  }).compileComponents();
  const fixture = TestBed.createComponent(App);
  TestBed.inject(MatDialog).open(SessionDialog, { data: { kind } });
  await settle(fixture);
  return { fixture, dialog: document.querySelector('trove-session-dialog') as HTMLElement };
}

describe('Session dialogs (R-UI-004, R-UI-042)', () => {
  it('R-UI-004 the ended dialog offers "Copy draft as JSON" while an editor holds unsaved changes', async () => {
    TestBed.resetTestingModule();
    await TestBed.configureTestingModule({
      imports: [App],
      providers: [provideRouter([{ path: '**', children: [] }]), provideHttpClient(), ...provideFakeApis()],
    }).compileComponents();
    const fixture = TestBed.createComponent(App);
    const drafts = TestBed.inject(DraftStore);
    drafts.track(() => ({ name: 'team-x', entries: [] }), TestBed.inject(DestroyRef));
    TestBed.inject(MatDialog).open(SessionDialog, { data: { kind: 'ended' } });
    await settle(fixture);
    const dialog = document.querySelector('trove-session-dialog') as HTMLElement;
    const copy = [...dialog.querySelectorAll('button')].find((b) => b.textContent?.includes('Copy draft as JSON'));
    expect(copy).toBeTruthy();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    copy!.click();
    expect(JSON.parse(writeText.mock.calls[0]?.[0] as string)).toEqual({ name: 'team-x', entries: [] });
    expect(await seriousViolations(dialog)).toEqual([]);
  });

  it('R-UI-004 without an unsaved draft the ended dialog has no copy button', async () => {
    const { dialog } = await openDialog('ended');
    expect(dialog.textContent).toContain('Your session has ended');
    expect(dialog.textContent).not.toContain('Copy draft');
  });

  it('R-UI-042 the re-authentication dialog explains why and leads to the sign-in with reauth=1', async () => {
    const { fixture, dialog } = await openDialog('reauth');
    expect(dialog.textContent).toContain('needs a recent sign-in');
    expect(dialog.textContent).not.toContain('Copy draft');
    expect(await seriousViolations(dialog)).toEqual([]);
    const router = TestBed.inject(Router);
    const navigate = vi.spyOn(router, 'navigate').mockResolvedValue(true);
    [...dialog.querySelectorAll('button')].find((b) => b.textContent?.includes('Sign in again'))!.click();
    await settle(fixture);
    expect(navigate).toHaveBeenCalledWith(['/login'], { queryParams: { returnTo: '/', reauth: 1 } });
  });
});
