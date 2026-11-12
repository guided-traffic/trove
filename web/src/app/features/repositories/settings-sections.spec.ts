import { ApiError } from '../../api/problem';
import { RepositoriesApi, Repository } from '../../api/repositories-api';
import {
  DEB_FORMAT,
  DEB_SETTINGS_SCHEMA,
  FakeRepositoriesApi,
  GENERIC_FORMAT,
  OCI_FORMAT,
} from '../../api/testing/fakes-catalog';
import {
  button,
  expectAccessible,
  openSection,
  renderScreen,
  saveButton,
  sectionHeadings,
  settle,
  text,
  type,
} from '../../testing/screen';
import { RepositoryDetail } from './repository-detail';
import { EMPTY_FORM, FormatMemberOf, RepositoryForm } from './repository-model';
import { SettingSchema } from './settings-schema';
import { rebaseForm, sectionPatch } from './settings-sections';

const DEB: FormatMemberOf = { name: 'deb', schema: DEB_SETTINGS_SCHEMA as SettingSchema, formatMirror: true, remote: 'structural', mirror: true };

const OCI_REMOTE = {
  key: 'docker-hub',
  project: 'web',
  class: 'remote',
  format: 'oci',
  description: 'Docker Hub, cached',
  visibility: { artifacts: 'private', wiki: 'private' },
  remote: { url: 'https://registry-1.docker.io/', mode: 'pullThrough', mirror: {} },
} as unknown as Repository;

async function openSettings(repo: Repository, capabilities?: readonly string[]) {
  const r = await renderScreen(RepositoryDetail, {
    inputs: { project: repo.project ?? 'web', key: repo.key },
    ...(capabilities ? { capabilities } : {}),
    setup: (get) => {
      const fake = get(RepositoriesApi) as FakeRepositoriesApi;
      fake.formatsResult = [GENERIC_FORMAT, OCI_FORMAT, DEB_FORMAT];
      fake.repositories.push(repo);
    },
  });
  return { ...r, fake: r.get(RepositoriesApi) as FakeRepositoriesApi };
}

