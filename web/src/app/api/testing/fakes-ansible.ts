import { Provider } from '@angular/core';
import { AnsibleApi, AnsibleImportTask, AnsibleTombstone } from '../ansible-api';
import { Page, PageFetcher } from '../pagination';
import { ApiError } from '../problem';

/*
 * The in-memory fake of the Ansible Galaxy management operations (R-UI-047 component tier). The real operations answer 501
 * `ansible_not_implemented` until the local class lands; `failure` lets a spec throw that or any other refusal once.
 */

export const ANSIBLE_REPOSITORY = 'web/ansible-local';

export function importTask(over: Partial<AnsibleImportTask> = {}): AnsibleImportTask {
  return {
    id: '6f1c0000-0000-4000-8000-000000000001',
    state: 'completed',
    namespace: 'acme',
    name: 'tools',
    version: '1.0.0',
    createdAt: '2026-11-09T09:00:00Z',
    finishedAt: '2026-11-09T09:00:01Z',
    createdBy: 'user:alice',
    error: null,
    messages: [{ level: 'INFO', message: 'Collection acme.tools 1.0.0 stored', time: '2026-11-09T09:00:01Z' }],
    ...over,
  };
}

export function tombstone(over: Partial<AnsibleTombstone> = {}): AnsibleTombstone {
  return {
    name: 'acme.tools',
    version: '1.0.0',
    sha256: 'd3499c0000000000000000000000000000000000000000000000000000000001',
    deletedAt: '2026-11-08T09:00:00Z',
    deletedBy: 'user:alice',
    ...over,
  };
}

export function ansibleProblem(status: number, code: string, detail: string): ApiError {
  return new ApiError(status, { code, detail }, null);
}

export class FakeAnsibleApi extends AnsibleApi {
  tasks: AnsibleImportTask[] = [importTask()];
  stones: AnsibleTombstone[] = [tombstone()];
  /** The refusal the next call of any operation throws once. */
  failure: ApiError | null = null;
  calls: string[] = [];

  private enter(call: string): void {
    this.calls.push(call);
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
  }

  private pages<T>(call: string, all: () => readonly T[]): PageFetcher<T> {
    return async (cursor, limit): Promise<Page<T>> => {
      this.enter(call);
      const from = cursor === null ? 0 : Number(cursor);
      const items = all().slice(from, from + limit);
      return { items, nextCursor: from + limit < all().length ? String(from + limit) : null };
    };
  }

  override importTasks(): PageFetcher<AnsibleImportTask> {
    return this.pages('importTasks', () => this.tasks);
  }

  override tombstones(): PageFetcher<AnsibleTombstone> {
    return this.pages('tombstones', () => this.stones);
  }

  override async clearTombstone(_ref: string, collection: string, version: string): Promise<void> {
    this.enter(`clear:${collection}:${version}`);
    this.stones = this.stones.filter((s) => !(s.name === collection && s.version === version));
  }
}

export function provideAnsibleFakes(): Provider[] {
  return [{ provide: AnsibleApi, useClass: FakeAnsibleApi }];
}
