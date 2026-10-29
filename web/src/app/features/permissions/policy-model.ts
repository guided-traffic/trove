import { PermissionPolicy, PermissionPolicyInput } from '../../api/authz-api';

/** The six actions of R-AUTHZ-006 in the order the editor lists them. */
export const POLICY_ACTIONS = ['read', 'annotate', 'deploy', 'overwrite', 'delete', 'manage'] as const;

/** Presets the server expands when it writes a policy (R-AUTHZ-001). */
export const PRESETS: Readonly<Record<string, readonly string[]>> = {
  read: ['read'],
  write: ['read', 'annotate', 'deploy'],
  maintain: ['read', 'annotate', 'deploy', 'overwrite', 'delete'],
  full: ['read', 'annotate', 'deploy', 'overwrite', 'delete', 'manage'],
};

/** One-line explanation of each action (R-UI-027). */
export function actionHelp(action: string): string {
  switch (action) {
    case 'read':
      return $localize`:@@policy.action.read:Download and list.`;
    case 'annotate':
      return $localize`:@@policy.action.annotate:Change metadata such as tags and deprecation.`;
    case 'deploy':
      return $localize`:@@policy.action.deploy:Publish new files and versions.`;
    case 'overwrite':
      return $localize`:@@policy.action.overwrite:Replace what is already published.`;
    case 'delete':
      return $localize`:@@policy.action.delete:Delete files and versions.`;
    default:
      return $localize`:@@policy.action.manage:Manage the repository's settings.`;
  }
}

export interface EntryRow {
  readonly id: number;
  readonly principal: string;
  /** The six actions; presets are applied to this list in the editor. */
  readonly actions: readonly string[];
}

/** The editor's content: text areas hold one pattern or selector per line. */
export interface PolicyForm {
  readonly name: string;
  readonly description: string;
  readonly repositories: string;
  readonly packagesInclude: string;
  readonly packagesExclude: string;
  readonly pathsInclude: string;
  readonly pathsExclude: string;
  readonly entries: readonly EntryRow[];
}

export const EMPTY_FORM: PolicyForm = {
  name: '',
  description: '',
  repositories: '',
  packagesInclude: '',
  packagesExclude: '',
  pathsInclude: '',
  pathsExclude: '',
  entries: [],
};

export function lines(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l !== '');
}

let nextRowId = 1;
export function newRow(principal = '', actions: readonly string[] = ['read']): EntryRow {
  return { id: nextRowId++, principal, actions };
}

export function formFromPolicy(p: PermissionPolicy): PolicyForm {
  return {
    name: p.name,
    description: p.description,
    repositories: (p.resources.repositories ?? []).join('\n'),
    packagesInclude: (p.resources.packages?.include ?? []).join('\n'),
    packagesExclude: (p.resources.packages?.exclude ?? []).join('\n'),
    pathsInclude: (p.resources.paths?.include ?? []).join('\n'),
    pathsExclude: (p.resources.paths?.exclude ?? []).join('\n'),
    entries: p.entries.map((e) => newRow(e.principal, e.actions)),
  };
}

/** The body of create, replace and validate. `name` is part of it on create only. */
export function inputFromForm(f: PolicyForm, withName: boolean): PermissionPolicyInput {
  return {
    ...(withName ? { name: f.name.trim() } : {}),
    description: f.description.trim(),
    resourceType: 'repository',
    resources: {
      repositories: lines(f.repositories),
      packages: { include: lines(f.packagesInclude), exclude: lines(f.packagesExclude) },
      paths: { include: lines(f.pathsInclude), exclude: lines(f.pathsExclude) },
    },
    entries: f.entries.map((e) => ({ principal: e.principal.trim(), actions: [...e.actions] })),
  };
}

/** Actions with presets expanded, in the canonical order, without duplicates. */
export function expandActions(actions: readonly string[]): string[] {
  const set = new Set<string>();
  for (const a of actions) for (const x of PRESETS[a] ?? [a]) set.add(x);
  const known = POLICY_ACTIONS.filter((a) => set.has(a));
  const unknown = [...set].filter((a) => !(POLICY_ACTIONS as readonly string[]).includes(a));
  return [...known, ...unknown];
}

