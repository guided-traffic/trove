import { Provider } from '@angular/core';
import {
  CiApi,
  CiCondition,
  CiEvaluateRequest,
  CiEvaluateResult,
  CiGrants,
  CiInlineGrant,
  CiIssuance,
  CiIssuanceDetail,
  CiIssuer,
  CiIssuerChoice,
  CiIssuerCreate,
  CiIssuerInput,
  CiKeyStatus,
  CiLeafResult,
  CiMapping,
  CiMappingInput,
  CiRevokeRequest,
  CiTraceEntry,
  CiWarning,
  IssuanceFilters,
  SnippetFormat,
} from '../ci-api';
import { Page, PageFetcher } from '../pagination';
import { ApiError } from '../problem';
import { Versioned } from '../repositories-api';

/*
 * In-memory fake of the CI service (R-UI-035, R-UI-036, R-UI-047 component tier), written against the
 * contract of docs/developer/ci-oidc.md. It models what the screens
 * depend on: the `ETag` of a replace (412), a configuration issuer that refuses writes, the
 * validation codes of an issuer, the save-time rules and warnings of a mapping, a priority that is
 * taken (409), the bounds of a project's mapping, a dry run that evaluates the stored mappings in
 * tier and priority order with the leaves of each, the issuances with their filters and the bulk
 * revoke.
 */

const problem = (status: number, code: string, extra: Record<string, unknown> = {}): ApiError =>
  new ApiError(status, { code, ...extra }, 'req-1');

const invalid = (pointer: string, code: string): ApiError =>
  new ApiError(422, { code: 'validation_failed', errors: [{ pointer, code, detail: code }] }, 'req-1');

export function sampleIssuer(over: Partial<CiIssuer> = {}): CiIssuer {
  return {
    name: 'gitlab-com',
    kind: 'gitlab',
    issuer: 'https://gitlab.example.com',
    description: 'The company GitLab',
    enabled: true,
    discoveryUrl: null,
    jwksUri: null,
    allowedAudiences: ['https://trove.example.com', 'npm:trove.example.com'],
    exclusiveAudience: true,
    allowedAlgorithms: ['RS256'],
    allowedTyp: ['JWT'],
    allowMissingTyp: true,
    clockSkewSeconds: 60,
    requiredClaims: ['project_id', 'namespace_id', 'ref_protected'],
    publicIssuer: false,
    pinClaims: ['project_id', 'namespace_id'],
    singleUseSubjectTokens: false,
    maxTtlSeconds: null,
    caBundleRef: null,
    useOutboundProxy: false,
    jobTokenFallback: { enabled: false },
    source: 'api',
    removedFromConfiguration: false,
    mappingCount: 0,
    keys: { usable: 2, ignored: 0, fetchedAt: '2026-11-02T08:00:00Z', lastError: null, lastErrorAt: null },
    version: 1,
    createdAt: '2026-11-01T08:00:00Z',
    updatedAt: '2026-11-01T08:00:00Z',
    ...over,
  };
}

export function sampleMapping(over: Partial<CiMapping> = {}): CiMapping {
  return {
    name: 'team-a-release',
    description: 'Protected tags of team-a publish',
    priority: 100,
    enabled: true,
    subjectKinds: ['id_token'],
    match: {
      all: [
        { claim: 'namespace_id', in: ['4711'] },
        { claim: 'ref_protected', equals: true },
      ],
    },
    grants: {
      policies: ['ci-read-internal'],
      groups: [],
      inline: [
        {
          repositories: ['npm-internal'],
          packages: { include: ['@team-a/*'] },
          actions: ['read', 'deploy'],
          ownership: 'bind_project',
        },
      ],
    },
    maxTtlSeconds: 1800,
    options: { crossProjectSource: 'deny', allowUnprotectedRefs: false },
    tier: 'system',
    issuer: 'gitlab-com',
    version: 1,
    createdBy: 'user:alice',
    createdAt: '2026-11-01T09:00:00Z',
    updatedAt: '2026-11-01T09:00:00Z',
    ...over,
  };
}

