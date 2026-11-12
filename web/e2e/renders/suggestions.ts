import { Page } from '@playwright/test';
import { group, images, kinds, npmPackages, projects, repositories } from './seed';

/** The answer member of one kind (R-API-060). */
interface Member<T> {
  items: T[];
  more: boolean;
}

/**
 * The suggestions of the search field answered from the seed (R-API-060's shape), by `page.route`: the renders judge the list's
 * look and its states with the seed's short and long keys, and the server's matching and authorization have their own tests
 * (R-API-061, R-API-062). Every term of the text is part of the qualified name, the name, the key or the display name.
 */
export async function routeSuggestions(page: Page): Promise<void> {
  await page.route('**/api/v1/search/suggestions?**', async (route) => {
    const url = new URL(route.request().url());
    const terms = (url.searchParams.get('q') ?? '').trim().toLowerCase().split(/\s+/).filter(Boolean);
    const kindsAsked = (url.searchParams.get('kind') ?? 'repository,package,project').split(',');
    const limit = Number(url.searchParams.get('limit') ?? '5');
    const matches = (...texts: string[]): boolean => terms.every((t) => texts.some((s) => s.toLowerCase().includes(t)));
    const cut = <T>(rows: T[]): Member<T> => ({ items: rows.slice(0, limit), more: rows.length > limit });
    const body: Record<string, Member<unknown>> = {};
    if (kindsAsked.includes('repository')) {
      body['repositories'] = cut(
        repositories
          .filter((r) => matches(`${r.project}/${r.key}`))
          .map((r) => ({ project: r.project, key: r.key, format: r.format, class: r.class })),
      );
    }
    if (kindsAsked.includes('package')) {
      const npm = [...new Map(npmPackages.map((p) => [p.name, p])).values()].map((p, i) => ({
        id: `pkg_seed_npm_${i}`,
        format: 'npm',
        name: p.name,
        project: repositories.find((r) => r.key === p.repo)?.project ?? '',
        repository: p.repo,
      }));
      const oci = images.map((img, i) => ({ id: `pkg_seed_oci_${i}`, format: 'oci', name: img.name, project: img.project, repository: img.repo }));
      body['packages'] = cut([...npm, ...oci].filter((p) => matches(p.name)));
    }
    if (kindsAsked.includes('project')) {
      body['projects'] = cut(projects.filter((p) => matches(p.key) || matches(p.displayName)).map((p) => ({ key: p.key, displayName: p.displayName })));
    }
    if (kindsAsked.includes('user')) {
      body['users'] = cut(
        Object.values(kinds)
          .filter((u) => matches(u))
          .map((u, i) => ({ id: `0192f0c4-0000-7000-8000-00000000000${i}`, username: u, displayName: null, disabled: false })),
      );
    }
    if (kindsAsked.includes('group')) {
      body['groups'] = cut([group, 'users'].filter((g) => matches(g)).map((name) => ({ name, description: '' })));
    }
    await route.fulfill({ json: body });
  });
}

/** Every suggestion request answers 429 with `Retry-After`: the notice of R-UI-097. */
export async function routeTooMany(page: Page, seconds: number): Promise<void> {
  await page.unroute('**/api/v1/search/suggestions?**');
  await page.route('**/api/v1/search/suggestions?**', (route) =>
    route.fulfill({ status: 429, headers: { 'Retry-After': String(seconds) }, json: { status: 429, code: 'rate_limited' } }),
  );
}

/**
 * The repository list's `name` (R-API-063) answered from the list without it, for a server that does not take `name` yet: the
 * stored repositories of the search's store are confirmed the same way, filtered to what the caller may read.
 */
export async function routeRepositoryNames(page: Page): Promise<void> {
  await page.route(/\/api\/v1\/repositories\?(.*&)?name=/, async (route) => {
    const url = new URL(route.request().url());
    const names = url.searchParams.getAll('name').flatMap((n) => n.split(','));
    const all = new URL(url);
    all.searchParams.delete('name');
    all.searchParams.set('limit', '500');
    const response = await route.fetch({ url: all.toString() });
    if (!response.ok()) return route.fulfill({ response });
    const body = (await response.json()) as { items: { project: string; key: string }[] };
    const byName = new Map(body.items.map((r) => [`${r.project}/${r.key}`, r]));
    await route.fulfill({ json: { items: names.flatMap((n) => (byName.has(n) ? [byName.get(n)] : [])), nextCursor: null } });
  });
}
