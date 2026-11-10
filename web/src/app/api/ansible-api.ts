import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { PageFetcher } from './pagination';
import { seg } from './query';
import { repositoryUrl } from './repositories-api';
import { Schema } from './types';

/* The management operations of the Ansible Galaxy format: import tasks (R-ANS-009) and tombstones (R-ANS-012). */

export type AnsibleImportTask = Schema<'AnsibleImportTask'>;
export type AnsibleTombstone = Schema<'AnsibleTombstone'>;

export abstract class AnsibleApi {
  /** `GET …/ansible/imports`: the import tasks of a local repository (of the deployment target for a virtual one), newest first. */
  abstract importTasks(ref: string): PageFetcher<AnsibleImportTask>;
  /** `GET …/ansible/tombstones`: the deleted versions of a local repository whose numbers are not accepted again. */
  abstract tombstones(ref: string): PageFetcher<AnsibleTombstone>;
  /** `DELETE …/ansible/tombstones/{collection}/{version}`: clears a tombstone, so that the number can be published again. */
  abstract clearTombstone(ref: string, collection: string, version: string): Promise<void>;
}

@Injectable()
export class HttpAnsibleApi extends AnsibleApi {
  private readonly client = inject(ApiClient);

  override importTasks(ref: string): PageFetcher<AnsibleImportTask> {
    return this.client.page<AnsibleImportTask>(`${repositoryUrl(ref)}/ansible/imports`);
  }

  override tombstones(ref: string): PageFetcher<AnsibleTombstone> {
    return this.client.page<AnsibleTombstone>(`${repositoryUrl(ref)}/ansible/tombstones`);
  }

  override async clearTombstone(ref: string, collection: string, version: string): Promise<void> {
    await this.client.delete(`${repositoryUrl(ref)}/ansible/tombstones/${seg(collection)}/${seg(version)}`);
  }
}
