import { PackageDocOption, PackageDocReturnValue, PackageDocTree } from '../../api/packages-api';

/*
 * The pure part of the documentation screens (R-UI-060 to R-UI-062): the address of a page in the URL, the nodes of the tree, the rows of
 * the parameter tables and the text of a typed value. Nothing here touches the DOM or the network.
 */

/** The README, as the query parameter `doc` spells it. */
export const README_DOC = 'README.md';

export type DocAddress =
  | { readonly kind: 'file'; readonly name: string }
  | { readonly kind: 'entry'; readonly type: string; readonly name: string };

/** `README.md`, `file/<name>` and `<type>/<name>` (`module/x`, `filter/x`, `role/x`); anything else is no address. */
export function parseDoc(doc: string | undefined | null): DocAddress | null {
  if (!doc) return null;
  if (doc === README_DOC) return { kind: 'file', name: README_DOC };
  const slash = doc.indexOf('/');
  if (slash <= 0 || slash === doc.length - 1) return null;
  const head = doc.slice(0, slash);
  const rest = doc.slice(slash + 1);
  return head === 'file' ? { kind: 'file', name: rest } : { kind: 'entry', type: head, name: rest };
}

/** The `doc` value of a file of the tree: the README by its name, every other Markdown file below `file/`. */
export function fileDoc(name: string): string {
  return name === README_DOC ? README_DOC : `file/${name}`;
}

export function entryDoc(type: string, name: string): string {
  return `${type}/${name}`;
}

/** Whether the tree holds the page; a search narrows the tree, so the question is asked of the whole one. */
export function holdsDoc(tree: PackageDocTree, address: DocAddress | null): boolean {
  if (!address) return false;
  if (address.kind === 'file') return (tree.files ?? []).some((f) => f.name === address.name);
  return (tree.groups ?? []).some((g) =>
    g.types.some((t) => t.type === address.type && t.entries.some((e) => e.name === address.name)),
  );
}

/** The page shown when `doc` is absent or unknown: the README, else the first file, else the first entry; null for an empty tree. */
export function firstDoc(tree: PackageDocTree): string | null {
  const file = tree.files?.[0];
  if (file) return fileDoc(file.name);
  for (const g of tree.groups ?? [])
    for (const t of g.types) {
      const e = t.entries[0];
      if (e) return entryDoc(e.type, e.name);
    }
  return null;
}

/** One row of the tree pane. A leaf has a `doc`; a group or a type has children and a count. */
export interface DocNode {
  /** The tree value: the `doc` of a leaf, `group:<category>` or `type:<type>` of a branch. */
  readonly id: string;
  readonly label: string;
  readonly kind: 'files' | 'group' | 'type' | 'file' | 'entry';
  readonly count: number | null;
  readonly doc: string | null;
  readonly tooltip: string;
  /** An entry whose source holds no documentation: muted and labelled. */
  readonly muted: boolean;
  readonly children: readonly DocNode[];
}

/**
 * The nodes of the tree pane from the answer of the tree (the narrowed one while one searches). The first row is **Documentation**
 * (the README, the other Markdown files as children), then Modules, Plugins and Roles. A group that holds one type of its own
 * name (modules, roles) lists the entries directly; Plugins lists its types, each with its count. The counts are those the server
 * sends, which are the counts before the search.
 */
export function docNodes(view: PackageDocTree, all: PackageDocTree, searching: boolean): DocNode[] {
  const out: DocNode[] = [];
  const files = view.files ?? [];
  if (files.length > 0) {
    out.push({
      id: 'group:files',
      label: $localize`:@@docs.tree.documentation:Documentation`,
      kind: 'files',
      count: (all.files ?? []).length,
      doc: null,
      tooltip: '',
      muted: false,
      children: files.map((f) => ({
        id: fileDoc(f.name),
        label: f.title,
        kind: 'file' as const,
        count: null,
        doc: fileDoc(f.name),
        tooltip: f.name,
        muted: false,
        children: [],
      })),
    });
  }
  for (const g of view.groups ?? []) {
    if (searching && g.types.every((t) => t.entries.length === 0)) continue;
    if (!searching && g.count === 0) continue;
    const leaves = (type: string, entries: typeof g.types[number]['entries']): DocNode[] =>
      entries.map((e) => ({
        id: entryDoc(e.type, e.name),
        label: e.name,
        kind: 'entry' as const,
        count: null,
        doc: entryDoc(type, e.name),
        tooltip: e.shortDescription,
        // A role has its README and no doc_strings: it is not "without documentation" (the page shows the README).
        muted: !e.hasDocs && e.type !== 'role',
        children: [],
      }));
    const flat = g.types.length === 1 && g.types[0]?.type === g.category;
    out.push({
      id: `group:${g.category}`,
      label: groupLabel(g.category, g.label),
      kind: 'group',
      count: g.count,
      doc: null,
      tooltip: '',
      muted: false,
      children: flat
        ? leaves(g.category, g.types[0]?.entries ?? [])
        : g.types.map((t) => ({
            id: `type:${t.type}`,
            label: t.type,
            kind: 'type' as const,
            count: t.count,
            doc: null,
            tooltip: '',
            muted: false,
            children: leaves(t.type, t.entries),
          })),
    });
  }
  return out;
}

function groupLabel(category: string, fallback: string): string {
  switch (category) {
    case 'module':
      return $localize`:@@docs.tree.modules:Modules`;
    case 'plugin':
      return $localize`:@@docs.tree.plugins:Plugins`;
    case 'role':
      return $localize`:@@docs.tree.roles:Roles`;
    default:
      return fallback;
  }
}

