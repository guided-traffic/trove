import {
  Component,
  ElementRef,
  Injector,
  afterNextRender,
  computed,
  effect,
  inject,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { MatButtonModule } from '@angular/material/button';
import { MatDialog } from '@angular/material/dialog';
import { MatMenuModule } from '@angular/material/menu';
import { MatSidenav, MatSidenavContainer, MatSidenavModule } from '@angular/material/sidenav';
import { MatToolbarModule } from '@angular/material/toolbar';
import { NavigationEnd, Router, RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';
import { filter, map } from 'rxjs';
import { ApiError } from './api/problem';
import { Breadcrumbs } from './core/breadcrumbs';
import { FocusOnNavigation } from './core/focus-on-navigation';
import { Layout } from './core/layout';
import { NavGroup, Navigation } from './core/navigation';
import { NavState } from './core/nav-state';
import { PreferencesStore, UiTheme } from './core/preferences-store';
import { SessionStore } from './core/session-store';
import { Banner } from './shared/banner';
import { ErrorNotice } from './shared/error-notice';
import { Notices } from './shared/notices';
import { SessionDialog } from './shared/session-dialog';

/**
 * Root component (R-UI-002, Q-UI-68): skip link, the dark top bar (wordmark, repository search,
 * user menu), the dark navigation sidebar (areas, folding groups, icon rail), the breadcrumb trail,
 * banner area and router outlet.
 */
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
  protected readonly navState = inject(NavState);
  protected readonly breadcrumbs = inject(Breadcrumbs);
  private readonly router = inject(Router);
  private readonly dialog = inject(MatDialog);
  private readonly main = viewChild.required<ElementRef<HTMLElement>>('main');
  protected readonly sidenav = viewChild<MatSidenav>('drawer');
  private readonly frame = viewChild(MatSidenavContainer);
  private readonly injector = inject(Injector);

  /** The side bar shows once the application knows whether the caller is signed in or anonymous. */
  protected readonly showNav = computed(
    () => this.session.state() === 'authenticated' || this.session.state() === 'anonymous',
  );
  /** The sidebar is the icon rail: the viewer's choice, honoured only where the sidebar is pinned. */
  protected readonly rail = computed(() => this.layout.pinned() && this.navState.rail());
  /** The text of the repository search; the search opens the repository list filtered by it. */
  protected readonly searchText = signal('');
  /** The repository list is the API behind the search (`q` matches key and description, R-UI-020). */
  protected readonly canSearch = computed(() => this.session.has('repositories:read-config'));
  private readonly url = toSignal(
    this.router.events.pipe(
      filter((e): e is NavigationEnd => e instanceof NavigationEnd),
      map(() => this.router.url),
    ),
    { initialValue: this.router.url },
  );
  protected readonly loadFailure = computed(() =>
    this.session.state() === 'failed' ? (this.session.loadError() ?? new ApiError(0, null, null)) : null,
  );

  constructor() {
    inject(FocusOnNavigation).start();
    // The container measures the sidebar once; a change between the full sidebar and the rail has
    // to tell it, or the content keeps the old margin.
    effect(() => {
      this.rail();
      untracked(() =>
        afterNextRender(() => this.frame()?.updateContentMargins(), { injector: this.injector }),
      );
    });
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

  /** A folded group that holds the current page still shows it, by a mark on the group. */
  protected groupActive(group: NavGroup): boolean {
    const url = this.url();
    return group.items.some((i) => url === i.route || url.startsWith(i.route + '/') || url.startsWith(i.route + '?'));
  }

  protected submitSearch(event: Event): void {
    event.preventDefault();
    const q = this.searchText().trim();
    void this.router.navigate(['/admin/repositories'], { queryParams: { q: q || null } });
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