export interface DiffLine {
  readonly change: 'added' | 'removed' | 'changed';
  readonly text: string;
}

function setDiff(
  before: readonly string[],
  after: readonly string[],
  label: (item: string) => { added: string; removed: string },
): DiffLine[] {
  const b = new Set(before);
  const a = new Set(after);
  return [
    ...after.filter((x) => !b.has(x)).map((x) => ({ change: 'added' as const, text: label(x).added })),
    ...before.filter((x) => !a.has(x)).map((x) => ({ change: 'removed' as const, text: label(x).removed })),
  ];
}

/**
 * What a save changes against the loaded revision (R-UI-027): principals, resources and actions
 * added or removed, in that order. Nothing changed gives an empty list. Presets are expanded on
 * both sides first, so choosing "write" over three explicit actions is no change.
 */
export function diffPolicy(before: PermissionPolicyInput, after: PermissionPolicyInput): DiffLine[] {
  const out: DiffLine[] = [];
  if ((before.description ?? '') !== (after.description ?? '')) {
    out.push({
      change: 'changed',
      text: $localize`:@@policy.diff.description:Description changed.`,
    });
  }

  const entries = (i: PermissionPolicyInput): Map<string, string[]> =>
    new Map((i.entries ?? []).map((e) => [e.principal, expandActions(e.actions)]));
  const be = entries(before);
  const ae = entries(after);
  out.push(
    ...setDiff([...be.keys()], [...ae.keys()], (p) => ({
      added: $localize`:@@policy.diff.principal-added:Principal added: ${p}:principal:`,
      removed: $localize`:@@policy.diff.principal-removed:Principal removed: ${p}:principal:`,
    })),
  );
  for (const [principal, actions] of ae) {
    const old = be.get(principal);
    if (!old) continue;
    out.push(
      ...setDiff(old, actions, (a) => ({
        added: $localize`:@@policy.diff.action-added:Action ${a}:action: added for ${principal}:principal:`,
        removed: $localize`:@@policy.diff.action-removed:Action ${a}:action: removed for ${principal}:principal:`,
      })),
    );
  }

  out.push(
    ...setDiff(before.resources?.repositories ?? [], after.resources?.repositories ?? [], (r) => ({
      added: $localize`:@@policy.diff.repo-added:Repository selector added: ${r}:selector:`,
      removed: $localize`:@@policy.diff.repo-removed:Repository selector removed: ${r}:selector:`,
    })),
  );
  const patterns = (
    dimension: 'packages' | 'paths',
    list: 'include' | 'exclude',
  ): DiffLine[] => {
    const dim =
      dimension === 'packages'
        ? $localize`:@@policy.dim.packages:package names`
        : $localize`:@@policy.dim.paths:paths`;
    const kind =
      list === 'include'
        ? $localize`:@@policy.list.include:include list`
        : $localize`:@@policy.list.exclude:exclude list`;
    return setDiff(
      before.resources?.[dimension]?.[list] ?? [],
      after.resources?.[dimension]?.[list] ?? [],
      (p) => ({
        added: $localize`:@@policy.diff.pattern-added:Pattern ${p}:pattern: added to the ${kind}:list: of the ${dim}:dimension:`,
        removed: $localize`:@@policy.diff.pattern-removed:Pattern ${p}:pattern: removed from the ${kind}:list: of the ${dim}:dimension:`,
      }),
    );
  };
  out.push(
    ...patterns('packages', 'include'),
    ...patterns('packages', 'exclude'),
    ...patterns('paths', 'include'),
    ...patterns('paths', 'exclude'),
  );
  return out;
}

/** The warning codes of R-AUTHZ-021 as sentences. */
export function warningText(code: string): string {
  switch (code) {
    case 'path_patterns_on_package_format':
      return $localize`:@@policy.warning.path-patterns:Path patterns on a package format: a path pattern can admit a file but not its package metadata. Prefer package-name patterns.`;
    case 'selector_matches_nothing':
      return $localize`:@@policy.warning.matches-nothing:A repository selector matches no repository yet.`;
    case 'anonymous_populates_remote':
      return $localize`:@@policy.warning.anonymous-remote:Anonymous read on a remote repository lets anyone fill its cache from upstream.`;
    default:
      return code;
  }
}
