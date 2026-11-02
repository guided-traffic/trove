import { Provider, inject } from '@angular/core';
import { Page, PageFetcher } from '../pagination';
import { Project, ProjectFilters, ProjectInput, ProjectPrincipal, ProjectsApi, PROJECT_KEY_PATTERN } from '../projects-api';
import { ApiError } from '../problem';
import { RepositoriesApi, Versioned } from '../repositories-api';
import { FakeRepositoriesApi } from './fakes-catalog';

/*
 * In-memory fake of the projects service (R-UI-041, R-UI-047 component tier). It models what the
 * screens depend on: the key pattern, the `If-Match` of the replace (412 with the current tag), the
 * delete of an empty project only (409 `project_not_empty`), the administrators and the assignment
 * of repositories, which it mirrors onto the fake repository service so that the project filter of
 * the repository list sees it.
 */

function problem(status: number, code: string, extra: Record<string, unknown> = {}): ApiError {
  return new ApiError(status, { code, ...extra }, 'req-1');
}

export function sampleProject(over: Partial<Project> = {}): Project {
  return {
    key: 'web',
    displayName: 'Web platform',
    description: "The web team's repositories and partners",
    managePolicies: true,
    manageRepositories: false,
    admins: ['group:web-leads'],
    repositories: 0,
    serviceAccounts: 0,
    policies: 0,
    version: 1,
    createdAt: '2026-11-01T08:00:00Z',
    createdBy: 'user:alice',
    updatedAt: '2026-11-01T08:00:00Z',
    updatedBy: 'user:alice',
    ...over,
  };
}

export class FakeProjectsApi extends ProjectsApi {
  private readonly repos = inject(RepositoriesApi);
  /** Every mutating call, for assertions. */
  readonly calls: string[] = [];
  readonly keys: string[] = [];
  /** The `If-Match` of every replace, in order. */
  readonly ifMatch: string[] = [];
  projects: Project[] = [sampleProject()];
  principals: ProjectPrincipal[] = [
    { kind: 'user', name: 'alice', principal: 'user:alice' },
    { kind: 'user', name: 'bob', principal: 'user:bob' },
    { kind: 'group', name: 'web-leads', principal: 'group:web-leads' },
    { kind: 'group', name: 'web-partners', principal: 'group:web-partners' },
  ];
  lookups: { key: string; q: string }[] = [];
  /** Rejects the next write once. */
  failure: ApiError | null = null;

  private fail(): void {
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
  }

  private find(key: string): Project {
    const found = this.projects.find((p) => p.key === key);
    if (!found) throw problem(404, 'not_found');
    return found;
  }

  private put(next: Project): void {
    this.projects = this.projects.map((p) => (p.key === next.key ? next : p));
  }

  private etag(p: Project): string {
    return `"${p.version}"`;
  }

  /** Simulates a write by someone else: the stored version moves on. */
  bump(key: string): void {
    const p = this.find(key);
    this.put({ ...p, version: p.version + 1 });
  }

  override list(filters: ProjectFilters): PageFetcher<Project> {
    return async (cursor, limit): Promise<Page<Project>> => {
      const q = filters.q?.toLowerCase();
      const all = this.projects
        .filter((p) => !q || p.key.includes(q) || p.displayName.toLowerCase().includes(q))
        .sort((a, b) => (filters.sort === '-key' ? -1 : 1) * a.key.localeCompare(b.key));
      const start = cursor === null ? 0 : Number.parseInt(cursor, 10);
      const end = start + limit;
      return { items: all.slice(start, end), nextCursor: end < all.length ? String(end) : null };
    };
  }

  override async get(key: string): Promise<Versioned<Project>> {
    const p = this.find(key);
    return { value: p, etag: this.etag(p) };
  }

  /** What the server stored per `Idempotency-Key`: the body and the outcome, a 4xx refusal too (R-API-014). */
  private readonly stored = new Map<string, { body: string; outcome: Versioned<Project> | ApiError }>();

  override async create(input: ProjectInput, idempotencyKey: string): Promise<Versioned<Project>> {
    this.fail();
    this.keys.push(idempotencyKey);
    this.calls.push(`create ${input.key}`);
    const body = JSON.stringify(input);
    const seen = this.stored.get(idempotencyKey);
    if (seen) {
      if (seen.body !== body) throw problem(422, 'idempotency_key_reused');
      if (seen.outcome instanceof ApiError) throw seen.outcome;
      return seen.outcome;
    }
    try {
      const created = this.createNew(input);
      this.stored.set(idempotencyKey, { body, outcome: created });
      return created;
    } catch (e) {
      if (e instanceof ApiError && e.status >= 400 && e.status < 500) this.stored.set(idempotencyKey, { body, outcome: e });
      throw e;
    }
  }

