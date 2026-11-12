import { TestBed } from '@angular/core/testing';
import { Router } from '@angular/router';
import { RepositoriesApi } from '../../api/repositories-api';
import {
  ANSIBLE_FORMAT,
  DEB_FORMAT,
  FakeRepositoriesApi,
  GENERIC_FORMAT,
  NUGET_FORMAT,
  OCI_FORMAT,
  PACKAGE_FORMAT,
} from '../../api/testing/fakes-catalog';
import { spokenText } from '../../testing/axe';
import { expectAccessible, inProject, renderScreen, settle, text, type } from '../../testing/screen';
import { RepositoryFormatChooser, matchingFormats } from './repository-format-chooser';

const ALL = [GENERIC_FORMAT, PACKAGE_FORMAT, NUGET_FORMAT, OCI_FORMAT, DEB_FORMAT, ANSIBLE_FORMAT];

async function chooser(url = '/projects/web/repositories/new', inputs: Record<string, unknown> = {}) {
  return renderScreen(
    RepositoryFormatChooser,
    inProject({ inputs, setup: (get) => ((get(RepositoriesApi) as FakeRepositoriesApi).formatsResult = ALL) }, url),
  );
}

const tiles = (root: ParentNode) => [...root.querySelectorAll<HTMLAnchorElement>('a.tile')];

describe('The format chooser of a new repository (R-UI-080)', () => {
  it('R-UI-080 is a page titled New repository with one link per installed format, its icon and its name, and no Next button', async () => {
    const r = await chooser();
    expect(text(r.root.querySelector('h1')!)).toBe('New repository');
    expect(text(r.root)).toContain('Choose the package format. It decides what the repository can do.');
    expect(tiles(r.root).map((t) => spokenText(t))).toEqual(['Generic', 'npm', 'NuGet', 'OCI (Docker)', 'Debian (APT)', 'Ansible Galaxy']);
    expect(tiles(r.root).map((t) => t.getAttribute('href'))).toEqual([
      '/projects/web/repositories/new/generic',
      '/projects/web/repositories/new/npm',
      '/projects/web/repositories/new/nuget',
      '/projects/web/repositories/new/oci',
      '/projects/web/repositories/new/deb',
      '/projects/web/repositories/new/ansible',
    ]);
    for (const t of tiles(r.root)) expect(t.querySelector('trove-format-icon.tile-icon')).not.toBeNull();
    expect([...r.root.querySelectorAll('button')].some((b) => /Next/.test(b.textContent ?? ''))).toBe(false);
    expect(r.root.querySelector('[role="listbox"]')).toBeNull();
    await expectAccessible(r.root);
  });

  it('R-UI-080 the search filters the tiles by name, says what is left, and Enter opens the one format that is left', async () => {
    const r = await chooser();
    const navigate = vi.spyOn(TestBed.inject(Router), 'navigate').mockResolvedValue(true);
    const search = r.root.querySelector<HTMLInputElement>('#f-format-search')!;
    type(search, 'nu');
    await settle(r.fixture);
    expect(tiles(r.root).map((t) => spokenText(t))).toEqual(['NuGet']);
    expect(text(r.root.querySelector('#format-count')!)).toBe('1 of 6 formats');
    search.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    await settle(r.fixture);
    expect(navigate).toHaveBeenCalledWith(['/projects/web/repositories/new/nuget'], { queryParams: {} });

    type(search, 'zzz');
    await settle(r.fixture);
    expect(tiles(r.root)).toEqual([]);
    expect(text(r.root.querySelector('#format-count')!)).toBe('No format matches the search.');
    await expectAccessible(r.root);
  });

  it('R-UI-080 the admin area carries the project of the list to the form', async () => {
    const r = await chooser('/admin/repositories/new', { project: 'data' });
    expect(tiles(r.root)[1]!.getAttribute('href')).toBe('/admin/repositories/new/npm?project=data');
  });

  it('R-UI-080 an unknown format of the address is named in a notice above the tiles', async () => {
    const r = await chooser('/projects/web/repositories/new/rpm', { unknownFormat: 'rpm' });
    expect(text(r.root.querySelector('[role="note"]')!)).toContain('This installation has no format rpm.');
    expect(tiles(r.root)[0]!.getAttribute('href')).toBe('/projects/web/repositories/new/generic');
  });

  it('R-UI-080 matches by display name and by name, whatever the case', () => {
    expect(matchingFormats(ALL, 'DOCKER').map((f) => f.name)).toEqual(['oci']);
    expect(matchingFormats(ALL, 'deb').map((f) => f.name)).toEqual(['deb']);
    expect(matchingFormats(ALL, ' ').length).toBe(6);
  });
});
