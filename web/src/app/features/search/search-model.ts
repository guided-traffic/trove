import { NavArea, NavItem } from '../../core/navigation';
import { SearchSuggestions, SuggestionKind } from '../../api/search-api';
import { packageCommands } from '../packages/package-links';
import { termsOf } from './highlight';

/** The filter of the field (R-UI-095): **All**, a kind of the main area, or for an instance administrator users or groups. */
export type SearchFilter = 'all' | SuggestionKind;

/** Where a suggestion leads: the main area, or the admin area (R-UI-099). */
export type Area = 'main' | 'admin';

/** One entry of the list. `commands` and `queryParams` are the router's; the one gate decides whether it opens (`gate`). */
export interface SearchEntry {
  /** Unique within the list: the option's DOM id is derived from it. */
  readonly id: string;
  readonly area: Area;
  readonly kind: 'place' | 'repository' | 'package' | 'project' | 'page' | 'user' | 'group' | 'more' | 'all-projects';
  /** The name shown; matched text in it is bold while a text is typed. */
  readonly label: string;
  /** The muted part after "·": a repository's project, a package's project and repository, a project's key, a username. */
  readonly secondary?: string;
  /** A side-bar icon (pages, places, groups). */
  readonly icon?: string;
  /** A format whose icon the entry shows (repositories, packages). */
  readonly format?: string;
  /** A project's letter mark. */
  readonly letter?: string;
  /** A user's ID, the seed of the avatar. */
  readonly avatar?: string;
  /** A disabled user. */
  readonly disabled?: boolean;
  /** Whether the label is a name the typed text matched (keys and names are `translate="no"`). */
  readonly literal: boolean;
  readonly commands: readonly string[];
  readonly queryParams?: Readonly<Record<string, string>>;
}

export interface SearchGroup {
  readonly id: string;
  /** The heading; null for a group of one entry without a heading (**View all my projects**). */
  readonly heading: string | null;
  readonly area: Area;
  readonly entries: readonly SearchEntry[];
}

/** The kinds a filter asks for: the main area's three, and users and groups for an instance administrator under **All**. */
export function kindsOf(filter: SearchFilter, instanceAdmin: boolean): SuggestionKind[] {
  if (filter !== 'all') return [filter];
  return instanceAdmin ? ['repository', 'package', 'project', 'user', 'group'] : ['repository', 'package', 'project'];
}

/** Whether a URL is in the admin area. */
export function isAdminUrl(url: string): boolean {
  return url === '/admin' || url.startsWith('/admin/') || url.startsWith('/admin?') || url.startsWith('/admin#');
}

/** One way out of the field: the serialized target URL, the area of the entry it came from, and whether it was chosen. */
export interface SearchExit {
  readonly url: string;
  readonly area: Area;
  /** A click on an entry, or Enter while it is the active entry: the only explicit choice (R-UI-099). */
  readonly chosen: boolean;
}

/**
 * The one gate of R-UI-099: every way out of the field passes it. A route of the main area opens; a route under `/admin` opens
 * only when it is an admin suggestion, chosen explicitly, by a caller who is an instance administrator at that moment. Returns the
 * URL to open, or null. Navigation, not access control: the API decides what the admin area shows.
 */
export function gate(exit: SearchExit, instanceAdmin: boolean): string | null {
  if (!isAdminUrl(exit.url)) return exit.url;
  return exit.area === 'admin' && exit.chosen && instanceAdmin ? exit.url : null;
}

/** A place of the empty list or a page of the typed list (R-UI-096, R-UI-097). */
export interface Place {
  readonly route: string;
  readonly icon: string;
  readonly label: string;
  /** Shown to an anonymous caller too. */
  readonly anonymous: boolean;
}

/** Group 1 of the empty list: the side bar's places of the main area (R-UI-096). */
export function places(): Place[] {
  return [
    { route: '/projects', icon: 'folder_shared', label: $localize`:@@nav.projects:Projects`, anonymous: true },
    { route: '/guides', icon: 'menu_book', label: $localize`:@@nav.guides:Guides`, anonymous: true },
    { route: '/profile', icon: 'person', label: $localize`:@@search.place.profile:Profile`, anonymous: false },
    { route: '/profile/tokens', icon: 'key', label: $localize`:@@search.place.tokens:Personal tokens`, anonymous: false },
  ];
}

