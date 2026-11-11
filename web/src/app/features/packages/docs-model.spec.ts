import { commonFixture, docTree } from '../../api/testing/fakes-ansible-docs';
import {
  README_DOC,
  allBranches,
  branchesOf,
  docNodes,
  firstDoc,
  holdsDoc,
  optionRows,
  parseDoc,
  returnRows,
  valueText,
  visibleRows,
} from './docs-model';

describe('The model of the documentation screens', () => {
  it('R-UI-060 reads the address of a page: the README, a file, an entry; anything else is no address', () => {
    expect(parseDoc('README.md')).toEqual({ kind: 'file', name: 'README.md' });
    expect(parseDoc('file/install.md')).toEqual({ kind: 'file', name: 'install.md' });
    expect(parseDoc('module/check_host_reachable')).toEqual({ kind: 'entry', type: 'module', name: 'check_host_reachable' });
    expect(parseDoc('filter/is_cidr')).toEqual({ kind: 'entry', type: 'filter', name: 'is_cidr' });
    expect(parseDoc('role/web')).toEqual({ kind: 'entry', type: 'role', name: 'web' });
    for (const bad of [undefined, null, '', 'module', 'module/', '/x', 'install.md']) expect(parseDoc(bad), String(bad)).toBeNull();
  });

  it('R-UI-060 an unknown page is told from the whole tree and the first page is the README, else a file, else an entry', () => {
    const tree = docTree(commonFixture());
    expect(holdsDoc(tree, parseDoc('module/check_host_reachable'))).toBe(true);
    expect(holdsDoc(tree, parseDoc('module/gone'))).toBe(false);
    expect(holdsDoc(tree, parseDoc('file/install.md'))).toBe(true);
    expect(firstDoc(tree)).toBe(README_DOC);
    expect(firstDoc(docTree(commonFixture({ files: [] })))).toBe('module/check_host_reachable');
    expect(firstDoc(docTree(commonFixture({ files: [], entries: [] })))).toBeNull();
  });

  it('R-UI-060 the tree has Documentation, Modules (8) and Plugins (24) with types below the plugins, and keeps the counts under a search', () => {
    const all = docTree(commonFixture());
    const nodes = docNodes(all, all, false);
    expect(nodes.map((n) => [n.label, n.count])).toEqual([
      ['Documentation', 2],
      ['Modules', 8],
      ['Plugins', 24],
    ]);
    expect(nodes[0]?.children.map((c) => [c.label, c.doc])).toEqual([
      ['hans_fischer.common', 'README.md'],
      ['install', 'file/install.md'],
    ]);
    // Modules list their entries; Plugins list their types, each with its count.
    expect(nodes[1]?.children.every((c) => c.kind === 'entry')).toBe(true);
    expect(nodes[2]?.children.map((c) => [c.label, c.count])).toEqual([
      ['filter', 8],
      ['module_utils', 16],
    ]);
    const narrowed = docTree(commonFixture(), 'host');
    const found = docNodes(narrowed, all, true);
    expect(found.find((n) => n.label === 'Modules')?.count).toBe(8);
    expect(found.find((n) => n.label === 'Plugins')?.count).toBe(24);
    const entries = found.flatMap((n) => allLeaves(n)).filter((n) => n.kind === 'entry');
    expect(entries.map((e) => e.label)).toEqual(['check_host_reachable', 'collect_facts', 'cidr_hosts', 'first_host', 'last_host']);
  });

  it('R-UI-060 an entry without documentation is muted, a group with no match is left out of a search, and the branches above a page are found', () => {
    const all = docTree(commonFixture());
    const nodes = docNodes(all, all, false);
    const filter = nodes[2]!.children[0]!.children[0]!;
    expect(filter.muted).toBe(true);
    expect(branchesOf(nodes, 'filter/is_cidr')).toEqual(['group:plugin', 'type:filter']);
    expect(branchesOf(nodes, 'module/check_host_reachable')).toEqual(['group:module']);
    expect(branchesOf(nodes, null)).toEqual([]);
    const none = docTree(commonFixture(), 'zzzz');
    expect(docNodes(none, all, true)).toEqual([]);
    expect(allBranches(nodes)).toEqual(['group:files', 'group:module', 'group:plugin', 'type:filter', 'type:module_utils']);
  });

  it('R-UI-061 shows a typed value as text: a number as 1000, a boolean as false, a string as written, a structure as JSON', () => {
    expect(valueText(1000)).toBe('1000');
    expect(valueText(false)).toBe('false');
    expect(valueText('<b>x</b>')).toBe('<b>x</b>');
    expect(valueText({ a: [1] })).toBe('{"a":[1]}');
    expect(valueText(null)).toBe('');
    expect(valueText(undefined)).toBe('');
  });

  it('R-UI-061 flattens the parameters with their depth and hides what a folded parameter holds', () => {
    const rows = optionRows([
      {
        name: 'a',
        type: 'dict',
        suboptions: [
          { name: 'b', type: 'list', elements: 'str', suboptions: [{ name: 'c', default: false }] },
          { name: 'd' },
        ],
      },
      { name: 'e', required: true, default: 1000, choices: [1, 'x'], aliases: ['f', 'g'] },
    ]);
    expect(rows.map((r) => [r.name, r.depth, r.parent, r.children])).toEqual([
      ['a', 0, '', 2],
      ['b', 1, 'a', 1],
      ['c', 2, 'b', 0],
      ['d', 1, 'a', 0],
      ['e', 0, '', 0],
    ]);
    expect(rows[1]?.type).toBe('list / str');
    expect(rows[2]?.defaultText).toBe('false');
    expect(rows[2]?.hasDefault).toBe(true);
    expect(rows[4]).toMatchObject({ required: true, defaultText: '1000', choices: ['1', 'x'], aliases: 'f, g' });
    expect(visibleRows(rows, new Set()).map((r) => r.name)).toEqual(['a', 'b', 'c', 'd', 'e']);
    expect(visibleRows(rows, new Set([rows[1]!.id])).map((r) => r.name)).toEqual(['a', 'b', 'd', 'e']);
    expect(visibleRows(rows, new Set([rows[0]!.id])).map((r) => r.name)).toEqual(['a', 'e']);
  });

  it('R-UI-061 flattens the return values with what they contain and shows a structure as indented JSON', () => {
    const rows = returnRows([
      { name: 'x', type: 'dict', returned: 'always', sample: { k: 1 }, contains: [{ name: 'k', sample: 1 }] },
      { name: 'y', sample: 'text' },
    ]);
    expect(rows.map((r) => [r.name, r.depth])).toEqual([
      ['x', 0],
      ['k', 1],
      ['y', 0],
    ]);
    expect(rows[0]?.sample).toBe('{\n  "k": 1\n}');
    expect(rows[1]?.sample).toBe('1');
    expect(rows[2]?.sample).toBe('text');
  });
});

function allLeaves(n: ReturnType<typeof docNodes>[number]): ReturnType<typeof docNodes> {
  return n.children.length === 0 ? [n] : n.children.flatMap(allLeaves);
}
