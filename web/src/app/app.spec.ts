import { TestBed } from '@angular/core/testing';
import { Router, provideRouter } from '@angular/router';
import { App } from './app';
import { routes } from './app.routes';
import { BannerStore } from './core/banner-store';

async function render() {
  await TestBed.configureTestingModule({
    imports: [App],
    providers: [provideRouter(routes)],
  }).compileComponents();
  const fixture = TestBed.createComponent(App);
  await TestBed.inject(Router).navigateByUrl('/');
  await fixture.whenStable();
  return { fixture, root: fixture.nativeElement as HTMLElement };
}

describe('App shell', () => {
  it('R-UI-002 renders header, navigation, main region and the home placeholder', async () => {
    const { root } = await render();
    expect(root.querySelector('mat-toolbar')?.textContent).toContain('trove');
    expect(root.querySelector('nav[aria-label]')).toBeTruthy();
    expect(root.querySelector('main#main')).toBeTruthy();
    expect(root.querySelector('main h1')?.textContent).toContain('Status');
  });

  it('R-UI-002 the skip link is the first focusable element and focuses the main heading', async () => {
    const { root } = await render();
    document.body.appendChild(root);
    const first = root.querySelector<HTMLElement>('a, button, [tabindex="0"]');
    expect(first?.classList.contains('skip-link')).toBe(true);
    first?.click();
    expect(document.activeElement?.tagName).toBe('H1');
    root.remove();
  });

  it('R-UI-002 shows the banner area only while a banner text is set', async () => {
    const { fixture, root } = await render();
    expect(root.querySelector('[role="region"]')).toBeNull();
    TestBed.inject(BannerStore).set('Maintenance tonight');
    await fixture.whenStable();
    const banner = root.querySelector('[role="region"]');
    expect(banner?.getAttribute('aria-label')).toBeTruthy();
    expect(banner?.textContent).toContain('Maintenance tonight');
  });

  it('R-UI-001 shows the not-found page for an unknown route', async () => {
    const { fixture, root } = await render();
    await TestBed.inject(Router).navigateByUrl('/no/such/page');
    await fixture.whenStable();
    expect(root.querySelector('main h1')?.textContent).toContain('Page not found');
  });
});