/** The pages the typed text is matched against: the places and Client setup, Sessions and Documentation (R-UI-097). */
export function pages(): Place[] {
  return [
    ...places(),
    { route: '/setup', icon: 'terminal', label: $localize`:@@shell.menu.setup:Client setup`, anonymous: false },
    { route: '/profile/sessions', icon: 'devices', label: $localize`:@@search.place.sessions:Sessions`, anonymous: false },
    { route: '/help', icon: 'help_outline', label: $localize`:@@shell.documentation:Documentation`, anonymous: true },
  ];
}

/** The admin side bar's entries the caller may use, flattened in their order. */
export function adminItems(admin: NavArea | null): NavItem[] {
  if (!admin) return [];
  return admin.entries.flatMap((e) => (e.kind === 'item' ? [e] : e.items));
}

/**
 * The **Admin area** group of the empty list (R-UI-096): Dashboard, Repositories, Users, Groups and Settings, each the admin side
 * bar's entry where the administrator may use it; Settings is the side bar's group of that name, opened at its first page.
 */
export function adminShortcuts(admin: NavArea | null): Place[] {
  if (!admin) return [];
  const wanted = ['/admin/dashboard', '/admin/repositories', '/admin/users', '/admin/groups'];
  const items = adminItems(admin);
  const out: Place[] = wanted.flatMap((route) => {
    const i = items.find((x) => x.route === route);
    return i ? [{ route: i.route, icon: i.icon, label: i.label, anonymous: false }] : [];
  });
  const settings = admin.entries.find((e) => e.kind === 'group' && e.id === 'settings');
  if (settings && settings.kind === 'group' && settings.items[0]) {
    out.push({ route: settings.items[0].route, icon: settings.icon, label: settings.label, anonymous: false });
  }
  return out;
}

/**
 * The project rule of R-API-062 for labels the browser holds: every term is part of the label; equal first, then a label that
 * starts with the text, then one with a word that starts with it, then any other, each by the label.
 */
export function matchPlaces<T extends { readonly label: string }>(items: readonly T[], q: string, limit: number): T[] {
  const terms = termsOf(q);
  const text = terms.join(' ');
  if (terms.length === 0) return [];
  const rank = (label: string): number => {
    const l = label.toLowerCase();
    if (l === text) return 0;
    if (l.startsWith(text)) return 1;
    if (l.split(/\s+/).some((w) => w.startsWith(text))) return 2;
    return 3;
  };
  return items
    .filter((i) => terms.every((t) => i.label.toLowerCase().includes(t)))
    .map((i) => ({ i, r: rank(i.label) }))
    .sort((a, b) => a.r - b.r || a.i.label.localeCompare(b.i.label))
    .slice(0, limit)
    .map((x) => x.i);
}

/** The first letter of a project's display name, or of its key: the project switcher's mark. */
export function letterOf(displayName: string | null | undefined, key: string): string {
  return ((displayName ?? '').trim().charAt(0) || key.charAt(0)).toUpperCase();
}

export function placeEntry(p: Place, area: Area, prefix: string): SearchEntry {
  return { id: `${prefix}-${p.route}`, area, kind: area === 'admin' ? 'page' : 'place', label: p.label, icon: p.icon, literal: false, commands: [p.route] };
}

export function repositoryEntry(project: string, key: string, format: string | undefined, prefix: string): SearchEntry {
  return {
    id: `${prefix}-r-${project}/${key}`,
    area: 'main',
    kind: 'repository',
    label: key,
    secondary: project,
    format,
    literal: true,
    commands: ['/projects', project, 'repositories', key],
  };
}

export function projectEntry(key: string, displayName: string | null | undefined, prefix: string): SearchEntry {
  return {
    id: `${prefix}-p-${key}`,
    area: 'main',
    kind: 'project',
    label: displayName || key,
    secondary: key,
    letter: letterOf(displayName, key),
    literal: true,
    commands: ['/projects', key],
  };
}

/** The headings of the groups. */
export function headings(): Record<string, string> {
  return {
    places: $localize`:@@search.group.places:Places`,
    'frequent-repositories': $localize`:@@search.group.frequent-repositories:Frequently visited repositories`,
    'frequent-projects': $localize`:@@search.group.frequent-projects:Frequently visited projects`,
    'admin-area': $localize`:@@search.group.admin-area:Admin area`,
    repository: $localize`:@@search.group.repositories:Repositories`,
    package: $localize`:@@search.group.packages:Packages`,
    project: $localize`:@@search.group.projects:Projects`,
    pages: $localize`:@@search.group.pages:Pages`,
    'admin-pages': $localize`:@@search.group.admin-pages:Admin pages`,
    user: $localize`:@@search.group.users:Users`,
    group: $localize`:@@search.group.groups:Groups`,
  };
}

