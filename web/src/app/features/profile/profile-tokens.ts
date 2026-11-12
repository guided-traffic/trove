import { Component } from '@angular/core';
import { PageHeader } from '../../shared/ui/page-header';
import { TokenManager } from '../tokens/token-manager';
import { ProfileNav } from './profile-nav';

/**
 * Personal tokens (R-UI-029): list, create with one-time display, revoke, rotate. The shared header holds **Create token** at its
 * right end (Q-UI-307, UI-A-20); the manager opens its form below.
 */
@Component({
  selector: 'trove-profile-tokens',
  imports: [PageHeader, ProfileNav, TokenManager],
  template: `
    <trove-page-header [heading]="heading" [description]="intro">
      @if (!manager.isFormOpen()) {
        <button actions class="btn btn-confirm" type="button" (click)="manager.openForm()" i18n="@@tokens.create">Create token</button>
      }
    </trove-page-header>
    <trove-profile-nav />
    <trove-token-manager #manager [scope]="scope" [createInToolbar]="false" />
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class ProfileTokens {
  protected readonly scope = { kind: 'own' } as const;
  protected readonly heading = $localize`:@@profile.tokens.heading:Personal tokens`;
  protected readonly intro = $localize`:@@profile.tokens.description:A personal token lets a tool such as npm act as you; give each tool its own token.`;
}
