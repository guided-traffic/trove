import {
  PackageDocEntry,
  PackageDocEntryHead,
  PackageDocFile,
  PackageDocGroup,
  PackageDocTree,
} from '../packages-api';

/*
 * The documentation of an Ansible collection version as the fakes of the three operations serve it (R-ANS-036, R-UI-047 component
 * tier). The tree is built from the pages the way the server builds it: groups in the order modules, plugins, roles, types and
 * entries in alphabetical order, the counts before the search, `matchCount` after it.
 */

export interface DocFile {
  readonly name: string;
  readonly title: string;
  readonly html: string;
  readonly truncated?: boolean;
  readonly externalImagesBlocked?: number;
}

export interface DocsFixture {
  state: PackageDocTree['state'];
  reason?: PackageDocTree['reason'];
  source: 'generated' | 'upstream';
  repository: string;
  builtAt: string;
  truncated?: boolean;
  files: DocFile[];
  entries: PackageDocEntry[];
  warnings?: PackageDocTree['warnings'];
}

const LABELS = { module: 'Modules', plugin: 'Plugins', role: 'Roles' } as const;
const CATEGORIES = ['module', 'plugin', 'role'] as const;

function categoryOf(type: string): 'module' | 'plugin' | 'role' {
  return type === 'module' ? 'module' : type === 'role' ? 'role' : 'plugin';
}

function head(e: PackageDocEntry): PackageDocEntryHead {
  return { type: e.type, name: e.name, shortDescription: e.shortDescription ?? '', hasDocs: e.hasDocs };
}

const words = (text: string): string[] => text.toLowerCase().split(/[^a-z0-9]+/).filter((w) => w !== '');
const plain = (html: string): string => html.replace(/<[^>]*>/g, ' ');

/** The rule of R-ANS-036: every term matches the name (substring) or a word of the description (prefix). */
function matches(e: PackageDocEntry, terms: readonly string[]): boolean {
  const vocabulary = words(
    [
      e.shortDescription ?? '',
      ...(e.description ?? []).map(plain),
      ...(e.options ?? []).map((o) => o.name),
      ...(e.returnValues ?? []).map((r) => r.name),
    ].join(' '),
  );
  return terms.every((t) => e.name.toLowerCase().includes(t) || vocabulary.some((w) => w.startsWith(t)));
}

export function docTree(fx: DocsFixture, q?: string): PackageDocTree {
  if (fx.state !== 'available') {
    return { state: fx.state, repository: fx.repository, ...(fx.reason ? { reason: fx.reason } : {}) };
  }
  const terms = (q ?? '').toLowerCase().split(/\s+/).filter((t) => t !== '');
  const searching = terms.length > 0;
  const groups: PackageDocGroup[] = CATEGORIES.map((category) => {
    const inGroup = fx.entries.filter((e) => categoryOf(e.type) === category);
    const types = [...new Set(inGroup.map((e) => e.type))].sort().map((type) => {
      const all = inGroup.filter((e) => e.type === type).sort((a, b) => a.name.localeCompare(b.name));
      const shown = searching ? all.filter((e) => matches(e, terms)) : all;
      return { type, count: all.length, entries: shown.map(head) };
    });
    return {
      category,
      label: LABELS[category],
      count: inGroup.length,
      types: searching ? types.filter((t) => t.entries.length > 0) : types,
    };
  });
  const files = fx.files
    .filter((f) => !searching || terms.every((t) => f.title.toLowerCase().includes(t)))
    .map((f) => ({ name: f.name, title: f.title }));
  const left = groups.reduce((n, g) => n + g.types.reduce((m, t) => m + t.entries.length, 0), 0);
  return {
    state: 'available',
    source: fx.source,
    repository: fx.repository,
    builtAt: fx.builtAt,
    truncated: fx.truncated ?? false,
    files,
    groups,
    ...(searching ? { matchCount: left } : {}),
    ...(fx.warnings ? { warnings: fx.warnings } : {}),
  };
}

export function docFile(fx: DocsFixture, name: string): PackageDocFile | null {
  const f = fx.files.find((x) => x.name === name);
  return f
    ? {
        name: f.name,
        contentType: 'text/markdown',
        html: f.html,
        truncated: f.truncated ?? false,
        externalImagesBlocked: f.externalImagesBlocked ?? 0,
      }
    : null;
}

/** A page without a documentation block: the header fields only. */
export function bareEntry(type: string, name: string, source: 'generated' | 'upstream' = 'generated'): PackageDocEntry {
  return { type, name, fullName: `hans_fischer.common.${name}`, hasDocs: false, source };
}