export function sampleIssuance(over: Partial<CiIssuance> = {}): CiIssuance {
  return {
    tokenId: '11111111-1111-4111-8111-111111111111',
    issuer: 'gitlab-com',
    mappingNames: ['team-a-release'],
    subjectKind: 'id_token',
    jti: 'jti-1',
    projectId: '4242',
    projectPath: 'team-a/app',
    namespaceId: '4711',
    pipelineId: '555',
    jobId: '98765',
    ref: 'v1.0.0',
    refType: 'tag',
    refProtected: true,
    pipelineSource: 'push',
    environment: null,
    sha: 'abc123',
    userId: '7',
    userLogin: 'alice',
    claims: { project_path: 'team-a/app', ref: 'v1.0.0' },
    grants: { inline: [{ repositories: ['npm-internal'], actions: ['read', 'deploy'] }] },
    requestedScope: null,
    grantedScope: 'read deploy:npm-internal',
    crossProject: false,
    issuedAt: '2026-11-02T08:00:00Z',
    expiresAt: '2026-11-02T08:15:00Z',
    revokedAt: null,
    active: true,
    clientIp: '203.0.113.9',
    userAgent: 'curl/8',
    ...over,
  };
}

const WRITES = new Set(['deploy', 'overwrite', 'delete']);

function stringify(v: unknown): string {
  return typeof v === 'string' ? v : JSON.stringify(v);
}

function globRegex(glob: string): RegExp {
  const escaped = glob
    .replace(/[.+^${}()|[\]\\]/g, '\\$&')
    .replace(/\*\*/g, '\uE000')
    .replace(/\*/g, '[^/]*')
    .replace(/\uE000/g, '.*')
    .replace(/\?/g, '[^/]');
  return new RegExp(`^${escaped}$`);
}

/** A leaf of a condition, evaluated against claims: the trace entry of the dry run (R-CI-019). */
function leaf(c: CiCondition, claims: Record<string, unknown>, pointer: string): CiLeafResult {
  const claim = c.claim ?? (c.path ?? []).join('.');
  const raw = c.path ? undefined : claims[claim];
  const absent = raw === undefined || raw === null;
  const actual = absent ? [] : [stringify(raw)];
  const fold = (s: string): string => (c.ignoreCase ? s.toLowerCase() : s);
  let operator = 'equals';
  let expected: string[];
  let passed: boolean;
  if (c.in !== undefined) {
    operator = 'in';
    expected = c.in.map(stringify);
    passed = !absent && expected.some((e) => fold(e) === fold(actual[0]!));
  } else if (c.glob !== undefined) {
    operator = 'glob';
    expected = [c.glob];
    passed = !absent && new RegExp(globRegex(fold(c.glob)).source).test(fold(actual[0]!));
  } else if (c.regex !== undefined) {
    operator = 'regex';
    expected = [c.regex];
    passed = !absent && new RegExp(`^(?:${c.regex})$`, c.ignoreCase ? 'i' : '').test(actual[0]!);
  } else if (c.present !== undefined) {
    operator = 'present';
    expected = [String(c.present)];
    passed = c.present ? !absent : absent;
  } else {
    expected = [stringify(c.equals)];
    passed = !absent && fold(expected[0]!) === fold(actual[0]!);
  }
  return { pointer, claim, operator, expected, actual, absent, passed };
}

/** The truth of a condition and the leaves it looked at, in order. */
function truth(c: CiCondition, claims: Record<string, unknown>, pointer: string, leaves: CiLeafResult[]): boolean {
  if (c.all) return c.all.map((x, i) => truth(x, claims, `${pointer}/all/${i}`, leaves)).every(Boolean);
  if (c.any) return c.any.map((x, i) => truth(x, claims, `${pointer}/any/${i}`, leaves)).some(Boolean);
  if (c.not) {
    const sub: CiLeafResult[] = [];
    const inner = truth(c.not, claims, `${pointer}/not`, sub);
    leaves.push(...sub.map((l) => ({ ...l, passed: !l.passed })));
    return !inner;
  }
  const result = leaf(c, claims, pointer);
  leaves.push(result);
  return result.passed;
}

function render(template: string, claims: Record<string, unknown>): string {
  return template.replace(/\{\{\s*([a-z_][a-z0-9_]*)(?:\s*\|\s*(slug|lower))?\s*\}\}/g, (_m, name: string, filter?: string) => {
    const value = String(claims[name] ?? '');
    if (filter === 'slug') return value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
    return filter === 'lower' ? value.toLowerCase() : value;
  });
}

