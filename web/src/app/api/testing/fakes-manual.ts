import { Provider } from '@angular/core';
import { ManualApi, ManualContents, ManualPage } from '../manual-api';
import { ApiError } from '../problem';

/*
 * The in-memory fake of the manual operations (R-UI-047 component tier): two sections with two
 * pages each, HTML the way the server renders it (heading anchors `user-content-…`, links between
 * pages as `help/<section>/<page>`), and the reading order across the sections.
 */

const PAGES: readonly (Omit<ManualPage, 'previous' | 'next'>)[] = [
  {
    section: 'user',
    id: 'getting-started',
    title: 'Getting started',
    summary: 'How to sign in and find your way around.',
    html:
      '<p>How to sign in and find your way around.</p>' +
      '<h2 id="user-content-sign-in">Sign in</h2><p>See <a href="help/user/access-tokens#user-content-create-a-token">tokens</a> and <a href="#user-content-the-screen">below</a>.</p>' +
      '<h2 id="user-content-the-screen">The screen</h2><h3 id="user-content-the-top-bar">The top bar</h3><p>Text.</p>',
    headings: [
      { level: 2, id: 'user-content-sign-in', text: 'Sign in' },
      { level: 2, id: 'user-content-the-screen', text: 'The screen' },
      { level: 3, id: 'user-content-the-top-bar', text: 'The top bar' },
    ],
  },
  {
    section: 'user',
    id: 'access-tokens',
    title: 'Personal access tokens',
    summary: 'How to create, use and revoke tokens.',
    html: '<p>How to create, use and revoke tokens.</p><h2 id="user-content-create-a-token">Create a token</h2><p>Text.</p>',
    headings: [{ level: 2, id: 'user-content-create-a-token', text: 'Create a token' }],
  },
  {
    section: 'admin',
    id: 'overview',
    title: 'How trove runs',
    summary: 'The roles, the dependencies and the listeners.',
    html: '<p>The roles, the dependencies and the listeners.</p><h2 id="user-content-roles">Roles</h2><p>Text.</p><table><thead><tr><th>Role</th><th>Purpose</th></tr></thead><tbody><tr><td><code>all</code></td><td>Serves the API and the UI.</td></tr></tbody></table>',
    headings: [{ level: 2, id: 'user-content-roles', text: 'Roles' }],
  },
  {
    section: 'admin',
    id: 'endpoints',
    title: 'Endpoints and exposure rules',
    summary: 'Further listeners and what they show.',
    html: '<p>Further listeners and what they show.</p><h2 id="user-content-exposure-rules">Exposure rules</h2><p>Text.</p>',
    headings: [{ level: 2, id: 'user-content-exposure-rules', text: 'Exposure rules' }],
  },
];

const TITLES: Record<string, string> = { user: 'User guide', admin: 'Administrator guide' };

export class FakeManualApi extends ManualApi {
  calls: string[] = [];
  failure: ApiError | null = null;

  override async contents(): Promise<ManualContents> {
    this.calls.push('contents');
    if (this.failure) throw this.failure;
    return {
      sections: ['user', 'admin'].map((id) => ({
        id,
        title: TITLES[id] ?? id,
        pages: PAGES.filter((p) => p.section === id).map((p) => ({ id: p.id, title: p.title, summary: p.summary })),
      })),
    };
  }

  override async page(section: string, page: string): Promise<ManualPage> {
    this.calls.push(`${section}/${page}`);
    if (this.failure) throw this.failure;
    const i = PAGES.findIndex((p) => p.section === section && p.id === page);
    const p = PAGES[i];
    if (!p) throw new ApiError(404, { code: 'not_found', detail: 'The manual has no such page.' }, null);
    const ref = (q: (typeof PAGES)[number] | undefined) => (q ? { section: q.section, id: q.id, title: q.title } : null);
    return { ...p, previous: ref(PAGES[i - 1]), next: ref(PAGES[i + 1]) };
  }
}

export function provideManualFakes(): Provider[] {
  return [{ provide: ManualApi, useClass: FakeManualApi }];
}