/** A module whose parameters nest three levels deep, with a hostile parameter name and default. */
export const CHECK_HOST_REACHABLE: PackageDocEntry = {
  type: 'module',
  name: 'check_host_reachable',
  fullName: 'hans_fischer.common.check_host_reachable',
  shortDescription: 'Checks whether a host answers',
  hasDocs: true,
  source: 'generated',
  builtAt: '2026-11-11T09:41:02Z',
  versionAdded: '1.1.0',
  description: [
    'Opens a connection to <code>host</code> and reports whether it answers.',
    'Use it before <a href="https://example.com/play">a play</a> that needs the host.',
  ],
  notes: ['The check does not use <code>ping</code>.'],
  requirements: ['python >= 3.9'],
  author: ['Jane Maintainer (@jane)'],
  options: [
    {
      name: 'host',
      type: 'str',
      required: true,
      aliases: ['hostname'],
      description: ['The host to check.'],
    },
    {
      name: 'port',
      type: 'int',
      default: 22,
      choices: [22, 443],
      description: ['The port to open.'],
    },
    {
      name: 'retry',
      type: 'dict',
      description: ['How to retry.'],
      suboptions: [
        {
          name: 'count',
          type: 'int',
          default: 3,
          description: ['How often to try again.'],
        },
        {
          name: 'backoff',
          type: 'dict',
          description: ['The waiting time.'],
          suboptions: [
            { name: 'seconds', type: 'float', default: 1.5, description: ['Seconds between two tries.'] },
            { name: 'jitter', type: 'bool', default: false, description: ['Spread the tries.'] },
          ],
        },
      ],
    },
    {
      name: '<img src=x onerror=1>',
      type: 'str',
      default: '<script>alert(1)</script>',
      description: ['A parameter whose name is markup.'],
    },
  ],
  attributes: [{ name: 'check_mode', support: 'full', description: ['Can run in check mode.'] }],
  examples: '- name: Check the gateway\n  hans_fischer.common.check_host_reachable:\n    host: gateway.example.net\n',
  returnValues: [
    {
      name: 'reachable',
      type: 'bool',
      returned: 'always',
      description: ['Whether the host answered.'],
      sample: true,
    },
    {
      name: 'details',
      type: 'dict',
      returned: 'on success',
      description: ['What was measured.'],
      sample: { rtt_ms: 12, route: ['a', 'b'], note: 'a longer sample so that it is shown as a block' },
      contains: [{ name: 'rtt_ms', type: 'int', returned: 'success', description: ['The round trip time.'], sample: 12 }],
    },
  ],
  seealso: [
    { module: 'hans_fischer.common.read_config', description: 'Reads the configuration.' },
    { description: 'The reference', link: 'https://example.com/reference' },
  ],
};

const MODULE_NAMES = [
  ['read_config', 'Reads the configuration'],
  ['write_config', 'Writes the configuration'],
  ['rotate_logs', 'Rotates the logs of a service'],
  ['wait_for_port', 'Waits until a port opens'],
  ['collect_facts', 'Collects facts about a host'],
  ['restart_unit', 'Restarts a service unit'],
  ['sync_dir', 'Synchronises a directory'],
] as const;

const FILTER_NAMES = ['is_cidr', 'to_cidr', 'cidr_hosts', 'first_host', 'last_host', 'netmask', 'prefix_len', 'wildcard'];

/**
 * The collection of `hans_fischer.common`: a README, eight modules of which one is documented in full, eight filters and sixteen
 * `module_utils` files without documentation in their source (the owner's collection has `doc_strings` null everywhere).
 */
export function commonFixture(over: Partial<DocsFixture> = {}): DocsFixture {
  const modules: PackageDocEntry[] = [
    CHECK_HOST_REACHABLE,
    ...MODULE_NAMES.map(
      ([name, short]): PackageDocEntry => ({
        type: 'module',
        name,
        fullName: `hans_fischer.common.${name}`,
        shortDescription: short,
        hasDocs: true,
        source: 'generated',
        description: [`${short}.`],
        options: [{ name: 'path', type: 'str', description: ['The path.'] }],
      }),
    ),
  ];
  return {
    state: 'available',
    source: 'generated',
    repository: 'web/ansible-local',
    builtAt: '2026-11-11T09:41:02Z',
    files: [
      { name: 'README.md', title: 'hans_fischer.common', html: '<h1>hans_fischer.common</h1><p>Common roles and modules.</p>' },
      { name: 'install.md', title: 'install', html: '<h1>Install</h1><p>Run ansible-galaxy.</p>' },
    ],
    entries: [
      ...modules,
      ...FILTER_NAMES.map((name) => bareEntry('filter', name)),
      ...Array.from({ length: 16 }, (_, i) => bareEntry('module_utils', `util_${String(i).padStart(2, '0')}`)),
    ],
    ...over,
  };
}