export class FakeCiApi extends CiApi {
  /** Every mutating call, for assertions. */
  readonly calls: string[] = [];
  /** The body of every dry run, to prove what leaves the browser (R-UI-036). */
  readonly evaluations: { issuer: string; project: string | null; request: CiEvaluateRequest }[] = [];
  /** The filters of every issuance list read, in order. */
  readonly listFilters: IssuanceFilters[] = [];
  /** `ETag` of every replace, in order. */
  readonly ifMatch: string[] = [];
  /** Rejects the next call once. */
  failure: ApiError | null = null;
  issuers: CiIssuer[] = [
    sampleIssuer({ mappingCount: 1 }),
    sampleIssuer({
      name: 'corp-gitlab',
      issuer: 'https://git.corp.example.com',
      description: 'Declared in the Helm values',
      source: 'configuration',
      mappingCount: 0,
      keys: { usable: 0, ignored: 1, fetchedAt: null, lastError: 'fetch failed: HTTP 502 from git.corp.example.com', lastErrorAt: '2026-11-02T07:00:00Z' },
    }),
  ];
  mappings: CiMapping[] = [sampleMapping()];
  issuances: CiIssuance[] = [sampleIssuance()];
  /** The repositories of each project, for the bound of a project's inline grants. */
  projectRepositories: Record<string, readonly string[]> = { web: ['generic-local'] };
  projectPolicies: Record<string, readonly string[]> = { web: ['web-read'] };
  systemPolicies: readonly string[] = ['ci-read-internal', 'team-web-publish'];
  /** The codes `createIssuer` refuses with while `validate` is on, by issuer URL. */
  discoveryFailures: Record<string, string> = {};
  snippets = new Map<string, string>();
  usage: CiIssuanceDetail['usage'] = { requests: 12, lastUsedAt: '2026-11-02T08:10:00Z', packagesDeployed: ['@team-a/ui@1.0.0'] };

  private gate(): void {
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
  }

  private issuer(name: string): CiIssuer {
    const found = this.issuers.find((i) => i.name === name);
    if (!found) throw problem(404, 'not_found');
    return found;
  }

  private withCount(i: CiIssuer): CiIssuer {
    return { ...i, mappingCount: this.mappings.filter((m) => m.issuer === i.name).length };
  }

  override async listIssuers(): Promise<readonly CiIssuer[]> {
    this.gate();
    return [...this.issuers].sort((a, b) => a.name.localeCompare(b.name)).map((i) => this.withCount(i));
  }

  override async getIssuer(name: string): Promise<Versioned<CiIssuer>> {
    this.gate();
    const i = this.withCount(this.issuer(name));
    return { value: i, etag: `"${i.version}"` };
  }

  override async createIssuer(input: CiIssuerCreate): Promise<Versioned<CiIssuer>> {
    this.gate();
    this.calls.push(`createIssuer ${input.name}`);
    if (input.kind !== 'gitlab') throw invalid('/kind', 'kind_not_available');
    if (this.issuers.some((i) => i.name === input.name || i.issuer === input.issuer)) throw problem(409, 'already_exists');
    if (input.validate !== false && this.discoveryFailures[input.issuer]) {
      throw new ApiError(422, { code: this.discoveryFailures[input.issuer]! }, 'req-1');
    }
    const created = sampleIssuer({
      ...(input as Partial<CiIssuer>),
      source: 'api',
      mappingCount: 0,
      version: 1,
      allowedAudiences: input.allowedAudiences ?? ['https://trove.example.com'],
    });
    this.issuers = [...this.issuers, created];
    return { value: created, etag: '"1"' };
  }

  override async replaceIssuer(name: string, input: CiIssuerInput, etag: string): Promise<Versioned<CiIssuer>> {
    this.gate();
    this.calls.push(`replaceIssuer ${name}`);
    this.ifMatch.push(etag);
    const current = this.issuer(name);
    if (current.source === 'configuration') throw problem(409, 'issuer_managed_by_configuration');
    if (etag !== `"${current.version}"`) throw problem(412, 'precondition_failed', { currentEtag: `"${current.version}"` });
    const next: CiIssuer = { ...current, ...(input as Partial<CiIssuer>), name: current.name, version: current.version + 1 };
    this.issuers = this.issuers.map((i) => (i.name === name ? next : i));
    return { value: next, etag: `"${next.version}"` };
  }

  override async deleteIssuer(name: string, cascade: boolean): Promise<void> {
    this.gate();
    this.calls.push(`deleteIssuer ${name}${cascade ? ' cascade' : ''}`);
    this.issuer(name);
    if (!cascade && this.mappings.some((m) => m.issuer === name)) throw problem(409, 'conflict');
    this.issuers = this.issuers.filter((i) => i.name !== name);
    this.mappings = this.mappings.filter((m) => m.issuer !== name);
  }

