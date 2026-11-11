import { Component, computed, input } from '@angular/core';
import { PackageReadme } from '../../api/packages-api';
import { PackageReadmeView } from './package-readme';

/**
 * The prose of a documentation entry (R-UI-061): an array of HTML strings the server converted from the Ansible markup and
 * sanitized (R-ANS-039), one per paragraph. Each is bound the way the README is, through {@link PackageReadmeView}, so Angular's own
 * sanitizer runs a second time and links that leave trove get their `rel`; nothing here trusts the HTML.
 */
@Component({
  selector: 'trove-doc-prose',
  imports: [PackageReadmeView],
  template: `
    @for (r of readmes(); track $index) {
      <trove-package-readme [readme]="r" />
    }
  `,
  host: { class: 'doc-prose' },
})
export class DocProse {
  readonly html = input.required<readonly string[]>();

  protected readonly readmes = computed<readonly PackageReadme[]>(() =>
    this.html()
      .filter((h) => h.trim() !== '')
      .map((h) => ({ contentType: 'text/markdown', html: h, truncated: false, externalImagesBlocked: 0 })),
  );
}
