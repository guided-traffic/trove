import { CiCondition, CiGrants, CiInlineGrant, CiMapping, CiMappingInput } from '../../api/ci-api';

/*
 * The model behind the CI screens (R-UI-035, R-UI-036): the condition tree of R-CI-010 as an editable
 * structure, the summaries of conditions and grants, the form of a mapping and its conversion to the
 * wire form, the client-side hints that mirror the save-time rules (the server decides), and the
 * claim input of the tester. Everything here is pure.
 */

/** The claims GitLab puts into an ID token and that a condition can name (R-CI-010, R-CI-011). */
export const GITLAB_CLAIMS: readonly string[] = [
  'project_id',
  'namespace_id',
  'root_namespace_id',
  'project_path',
  'namespace_path',
  'ref',
  'ref_type',
  'ref_path',
  'ref_protected',
  'environment',
  'environment_protected',
  'environment_action',
  'deployment_tier',
  'pipeline_source',
  'pipeline_id',
  'job_id',
  'user_id',
  'user_login',
  'user_email',
  'sha',
  'runner_environment',
  'project_visibility',
  'sub',
  'aud',
  'iss',
];

/** The ID claims of a GitLab ID token: immutable, so a condition on them cannot be bypassed by a rename (R-CI-001). */
export const PIN_CLAIMS: readonly string[] = ['project_id', 'namespace_id', 'root_namespace_id'];

/** Claims whose value is a boolean in the condition (GitLab sends them as strings; comparison normalizes). */
const BOOLEAN_CLAIMS: ReadonlySet<string> = new Set(['ref_protected', 'environment_protected']);

export type Operator = 'equals' | 'in' | 'glob' | 'regex' | 'present';
export const OPERATORS: readonly Operator[] = ['equals', 'in', 'glob', 'regex', 'present'];

export const INLINE_ACTIONS: readonly string[] = ['read', 'annotate', 'deploy', 'overwrite', 'delete'];
const WRITE_ACTIONS: ReadonlySet<string> = new Set(['deploy', 'overwrite', 'delete']);

let nextNodeId = 1;
/** A fresh identity for a node or row, so that a list keeps its focus and state while it is edited. */
export const newId = (): number => nextNodeId++;

export interface LeafNode {
  readonly kind: 'leaf';
  readonly id: number;
  readonly claim: string;
  /** A nested value (`path`): shown read-only, kept as it is. */
  readonly path: readonly string[] | null;
  readonly operator: Operator;
  /** The literal for `equals`, `glob` and `regex`; the values for `in`, one per line or separated by commas. */
  readonly value: string;
  /** For `present`: `true` requires the claim, `false` requires it to be absent. */
  readonly present: boolean;
  readonly ignoreCase: boolean;
}
export interface GroupNode {
  readonly kind: 'all' | 'any';
  readonly id: number;
  readonly children: readonly CondNode[];
}
export interface NotNode {
  readonly kind: 'not';
  readonly id: number;
  readonly child: CondNode;
}
export type CondNode = LeafNode | GroupNode | NotNode;

export function newLeaf(claim = '', over: Partial<LeafNode> = {}): LeafNode {
  return { kind: 'leaf', id: newId(), claim, path: null, operator: 'equals', value: '', present: true, ignoreCase: false, ...over };
}
export function newGroup(kind: 'all' | 'any', children: readonly CondNode[] = []): GroupNode {
  return { kind, id: newId(), children };
}

/** The empty condition of a new mapping: an `all` with one empty leaf for the user to fill. */
export const emptyCondition = (): GroupNode => newGroup('all', [newLeaf('project_id', { operator: 'in' })]);