  private status(i: CiIssuer): CiKeyStatus {
    return {
      issuer: i.name,
      jwksUri: `${i.issuer}/oauth/discovery/keys`,
      keys: Array.from({ length: i.keys?.usable ?? 0 }, (_v, n) => ({ kid: `kid-${n + 1}`, kty: 'RSA', alg: 'RS256', ignored: false })).concat(
        Array.from({ length: i.keys?.ignored ?? 0 }, (_v, n) => ({ kid: `weak-${n + 1}`, kty: 'RSA', alg: 'RS256', ignored: true, ignoredReason: 'key_too_small' }) as never),
      ),
      fetchedAt: i.keys?.fetchedAt ?? null,
      nextRefreshAt: '2026-11-02T08:30:00Z',
      lastError: i.keys?.lastError ?? null,
      lastErrorAt: i.keys?.lastErrorAt ?? null,
    };
  }

  override async keys(name: string): Promise<CiKeyStatus> {
    this.gate();
    return this.status(this.issuer(name));
  }

  override async refreshKeys(name: string): Promise<CiKeyStatus> {
    this.gate();
    this.calls.push(`refreshKeys ${name}`);
    const i = this.issuer(name);
    if (this.discoveryFailures[i.issuer]) throw problem(502, 'upstream_error');
    const next = { ...i, keys: { usable: 2, ignored: 0, fetchedAt: '2026-11-02T09:00:00Z', lastError: null, lastErrorAt: null } };
    this.issuers = this.issuers.map((x) => (x.name === name ? next : x));
    return this.status(next);
  }

  override async projectIssuers(project: string): Promise<readonly CiIssuerChoice[]> {
    this.gate();
    if (!this.projectRepositories[project]) throw problem(404, 'not_found');
    return this.issuers.map((i) => ({ name: i.name, kind: i.kind, issuer: i.issuer, publicIssuer: i.publicIssuer }));
  }

  private tierOf(project: string | null): string {
    return project ? `project:${project}` : 'system';
  }

  override async listMappings(issuer: string, project: string | null): Promise<readonly CiMapping[]> {
    this.gate();
    if (!project) this.issuer(issuer);
    return this.mappings
      .filter((m) => m.tier === this.tierOf(project) && (project ? !issuer || m.issuer === issuer : m.issuer === issuer))
      .sort((a, b) => a.priority - b.priority);
  }

  override async getMapping(issuer: string, name: string, project: string | null): Promise<Versioned<CiMapping>> {
    this.gate();
    const m = this.mappings.find((x) => x.issuer === issuer && x.name === name && x.tier === this.tierOf(project));
    if (!m) throw problem(404, 'not_found');
    return { value: m, etag: `"${m.version}"` };
  }

