import { Component } from '@angular/core';
import { RouterLink, RouterLinkActive } from '@angular/router';

/** The three pages of the profile as a link list; the current page is marked with `aria-current`. */
@Component({
  selector: 'trove-profile-nav',
  imports: [RouterLink, RouterLinkActive],
  template: `
    <nav i18n-aria-label="@@profile.nav" aria-label="Profile pages">
      <ul class="tabs">
        <li>
          <a routerLink="/profile" routerLinkActive="active" [routerLinkActiveOptions]="{ exact: true }" ariaCurrentWhenActive="page" i18n="@@profile.nav.profile">Profile</a>
        </li>
        <li>
          <a routerLink="/profile/tokens" routerLinkActive="active" ariaCurrentWhenActive="page" i18n="@@profile.nav.tokens">Personal tokens</a>
        </li>
        <li>
          <a routerLink="/profile/sessions" routerLinkActive="active" ariaCurrentWhenActive="page" i18n="@@profile.nav.sessions">Sessions</a>
        </li>
      </ul>
    </nav>
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProfileNav {}
