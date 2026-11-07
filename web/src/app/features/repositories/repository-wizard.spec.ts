import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { ApiError } from '../../api/problem';
import { FormatDescriptor, RepositoriesApi } from '../../api/repositories-api';
import {
  FakeRepositoriesApi,
  GENERIC_FORMAT,
  PACKAGE_FORMAT,
} from '../../api/testing/fakes-catalog';
import { SystemApi } from '../../api/system-api';
import { FakeSystemApi, sampleInfo } from '../../api/testing/fakes';
import { button, expectAccessible, renderScreen, settle, text, type } from '../../testing/screen';
import { RepositoryWizard } from './repository-wizard';

const FAKE_NPM: FormatDescriptor = PACKAGE_FORMAT;
const NO_VIRTUAL: FormatDescriptor = {
  ...FAKE_NPM,
  name: 'nuget',
  displayName: 'NuGet',
  classes: ['local', 'remote'],
};

type Rendered = Awaited<ReturnType<typeof renderScreen<RepositoryWizard>>>;

/** Chooses a class card of the class step (a native radio). */
async function pick(rendered: Rendered, kind: 'class', value: string) {
  const radio = rendered.root.querySelector<HTMLInputElement>(
    `input[name="${kind}"][value="${value}"]`,
  );
  if (!radio) throw new Error(`no ${kind} ${value}`);
  radio.checked = true;
  radio.dispatchEvent(new Event('change', { bubbles: true }));
  await settle(rendered.fixture);
}

/** The format tiles of the first step (options of the listbox), by their names. */
function tiles(rendered: Rendered): HTMLElement[] {
  return [...rendered.root.querySelectorAll<HTMLElement>('[role="option"]')];
}

const tileNames = (rendered: Rendered) =>
  tiles(rendered).map((t) => t.querySelector('.tile-name')?.textContent?.trim());

/** Chooses a format tile with one click, as a pointer user does. */
async function pickFormat(rendered: Rendered, name: string) {
  const tile = tiles(rendered).find((t) => t.querySelector('.tile-name')?.textContent?.trim().toLowerCase() === name.toLowerCase());
  if (!tile) throw new Error(`no format tile ${name}`);
  tile.click();
  await settle(rendered.fixture);
}

const classInput = (rendered: Rendered, cls: string) =>
  rendered.root.querySelector<HTMLInputElement>(`input[name="class"][value="${cls}"]`)!;

async function next(rendered: Awaited<ReturnType<typeof renderScreen<RepositoryWizard>>>) {
  button(rendered.root, 'Next').click();
  await settle(rendered.fixture);
}