/** The "All … for" line of a main-area group whose answer says `more` (R-UI-097). */
function moreEntry(kind: 'repository' | 'package' | 'project', q: string): SearchEntry {
  const label =
    kind === 'repository'
      ? $localize`:@@search.more.repositories:All repositories for “${q}:text:”`
      : kind === 'package'
        ? $localize`:@@search.more.packages:All packages for “${q}:text:”`
        : $localize`:@@search.more.projects:All projects for “${q}:text:”`;
  return { id: `more-${kind}`, area: 'main', kind: 'more', label, icon: 'arrow_forward', literal: false, commands: ['/search'], queryParams: { q, kind } };
}

/** The input of the typed list. */
export interface LiveInput {
  readonly q: string;
  readonly filter: SearchFilter;
  readonly answer: SearchSuggestions;
  readonly instanceAdmin: boolean;
  /** The pages of the main area the caller may open. */
  readonly pages: readonly Place[];
  /** The admin side bar's entries the administrator may use. */
  readonly adminPages: readonly NavItem[];
}

/**
 * The typed list (R-UI-097, Q-UI-407): Repositories, Packages, Projects, Pages; then, for an instance administrator, Admin pages,
 * Users and Groups. Five per group, three for the pages, ten with a kind chosen (the server cut the answer at its `limit`); a group
 * without a match is left out; a main-area group whose answer says `more` ends with its **All … for** line.
 */
export function liveGroups(input: LiveInput): SearchGroup[] {
  const h = headings();
  const { q, answer } = input;
  const out: SearchGroup[] = [];
  const add = (id: string, area: Area, entries: SearchEntry[]): void => {
    if (entries.length > 0) out.push({ id, heading: h[id] ?? null, area, entries });
  };
  if (answer.repositories) {
    const rows = answer.repositories.items.map((r) => repositoryEntry(r.project, r.key, r.format, 'live'));
    add('repository', 'main', answer.repositories.more ? [...rows, moreEntry('repository', q)] : rows);
  }
  if (answer.packages) {
    const rows = answer.packages.items.map(
      (p): SearchEntry => ({
        id: `live-k-${p.id}`,
        area: 'main',
        kind: 'package',
        label: p.name,
        secondary: `${p.project} / ${p.repository}`,
        format: p.format,
        literal: true,
        commands: packageCommands(p.project, p.format, p.name),
      }),
    );
    add('package', 'main', answer.packages.more ? [...rows, moreEntry('package', q)] : rows);
  }
  if (answer.projects) {
    const rows = answer.projects.items.map((p) => projectEntry(p.key, p.displayName, 'live'));
    add('project', 'main', answer.projects.more ? [...rows, moreEntry('project', q)] : rows);
  }
  if (input.filter === 'all') {
    add('pages', 'main', matchPlaces(input.pages, q, 3).map((p) => placeEntry(p, 'main', 'page')));
  }
  if (!input.instanceAdmin) return out;
  if (input.filter === 'all') {
    add(
      'admin-pages',
      'admin',
      matchPlaces(input.adminPages, q, 3).map((p) => placeEntry({ ...p, anonymous: false }, 'admin', 'admin-page')),
    );
  }
  if (answer.users) {
    add(
      'user',
      'admin',
      answer.users.items.map((u) => ({
        id: `live-u-${u.username}`,
        area: 'admin',
        kind: 'user',
        label: u.displayName || u.username,
        secondary: u.username,
        avatar: u.id,
        disabled: u.disabled,
        literal: true,
        commands: ['/admin/users', u.username],
      })),
    );
  }
  if (answer.groups) {
    add(
      'group',
      'admin',
      answer.groups.items.map((g) => ({
        id: `live-g-${g.name}`,
        area: 'admin',
        kind: 'group',
        label: g.name,
        icon: 'group',
        literal: true,
        commands: ['/admin/groups', g.name],
      })),
    );
  }
  return out;
}

/** The tab of the results page that the first group with a suggestion names (R-UI-098). */
export function firstKindOf(groups: readonly SearchGroup[]): 'repository' | 'package' | 'project' | null {
  for (const g of groups) {
    if (g.id === 'repository' || g.id === 'package' || g.id === 'project') return g.id;
  }
  return null;
}