export function nodeFromCondition(c: CiCondition): CondNode {
  if (c.all) return newGroup('all', c.all.map(nodeFromCondition));
  if (c.any) return newGroup('any', c.any.map(nodeFromCondition));
  if (c.not) return { kind: 'not', id: newId(), child: nodeFromCondition(c.not) };
  const base = { path: c.path ?? null, claim: c.claim ?? '', ignoreCase: c.ignoreCase === true };
  if (c.in !== undefined) return newLeaf('', { ...base, operator: 'in', value: c.in.map(String).join('\n') });
  if (c.glob !== undefined) return newLeaf('', { ...base, operator: 'glob', value: c.glob });
  if (c.regex !== undefined) return newLeaf('', { ...base, operator: 'regex', value: c.regex });
  if (c.present !== undefined) return newLeaf('', { ...base, operator: 'present', present: c.present });
  return newLeaf('', { ...base, operator: 'equals', value: c.equals === undefined ? '' : String(c.equals) });
}

/** The values of an `in` entry: one per line or separated by commas, empty entries dropped. */
export function splitValues(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((v) => v.trim())
    .filter((v) => v !== '');
}

function literal(claim: string, text: string): string | boolean {
  if (BOOLEAN_CLAIMS.has(claim) && (text === 'true' || text === 'false')) return text === 'true';
  return text;
}

export function conditionFromNode(n: CondNode): CiCondition {
  switch (n.kind) {
    case 'all':
      return { all: n.children.map(conditionFromNode) };
    case 'any':
      return { any: n.children.map(conditionFromNode) };
    case 'not':
      return { not: conditionFromNode(n.child) };
    case 'leaf': {
      const subject: CiCondition = n.path ? { path: [...n.path] } : { claim: n.claim.trim() };
      const text = n.value.trim();
      switch (n.operator) {
        case 'equals':
          return { ...subject, equals: literal(n.claim, text), ...(n.ignoreCase ? { ignoreCase: true } : {}) };
        case 'in':
          return { ...subject, in: splitValues(n.value).map((v) => literal(n.claim, v)), ...(n.ignoreCase ? { ignoreCase: true } : {}) };
        case 'glob':
          return { ...subject, glob: text, ...(n.ignoreCase ? { ignoreCase: true } : {}) };
        case 'regex':
          return { ...subject, regex: text, ...(n.ignoreCase ? { ignoreCase: true } : {}) };
        case 'present':
          return { ...subject, present: n.present };
      }
    }
  }
}

/** A readable one-line form of a condition: `namespace_id in [4711] and ref_protected = true`. */
export function summarizeCondition(c: CiCondition, top = true): string {
  const wrap = (s: string): string => (top ? s : `(${s})`);
  if (c.all) return wrap(c.all.map((x) => summarizeCondition(x, false)).join(` ${AND} `));
  if (c.any) return wrap(c.any.map((x) => summarizeCondition(x, false)).join(` ${OR} `));
  if (c.not) return `${NOT} ${summarizeCondition(c.not, false)}`;
  const subject = c.path ? c.path.join('.') : (c.claim ?? '');
  const fold = c.ignoreCase ? ` ${IGNORE_CASE}` : '';
  if (c.in !== undefined) return `${subject} in [${c.in.map(String).join(', ')}]${fold}`;
  if (c.glob !== undefined) return `${subject} ${GLOB} "${c.glob}"${fold}`;
  if (c.regex !== undefined) return `${subject} ${MATCHES} /${c.regex}/${fold}`;
  if (c.present !== undefined) return `${subject} ${c.present ? PRESENT : ABSENT}`;
  return `${subject} = ${String(c.equals)}${fold}`;
}
const AND = $localize`:@@ci.summary.and:and`;
const OR = $localize`:@@ci.summary.or:or`;
const NOT = $localize`:@@ci.summary.not:not`;
const GLOB = $localize`:@@ci.summary.glob:matches glob`;
const MATCHES = $localize`:@@ci.summary.regex:matches`;
const PRESENT = $localize`:@@ci.summary.present:is present`;
const ABSENT = $localize`:@@ci.summary.absent:is absent`;
const IGNORE_CASE = $localize`:@@ci.summary.ignore-case:(ignoring case)`;

