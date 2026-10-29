import { Component } from '@angular/core';
import { TokenManager } from '../tokens/token-manager';
import { ProfileNav } from './profile-nav';

/** Personal tokens (R-UI-029): list, create with one-time display, revoke, rotate. */
@Component({
  selector: 'trove-profile-tokens',
  imports: [ProfileNav, TokenManager],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@profile.tokens.heading">Personal tokens</h1>
    </header>
    <trove-profile-nav />
    <p i18n="@@profile.tokens.intro">
      A personal token lets a tool such as npm act as you. Give each tool its own token and revoke it when you no longer need it.
    </p>
    <trove-token-manager [scope]="scope" />
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProfileTokens {
  protected readonly scope = { kind: 'own' } as const;
}
