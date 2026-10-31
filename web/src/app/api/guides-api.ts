import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { seg } from './query';
import { Schema } from './types';

/* The guides of the formats: R-API-053, R-UI-049. */

export type Guide = Schema<'Guide'>;
export type GuideSection = Schema<'GuideSection'>;
export type FormatClient = Schema<'FormatClient'>;

/** The guide of a repository, or of a class of a format when no repository is chosen. */
export abstract class GuidesApi {
  abstract repository(key: string, client: string): Promise<Guide>;
  abstract format(format: string, cls: string, client: string): Promise<Guide>;
}

@Injectable()
export class HttpGuidesApi extends GuidesApi {
  private readonly client = inject(ApiClient);

  override async repository(key: string, client: string): Promise<Guide> {
    return (await this.client.getJson<Guide>(`/api/v1/repositories/${seg(key)}/guide`, { params: { client } })).body;
  }

  override async format(format: string, cls: string, client: string): Promise<Guide> {
    return (
      await this.client.getJson<Guide>(`/api/v1/formats/${seg(format)}/guide`, { params: { class: cls, client } })
    ).body;
  }
}
