import notice from '../../../public/formats/NOTICE.txt' with { loader: 'text' };
import debian from '../../../public/formats/deb/openlogo-nd.svg' with { loader: 'text' };
import nuget from '../../../public/formats/nuget/nuget.svg' with { loader: 'text' };
import ociColour from '../../../public/formats/oci/oci-icon-color.svg' with { loader: 'text' };
import ociWhite from '../../../public/formats/oci/oci-icon-white.svg' with { loader: 'text' };
import { expectAccessible, renderScreen } from '../testing/screen';
import { spokenText } from '../testing/axe';
import { FormatIcon, FormatIconView, formatIcon } from './format-icon';

/** The formats trove has today; the UI holds no such list (FMT-001), the spec does to cover each answer. */
const FORMATS = ['generic', 'npm', 'nuget', 'oci', 'deb', 'ansible'] as const;

/** The files a logo names: its mark and its variant for a dark ground. */
const files = (i: FormatIcon): string[] => (i.kind === 'logo' ? [i.src, ...(i.dark ? [i.dark] : [])] : []);

/** The SVG files of public/formats as text; a PNG is not a document and cannot carry script. */
const SVG: Readonly<Record<string, string>> = {
  'formats/deb/openlogo-nd.svg': debian,
  'formats/nuget/nuget.svg': nuget,
  'formats/oci/oci-icon-color.svg': ociColour,
  'formats/oci/oci-icon-white.svg': ociWhite,
};

async function render(format: string) {
  return renderScreen(FormatIconView, { inputs: { format } });
}

describe('formatIcon', () => {
  it('R-UI-006 R-UI-021 Q-UI-225 Debian, NuGet and OCI show the logo of their owner, OCI the Open Container Initiative mark', () => {
    expect(formatIcon('deb')).toEqual({ kind: 'logo', src: 'formats/deb/openlogo-nd.svg', dark: null, plate: true });
    expect(formatIcon('nuget')).toEqual({
      kind: 'logo',
      src: 'formats/nuget/nuget.svg',
      dark: 'formats/nuget/nuget_96_mono_w.png',
      plate: false,
    });
    expect(formatIcon('oci')).toEqual({
      kind: 'logo',
      src: 'formats/oci/oci-icon-color.svg',
      dark: 'formats/oci/oci-icon-white.svg',
      plate: false,
    });
  });

  it('R-UI-006 R-UI-021 Q-UI-225 npm and Ansible, whose owners do not allow their logos here, the generic format and an unknown one get the neutral glyph', () => {
    for (const name of ['npm', 'ansible', 'generic', 'cargo', '']) {
      expect(formatIcon(name)).toEqual({ kind: 'glyph', ligature: 'inventory_2' });
    }
  });

  it('Q-UI-227 every logo file is named in the notices with its owner, source and licence', () => {
    const logos = FORMATS.flatMap((f) => files(formatIcon(f)));
    expect(logos.length).toBe(5);
    const named = [...notice.matchAll(/^Files?: (.+)$/gm)].flatMap((m) =>
      (m[1] ?? '').replace(/ \([^)]*\)/g, '').split(', '),
    );
    for (const file of logos) {
      expect(named).toContain(file);
    }
    expect(notice).toContain('trove is not affiliated with, sponsored or endorsed by any of them.');
    expect(notice).toContain('Debian is a registered trademark owned by Software in the Public Interest, Inc.');
    expect(notice).toContain('Creative Commons Attribution 2.0');
    expect(notice).toContain('Apache License, Version 2.0');
  });

  it('Q-UI-226 no logo carries a script, an event handler, a foreign object or a reference outside its own file', () => {
    // The files are served at trove's origin without the page's CSP; opened on their own, an SVG document would run its
    // script there. The owners' files are taken unchanged, so this is checked rather than cleaned.
    const svgs = FORMATS.flatMap((f) => files(formatIcon(f))).filter((p) => p.endsWith('.svg'));
    for (const path of svgs) {
      const text = SVG[path];
      expect(text, `${path} is not checked here`).toBeDefined();
      expect(text).not.toMatch(/<script|<foreignObject|\son[a-z]+\s*=|javascript:|<!ENTITY[^>]*SYSTEM/i);
      for (const ref of (text ?? '').matchAll(/(?:href|src)\s*=\s*"([^"]*)"|url\(([^)]*)\)/g)) {
        expect(ref[1] ?? ref[2], `${path} refers outside itself`).toMatch(/^#/);
      }
    }
  });
});

describe('FormatIconView', () => {
  it('R-UI-006 Q-UI-226 a logo is an image with an empty alt and the owner variant for the dark scheme beside it, both hidden from assistive technology', async () => {
    const r = await render('oci');
    const imgs = [...r.root.querySelectorAll('img')];
    expect(imgs.map((i) => [i.getAttribute('src'), i.getAttribute('alt'), i.className])).toEqual([
      ['formats/oci/oci-icon-color.svg', '', 'logo-on-light'],
      ['formats/oci/oci-icon-white.svg', '', 'logo-on-dark'],
    ]);
    expect(r.root.querySelector('.material-icons')).toBeNull();
    expect(spokenText(r.root)).toBe('');
    await expectAccessible(r.root);
  });

  it('R-UI-006 Q-UI-226 a mark without a dark variant stands on the plate, in both schemes the same image', async () => {
    const r = await render('deb');
    const imgs = [...r.root.querySelectorAll('img')];
    expect(imgs.length).toBe(1);
    expect(imgs[0]?.getAttribute('src')).toBe('formats/deb/openlogo-nd.svg');
    expect(imgs[0]?.getAttribute('alt')).toBe('');
    expect(imgs[0]?.classList.contains('plate')).toBe(true);
    expect(imgs[0]?.classList.contains('logo-on-light')).toBe(false);
  });

  it('R-UI-006 Q-UI-225 a format without a logo shows the neutral glyph, hidden from assistive technology', async () => {
    const r = await render('npm');
    expect(r.root.querySelector('img')).toBeNull();
    const glyph = r.root.querySelector('.material-icons')!;
    expect(glyph.textContent?.trim()).toBe('inventory_2');
    expect(glyph.getAttribute('aria-hidden')).toBe('true');
    expect(spokenText(r.root)).toBe('');
  });

  it('R-UI-006 another format replaces the icon', async () => {
    const r = await render('nuget');
    expect(r.root.querySelector('img')?.getAttribute('src')).toBe('formats/nuget/nuget.svg');
    r.fixture.componentRef.setInput('format', 'ansible');
    await r.fixture.whenStable();
    r.fixture.detectChanges();
    expect(r.root.querySelector('img')).toBeNull();
    expect(r.root.querySelector('.material-icons')?.textContent?.trim()).toBe('inventory_2');
  });
});
