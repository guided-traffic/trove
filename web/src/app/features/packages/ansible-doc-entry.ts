import { Component, computed, input, linkedSignal } from '@angular/core';
import { PackageDocEntry, PackageReadme } from '../../api/packages-api';
import { CopyButton } from '../../shared/copy-button';
import { DataTable, TableColumn } from '../../shared/ui/data-table';
import { DocProse } from './doc-prose';
import { DocRow, optionRows, returnRows, visibleRows } from './docs-model';
import { PackageReadmeView } from './package-readme';
import { safeHttpUrl, EXTERNAL_REL } from './package-links';

/** A sample of this many characters or fewer on one line stays inline in its cell; anything else is a code block with a copy button. */
const INLINE_SAMPLE = 60;

/**
 * One page of the documentation of a module, a plugin or a role (R-UI-061, Q-UI-242), in the order ansible-doc prints it: the head, then
 * Synopsis, Parameters, Notes, Requirements, Attributes, See also, Examples, Return values and Authors, each only when it has content.
 *
 * Only the prose (`description`, `notes`, `requirements`, the description of an option) is HTML, and it is bound through the README's
 * component. Every other value of the entry, a name, a type, a default, a choice, a sample, an author, is bound as text, so that a hostile
 * default cannot become markup.
 */
@Component({
  selector: 'trove-ansible-doc-entry',
  imports: [CopyButton, DataTable, DocProse, PackageReadmeView],
  templateUrl: './ansible-doc-entry.html',
  styleUrl: './ansible-doc-entry.scss',
})
export class AnsibleDocEntry {
  readonly entry = input.required<PackageDocEntry>();

  protected readonly externalRel = EXTERNAL_REL;
  protected readonly safeHttpUrl = safeHttpUrl;

  /** The parameters that are folded; a new page starts with every parameter open. */
  protected readonly foldedOptions = linkedSignal<PackageDocEntry, ReadonlySet<string>>({
    source: this.entry,
    computation: () => new Set<string>(),
  });
  protected readonly foldedReturns = linkedSignal<PackageDocEntry, ReadonlySet<string>>({
    source: this.entry,
    computation: () => new Set<string>(),
  });

  private readonly allOptions = computed(() => optionRows(this.entry().options));
  private readonly allReturns = computed(() => returnRows(this.entry().returnValues));
  protected readonly options = computed(() => visibleRows(this.allOptions(), this.foldedOptions()));
  protected readonly returns = computed(() => visibleRows(this.allReturns(), this.foldedReturns()));

  protected readonly readme = computed<PackageReadme | null>(() => {
    const html = this.entry().readmeHtml;
    return html ? { contentType: 'text/markdown', html, truncated: false, externalImagesBlocked: 0 } : null;
  });

  protected readonly optionColumns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@docs.col.name:Name` },
    { id: 'type', label: $localize`:@@docs.col.type:Type` },
    { id: 'required', label: $localize`:@@docs.col.required:Required` },
    { id: 'default', label: $localize`:@@docs.col.default:Default` },
    { id: 'choices', label: $localize`:@@docs.col.choices:Choices` },
    { id: 'description', label: $localize`:@@docs.col.description:Description` },
  ];
  protected readonly returnColumns: readonly TableColumn[] = [
    { id: 'name', label: $localize`:@@docs.col.name:Name` },
    { id: 'type', label: $localize`:@@docs.col.type:Type` },
    { id: 'returned', label: $localize`:@@docs.col.returned:Returned` },
    { id: 'sample', label: $localize`:@@docs.col.sample:Sample` },
    { id: 'description', label: $localize`:@@docs.col.description:Description` },
  ];
  protected readonly optionsCaption = $localize`:@@docs.options.caption:Parameters of this entry`;
  protected readonly returnsCaption = $localize`:@@docs.returns.caption:Return values of this entry`;
  protected readonly copyName = $localize`:@@docs.copy-name:Copy the full name`;
  protected readonly copyExamples = $localize`:@@docs.copy-examples:Copy the examples`;

  /** 16 px per level (Q-UI-242). */
  protected indent(row: DocRow): string {
    return `calc(var(--trove-space-3) + ${row.depth * 16}px)`;
  }

  protected inlineSample(row: DocRow): boolean {
    return row.sample.length <= INLINE_SAMPLE && !row.sample.includes('\n');
  }

  protected toggleOption(row: DocRow): void {
    this.foldedOptions.update((s) => flip(s, row.id));
  }

  protected toggleReturn(row: DocRow): void {
    this.foldedReturns.update((s) => flip(s, row.id));
  }

  protected foldLabel(row: DocRow, folded: ReadonlySet<string>): string {
    return folded.has(row.id)
      ? $localize`:@@docs.unfold:Show the suboptions of ${row.name}:name:`
      : $localize`:@@docs.fold:Hide the suboptions of ${row.name}:name:`;
  }
}

function flip(set: ReadonlySet<string>, id: string): ReadonlySet<string> {
  const next = new Set(set);
  if (!next.delete(id)) next.add(id);
  return next;
}