  private createNew(input: ProjectInput): Versioned<Project> {
    const key = input.key ?? '';
    if (!PROJECT_KEY_PATTERN.test(key)) {
      throw problem(422, 'invalid_request', {
        errors: [{ pointer: '/key', code: 'invalid_key', detail: 'The key does not match the pattern.' }],
      });
    }
    if (!(input.displayName ?? '').trim() || (input.displayName ?? '').length > 128) {
      throw problem(422, 'validation_failed', {
        errors: [{ pointer: '/displayName', code: 'invalid_length', detail: 'The display name is required, 1 to 128 characters.' }],
      });
    }
    if (this.projects.some((p) => p.key === key)) throw problem(409, 'already_exists');
    if (this.projects.some((p) => key.startsWith(`${p.key}-`) || p.key.startsWith(`${key}-`))) {
      throw problem(409, 'project_key_overlap');
    }
    const created = sampleProject({
      key,
      displayName: input.displayName ?? key,
      description: input.description,
      managePolicies: input.managePolicies ?? true,
      manageRepositories: input.manageRepositories ?? false,
      admins: [],
    });
    this.projects = [...this.projects, created];
    return { value: created, etag: this.etag(created) };
  }

  override async replace(key: string, input: ProjectInput, etag: string): Promise<Versioned<Project>> {
    this.fail();
    this.ifMatch.push(etag);
    this.calls.push(`replace ${key}`);
    const current = this.find(key);
    if (etag !== this.etag(current)) {
      throw problem(412, 'precondition_failed', { currentEtag: this.etag(current) });
    }
    const next: Project = {
      ...current,
      displayName: input.displayName ?? current.displayName,
      description: input.description,
      managePolicies: input.managePolicies ?? current.managePolicies,
      manageRepositories: input.manageRepositories ?? current.manageRepositories,
      version: current.version + 1,
    };
    this.put(next);
    return { value: next, etag: this.etag(next) };
  }

  override async delete(key: string): Promise<void> {
    this.fail();
    this.calls.push(`delete ${key}`);
    const p = this.find(key);
    if (p.repositories > 0 || p.serviceAccounts > 0) throw problem(409, 'project_not_empty');
    this.projects = this.projects.filter((x) => x.key !== key);
  }

  override async addAdmin(key: string, principal: string): Promise<void> {
    this.fail();
    this.calls.push(`addAdmin ${key} ${principal}`);
    const p = this.find(key);
    if (!/^(user|group):/.test(principal)) throw problem(422, 'invalid_member');
    if (!p.admins.includes(principal)) this.put({ ...p, admins: [...p.admins, principal].sort() });
  }

  override async removeAdmin(key: string, principal: string): Promise<void> {
    this.fail();
    this.calls.push(`removeAdmin ${key} ${principal}`);
    const p = this.find(key);
    if (!p.admins.includes(principal)) throw problem(404, 'not_found');
    this.put({ ...p, admins: p.admins.filter((a) => a !== principal) });
  }

  override async assignRepository(key: string, repository: string, move: boolean): Promise<void> {
    this.fail();
    this.calls.push(`assign ${key} ${repository}${move ? ' move' : ''}`);
    const target = this.find(key);
    const repo = this.catalog().repositories.find((r) => r.key === repository);
    if (!repo) throw problem(404, 'not_found');
    const from = repo.project ?? null;
    if (from === key) return;
    if (from !== null && !move) throw problem(409, 'repository_assigned');
    if (from !== null) {
      const old = this.find(from);
      this.put({ ...old, repositories: Math.max(0, old.repositories - 1) });
    }
    repo.project = key;
    this.put({ ...target, repositories: target.repositories + 1 });
  }

  override async unassignRepository(key: string, repository: string): Promise<void> {
    this.fail();
    this.calls.push(`unassign ${key} ${repository}`);
    const p = this.find(key);
    const repo = this.catalog().repositories.find((r) => r.key === repository);
    if (!repo || repo.project !== key) throw problem(404, 'not_found');
    repo.project = null;
    this.put({ ...p, repositories: Math.max(0, p.repositories - 1) });
  }

  override async lookupPrincipals(
    key: string,
    q: string,
    kind?: ProjectPrincipal['kind'],
  ): Promise<readonly ProjectPrincipal[]> {
    this.find(key);
    this.lookups.push({ key, q });
    if (q.length < 2) throw problem(422, 'invalid_request');
    return this.principals.filter((p) => p.name.startsWith(q) && (!kind || p.kind === kind)).slice(0, 20);
  }

  private catalog(): FakeRepositoriesApi {
    return this.repos as FakeRepositoriesApi;
  }
}

export function provideProjectFakes(): Provider[] {
  return [{ provide: ProjectsApi, useClass: FakeProjectsApi }];
}
