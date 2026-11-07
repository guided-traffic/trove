import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { settle } from '../../testing/screen';
import { TroveAvatar } from './avatar';
import { robotDataUri } from './robot';

@Component({
  imports: [TroveAvatar],
  template: `<trove-avatar [seed]="seed()" [imageUrl]="imageUrl()" />`,
})
class Host {
  readonly seed = signal('00000000-0000-0000-0000-000000000001');
  readonly imageUrl = signal<string | null>(null);
}

describe('trove avatar (R-UI-002)', () => {
  async function render() {
    await TestBed.configureTestingModule({ imports: [Host] }).compileComponents();
    const fixture = TestBed.createComponent(Host);
    await settle(fixture);
    await fixture.whenStable();
    const img = () => fixture.nativeElement.querySelector('img') as HTMLImageElement;
    return { fixture, host: fixture.componentInstance, img };
  }

  it('R-UI-002 the robot of a seed is the same SVG every time and differs between seeds', () => {
    const a = robotDataUri('00000000-0000-0000-0000-000000000001');
    expect(robotDataUri('00000000-0000-0000-0000-000000000001')).toBe(a);
    expect(a.startsWith('data:image/svg+xml')).toBe(true);
    const b = robotDataUri('00000000-0000-0000-0000-000000000002');
    expect(b).not.toBe(a);

    // The SVG references nothing outside itself: no script, no embedded image or foreign content, and every link is
    // a fragment of the document (the URLs in its metadata are text, which an <img> never loads).
    const svg = decodeURIComponent(a.slice(a.indexOf(',') + 1));
    expect(svg.startsWith('<svg')).toBe(true);
    expect(svg).not.toMatch(/<script|<image|<foreignObject|@import|url\(\s*['"]?(?!#)/i);
    const links = [...svg.matchAll(/\bhref="([^"]*)"/g)].map((m) => m[1]);
    expect(links.length).toBeGreaterThan(0);
    expect(links.every((l) => l!.startsWith('#'))).toBe(true);
  });

  it('R-UI-002 the image is decorative and shows the robot of the seed', async () => {
    const { img, host } = await render();
    expect(img().getAttribute('alt')).toBe('');
    expect(img().getAttribute('src')).toBe(robotDataUri(host.seed()));
  });

  it('R-UI-002 an uploaded avatar wins over the robot, and the robot returns without it', async () => {
    const { fixture, host, img } = await render();
    host.imageUrl.set('/ui/avatars/alice.png');
    await settle(fixture);
    expect(img().getAttribute('src')).toBe('/ui/avatars/alice.png');
    host.imageUrl.set(null);
    await settle(fixture);
    expect(img().getAttribute('src')).toBe(robotDataUri(host.seed()));
  });
});
