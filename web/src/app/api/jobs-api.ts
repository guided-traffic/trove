import { Injectable, inject } from '@angular/core';
import { ApiClient, QueryParams } from './client';
import { PageFetcher, collectAll } from './pagination';
import { Schema } from './types';

export type Job = Schema<'Job'>;
export type JobState = Schema<'JobState'>;
export type JobReference = Schema<'JobReference'>;
export type Task = Schema<'Task'>;

export interface JobFilters {
  readonly kind?: readonly string[];
  readonly state?: readonly JobState[];
}

/** States in which a job has not ended (R-API-015). */
export function jobActive(state: JobState): boolean {
  return state === 'queued' || state === 'running' || state === 'cancelling';
}

/** States in which "Cancel" is offered (R-UI-038). */
export function jobCancellable(state: JobState): boolean {
  return state === 'queued' || state === 'running';
}

/** Background jobs and maintenance tasks: R-UI-038, R-UI-042 (progress), R-API-015. */
export abstract class JobsApi {
  abstract jobs(filters: JobFilters): PageFetcher<Job>;
  abstract job(id: string): Promise<Job>;
  abstract cancel(id: string): Promise<Job>;
  abstract tasks(): Promise<readonly Task[]>;
  /** `POST /api/v1/system/tasks/{name}/run`; the job that was queued. */
  abstract runTask(
    name: string,
    parameters: Readonly<Record<string, unknown>> | undefined,
    idempotencyKey: string,
  ): Promise<JobReference>;
}

@Injectable()
export class HttpJobsApi extends JobsApi {
  private readonly client = inject(ApiClient);

  jobs(filters: JobFilters): PageFetcher<Job> {
    const params: Record<string, readonly string[] | undefined> = {
      kind: filters.kind?.length ? filters.kind : undefined,
      state: filters.state?.length ? filters.state : undefined,
    };
    return this.client.page<Job>('/api/v1/system/jobs', params as QueryParams);
  }

  async job(id: string): Promise<Job> {
    return (await this.client.getJson<Job>(`/api/v1/system/jobs/${encodeURIComponent(id)}`)).body;
  }

  async cancel(id: string): Promise<Job> {
    return (await this.client.post<Job>(`/api/v1/system/jobs/${encodeURIComponent(id)}/cancel`))
      .body;
  }

  tasks(): Promise<readonly Task[]> {
    return collectAll(this.client.page<Task>('/api/v1/system/tasks'));
  }

  async runTask(
    name: string,
    parameters: Readonly<Record<string, unknown>> | undefined,
    idempotencyKey: string,
  ): Promise<JobReference> {
    const r = await this.client.post<{ job: JobReference }>(
      `/api/v1/system/tasks/${encodeURIComponent(name)}/run`,
      parameters,
      { idempotencyKey },
    );
    return r.body.job;
  }
}
