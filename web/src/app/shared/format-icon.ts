import { Component, computed, input } from '@angular/core';

/**
 * What stands for a format in the package list's type column, on the package page and on the format tiles of the
 * repository wizard (R-UI-006, R-UI-021, Q-UI-225): the logo of the format's own ecosystem, where its owner's terms allow
 * a product to show it, or the neutral package glyph of the icon font.
 *
 * A logo is a file of `web/public/formats/`, unchanged from its owner's source, named relative to the base href. `dark`
 * is the owner's own variant for a dark ground, shown in the dark scheme in place of `src`; `plate` puts a light plate
 * behind a mark that has no such variant and would sink into the dark ground (Q-UI-226). Each file's owner, source and
 * terms are in `web/public/formats/NOTICE.txt`, which the build appends to the notices (Q-UI-227).
 */
export type FormatIcon =
  | { readonly kind: 'logo'; readonly src: string; readonly dark: string | null; readonly plate: boolean }
  | { readonly kind: 'glyph'; readonly ligature: string };

/** The neutral glyph: a format without a brand (generic), whose owner does not allow the logo here (npm, Ansible), or unknown. */
const NEUTRAL: FormatIcon = { kind: 'glyph', ligature: 'inventory_2' };

/**
 * The one function that decides a format's icon. The UI knows no list of formats (FMT-001): a format not named here gets
 * the neutral glyph. npm's and Ansible's owners allow their logos only with their permission (Q-UI-225), so both keep the
 * glyph; OCI shows the Open Container Initiative's mark, not Docker's, whose terms admit the logo only with Docker's
 * authorization.
 */
export function formatIcon(name: string): FormatIcon {
  switch (name) {
    case 'nuget':
      return { kind: 'logo', src: 'formats/nuget/nuget.svg', dark: 'formats/nuget/nuget_96_mono_w.png', plate: false };
    case 'oci':
      return {
        kind: 'logo',
        src: 'formats/oci/oci-icon-color.svg',
        dark: 'formats/oci/oci-icon-white.svg',
        plate: false,
      };
    case 'deb':
      return { kind: 'logo', src: 'formats/deb/openlogo-nd.svg', dark: null, plate: true };
    default:
      return NEUTRAL;
  }
}

/**
 * `<trove-format-icon [format]>`: the icon `formatIcon` decides, one em square, so the place sets its size with
 * `font-size`. It is decoration: every place shows the format's name as text beside it (R-UI-006: icon plus text, never
 * icon alone), so the logo has an empty `alt` and the glyph is hidden from assistive technology, and a screen reader says
 * the name once. Which variant of a logo shows follows trove's theme, not only the system's: the classes `logo-on-light`
 * and `logo-on-dark` are switched by `_icons.scss` on `data-theme` (an `<img>` cannot read it).
 */
@Component({
  selector: 'trove-format-icon',
  template: `
    @let i = icon();
    @if (i.kind === 'logo') {
      <img [src]="i.src" alt="" [class.logo-on-light]="i.dark !== null" [class.plate]="i.plate" />
      @if (i.dark; as dark) {
        <img [src]="dark" alt="" class="logo-on-dark" />
      }
    } @else {
      <span class="material-icons" aria-hidden="true">{{ i.ligature }}</span>
    }
  `,
  styles: `
    :host {
      display: inline-flex;
      flex: none;
      align-items: center;
      justify-content: center;
      inline-size: 1em;
      block-size: 1em;
      line-height: 1;
      vertical-align: middle;
    }
    .material-icons {
      font-size: 1em;
    }
    img {
      box-sizing: border-box;
      inline-size: 100%;
      block-size: 100%;
      padding: 0.0625em;
      border-radius: 0.1875em;
      object-fit: contain;
    }
    .plate {
      background: var(--trove-logo-plate);
    }
  `,
})
export class FormatIconView {
  /** The format's name as the API spells it (`deb`, `nuget`, `oci`, …). */
  readonly format = input.required<string>();

  protected readonly icon = computed(() => formatIcon(this.format()));
}
