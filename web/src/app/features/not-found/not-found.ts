import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';

@Component({
  selector: 'trove-not-found',
  imports: [RouterLink],
  template: `
    <h1 tabindex="-1" i18n="@@notfound.heading">Page not found</h1>
    <p i18n="@@notfound.text">This address does not lead to a page of this application.</p>
    <a routerLink="/" i18n="@@notfound.home">Back to the start page</a>
  `,
})
export class NotFound {}