  /** The save-time rules of R-CI-009 to R-CI-013 and the bounds of Q-CI-26, as far as the screens depend on them. */
  private check(input: CiMappingInput, project: string | null, own?: string): { warnings: CiWarning[] } {
    const warnings: CiWarning[] = [];
    if (project && !input.name.startsWith(`${project}-`)) throw invalid('/name', 'project_prefix_required');
    const g = input.grants;
    if (project && (g.groups ?? []).length > 0) throw invalid('/grants/groups', 'groups_not_allowed_in_project');
    const policies = project ? this.projectPolicies[project] ?? [] : this.systemPolicies;
    (g.policies ?? []).forEach((p, i) => {
      if (!policies.includes(p)) throw invalid(`/grants/policies/${i}`, project ? 'policy_outside_project' : 'unknown_policy');
    });
    const inline = g.inline ?? [];
    if ((g.policies ?? []).length + (g.groups ?? []).length + inline.length === 0) throw invalid('/grants', 'empty_grant');
    inline.forEach((row: CiInlineGrant, i) => {
      if ((row.actions as string[]).includes('manage')) throw invalid(`/grants/inline/${i}/actions`, 'action_not_allowed');
      if (project) {
        row.repositories.forEach((r, n) => {
          if (r !== '@any' && !(this.projectRepositories[project] ?? []).includes(r) && !r.includes('{{')) {
            throw invalid(`/grants/inline/${i}/repositories/${n}`, 'repository_outside_project');
          }
        });
      }
      if (row.repositories.some((r) => /^\{\{[^}]*\}\}$/.test(r.trim()))) throw invalid(`/grants/inline/${i}/repositories`, 'template_unanchored');
      if (row.ownership === 'none' && row.actions.some((a) => WRITES.has(a))) {
        warnings.push({ code: 'shared_ownership', pointer: `/grants/inline/${i}/ownership`, detail: 'Every project this mapping matches co-owns the package names it claims.' });
      }
    });
    const writes = inline.some((r) => r.actions.some((a) => WRITES.has(a)));
    const top = input.match.all ?? [];
    const protectedRef = top.some((n) => n.claim === 'ref_protected' && (n.equals === true || n.equals === 'true'));
    if (writes && !protectedRef) {
      if (!input.options?.allowUnprotectedRefs) throw invalid('/match', 'unprotected_write');
      warnings.push({ code: 'unprotected_refs_allowed', pointer: '/options/allowUnprotectedRefs', detail: 'Any ref can use the write grants.' });
    }
    if (writes && !top.some((n) => n.claim === 'pipeline_source')) {
      warnings.push({ code: 'write_without_pipeline_source', pointer: '/match', detail: 'Add a pipeline_source condition such as push, web or schedule.' });
    }
    if (input.match.all?.length === 0) throw invalid('/match/all', 'condition_invalid');
    if (this.mappings.some((m) => m.tier === this.tierOf(project) && m.priority === input.priority && m.name !== own)) {
      throw problem(409, 'priority_taken');
    }
    return { warnings };
  }

  override async createMapping(issuer: string, input: CiMappingInput, project: string | null): Promise<Versioned<CiMapping>> {
    this.gate();
    this.calls.push(`createMapping ${project ?? 'system'} ${issuer}/${input.name}`);
    this.issuer(issuer);
    if (this.mappings.some((m) => m.issuer === issuer && m.name === input.name)) throw problem(409, 'already_exists');
    const { warnings } = this.check(input, project);
    const created: CiMapping = sampleMapping({
      ...(input as Partial<CiMapping>),
      description: input.description ?? '',
      maxTtlSeconds: input.maxTtlSeconds ?? null,
      options: { crossProjectSource: 'deny', allowUnprotectedRefs: false, ...input.options },
      tier: this.tierOf(project),
      issuer,
      version: 1,
      warnings,
    });
    this.mappings = [...this.mappings, { ...created, warnings: undefined }];
    return { value: created, etag: '"1"' };
  }

  override async replaceMapping(
    issuer: string,
    name: string,
    input: CiMappingInput,
    etag: string,
    project: string | null,
  ): Promise<Versioned<CiMapping>> {
    this.gate();
    this.calls.push(`replaceMapping ${project ?? 'system'} ${issuer}/${name}`);
    this.ifMatch.push(etag);
    const current = (await this.getMapping(issuer, name, project)).value;
    if (etag !== `"${current.version}"`) throw problem(412, 'precondition_failed', { currentEtag: `"${current.version}"` });
    const { warnings } = this.check({ ...input, name }, project, name);
    const next: CiMapping = { ...current, ...(input as Partial<CiMapping>), name, version: current.version + 1, warnings };
    this.mappings = this.mappings.map((m) => (m === current ? { ...next, warnings: undefined } : m));
    return { value: next, etag: `"${next.version}"` };
  }

  override async deleteMapping(issuer: string, name: string, project: string | null): Promise<void> {
    this.gate();
    this.calls.push(`deleteMapping ${project ?? 'system'} ${issuer}/${name}`);
    const m = (await this.getMapping(issuer, name, project)).value;
    this.mappings = this.mappings.filter((x) => x !== m);
  }

  override async evaluate(issuer: string, request: CiEvaluateRequest, project: string | null): Promise<CiEvaluateResult> {
    this.gate();
    this.evaluations.push({ issuer, project, request });
    this.issuer(issuer);
    const claims = request.claims;
    const tiers = project ? [`project:${project}`] : ['system', ...new Set(this.mappings.filter((m) => m.tier !== 'system').map((m) => m.tier))];
    const trace: CiTraceEntry[] = [];
    const selected: CiMapping[] = [];
    for (const tier of tiers) {
      const candidates = this.mappings.filter((m) => m.issuer === issuer && m.tier === tier && m.enabled).sort((a, b) => a.priority - b.priority);
      for (const m of candidates) {
        const leaves: CiLeafResult[] = [];
        const matched = truth(m.match, claims, '', leaves);
        const failed = leaves.find((l) => !l.passed);
        trace.push({
          tier,
          mapping: m.name,
          matched,
          contributes: matched,
          leaves,
          ...(failed ? { failedLeaf: failed.pointer, detail: `${failed.claim} ${failed.operator} ${failed.expected.join(', ')}: ${failed.absent ? 'absent' : `value ${failed.actual.join(', ')}`}` } : {}),
        });
        if (matched) {
          selected.push(m);
          break;
        }
      }
    }
    if (selected.length === 0) {
      return { crossProject: false, trace, selected: [], failure: 'no_matching_mapping', detail: 'No mapping of the issuer matches these claims.' };
    }
    const grants: CiGrants = {
      policies: selected.flatMap((m) => m.grants.policies ?? []),
      groups: selected.flatMap((m) => m.grants.groups ?? []),
      inline: selected.flatMap((m) =>
        (m.grants.inline ?? []).map((row) => ({
          ...row,
          repositories: row.repositories.map((r) => render(r, claims)),
          ...(row.packages ? { packages: { include: row.packages.include?.map((p) => render(p, claims)) } } : {}),
        })),
      ),
    };
    const ttl = Math.min(...selected.map((m) => m.maxTtlSeconds ?? 900), 900);
    return {
      crossProject: false,
      trace,
      selected: selected.map((m) => m.name),
      grants,
      scope: request.scope ?? 'read deploy',
      ttlSeconds: ttl,
      warnings: [],
    };
  }

  override async snippet(issuer: string, mapping: string, format: SnippetFormat, project: string | null): Promise<string> {
    this.gate();
    this.calls.push(`snippet ${project ?? 'system'} ${issuer}/${mapping}`);
    this.issuer(issuer);
    const m = this.mappings.find((x) => x.issuer === issuer && x.name === mapping);
    if (!m || m.tier !== this.tierOf(project)) throw problem(404, 'not_found');
    return (
      this.snippets.get(`${issuer}/${mapping}/${format}`) ??
      `# ${format} snippet for ${issuer}/${mapping}\nid_tokens:\n  TROVE_ID_TOKEN:\n    aud: https://trove.example.com\n`
    );
  }

  override listIssuances(filters: IssuanceFilters, project: string | null): PageFetcher<CiIssuance> {
    return async (cursor, limit): Promise<Page<CiIssuance>> => {
      this.gate();
      this.listFilters.push(filters);
      const mine = (i: CiIssuance): boolean => !project || i.mappingNames.some((n) => this.mappings.some((m) => m.name === n && m.tier === `project:${project}`));
      const all = this.issuances.filter(
        (i) =>
          mine(i) &&
          (!filters.issuer || i.issuer === filters.issuer) &&
          (!filters.projectId || i.projectId === filters.projectId) &&
          (!filters.projectPath || i.projectPath === filters.projectPath) &&
          (!filters.pipelineId || i.pipelineId === filters.pipelineId) &&
          (!filters.jobId || i.jobId === filters.jobId) &&
          (!filters.mapping || i.mappingNames.includes(filters.mapping)) &&
          (filters.active === undefined || i.active === filters.active) &&
          (!filters.from || i.issuedAt >= filters.from) &&
          (!filters.to || i.issuedAt < filters.to),
      );
      const start = cursor === null ? 0 : Number.parseInt(cursor, 10);
      const end = start + limit;
      return { items: all.slice(start, end), nextCursor: end < all.length ? String(end) : null };
    };
  }

  override async getIssuance(tokenId: string): Promise<CiIssuanceDetail> {
    this.gate();
    const issuance = this.issuances.find((i) => i.tokenId === tokenId);
    if (!issuance) throw problem(404, 'not_found');
    return { issuance, usage: this.usage };
  }

  override async revokeIssuances(request: CiRevokeRequest): Promise<number> {
    this.gate();
    this.calls.push(`revoke ${JSON.stringify(request)}`);
    if (!request.projectId && !request.pipelineId && !request.jobId && !request.mapping) throw invalid('/', 'validation_failed');
    let revoked = 0;
    this.issuances = this.issuances.map((i) => {
      const hit =
        i.active &&
        i.issuer === request.issuer &&
        (!request.projectId || i.projectId === request.projectId) &&
        (!request.pipelineId || i.pipelineId === request.pipelineId) &&
        (!request.jobId || i.jobId === request.jobId) &&
        (!request.mapping || i.mappingNames.includes(request.mapping));
      if (!hit) return i;
      revoked++;
      return { ...i, active: false, revokedAt: '2026-11-02T09:30:00Z' };
    });
    return revoked;
  }
}

export function provideCiFakes(): Provider[] {
  return [{ provide: CiApi, useClass: FakeCiApi }];
}
