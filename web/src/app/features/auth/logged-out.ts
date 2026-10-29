import { Component } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { RouterLink } from '@angular/router';

/**
 * Landing page of the post-logout redirect `<baseURL>/ui/logged-out` (R-UI-004). It needs no
 * session, never asks the server who the caller is and never sends the browser to an identity
 * provider on its own: with auto-redirect that would sign the user straight back in. The route
 * name is registered at the identity provider and therefore a contract.
 */
@Component({
  selector: 'trove-logged-out',
  imports: [MatButtonModule, RouterLink],
  template: `
    <h1 tabindex="-1" i18n="@@loggedout.heading">Signed out</h1>
    <p i18n="@@loggedout.text">You have been signed out.</p>
    <a mat-flat-button routerLink="/login" i18n="@@loggedout.again">Sign in again</a>
  `,
})
export class LoggedOut {}