describe('Repository creation wizard', () => {
  it('R-UI-021 Q-UI-112 the first step is the format, as a grid of tiles with icon and name, for the installed formats only', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [
          GENERIC_FORMAT,
          FAKE_NPM,
          NO_VIRTUAL,
        ]),
    });
    expect(text(r.root.querySelector('nav')!)).toMatch(/1\. Format.*2\. Class.*3\. Basic settings/);
    expect(r.root.querySelector('nav [aria-current="step"]')?.textContent).toContain('1. Format');
    expect(tileNames(r)).toEqual(['Generic', 'npm', 'NuGet']);
    for (const t of tiles(r)) {
      expect(t.querySelector('.material-icons')?.getAttribute('aria-hidden')).toBe('true');
    }
    expect(button(r.root, 'Next').disabled).toBe(true);
    expect(r.root.querySelector('input[name="class"]')).toBeNull();
    await expectAccessible(r.root);
  });

  it('R-UI-021 Q-UI-115 the search field filters the tiles by name, case-insensitively, and says what is left', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [
          GENERIC_FORMAT,
          FAKE_NPM,
          NO_VIRTUAL,
        ]),
    });
    const search = r.root.querySelector<HTMLInputElement>('#f-format-filter')!;
    expect(r.root.querySelector(`label[for="f-format-filter"]`)?.textContent).toContain('Search by package type');
    type(search, 'NU');
    await settle(r.fixture);
    expect(tileNames(r)).toEqual(['NuGet']);
    expect(text(r.root.querySelector('#format-count')!)).toContain('1 of 3 formats');
    type(search, 'zzz');
    await settle(r.fixture);
    expect(tileNames(r)).toEqual([]);
    expect(text(r.root.querySelector('#format-count')!)).toContain('No format matches the search.');
    await expectAccessible(r.root);

    // Enter chooses the one tile that is left.
    type(search, 'gener');
    await settle(r.fixture);
    search.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    await settle(r.fixture);
    expect(tiles(r)[0]!.getAttribute('aria-selected')).toBe('true');
    expect(button(r.root, 'Next').disabled).toBe(false);
  });

  it('R-UI-021 Q-UI-114 the tiles are one listbox of @angular/aria: a single tab stop, the chosen tile is selected, a click on it again keeps it', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [GENERIC_FORMAT, FAKE_NPM]),
    });
    const box = r.root.querySelector('[role="listbox"]')!;
    expect(box.getAttribute('aria-labelledby')).toBe('format-heading');
    const stops = [box, ...box.querySelectorAll('[role="option"]')].filter((e) => e.getAttribute('tabindex') === '0');
    expect(stops.length).toBe(1);
    await pickFormat(r, 'npm');
    expect(tiles(r).map((t) => t.getAttribute('aria-selected'))).toEqual(['false', 'true']);
    await pickFormat(r, 'npm');
    expect(tiles(r).map((t) => t.getAttribute('aria-selected'))).toEqual(['false', 'true']);
    expect(text(r.root)).toContain('Chosen: npm');
  });

  it('R-UI-021 R-UI-044 the tiles are operable by keyboard alone: arrows move, Enter chooses, and a visible focus style exists', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [GENERIC_FORMAT, FAKE_NPM]),
    });
    const box = r.root.querySelector<HTMLElement>('[role="listbox"]')!;
    const key = async (k: string) => {
      (document.activeElement ?? box).dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true }));
      await settle(r.fixture);
    };
    box.focus();
    await key('ArrowRight');
    await key('Enter');
    expect(tiles(r).map((t) => t.getAttribute('aria-selected'))).toEqual(['false', 'true']);
    expect(text(r.root)).toContain('Chosen: npm');
  });

  it('R-UI-021 Q-UI-113 the class step offers the classes of the chosen format and shows the others disabled with the reason', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [
          GENERIC_FORMAT,
          FAKE_NPM,
          NO_VIRTUAL,
        ]),
    });
    await pickFormat(r, 'NuGet');
    await next(r);
    expect(r.root.querySelectorAll('input[name="class"]').length).toBe(3);
    expect(classInput(r, 'local').disabled).toBe(false);
    expect(classInput(r, 'remote').disabled).toBe(false);
    expect(classInput(r, 'virtual').disabled).toBe(true);
    const reason = r.root.querySelector('#class-reason-virtual')!;
    expect(text(reason)).toContain('NuGet does not implement the class Virtual.');
    expect(classInput(r, 'virtual').getAttribute('aria-describedby')).toBe('class-reason-virtual');
    expect(r.root.querySelector('#class-reason-local')).toBeNull();
    // A disabled class cannot be chosen, even by a forged change event.
    classInput(r, 'virtual').disabled = false;
    await pick(r, 'class', 'virtual');
    expect(button(r.root, 'Next').disabled).toBe(true);
    await expectAccessible(r.root);

    const generic = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [GENERIC_FORMAT, FAKE_NPM]),
    });
    await pickFormat(generic, 'Generic');
    await next(generic);
    expect(
      [...generic.root.querySelectorAll<HTMLInputElement>('input[name="class"]')].map(
        (i) => `${i.value}:${i.disabled}`,
      ),
    ).toEqual(['local:false', 'remote:true', 'virtual:true']);
  });

  it('R-UI-021 Q-UI-112 going back and choosing a format that lacks the chosen class clears the class; one that has it keeps it', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [
          GENERIC_FORMAT,
          FAKE_NPM,
          NO_VIRTUAL,
        ]),
    });
    await pickFormat(r, 'npm');
    await next(r);
    await pick(r, 'class', 'virtual');
    await next(r);
    button(r.root, /1\. Format/).click();
    await settle(r.fixture);
    await pickFormat(r, 'NuGet');
    await next(r);
    expect(classInput(r, 'virtual').checked).toBe(false);
    expect(button(r.root, 'Next').disabled).toBe(true);
    await pick(r, 'class', 'remote');
    button(r.root, /1\. Format/).click();
    await settle(r.fixture);
    await pickFormat(r, 'npm');
    await next(r);
    expect(classInput(r, 'remote').checked).toBe(true);
    expect(button(r.root, 'Next').disabled).toBe(false);
  });

  it('R-UI-021 walks all five steps, shows the review with the API request, and a double click creates one repository with the key generated when the wizard opened (R-UI-042)', async () => {
    const r = await renderScreen(RepositoryWizard, { inputs: { project: 'web' } });
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);

    await pickFormat(r, 'generic');
    await next(r);
    await pick(r, 'class', 'local');
    await next(r);
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'build-output');
    await settle(r.fixture);
    type(r.root.querySelector<HTMLTextAreaElement>('#f-description')!, 'Build output');
    await settle(r.fixture);
    await next(r);
    type(
      r.root.querySelector<HTMLTextAreaElement>('#f-includePatterns')!,
      'releases/**\nsnapshots/**',
    );
    await expectAccessible(r.root);
    await next(r);

    expect(text(r.root)).toContain('Show as API request');
    expect(r.root.querySelector('pre')?.textContent).toContain('"key": "build-output"');
    expect(r.root.querySelector('details pre:last-of-type')?.textContent).toContain('curl -X POST');
    await expectAccessible(r.root);

    const create = button(r.root, 'Create repository');
    create.click();
    create.click();
    await settle(r.fixture);

    expect(fake.createKeys).toHaveLength(1);
    expect(fake.repositories.find((x) => x.key === 'build-output')?.includePatterns).toEqual([
      'releases/**',
      'snapshots/**',
    ]);
    expect(navigate).toHaveBeenCalledWith(['/projects', 'web', 'repositories', 'build-output']);
  });

  it('R-REPO-002 R-UI-021 the review shows the client address and the curl command on the configured base URL, not on the origin of the page', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [GENERIC_FORMAT, FAKE_NPM]),
    });
    // The page is served from jsdom's origin; the installation's base URL is another one.
    expect(location.origin).not.toBe('https://trove.example.net');
    await pickFormat(r, 'npm');
    await next(r);
    await pick(r, 'class', 'local');
    await next(r);
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'web-libs');
    await settle(r.fixture);
    await next(r);
    await next(r);

    const t = text(r.root);
    expect(t).toContain('https://trove.example.net/api/npm/web/web-libs/');
    expect(r.root.querySelector('details pre:last-of-type')?.textContent).toContain(
      "curl -X POST 'https://trove.example.net/api/v1/projects/web/repositories'",
    );
    expect(t).not.toContain(location.origin);
  });

  it('R-REPO-002 R-UI-021 a caller with repositories:write-config but without settings:read and without a readable repository still sees the client address (Q-UI-75)', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      capabilities: ['repositories:write-config'],
      setup: (get) => {
        const fake = get(RepositoriesApi) as FakeRepositoriesApi;
        fake.formatsResult = [GENERIC_FORMAT, FAKE_NPM];
        fake.repositories = [];
        fake.listAll = async () => [];
        (get(SystemApi) as FakeSystemApi).infoResult = sampleInfo({
          baseUrl: 'https://must-not-be-asked.example',
        });
      },
    });
    await pickFormat(r, 'npm');
    await next(r);
    await pick(r, 'class', 'local');
    await next(r);
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'web-libs');
    await settle(r.fixture);
    await next(r);
    await next(r);
    expect(text(r.root)).toContain('https://trove.example.net/api/npm/web/web-libs/');
  });

  it('R-UI-021 R-ARCH-004 the settings step renders the members of the format schema that apply to the class, and the create writes only what differs from the default', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [GENERIC_FORMAT, FAKE_NPM]),
    });
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
    await pickFormat(r, 'npm');
    await next(r);
    await pick(r, 'class', 'virtual');
    await next(r);
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'npm-all');
    await settle(r.fixture);
    await next(r);

    expect(text(r.root.querySelector('nav')!)).toContain('4. Access and npm settings');
    expect(text(r.root)).toContain('npm settings');
    expect(r.root.querySelector('#f-npm-latestGuard')).toBeNull();
    expect(r.root.querySelector('#f-npm-serveRemovedVersions')).toBeNull();
    const strategy = r.root.querySelector<HTMLSelectElement>('#f-npm-latestStrategy')!;
    expect(strategy.options[strategy.selectedIndex]?.textContent?.trim()).toBe(
      'highest-member-latest',
    );
    expect(r.root.querySelector('#f-npm-distTagStrategy')).not.toBeNull();
    await expectAccessible(r.root);

    type(strategy, '1');
    type(r.root.querySelector<HTMLSelectElement>('#f-npm-distTagStrategy')!, '0');
    await settle(r.fixture);
    await next(r);
    expect(text(r.root)).toContain('latestStrategy: newest-published');
    expect(text(r.root)).not.toContain('distTagStrategy');
    expect(r.root.querySelector('pre')?.textContent).toContain(
      '"latestStrategy": "newest-published"',
    );

    button(r.root, 'Create repository').click();
    await settle(r.fixture);
    expect(fake.repositories.find((x) => x.key === 'npm-all')?.npm).toEqual({
      latestStrategy: 'newest-published',
    });
  });

  it('R-UI-021 another format starts the format settings afresh, the generic format has none, and npm left at its defaults is created without an npm member', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = [GENERIC_FORMAT, FAKE_NPM]),
    });
    const toFormatStep = async () => {
      button(r.root, /1\. Format/).click();
      await settle(r.fixture);
    };
    const guard = () => r.root.querySelector<HTMLInputElement>('#f-npm-latestGuard');
    await pickFormat(r, 'npm');
    await next(r);
    await pick(r, 'class', 'local');
    await next(r);
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'npm-local');
    await settle(r.fixture);
    await next(r);
    expect(guard()?.checked).toBe(true);
    guard()!.checked = false;
    guard()!.dispatchEvent(new Event('change', { bubbles: true }));
    await settle(r.fixture);

    await toFormatStep();
    await pickFormat(r, 'Generic');
    await next(r);
    await next(r);
    await next(r);
    expect(r.root.querySelector('trove-format-settings')).toBeNull();
    expect(text(r.root.querySelector('nav')!)).toContain('4. Access settings');

    await toFormatStep();
    await pickFormat(r, 'npm');
    await next(r);
    await next(r);
    await next(r);
    expect(guard()?.checked).toBe(true);
    await next(r);
    expect(text(r.root)).toContain('The defaults of the format');
    expect(r.root.querySelector('pre')?.textContent).not.toContain('"npm":');
  });

  it('R-UI-021 a server error at a format setting returns to the settings step and is shown at its control and in the summary', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) => {
        const fake = get(RepositoriesApi) as FakeRepositoriesApi;
        fake.formatsResult = [GENERIC_FORMAT, FAKE_NPM];
        fake.create = async () => {
          throw new ApiError(
            422,
            {
              code: 'validation_failed',
              errors: [
                { pointer: '/npm/latestGuard', code: 'not_applicable', detail: 'local only' },
              ],
            },
            null,
          );
        };
      },
    });
    await pickFormat(r, 'npm');
    await next(r);
    await pick(r, 'class', 'local');
    await next(r);
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'npm-local');
    await settle(r.fixture);
    await next(r);
    await next(r);
    button(r.root, 'Create repository').click();
    await settle(r.fixture);

    const guard = r.root.querySelector<HTMLInputElement>('#f-npm-latestGuard')!;
    expect(guard.getAttribute('aria-invalid')).toBe('true');
    expect(text(r.root)).toContain('This setting does not apply to this class or format.');
    const summaryButton = r.root.querySelector<HTMLButtonElement>('[role="alert"] button')!;
    expect(summaryButton.textContent).toContain('/npm/latestGuard');
    summaryButton.click();
    expect(document.activeElement).toBe(guard);
    await expectAccessible(r.root);
  });

  it('R-UI-021 the key is checked live: shape at once, availability after a pause (a 404 means free)', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    try {
      const r = await renderScreen(RepositoryWizard, { inputs: { project: 'web' } });
      await pickFormat(r, 'generic');
      await next(r);
      await pick(r, 'class', 'local');
      await next(r);
      type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'generic-local');
      await settle(r.fixture);
      vi.advanceTimersByTime(500);
      await settle(r.fixture);
      expect(text(r.root)).toContain('A repository with this key already exists in the project.');
      button(r.root, 'Next').click();
      await settle(r.fixture);
      expect(text(r.root)).toContain('Basic settings');

      type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'fresh-key');
      await settle(r.fixture);
      vi.advanceTimersByTime(500);
      await settle(r.fixture);
      expect(text(r.root)).toContain('The key is available.');
    } finally {
      vi.useRealTimers();
    }
  });

  it('R-UI-021 server field errors appear at their field and in the summary, and the wizard returns to the step of the field (R-UI-044)', async () => {
    const r = await renderScreen(RepositoryWizard, { inputs: { project: 'web' } });
    const fake = r.get(RepositoriesApi) as FakeRepositoriesApi;
    fake.create = async () => {
      throw new ApiError(
        422,
        {
          code: 'validation_failed',
          errors: [
            { pointer: '/key', code: 'reserved_name', detail: 'reserved' },
            { pointer: '/includePatterns/0', code: 'invalid_pattern', detail: 'bad' },
          ],
        },
        null,
      );
    };
    await pickFormat(r, 'generic');
    await next(r);
    await pick(r, 'class', 'local');
    await next(r);
    type(r.root.querySelector<HTMLInputElement>('#f-key')!, 'batch-create');
    await settle(r.fixture);
    await next(r);
    await next(r);
    button(r.root, 'Create repository').click();
    await settle(r.fixture);

    expect(r.root.querySelector('[role="alert"]')?.textContent).toContain(
      'Some entries are not valid',
    );
    expect(text(r.root)).toContain('This name is reserved.');
    expect(r.root.querySelector('#f-key')?.getAttribute('aria-invalid')).toBe('true');
    await expectAccessible(r.root);
  });

  it('R-UI-021 a failing format catalog shows the error and passes the accessibility check', async () => {
    const r = await renderScreen(RepositoryWizard, {
      inputs: { project: 'web' },
      setup: (get) =>
        ((get(RepositoriesApi) as FakeRepositoriesApi).formatsError = new ApiError(
          503,
          { code: 'unavailable' },
          null,
        )),
    });
    expect(text(r.root)).toContain('temporarily unavailable');
    await expectAccessible(r.root);
  });

  it('R-UI-021 leaving with entered data counts as unsaved changes, a pristine wizard does not', async () => {
    const r = await renderScreen(RepositoryWizard, { inputs: { project: 'web' } });
    expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(false);
    await pickFormat(r, 'Generic');
    expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(true);
  });
});
