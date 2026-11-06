import {
  Component,
  ElementRef,
  computed,
  effect,
  inject,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { CdkTrapFocus } from '@angular/cdk/a11y';
import { OpenSubmenuOnClick } from './shared/ui/open-submenu-on-click';
import { CdkMenu, CdkMenuItem, CdkMenuItemRadio, CdkMenuTrigger } from '@angular/cdk/menu';
import { TroveDialog } from './shared/ui/dialog';
import {
  ActivatedRouteSnapshot,
  NavigationEnd,
  NavigationError,
  Router,
  RouterLink,
  RouterLinkActive,
  RouterOutlet,
} from '@angular/router';
import { filter, map } from 'rxjs';
import { ApiError } from './api/problem';
import { Breadcrumbs } from './core/breadcrumbs';
import { FocusOnNavigation } from './core/focus-on-navigation';
import { helpFor } from './core/help-topics';
import { Layout } from './core/layout';
import { NavGroup, Navigation } from './core/navigation';
import { NavState } from './core/nav-state';
import { PreferencesStore, UiTheme } from './core/preferences-store';
import { ProjectFilter } from './core/project-filter';
import { SessionStore } from './core/session-store';
import { Banner } from './shared/banner';
import { ErrorNotice } from './shared/error-notice';
import { Notices } from './shared/notices';
import { SessionDialog } from './shared/session-dialog';
import { TroveMark } from './shared/brand/trove-mark';

/** Whether a route on the path to the active screen carries `data: { bare: true }`. */
function isBare(route: ActivatedRouteSnapshot | null): boolean {
  for (let r = route; r; r = r.firstChild) {
    if (r.data['bare'] === true) return true;
  }
  return false;
}

/**
 * Root component (R-UI-002, Q-UI-68, Q-UI-127): skip link, the top bar (mark, repository search, project
 * filter, Admin button, help, theme, user menu), the sidebar of the current area (the content, or the
 * admin area behind the Admin button) with folding groups, collapsible where it is pinned and a drawer
 * below, and the content panel with the breadcrumb trail, banner area and router outlet. A screen whose route carries `data: { bare: true }`
 * (sign-in and signed out, Q-UI-125) stands alone: none of the shell renders around it.
 */
@Component({
  selector: 'trove-root',
  imports: [
    RouterOutlet,
    RouterLink,
    RouterLinkActive,
    CdkMenu,
    CdkMenuItem,
    CdkMenuItemRadio,
    CdkMenuTrigger,
    OpenSubmenuOnClick,
    CdkTrapFocus,
    TroveMark,
    Banner,
    ErrorNotice,
    Notices,
  ],
  // Escape closes the drawer (the key bubbles up from the focus trapped in it); the pinned sidebar stays.
  host: { '(keydown.escape)': 'closeDrawer()' },
  templateUrl: './app.html',
  styleUrl: './app.scss',
})
export class App {
  protected readonly layout = inject(Layout);
  protected readonly session = inject(SessionStore);
  protected readonly navigation = inject(Navigation);
  protected readonly preferences = inject(PreferencesStore);
  protected readonly navState = inject(NavState);
  protected readonly projectFilter = inject(ProjectFilter);
  protected readonly breadcrumbs = inject(Breadcrumbs);
  private readonly router = inject(Router);
  private readonly dialog = inject(TroveDialog);
  private readonly main = viewChild.required<ElementRef<HTMLElement>>('main');

  /**
   * Whether the active screen stands alone (route data `bare`), read when a navigation ends or fails;
   * null until the first one has. A cancelled navigation is left out: a guard's redirect cancels one
   * and starts the next, and the screen is only known when that one ends.
   */
  private readonly bare = toSignal(
    this.router.events.pipe(
      filter((e) => e instanceof NavigationEnd || e instanceof NavigationError),
      map(() => isBare(this.router.routerState.snapshot.root)),
    ),
    { initialValue: null },
  );
  /**
   * The shell renders only around a screen that is not bare, and only after the first navigation ended:
   * opening `/login` directly and arriving there from a guarded route look the same, and the top bar
   * never flashes before the sign-in screen. The router outlet itself never moves, so a screen is
   * created once.
   */
  protected readonly shell = computed(() => this.bare() === false);
  /** The side bar shows once the application knows whether the caller is signed in or anonymous. */
  protected readonly showNav = computed(
    () => this.session.state() === 'authenticated' || this.session.state() === 'anonymous',
  );
  /** The drawer below the pinned width is open. */
  private readonly drawerOpen = signal(false);
  /** The sidebar shows: pinned unless the viewer collapsed it, as a drawer while it is open. */
  protected readonly sidebarOpen = computed(() =>
    this.layout.pinned() ? !this.navState.collapsed() : this.drawerOpen(),
  );
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
  /** The manual page of the current screen, or the help overview (R-UI-050, Q-UI-90). */
  protected readonly help = computed(() => helpFor(this.url()));
  /** The area of the current route: its sidebar shows, and the Admin button is pressed in the admin area. */
  protected readonly area = computed(() => Navigation.areaOf(this.url()));
  protected readonly sidebarArea = computed(() => this.navigation.area(this.area()));
  /** The first letter of the signed-in name, for the badge of the user menu. */
  protected readonly initial = computed(() => (this.session.whoami()?.principal?.name ?? '').trim().charAt(0));
  protected readonly themes: readonly { value: UiTheme | null; label: string; icon: string }[] = [
    { value: null, label: $localize`:@@shell.theme.system:Follow the system`, icon: 'brightness_auto' },
    { value: 'light', label: $localize`:@@shell.theme.light:Light`, icon: 'light_mode' },
    { value: 'dark', label: $localize`:@@shell.theme.dark:Dark`, icon: 'dark_mode' },
  ];
  protected readonly loadFailure = computed(() =>
    this.session.state() === 'failed' ? (this.session.loadError() ?? new ApiError(0, null, null)) : null,
  );

  constructor() {
    inject(FocusOnNavigation).start();
    // Leaving the drawer's width closes it, so it does not reappear open when the window narrows again.
    effect(() => {
      if (this.layout.pinned()) untracked(() => this.drawerOpen.set(false));
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
          void ref.closed.forEach(() => this.session.clearReauthRequired());
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

  protected openSidebar(): void {
    if (this.layout.pinned()) this.navState.setCollapsed(false);
    else this.drawerOpen.set(true);
  }

  /** "Collapse sidebar": collapses the pinned sidebar (stored), closes the drawer. */
  protected closeSidebar(): void {
    if (this.layout.pinned()) this.navState.setCollapsed(true);
    else this.drawerOpen.set(false);
  }

  /** A link in the drawer, or Escape in it, closes it; the pinned sidebar stays. */
  protected closeDrawer(): void {
    if (!this.layout.pinned()) this.drawerOpen.set(false);
  }

  protected go(url: string): void {
    void this.router.navigateByUrl(url);
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

  protected setProject(key: string | null): void {
    void this.projectFilter.select(key);
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