/** The ids of the branches that hold the node with this id, from the top. */
export function branchesOf(nodes: readonly DocNode[], id: string | null): string[] {
  if (id === null) return [];
  const walk = (list: readonly DocNode[], trail: string[]): string[] | null => {
    for (const n of list) {
      if (n.id === id) return trail;
      const found = walk(n.children, [...trail, n.id]);
      if (found) return found;
    }
    return null;
  };
  return walk(nodes, []) ?? [];
}

/** The ids of every branch of the tree (a search opens them all: only the groups with matches are left). */
export function allBranches(nodes: readonly DocNode[]): string[] {
  return nodes.flatMap((n) => (n.children.length > 0 ? [n.id, ...allBranches(n.children)] : []));
}

/** The text of a typed JSON value: a string as written, a number or a boolean as JSON writes it, a structure as compact JSON. */
export function valueText(v: unknown): string {
  if (v === undefined || v === null) return '';
  if (typeof v === 'string') return v;
  if (typeof v === 'number' || typeof v === 'boolean') return String(v);
  try {
    return JSON.stringify(v);
  } catch {
    return '';
  }
}

/** A row of a parameter table or a return value table, flattened with its depth. */
export interface DocRow {
  /** The path of names from the top, unique in a table. */
  readonly id: string;
  readonly depth: number;
  /** The name of the row above, empty at the top. */
  readonly parent: string;
  readonly name: string;
  readonly aliases: string;
  readonly type: string;
  readonly required: boolean;
  readonly defaultText: string;
  readonly hasDefault: boolean;
  readonly choices: readonly string[];
  readonly returned: string;
  readonly sample: string;
  readonly versionAdded: string;
  readonly description: readonly string[];
  readonly children: number;
}

function typeText(type: string | undefined, elements: string | undefined): string {
  if (!type) return '';
  return elements ? `${type} / ${elements}` : type;
}

/** The rows of the options, in document order, each followed by its suboptions. */
export function optionRows(options: readonly PackageDocOption[] | undefined): DocRow[] {
  const out: DocRow[] = [];
  const walk = (list: readonly PackageDocOption[], depth: number, parent: string, path: string): void => {
    for (const o of list) {
      const id = `${path}/${o.name}`;
      const subs = o.suboptions ?? [];
      out.push({
        id,
        depth,
        parent,
        name: o.name,
        aliases: (o.aliases ?? []).join(', '),
        type: typeText(o.type, o.elements),
        required: o.required === true,
        defaultText: valueText(o.default),
        hasDefault: o.default !== undefined && o.default !== null,
        choices: (o.choices ?? []).map(valueText),
        returned: '',
        sample: '',
        versionAdded: o.versionAdded ?? '',
        description: o.description ?? [],
        children: subs.length,
      });
      walk(subs, depth + 1, o.name, id);
    }
  };
  walk(options ?? [], 0, '', '');
  return out;
}

/** The rows of the return values, each followed by what it contains. */
export function returnRows(values: readonly PackageDocReturnValue[] | undefined): DocRow[] {
  const out: DocRow[] = [];
  const walk = (list: readonly PackageDocReturnValue[], depth: number, parent: string, path: string): void => {
    for (const r of list) {
      const id = `${path}/${r.name}`;
      const subs = r.contains ?? [];
      out.push({
        id,
        depth,
        parent,
        name: r.name,
        aliases: '',
        type: typeText(r.type, r.elements),
        required: false,
        defaultText: '',
        hasDefault: false,
        choices: [],
        returned: r.returned ?? '',
        sample: r.sample === undefined ? '' : sampleText(r.sample),
        versionAdded: r.versionAdded ?? '',
        description: r.description ?? [],
        children: subs.length,
      });
      walk(subs, depth + 1, r.name, id);
    }
  };
  walk(values ?? [], 0, '', '');
  return out;
}

/** A sample: a string as written, anything else as indented JSON, so that a structure reads as a block. */
function sampleText(v: unknown): string {
  if (typeof v === 'string') return v;
  if (v !== null && typeof v === 'object') {
    try {
      return JSON.stringify(v, null, 2);
    } catch {
      return '';
    }
  }
  return valueText(v);
}

/** The rows that are visible: a row hides while any row above it on its path is collapsed. */
export function visibleRows(rows: readonly DocRow[], collapsed: ReadonlySet<string>): DocRow[] {
  const out: DocRow[] = [];
  let hiddenBelow = Infinity;
  for (const r of rows) {
    if (r.depth > hiddenBelow) continue;
    hiddenBelow = Infinity;
    out.push(r);
    if (collapsed.has(r.id)) hiddenBelow = r.depth;
  }
  return out;
}

/** The reason a version's documentation is unavailable, in words (R-UI-060). */
export function unavailableText(reason: PackageDocTree['reason']): string {
  switch (reason) {
    case 'upstream_error':
      return $localize`:@@docs.unavailable.upstream:The upstream did not deliver the documentation of this version.`;
    case 'too_large':
      return $localize`:@@docs.unavailable.too-large:The documentation of this version is larger than trove keeps.`;
    case 'invalid_document':
      return $localize`:@@docs.unavailable.invalid:The upstream sent a documentation document that trove cannot read.`;
    default:
      return $localize`:@@docs.unavailable.unknown:The documentation of this version is not available.`;
  }
}
