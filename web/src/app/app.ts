import { Component, ElementRef, inject, viewChild } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatListModule } from '@angular/material/list';
import { MatSidenav, MatSidenavModule } from '@angular/material/sidenav';
import { MatToolbarModule } from '@angular/material/toolbar';
import { RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';
import { Layout } from './core/layout';
import { Banner } from './shared/banner';

/** Root component: skip link, header, navigation, banner area and router outlet (R-UI-002). */
@Component({
  selector: 'trove-root',
  imports: [
    RouterOutlet,
    RouterLink,
    RouterLinkActive,
    MatButtonModule,
    MatListModule,
    MatSidenavModule,
    MatToolbarModule,
    Banner,
  ],
  templateUrl: './app.html',
  styleUrl: './app.scss',
})
export class App {
  protected readonly layout = inject(Layout);
  private readonly main = viewChild.required<ElementRef<HTMLElement>>('main');

  /** Skip link target: moves focus to the main heading, or to the main region without one. */
  protected skipToMain(event: Event): void {
    event.preventDefault();
    const region = this.main().nativeElement;
    const heading = region.querySelector<HTMLElement>('h1');
    (heading ?? region).focus();
  }

  protected closeDrawer(drawer: MatSidenav): void {
    if (!this.layout.pinned()) {
      void drawer.close();
    }
  }
}
