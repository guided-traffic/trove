import { Injectable, inject } from '@angular/core';
import { ApiClient } from './client';
import type { operations } from './generated/schema';
import { Schema } from './types';

/** The answer of `GET /api/v1/search/suggestions` (R-API-060): one member per kind asked. */
export type SearchSuggestions = Schema<'SearchSuggestions'>;
export type RepositorySuggestion = Schema<'RepositorySuggestion'>;
export type PackageSuggestion = Schema<'PackageSuggestion'>;
export type ProjectSuggestion = Schema<'ProjectSuggestion'>;
export type UserSuggestion = Schema<'UserSuggestion'>;
export type GroupSuggestion = Schema<'GroupSuggestion'>;

/** A kind of the suggestions: `user` and `group` need `identities:read`, else the answer is 403 (Q-API-272). */
export type SuggestionKind = NonNullable<
  operations['getSearchSuggestions']['parameters']['query']['kind']
>[number];

/** What the search field asks for one settled text (R-UI-097). */
export interface SuggestRequest {
  /** The text; the server trims it and answers 400 below 2 or above 100 characters. */
  readonly q: string;
  /** The kinds of the filter; the server's default `repository,package,project` when absent or empty. */
  readonly kinds?: readonly SuggestionKind[];
  /** Suggestions per kind, 1 to 10; the server's default is 5. */
  readonly limit?: number;
  /** Cancels the request when a newer text supersedes it; the promise then rejects with an `AbortError`. */
  readonly signal?: AbortSignal;
}

/** The suggestions of the top bar's search field: R-API-060 to R-API-064, R-UI-097. */
export abstract class SearchApi {
  /** `GET /api/v1/search/suggestions?q=&kind=&limit=`: one request for every kind of the filter. */
  abstract suggest(request: SuggestRequest): Promise<SearchSuggestions>;
}

@Injectable()
export class HttpSearchApi extends SearchApi {
  private readonly client = inject(ApiClient);

  async suggest(request: SuggestRequest): Promise<SearchSuggestions> {
    const response = await this.client.get('/api/v1/search/suggestions', {
      params: {
        q: request.q,
        kind: request.kinds?.length ? request.kinds.join(',') : undefined,
        limit: request.limit,
      },
      signal: request.signal,
      // The field shows a 429's notice itself and waits for Retry-After (R-UI-097): no automatic retry.
      noRetry: true,
    });
    return response.body;
  }
}