describe('The sections of the repository settings page (R-UI-087)', () => {
  describe('the pure rules', () => {
    const base: RepositoryForm = { ...EMPTY_FORM, formatMember: { origin: 'acme', mirror: { suites: ['stable'] } } };

    it('R-UI-087 each section patches only its members, and the format member is split between Mirror and the format settings', () => {
      const now: RepositoryForm = {
        ...base,
        description: 'new',
        includePatterns: 'a/**',
        formatMember: { origin: 'other', mirror: { suites: ['stable', 'testing'] } },
      };
      expect(sectionPatch('general', base, now, DEB)).toEqual({ description: 'new' });
      expect(sectionPatch('access', base, now, DEB)).toEqual({ includePatterns: ['a/**'] });
      expect(sectionPatch('format', base, now, DEB)).toEqual({ deb: { origin: 'other' } });
      expect(sectionPatch('mirror', base, now, DEB)).toEqual({ deb: { mirror: { suites: ['stable', 'testing'] } } });
      expect(sectionPatch('notes', base, now, DEB)).toEqual({});
    });

    it('R-UI-087 emptying the format settings beside a mirror object clears members, never the whole member', () => {
      const now: RepositoryForm = { ...base, formatMember: { mirror: { suites: ['stable'] } } };
      expect(sectionPatch('format', base, now, DEB)).toEqual({ deb: { origin: null } });
      const plain: FormatMemberOf = { name: 'deb', schema: DEB_SETTINGS_SCHEMA as SettingSchema };
      expect(sectionPatch('format', { ...EMPTY_FORM, formatMember: { origin: 'acme' } }, EMPTY_FORM, plain)).toEqual({ deb: null });
    });

    it('R-UI-087 after a save the saved section takes the stored values and every other section keeps its unsaved edits', () => {
      const current: RepositoryForm = { ...base, description: 'mine', adminNotes: 'unsaved note' };
      const stored: RepositoryForm = { ...base, description: 'mine' };
      const next = rebaseForm(base, current, stored, DEB, 'general');
      expect(next.description).toBe('mine');
      expect(next.adminNotes).toBe('unsaved note');
      expect(rebaseForm(base, current, { ...stored, description: 'theirs' }, DEB, 'notes').adminNotes).toBe('');
    });
  });

  describe('the page', () => {
    it('R-UI-087 a remote OCI repository shows key, description, visibility and upstream open, every other section closed, and no tab', async () => {
      const r = await openSettings(OCI_REMOTE);
      expect(r.root.querySelector('[role="tab"]')).toBeNull();
      expect([...r.root.querySelectorAll('trove-page-header a')].map((a) => text(a))).toEqual(['Browse', 'Vulnerabilities']);
      expect(r.root.querySelector<HTMLInputElement>('#f-key')!.readOnly).toBe(true);
      expect(r.root.querySelector<HTMLTextAreaElement>('#f-description')!.value).toBe('Docker Hub, cached');
      expect(r.root.querySelector('trove-repository-visibility')).not.toBeNull();
      expect(r.root.querySelector('trove-upstream-settings')).not.toBeNull();
      expect(sectionHeadings(r.root)).toEqual(['Mirror', 'Access details', 'Administrator notes', 'How to use', 'Details', 'API', 'Advanced']);
      for (const toggle of r.root.querySelectorAll('trove-disclosure .toggle')) expect(toggle.getAttribute('aria-expanded')).toBe('false');
      for (const h of r.root.querySelectorAll('h1, h2, h3')) expect(text(h)).not.toMatch(/ and /);
      await expectAccessible(r.root);
    });

    it('R-UI-087 saving a changed description sends it and nothing else', async () => {
      const r = await openSettings(OCI_REMOTE);
      type(r.root.querySelector<HTMLTextAreaElement>('#f-description')!, 'Docker Hub');
      await settle(r.fixture);
      saveButton(r.root).click();
      await settle(r.fixture);
      expect(r.fake.patches.map((p) => p.patch)).toEqual([{ description: 'Docker Hub' }]);
    });

    it('R-UI-087 an unsaved include pattern in the closed Access details says so after its title, and leaving asks', async () => {
      const r = await openSettings(OCI_REMOTE);
      await openSection(r.fixture, r.root, 'Access details');
      type(r.root.querySelector<HTMLTextAreaElement>('#f-includePatterns')!, 'library/**');
      await settle(r.fixture);
      button(r.root, /Access details/).click();
      await settle(r.fixture);
      const toggle = button(r.root, /Access details/);
      expect(toggle.getAttribute('aria-expanded')).toBe('false');
      expect(text(toggle)).toContain('Unsaved changes');
      expect(r.fixture.componentInstance.hasUnsavedChanges()).toBe(true);
    });

    it('R-UI-087 R-UI-042 a 412 on one section opens the conflict dialog for that section, and another section keeps its unsaved edit', async () => {
      const r = await openSettings(OCI_REMOTE);
      await openSection(r.fixture, r.root, 'Administrator notes');
      type(r.root.querySelector<HTMLTextAreaElement>('#f-adminNotes')!, 'note of mine');
      type(r.root.querySelector<HTMLTextAreaElement>('#f-description')!, 'mine');
      await settle(r.fixture);
      r.fake.beforePatch = () => {
        r.fake.versions.set('web/docker-hub', 2);
        r.fake.beforePatch = null;
      };
      saveButton(r.root, 'Description (optional)').click();
      await settle(r.fixture);
      const dialog = r.root.ownerDocument.body.querySelector<HTMLElement>('[role="dialog"]')!;
      expect(text(dialog)).toContain('description');
      expect(text(dialog)).not.toContain('adminNotes');
      button(dialog, /Overwrite/).click();
      await settle(r.fixture);
      expect(r.fake.patches.at(-1)).toMatchObject({ patch: { description: 'mine' }, etag: '"2"' });
      expect(r.root.querySelector<HTMLTextAreaElement>('#f-adminNotes')!.value).toBe('note of mine');
    });

    it('R-UI-087 a server error in a section is shown in that section only', async () => {
      const r = await openSettings(OCI_REMOTE);
      r.fake.patch = async () => {
        throw new ApiError(
          422,
          { code: 'validation_failed', errors: [{ pointer: '/includePatterns/0', code: 'invalid_pattern', detail: 'bad' }] },
          null,
        );
      };
      await openSection(r.fixture, r.root, 'Access details');
      type(r.root.querySelector<HTMLTextAreaElement>('#f-includePatterns')!, '[');
      await settle(r.fixture);
      saveButton(r.root, 'Access details').click();
      await settle(r.fixture);
      expect(r.root.querySelector('#f-includePatterns')!.getAttribute('aria-invalid')).toBe('true');
      expect(r.root.querySelectorAll('[role="alert"]').length).toBe(1);
      const access = [...r.root.querySelectorAll('trove-disclosure')].find((d) => d.querySelector('#f-includePatterns'))!;
      expect(access.querySelector('[role="alert"]')).not.toBeNull();
    });

    it('R-UI-087 Disable repository asks first, patches disabled alone and shows the badge; Enable turns it back', async () => {
      const r = await openSettings(OCI_REMOTE);
      await openSection(r.fixture, r.root, 'Advanced');
      button(r.root, 'Disable repository').click();
      await settle(r.fixture);
      const dialog = r.root.ownerDocument.body.querySelector<HTMLElement>('[role="dialog"]')!;
      expect(text(dialog)).toContain('Clients can no longer download from it or publish to it');
      button(dialog, 'Disable repository').click();
      await settle(r.fixture);
      expect(r.fake.patches.map((p) => p.patch)).toEqual([{ disabled: true }]);
      expect(text(r.root.querySelector('.subject')!)).toContain('Disabled');
      button(r.root, 'Enable repository').click();
      await settle(r.fixture);
      button(r.root.ownerDocument.body.querySelector<HTMLElement>('[role="dialog"]')!, 'Enable repository').click();
      await settle(r.fixture);
      expect(r.fake.patches.at(-1)?.patch).toEqual({ disabled: false });
    });

    it('R-UI-087 a caller who may read but not change the configuration sees the fields read-only and no save', async () => {
      const r = await openSettings(OCI_REMOTE, ['repositories:read-config']);
      expect(r.root.querySelector<HTMLTextAreaElement>('#f-description')!.readOnly).toBe(true);
      expect([...r.root.querySelectorAll('button')].some((b) => /Save changes/.test(b.textContent ?? ''))).toBe(false);
      expect(sectionHeadings(r.root)).not.toContain('Advanced');
      await expectAccessible(r.root);
    });
  });
});