/** The grants of a mapping as short lines for a list: policies, groups and each inline grant. */
export function summarizeGrants(g: CiGrants): string[] {
  const out: string[] = [];
  for (const p of g.policies ?? []) out.push($localize`:@@ci.grant.policy:Policy ${p}:policy:`);
  for (const group of g.groups ?? []) out.push($localize`:@@ci.grant.group:Group ${group}:group:`);
  for (const i of g.inline ?? []) {
    const patterns = i.packages ?? i.paths;
    const include = (patterns?.include ?? []).join(', ') || '*';
    out.push(`${i.actions.join(', ')}: ${i.repositories.join(', ')} · ${include}`);
  }
  return out;
}

export function tierLabel(tier: string): string {
  if (tier === 'system') return $localize`:@@ci.tier.system:System`;
  if (tier.startsWith('project:')) return $localize`:@@ci.tier.project:Project ${tier.slice('project:'.length)}:project:`;
  return tier;
}

/** The priority a new mapping of the list's tier starts with: the next multiple of ten above the highest. */
export function nextPriority(mappings: readonly Pick<CiMapping, 'priority'>[]): number {
  const highest = mappings.reduce((m, x) => Math.max(m, x.priority), 0);
  return (Math.floor(highest / 10) + 1) * 10;
}

/* ---- the mapping form ---- */

export interface InlineRow {
  readonly id: number;
  /** One repository key, pattern or template per line. */
  readonly repositories: string;
  readonly kind: 'packages' | 'paths';
  readonly include: string;
  readonly exclude: string;
  readonly actions: readonly string[];
  /** Empty: the server resolves it (`bind_project` when the actions write, else `none`, Q-CI-6). */
  readonly ownership: '' | 'bind_project' | 'none';
}

export interface MappingForm {
  readonly name: string;
  readonly description: string;
  readonly priority: string;
  readonly enabled: boolean;
  readonly match: CondNode;
  readonly policies: readonly string[];
  readonly groups: readonly string[];
  readonly inline: readonly InlineRow[];
  readonly maxTtlSeconds: string;
  readonly crossProjectSource: 'deny' | 'read_only';
  readonly allowUnprotectedRefs: boolean;
}

export function newInlineRow(over: Partial<InlineRow> = {}): InlineRow {
  return { id: newId(), repositories: '', kind: 'packages', include: '', exclude: '', actions: ['read'], ownership: '', ...over };
}

const lines = (text: string): string[] =>
  text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l !== '');

export function inlineFromGrant(g: CiInlineGrant): InlineRow {
  const kind = g.paths ? 'paths' : 'packages';
  const set = g.paths ?? g.packages;
  return newInlineRow({
    repositories: g.repositories.join('\n'),
    kind,
    include: (set?.include ?? []).join('\n'),
    exclude: (set?.exclude ?? []).join('\n'),
    actions: g.actions,
    ownership: g.ownership ?? '',
  });
}

export function grantFromInline(r: InlineRow): CiInlineGrant {
  const set = { include: lines(r.include), ...(lines(r.exclude).length > 0 ? { exclude: lines(r.exclude) } : {}) };
  return {
    repositories: lines(r.repositories),
    [r.kind]: set,
    actions: INLINE_ACTIONS.filter((a) => r.actions.includes(a)) as CiInlineGrant['actions'],
    ...(r.ownership ? { ownership: r.ownership } : {}),
  };
}

export function emptyMappingForm(priority: number, namePrefix = ''): MappingForm {
  return {
    name: namePrefix,
    description: '',
    priority: String(priority),
    enabled: true,
    match: emptyCondition(),
    policies: [],
    groups: [],
    inline: [],
    maxTtlSeconds: '',
    crossProjectSource: 'deny',
    allowUnprotectedRefs: false,
  };
}

export function formFromMapping(m: CiMapping): MappingForm {
  return {
    name: m.name,
    description: m.description,
    priority: String(m.priority),
    enabled: m.enabled,
    match: nodeFromCondition(m.match),
    policies: m.grants.policies ?? [],
    groups: m.grants.groups ?? [],
    inline: (m.grants.inline ?? []).map(inlineFromGrant),
    maxTtlSeconds: m.maxTtlSeconds ? String(m.maxTtlSeconds) : '',
    crossProjectSource: m.options.crossProjectSource ?? 'deny',
    allowUnprotectedRefs: m.options.allowUnprotectedRefs === true,
  };
}

