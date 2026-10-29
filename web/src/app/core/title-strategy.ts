import { Injectable, inject } from '@angular/core';
import { Title } from '@angular/platform-browser';
import { RouterStateSnapshot, TitleStrategy } from '@angular/router';

/** Suffix of every page title (R-UI-003, WCAG 2.4.2). The product name is not translated. */
export const TITLE_SUFFIX = 'trove';

/** "Packages – trove": the route's `title` plus the product name. */
@Injectable({ providedIn: 'root' })
export class TroveTitleStrategy extends TitleStrategy {
  private readonly title = inject(Title);

  override updateTitle(snapshot: RouterStateSnapshot): void {
    const own = this.buildTitle(snapshot);
    this.title.setTitle(own ? `${own} – ${TITLE_SUFFIX}` : TITLE_SUFFIX);
  }
}
