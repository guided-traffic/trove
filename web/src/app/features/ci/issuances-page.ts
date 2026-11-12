import { Component } from '@angular/core';
import { IssuanceList } from './issuance-list';
import { PageHeader } from '../../shared/ui/page-header';

/** The issued CI tokens of every issuer (R-UI-035, R-CI-020), for the token administrators. */
@Component({
  selector: 'trove-ci-issuances-page',
  imports: [PageHeader, IssuanceList],
  template: `
    <trove-page-header heading="Issued CI tokens" i18n-heading="@@ci.issuances.heading" />
    <p i18n="@@ci.issuances.hint">
      Every token a job received through the exchange, with the job it was issued to. A record holds no token. Filter to find the tokens of one pipeline or job and revoke them together.
    </p>
    <trove-ci-issuance-list />
  `,
  styleUrl: '../../shared/access/access.scss',
})
export class IssuancesPage {}