/** The write body of a mapping; a project's mapping sends no groups (they are refused there, Q-CI-26). */
export function inputFromForm(f: MappingForm, project: string | null): CiMappingInput {
  const priority = Number.parseInt(f.priority, 10);
  const ttl = Number.parseInt(f.maxTtlSeconds, 10);
  return {
    name: f.name.trim(),
    description: f.description.trim(),
    priority: Number.isFinite(priority) ? priority : 0,
    enabled: f.enabled,
    subjectKinds: ['id_token'],
    match: conditionFromNode(f.match),
    grants: {
      policies: [...f.policies],
      ...(project ? {} : { groups: [...f.groups] }),
      inline: f.inline.map(grantFromInline),
    },
    maxTtlSeconds: Number.isFinite(ttl) && ttl > 0 ? ttl : null,
    options: { crossProjectSource: f.crossProjectSource, allowUnprotectedRefs: f.allowUnprotectedRefs },
  };
}

/* ---- the hints that mirror the save-time rules (the server decides) ---- */

/** True when a grant of the form can write (R-CI-011): an inline grant with deploy, overwrite or delete. */
export function writes(f: Pick<MappingForm, 'inline'>): boolean {
  return f.inline.some((r) => r.actions.some((a) => WRITE_ACTIONS.has(a)));
}

function isProtectedRefLeaf(n: CondNode): boolean {
  return n.kind === 'leaf' && n.claim === 'ref_protected' && n.operator === 'equals' && n.value.trim() === 'true';
}

/** True when the top-level `all` of the condition holds `ref_protected = true` (R-CI-011). */
export function requiresProtectedRef(root: CondNode): boolean {
  return root.kind === 'all' && root.children.some(isProtectedRefLeaf);
}

/** True when the top-level `all` pins an ID claim with `equals` or `in` (R-CI-010 pins, R-CI-011 `unpinned_write`). */
export function pinsProject(root: CondNode): boolean {
  return (
    root.kind === 'all' &&
    root.children.some(
      (n) => n.kind === 'leaf' && PIN_CLAIMS.includes(n.claim) && (n.operator === 'equals' || n.operator === 'in') && n.value.trim() !== '',
    )
  );
}

/** Whether a write without protected refs would be refused: it is unless the option allows it. */
export function unprotectedWrite(f: MappingForm): boolean {
  return writes(f) && !f.allowUnprotectedRefs && !requiresProtectedRef(f.match);
}

/** The tree with `ref_protected = true` added to its top-level `all` (the tree is wrapped when it is not one). */
export function withProtectedRef(root: CondNode): CondNode {
  const leaf = newLeaf('ref_protected', { value: 'true' });
  return root.kind === 'all' ? { ...root, children: [...root.children, leaf] } : newGroup('all', [root, leaf]);
}

/* ---- tree edits by path ---- */

/** Applies `fn` to the node at `path` (child indices from the root); `fn` returns null to remove it. */
export function editAt(root: CondNode, path: readonly number[], fn: (n: CondNode) => CondNode | null): CondNode | null {
  if (path.length === 0) return fn(root);
  const [head, ...rest] = path as [number, ...number[]];
  if (root.kind === 'not') {
    if (head !== 0) return root;
    const child = editAt(root.child, rest, fn);
    return child === null ? null : { ...root, child };
  }
  if (root.kind === 'leaf') return root;
  const children = root.children.flatMap((c, i) => {
    if (i !== head) return [c];
    const next = editAt(c, rest, fn);
    return next === null ? [] : [next];
  });
  return { ...root, children };
}

export function nodeAt(root: CondNode, path: readonly number[]): CondNode | null {
  let n: CondNode = root;
  for (const i of path) {
    if (n.kind === 'not') n = n.child;
    else if (n.kind === 'leaf') return null;
    else {
      const c: CondNode | undefined = n.children[i];
      if (!c) return null;
      n = c;
    }
  }
  return n;
}

