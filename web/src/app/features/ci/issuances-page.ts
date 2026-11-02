import { Component } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { RouterLink } from '@angular/router';
import { IssuanceList } from './issuance-list';

/** The issued CI tokens of every issuer (R-UI-035, R-CI-020), for the token administrators. */
@Component({
  selector: 'trove-ci-issuances-page',
  imports: [MatButtonModule, RouterLink, IssuanceList],
  template: `
    <header class="page-header">
      <h1 tabindex="-1" i18n="@@ci.issuances.heading">Issued CI tokens</h1>
      <a mat-button routerLink="/admin/ci" i18n="@@ci.issuances.back">CI identity</a>
    </header>
    <p i18n="@@ci.issuances.hint">
      Every token a job received through the exchange, with the job it was issued to. A record holds no token. Filter to find the tokens of one pipeline or job and revoke them together.
    </p>
    <trove-ci-issuance-list />
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class IssuancesPage {}
