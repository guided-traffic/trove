import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import { seg } from './query';
import { Schema } from './types';

/* The manual of the running version: R-API-054, R-UI-050. */

export type ManualContents = Schema<'ManualContents'>;
export type ManualSection = Schema<'ManualSection'>;
export type ManualPage = Schema<'ManualPage'>;

/** The contents and the pages of the manual. Every caller reads the same text, so both answers are cached per tab. */
export abstract class ManualApi {
  abstract contents(): Promise<ManualContents>;
  abstract page(section: string, page: string): Promise<ManualPage>;
}

@Injectable()
export class HttpManualApi extends ManualApi {
  private readonly client = inject(ApiClient);
  // The manual is the binary's and the same for every caller, so a read is kept for the tab; the
  // page title of a route and the page's screen share one request. A failed read is not kept.
  private readonly cache = new Map<string, Promise<unknown>>();

  override contents(): Promise<ManualContents> {
    return this.once('', () => this.client.getJson<ManualContents>('/api/v1/manual'));
  }

  override page(section: string, page: string): Promise<ManualPage> {
    return this.once(`${section}/${page}`, () =>
      this.client.getJson<ManualPage>(`/api/v1/manual/${seg(section)}/${seg(page)}`),
    );
  }

  private once<T>(key: string, read: () => Promise<{ body: T }>): Promise<T> {
    let p = this.cache.get(key) as Promise<T> | undefined;
    if (!p) {
      p = read().then(
        (r) => r.body,
        (e: unknown) => {
          this.cache.delete(key);
          throw e;
        },
      );
      this.cache.set(key, p);
    }
    return p;
  }
}