/** The deepest nesting of the tree; the server refuses more than 4 levels (R-CI-010). */
export function depthOf(n: CondNode): number {
  if (n.kind === 'leaf') return 1;
  if (n.kind === 'not') return 1 + depthOf(n.child);
  return 1 + n.children.reduce((m, c) => Math.max(m, depthOf(c)), 0);
}

export function leafCount(n: CondNode): number {
  if (n.kind === 'leaf') return 1;
  if (n.kind === 'not') return leafCount(n.child);
  return n.children.reduce((s, c) => s + leafCount(c), 0);
}

/* ---- the claim tester ---- */

/** The claims of a GitLab ID token for the tester (R-UI-036); `iss` and `aud` come from the chosen issuer. */
export function sampleClaims(issuer: string, audience: string, nowSeconds: number): Record<string, unknown> {
  return {
    iss: issuer,
    aud: audience,
    sub: 'project_path:team-a/app:ref_type:branch:ref:main',
    namespace_id: '4711',
    namespace_path: 'team-a',
    project_id: '4242',
    project_path: 'team-a/app',
    ref: 'main',
    ref_type: 'branch',
    ref_protected: 'true',
    pipeline_source: 'push',
    environment: 'production',
    user_login: 'alice',
    job_id: '98765',
    iat: nowSeconds,
    nbf: nowSeconds,
    exp: nowSeconds + 300,
  };
}

/**
 * The sample with the values a mapping's condition asks for: for each `equals`, `in` and positive
 * `present` leaf of its top-level `all` (and the `all` groups inside it), the first literal. The tester
 * opens with it, so a mapping starts out matching and the user changes one claim to see a leaf fail.
 */
export function claimsSatisfying(base: Record<string, unknown>, c: CiCondition): Record<string, unknown> {
  const out = { ...base };
  const walk = (n: CiCondition): void => {
    if (n.all) {
      n.all.forEach(walk);
      return;
    }
    if (!n.claim || n.any || n.not) return;
    if (n.equals !== undefined) out[n.claim] = String(n.equals);
    else if (n.in !== undefined && n.in.length > 0) out[n.claim] = String(n.in[0]);
    else if (n.present === true && out[n.claim] === undefined) out[n.claim] = 'x';
  };
  walk(c);
  return out;
}

export type ClaimsInput =
  | { readonly kind: 'claims'; readonly claims: Record<string, unknown> }
  | { readonly kind: 'jwt'; readonly claims: Record<string, unknown>; readonly stillValid: boolean }
  | { readonly kind: 'error'; readonly reason: 'empty' | 'not-json' | 'not-object' | 'bad-token' };

const COMPACT_JWT = /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$/;

function decodeSegment(segment: string): unknown {
  const padded = segment.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(segment.length / 4) * 4, '=');
  const bytes = Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
  return JSON.parse(new TextDecoder().decode(bytes));
}

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);

/**
 * Reads the text of the tester (R-UI-036): a JSON object, or a compact JWT whose header and payload
 * are decoded here without any signature check. The signature segment is never looked at and never
 * leaves this function: the caller keeps the claims and discards the token. `stillValid` is true when
 * `exp` lies after `nowSeconds`, so the user is told the pasted token is still a credential.
 */
export function parseClaimsInput(text: string, nowSeconds: number): ClaimsInput {
  const trimmed = text.trim();
  if (trimmed === '') return { kind: 'error', reason: 'empty' };
  if (COMPACT_JWT.test(trimmed)) {
    try {
      const [header, payload] = trimmed.split('.') as [string, string, string];
      if (!isObject(decodeSegment(header))) return { kind: 'error', reason: 'bad-token' };
      const claims = decodeSegment(payload);
      if (!isObject(claims)) return { kind: 'error', reason: 'bad-token' };
      const exp = claims['exp'];
      return { kind: 'jwt', claims, stillValid: typeof exp === 'number' && exp > nowSeconds };
    } catch {
      return { kind: 'error', reason: 'bad-token' };
    }
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    return { kind: 'error', reason: 'not-json' };
  }
  return isObject(parsed) ? { kind: 'claims', claims: parsed } : { kind: 'error', reason: 'not-object' };
}
