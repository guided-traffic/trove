import { Component } from '@angular/core';

/** Empty start page for M0. The system status screen (R-UI-038) replaces it. */
@Component({
  selector: 'trove-home',
  template: `
    <h1 tabindex="-1" i18n="@@home.heading">Status</h1>
    <p i18n="@@home.placeholder">
      The system status appears here once the management API is available.
    </p>
  `,
})
export class Home {}
