import { Component, signal } from '@angular/core';
import { TestBed } from '@angular/core/testing';
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
    // The column that cannot be sorted carries no aria-sort and no sort button. A stacked list shows exactly the headers with
    // aria-sort, as a row of sort buttons, and hides the others (Q-UI-391, _list-screens.scss).
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
    expect(root.querySelector('trove-empty-state')?.textContent).toContain('No widget matches.');
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

  it('R-UI-020 R-UI-065 a list with a fill column gives it what the declared widths of the others leave, and the headers carry them', async () => {
    @Component({
      imports: [DataTable],
      template: `
        <trove-data-table caption="Things" [columns]="columns">
          <tr>
            <th scope="row">one</th>
            <td>https://example.net/one/</td>
            <td>–</td>
            <td class="actions"><button type="button">x</button></td>
          </tr>
        </trove-data-table>
      `,
    })
    class Budget {
      readonly columns: readonly TableColumn[] = [
        { id: 'name', label: 'Name', width: '16rem' },
        { id: 'address', label: 'Address', fill: true, stackLabel: false },
        { id: 'when', label: 'When', width: '7rem', narrow: true },
        { id: 'actions', label: 'Actions', hideLabel: true, width: '2rem' },
      ];
    }
    TestBed.resetTestingModule();
    await TestBed.configureTestingModule({ imports: [Budget] }).compileComponents();
    const fixture = TestBed.createComponent(Budget);
    await fixture.whenStable();
    fixture.detectChanges();
    await fixture.whenStable();
    const root = fixture.nativeElement as HTMLElement;
    const table = root.querySelector<HTMLElement>('trove-data-table')!;
    expect(table.style.getPropertyValue('--trove-list-others')).toBe('calc(16rem + 7rem + 2rem + 4 * 2 * var(--trove-space-4))');
    expect(table.classList.contains('stack')).toBe(true);
    const heads = [...root.querySelectorAll<HTMLElement>('thead th')];
    expect(heads.map((h) => h.style.minInlineSize)).toEqual(['16rem', '', '7rem', '2rem']);
    // A narrow column gives its room back to the address below 64 rem of the list (UI-A-30).
    expect(table.style.getPropertyValue('--trove-list-narrow')).toBe('calc(7rem + 1 * 2 * var(--trove-space-4))');
    expect(heads.map((h) => h.classList.contains('narrow-drop'))).toEqual([false, false, true, false]);
    // Stacked, a cell names its column unless its column says not to; an empty value leaves its line out.
    const cells = [...root.querySelectorAll<HTMLElement>('tbody td')];
    expect(cells.map((c) => c.getAttribute('data-label'))).toEqual([null, 'When', null]);
    expect(cells[1]!.hasAttribute('data-empty')).toBe(true);
    expect(cells[0]!.hasAttribute('data-empty')).toBe(false);
  });
});
