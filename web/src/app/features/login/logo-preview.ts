import { Component } from '@angular/core';

/** The drafts of web/scripts/brand-logo.mjs that the preview shows; keep in step with its `alternatives`. */
const ALTERNATIVES: readonly { key: string; note: string }[] = [
  { key: 'Z1', note: 'the candidate: blue, neutral, red' },
  { key: 'Z4', note: 'red on the middle path up to half height, then curves out' },
  { key: 'G1', note: 'before: green, neutral, red' },
];

/**
 * The logo while it is being designed with the owner: the three forms of the candidate (the mark, the square with
 * the wordmark, the horizontal lockup) on a dark and a light ground, the mark at small sizes, and the alternatives.
 * Rendered by the sign-in screen in development builds only (`isDevMode()`); it goes once the logo is chosen.
 * The files come from web/scripts/brand-logo.mjs.
 */
@Component({
  selector: 'trove-logo-preview',
  template: `
    <section class="preview" aria-labelledby="logo-preview-title">
      <h2 id="logo-preview-title">Logo draft (development builds only)</h2>
      <div class="grounds">
        @for (scheme of schemes; track scheme) {
          <div class="ground" [class.light]="scheme === 'light'">
            <figure>
              <img
                [src]="'brand/drafts/mark-' + scheme + '.svg'"
                alt="Mark"
                width="128"
                height="128"
              />
              <figcaption>Mark</figcaption>
            </figure>
            <figure>
              <img
                src="brand/trove-square.svg"
                alt="Square with wordmark"
                width="160"
                height="160"
              />
              <figcaption>Square with wordmark</figcaption>
            </figure>
            <figure>
              <img
                [src]="'brand/drafts/horizontal-' + scheme + '.svg'"
                alt="Horizontal"
                width="285"
                height="96"
              />
              <figcaption>Horizontal</figcaption>
            </figure>
            <figure>
              <span class="sizes">
                @for (size of sizes; track size) {
                  <img
                    [src]="'brand/drafts/mark-' + scheme + '.svg'"
                    [alt]="'Mark at ' + size + ' px'"
                    [width]="size"
                    [height]="size"
                  />
                }
              </span>
              <figcaption>64, 32, 16 px</figcaption>
            </figure>
          </div>
        }
      </div>
      <h3>Alternatives</h3>
      <div class="ground alternatives">
        @for (alt of alternatives; track alt.key) {
          <figure>
            <img
              [src]="'brand/drafts/alt-' + alt.key + '.svg'"
              [alt]="'Draft ' + alt.key"
              width="96"
              height="96"
            />
            <figcaption>
              <strong>{{ alt.key }}</strong> {{ alt.note }}
            </figcaption>
          </figure>
        }
      </div>
    </section>
  `,
  styles: `
    :host {
      display: block;
      inline-size: min(64rem, calc(100vw - 2 * var(--trove-space-4)));
      margin-block-start: var(--trove-space-8);
      margin-inline-start: 50%;
      transform: translateX(-50%);
    }
    h2,
    h3 {
      font-size: 0.875rem;
      color: var(--trove-text-muted);
    }
    .grounds {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(28rem, 1fr));
      gap: var(--trove-space-4);
    }
    .ground {
      display: flex;
      flex-wrap: wrap;
      align-items: flex-end;
      gap: var(--trove-space-6);
      padding: var(--trove-space-6);
      border: 1px solid var(--trove-border);
      border-radius: var(--trove-radius-m);
      background: #040c18;
      color: #9aa1ad;
    }
    .ground.light {
      background: #ffffff;
      color: #5b6270;
    }
    figure {
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: var(--trove-space-2);
      margin: 0;
      font-size: 0.75rem;
    }
    .sizes {
      display: flex;
      align-items: flex-end;
      gap: var(--trove-space-3);
    }
    .alternatives {
      align-items: flex-start;
    }
    .alternatives figcaption {
      max-inline-size: 8rem;
      text-align: center;
    }
  `,
})
export class LogoPreview {
  protected readonly schemes = ['dark', 'light'] as const;
  protected readonly sizes = [64, 32, 16] as const;
  protected readonly alternatives = ALTERNATIVES;
}
