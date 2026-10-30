import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { MATERIAL_ANIMATIONS } from '@angular/material/core';
import { ApiError } from '../../api/problem';
import { seriousViolations } from '../../testing/axe';
import { DataTable, TableColumn, TableSort } from './data-table';

@Component({
  imports: [DataTable],
  template: `
    <trove-data-table
      caption="Widgets"
      [columns]="columns"
      [sort]="sort()"
      (sortChange)="sort.set($event); sorts.push($event)"
      [loaded]="loaded()"
      [loading]="loading()"
      [error]="error()"
      [empty]="rows().length === 0"
      emptyText="No widget matches."
      [hasMore]="hasMore()"
      (more)="mores = mores + 1"
      (retry)="retries = retries + 1"
    >
      @for (r of rows(); track r) {
        <tr>
          <th scope="row">{{ r }}</th>
          <td>detail</td>
        </tr>
      }
    </trove-data-table>
  `,
})
class Host {
  readonly columns: readonly TableColumn[] = [
    { id: 'name', label: 'Name', sortable: true },
    { id: 'detail', label: 'Detail', help: 'Explains the detail column.' },
  ];
  readonly sort = signal<TableSort | null>(null);
  readonly loaded = signal(true);
  readonly loading = signal(false);
  readonly error = signal<ApiError | null>(null);
  readonly rows = signal<readonly string[]>(['alpha', 'beta']);
  readonly hasMore = signal(false);
  readonly sorts: TableSort[] = [];
  mores = 0;
  retries = 0;
}

async function render() {
  await TestBed.configureTestingModule({
    imports: [Host],
    providers: [{ provide: MATERIAL_ANIMATIONS, useValue: { animationsDisabled: true } }],
  }).compileComponents();
  const fixture = TestBed.createComponent(Host);
  await fixture.whenStable();
  const root = fixture.nativeElement as HTMLElement;
  const settle = async () => {
    fixture.detectChanges();
    await fixture.whenStable();
  };
  return { fixture, root, host: fixture.componentInstance, settle };
}

describe('Data table', () => {
  it('R-UI-020 shows a captioned table in a focusable scroll region with column headers and the projected rows', async () => {
    const { root } = await render();
    const region = root.querySelector('[role="region"]')!;
    expect(region.getAttribute('tabindex')).toBe('0');
    expect(root.querySelector('caption')?.textContent?.trim()).toBe('Widgets');
    expect([...root.querySelectorAll('th[scope="col"]')].map((h) => h.textContent?.trim())).toEqual([
      'Nameunfold_more',
      'Detailhelp_outline',
    ]);
    expect(root.querySelectorAll('tbody tr')).toHaveLength(2);
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-020 a sortable header is a button that announces the order with aria-sort and asks the screen to sort', async () => {
    const { root, host, settle } = await render();
    const header = root.querySelector('th[scope="col"]')!;
    expect(header.getAttribute('aria-sort')).toBe('none');
    // The column that cannot be sorted carries no aria-sort and no sort button.
    expect(root.querySelectorAll('th[scope="col"]')[1]!.hasAttribute('aria-sort')).toBe(false);
    expect(root.querySelectorAll('th button.th-sort')).toHaveLength(1);

    header.querySelector<HTMLButtonElement>('button.th-sort')!.click();
    await settle();
    expect(host.sorts.at(-1)).toEqual({ column: 'name', direction: 'asc' });
    expect(header.getAttribute('aria-sort')).toBe('ascending');
    expect(header.querySelector('.th-arrow')?.textContent).toBe('arrow_upward');

    header.querySelector<HTMLButtonElement>('button.th-sort')!.click();
    await settle();
    expect(host.sorts.at(-1)).toEqual({ column: 'name', direction: 'desc' });
    expect(header.getAttribute('aria-sort')).toBe('descending');
    expect(header.querySelector('.th-arrow')?.textContent).toBe('arrow_downward');
  });

  it('R-UI-044 the help button of a column names the column, is a real button and is described by its tooltip text', async () => {
    const { root } = await render();
    const help = root.querySelectorAll<HTMLButtonElement>('th button.th-help');
    expect(help).toHaveLength(1);
    expect(help[0]!.type).toBe('button');
    expect(help[0]!.getAttribute('aria-label')).toBe('About the column Detail');
    // The tooltip directive points aria-describedby at its message, so the help is not mouse-only.
    expect(help[0]!.getAttribute('aria-describedby')).toBeTruthy();
  });

  it('R-UI-042 shows loading, empty and error states instead of the table', async () => {
    const { root, host, settle } = await render();
    host.loaded.set(false);
    host.loading.set(true);
    await settle();
    expect(root.querySelector('[role="status"]')?.textContent).toContain('Loading.');
    expect(root.querySelector('table')).toBeNull();

    host.loaded.set(true);
    host.loading.set(false);
    host.rows.set([]);
    await settle();
    expect(root.querySelector('.table-empty')?.textContent).toContain('No widget matches.');
    expect(root.querySelector('table')).toBeNull();

    host.error.set(new ApiError(503, { code: 'unavailable' }, 'rid-1'));
    await settle();
    const retry = [...root.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Retry')!;
    retry.click();
    expect(host.retries).toBe(1);
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-006 "Load more" appears while the list has more and asks the screen for the next page', async () => {
    const { root, host, settle } = await render();
    expect([...root.querySelectorAll('button')].some((b) => b.textContent?.trim() === 'Load more')).toBe(false);
    host.hasMore.set(true);
    await settle();
    [...root.querySelectorAll('button')].find((b) => b.textContent?.trim() === 'Load more')!.click();
    expect(host.mores).toBe(1);
  });
});
