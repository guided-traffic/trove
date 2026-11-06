import { Component } from '@angular/core';
import { MARK_GEOMETRY } from './mark-geometry';

/**
 * trove's mark drawn inline (web/scripts/brand-logo.mjs generates the geometry): the band and the neutral lane take
 * `currentColor`, so the mark follows the theme the viewer chose, which the SVG files in public/brand cannot (an
 * `<img>` only knows the system's colour scheme). Decorative: the element next to it carries the name.
 */
@Component({
  selector: 'trove-mark',
  template: `
    <svg [attr.viewBox]="g.viewBox" aria-hidden="true" focusable="false">
      <g [attr.transform]="g.transform">
        @for (l of g.lanes; track l.lane) {
          <path [attr.d]="l.d" fill="none" [attr.stroke-width]="l.width" [attr.class]="l.hue" />
        }
        <path
          class="neutral"
          [attr.d]="g.band.d"
          fill="none"
          [attr.stroke-width]="g.band.width"
          stroke-linecap="round"
        />
      </g>
    </svg>
  `,
  styles: `
    :host {
      display: inline-block;
      line-height: 0;
    }
    svg {
      block-size: 100%;
      inline-size: auto;
      overflow: hidden;
    }
    .neutral {
      stroke: currentColor;
    }
    .blue {
      stroke: var(--trove-brand-blue);
    }
    .red {
      stroke: var(--trove-brand-red);
    }
  `,
})
export class TroveMark {
  protected readonly g = MARK_GEOMETRY;
}
