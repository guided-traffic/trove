import { Component, ElementRef, computed, effect, inject, untracked, viewChild } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog } from '@angular/material/dialog';
import { MatMenuModule } from '@angular/material/menu';
import { MatSidenav, MatSidenavModule } from '@angular/material/sidenav';
import { MatToolbarModule } from '@angular/material/toolbar';
import { RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';
import { ApiError } from './api/problem';
import { FocusOnNavigation } from './core/focus-on-navigation';
import { Layout } from './core/layout';
import { Navigation } from './core/navigation';
import { PreferencesStore, UiTheme } from './core/preferences-store';
import { SessionStore } from './core/session-store';
import { Banner } from './shared/banner';
import { ErrorNotice } from './shared/error-notice';
import { Notices } from './shared/notices';
import { SessionDialog } from './shared/session-dialog';

/** Root component: skip link, header, navigation, banner area and router outlet (R-UI-002). */
@Component({
  selector: 'trove-root',
  imports: [
    RouterOutlet,
    RouterLink,
    RouterLinkActive,
    MatButtonModule,
    MatMenuModule,
    MatSidenavModule,
    MatToolbarModule,
    Banner,
    ErrorNotice,
    Notices,
  ],
  templateUrl: './app.html',
  styleUrl: './app.scss',
})
export class App {
  protected readonly layout = inject(Layout);
  protected readonly session = inject(SessionStore);
  protected readonly navigation = inject(Navigation);
  protected readonly preferences = inject(PreferencesStore);
  private readonly dialog = inject(MatDialog);
  private readonly main = viewChild.required<ElementRef<HTMLElement>>('main');
  protected readonly sidenav = viewChild<MatSidenav>('drawer');

  /** The side bar shows once the application knows whether the caller is signed in or anonymous. */
  protected readonly showNav = computed(
    () => this.session.state() === 'authenticated' || this.session.state() === 'anonymous',
  );
  protected readonly loadFailure = computed(() =>
    this.session.state() === 'failed' ? (this.session.loadError() ?? new ApiError(0, null, null)) : null,
  );

  constructor() {
    inject(FocusOnNavigation).start();
    // Load the preferences (theme) once a session exists.
    effect(() => {
      if (this.session.authenticated()) untracked(() => void this.preferences.load());
    });
    effect(() => {
      if (this.session.ended()) {
        untracked(() => this.dialog.open(SessionDialog, { data: { kind: 'ended' }, disableClose: true }));
      }
    });
    effect(() => {
      if (this.session.reauthRequired()) {
        untracked(() => {
          const ref = this.dialog.open(SessionDialog, { data: { kind: 'reauth' } });
          // `afterClosed` is an observable; the flag only has to be cleared, so use the promise form.
          void ref.afterClosed().forEach(() => this.session.clearReauthRequired());
        });
      }
    });
  }

  /** Skip link target: moves focus to the main heading, or to the main region without one. */
  protected skipToMain(event: Event): void {
    event.preventDefault();
    const region = this.main().nativeElement;
    const heading = region.querySelector<HTMLElement>('h1');
    (heading ?? region).focus();
  }

  protected closeDrawer(drawer: MatSidenav): void {
    if (!this.layout.pinned()) {
      void drawer.close();
    }
  }

  protected setTheme(theme: UiTheme | null): void {
    void this.preferences.setTheme(theme);
  }

  protected setLocale(locale: 'en' | 'de'): void {
    void this.preferences.setLocale(locale);
  }

  protected signOut(): void {
    void this.session.signOut();
  }

  protected retrySession(): void {
    void this.session.reload();
  }
}
