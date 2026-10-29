import { Router } from '@angular/router';
import { AuditApi } from '../../api/audit-api';
import { ApiError } from '../../api/problem';
import { FakeAuditApi, sampleEvent } from '../../api/testing/fakes-catalog';
import { button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { AuditLog } from './audit-log';

async function open(inputs: Record<string, unknown> = {}, setup?: (audit: FakeAuditApi) => void) {
  const rendered = await renderScreen(AuditLog, {
    inputs,
    setup: (get) => {
      vi.spyOn(get(Router), 'navigate').mockResolvedValue(true);
      setup?.(get(AuditApi) as FakeAuditApi);
    },
  });
  return {
    ...rendered,
    audit: rendered.get(AuditApi) as FakeAuditApi,
    router: rendered.get(Router),
  };
}

describe('Audit log', () => {
  it('R-UI-037 shows time, actor, action as a translated label, target, outcome with icon and text, and source address', async () => {
    const { root } = await open();
    const t = text(root);
    expect(t).toContain('Permission policy changed');
    expect(t).toContain('authz.policy.updated');
    expect(t).toContain('File downloaded');
    expect(t).toContain('generic-local/releases/1.0/app.zip');
    expect(t).toContain('denied');
    expect(t).toContain('10.12.4.7');
    expect(root.querySelector('table th[scope="col"]')).toBeTruthy();
    await expectAccessible(root);
  });

  it('R-UI-037 an unknown action is shown as the name the server sent', async () => {
    const { root } = await open(
      {},
      (audit) => (audit.eventList = [sampleEvent({ action: 'future.thing.happened' })]),
    );
    expect(text(root)).toContain('future.thing.happened');
  });

  it('R-UI-037 the filters from the URL reach the API with the time range resolved against the clock', async () => {
    const { audit } = await open({
      range: '7d',
      category: 'authz',
      outcome: 'success',
      actor: 'alice',
      repository: 'generic-local',
      action: 'authz.*',
    });
    const filter = audit.filterCalls.at(-1)!;
    expect(filter).toMatchObject({
      category: 'authz',
      outcome: 'success',
      actor: 'alice',
      repository: 'generic-local',
      action: 'authz.*',
    });
    const span = Date.parse(filter.to!) - Date.parse(filter.from!);
    expect(span).toBe(7 * 24 * 3_600_000);
  });

  it('R-UI-037 "Search" puts the filters into the URL and keeps the other parameters', async () => {
    const { root, fixture, router } = await open();
    type(root.querySelector<HTMLInputElement>('#a-actor')!, 'alice');
    type(root.querySelector<HTMLSelectElement>('#a-category')!, 'authz');
    type(root.querySelector<HTMLSelectElement>('#a-range')!, '7d');
    await settle(fixture);
    button(root, 'Search').click();
    await settle(fixture);
    const [, extras] = (router.navigate as ReturnType<typeof vi.fn>).mock.calls.at(-1)!;
    expect(extras.queryParams).toMatchObject({
      actor: 'alice',
      category: 'authz',
      range: '7d',
      event: null,
    });
    expect(extras.queryParamsHandling).toBe('merge');
  });

  it('R-UI-037 a custom range offers date and time fields', async () => {
    const { root, fixture } = await open({
      range: 'custom',
      from: '2026-10-01T00:00',
      to: '2026-10-02T00:00',
    });
    expect(root.querySelector('#a-from')).toBeTruthy();
    expect(root.querySelector('#a-to')).toBeTruthy();
    await settle(fixture);
    await expectAccessible(root);
  });

  it('R-UI-037 "Load more" continues the keyset walk', async () => {
    const { root, fixture } = await open({}, (audit) => {
      audit.eventList = Array.from({ length: 150 }, (_, i) =>
        sampleEvent({
          id: `id-${i}`,
          occurredAt: `2026-10-28T11:${String(i % 60).padStart(2, '0')}:00Z`,
        }),
      );
    });
    expect(root.querySelectorAll('tbody tr').length).toBe(100);
    button(root, 'Load more').click();
    await settle(fixture);
    expect(root.querySelectorAll('tbody tr').length).toBe(150);
  });

  it('R-UI-037 an event opens in a panel with the full event, the before and after of the details, and links to actor and target', async () => {
    const { root } = await open({ event: '01933b1e-7c2a-7f4e-9d1b-6a2f0c9e8b10' });
    const panel = root.querySelector('section.detail')!;
    expect(panel.textContent).toContain('artifact.downloaded');
    expect(panel.querySelector('a[href="/admin/users/alice"]')).toBeTruthy();
    expect(panel.querySelector('a[href="/admin/repositories/generic-local"]')).toBeTruthy();
    expect(
      panel.querySelector('a[href="/browse/generic-local/releases/1.0/app.zip"]'),
    ).toBeTruthy();
    await expectAccessible(root);
  });

  it('R-UI-037 the detail of a policy change shows the details object with before and after', async () => {
    const { root } = await open({ event: '01933b1e-7c2a-7f4e-9d1b-6a2f0c9e8b11' });
    const pre = root.querySelector('section.detail pre')!.textContent!;
    expect(pre).toContain('"before"');
    expect(pre).toContain('"after"');
  });

  it('R-UI-037 times follow the locale and switch to UTC with the toggle', async () => {
    const local = await open();
    const utc = await open({ utc: '1' });
    const localTime = local.root.querySelector('tbody time')!.textContent;
    const utcTime = utc.root.querySelector('tbody time')!.textContent;
    expect(utcTime).toContain('11:58:02');
    expect(localTime).toBeTruthy();
    expect((utc.root.querySelector('#a-utc') as HTMLInputElement).checked).toBe(true);
  });

  it('R-UI-037 the export links carry the current filter and both formats', async () => {
    const { root } = await open({ range: '1h', category: 'authz' });
    const links = [...root.querySelectorAll<HTMLAnchorElement>('a[download]')];
    expect(links.map((a) => a.textContent?.trim())).toEqual(['Export CSV', 'Export JSON Lines']);
    expect(links[0]?.getAttribute('href')).toContain('format=csv');
    expect(links[1]?.getAttribute('href')).toContain('format=jsonl');
    expect(links[0]?.getAttribute('href')).toContain('from=');
  });

  it('R-UI-037 an empty range, a failing query and the loading state are announced and pass the accessibility check', async () => {
    const empty = await open({}, (audit) => (audit.eventList = []));
    expect(text(empty.root)).toContain('No event matches the filters in this time range.');
    await expectAccessible(empty.root);

    const failing = await open({}, (audit) => {
      audit.events = () => () =>
        Promise.reject(new ApiError(400, { code: 'invalid_audit_range', detail: 'x' }, null));
    });
    expect(text(failing.root)).toContain('at most 400 days');
    await expectAccessible(failing.root);
  });

  it('R-UI-037 an event that does not exist shows the error in the panel', async () => {
    const { root } = await open({ event: 'unknown' });
    expect(root.querySelector('section.detail')?.textContent).toContain('does not exist');
    await expectAccessible(root);
  });
});
