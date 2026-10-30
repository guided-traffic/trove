import { Injectable, inject } from '@angular/core';
import { FormatDescriptor, RepositoriesApi } from '../api/repositories-api';

/** The path part of a client address: the format's `protocolBasePath` with the key filled in. */
export function protocolPath(format: FormatDescriptor, key: string): string {
  return format.protocolBasePath.replace('{key}', encodeURIComponent(key));
}

/**
 * The public base URL of the installation (Q-UI-69, Q-UI-75). Every address the UI shows or copies
 * is built from the server's configured base URL, never from the location of the page, which
 * differs behind a proxy or on another host name. The server hands the base URL to every caller
 * that may list the formats, in the same answer that carries the format's path template, so no
 * permission is needed to show a client address. `null` means that the server has no base URL
 * configured: the caller shows no address instead of a wrong one.
 */
@Injectable({ providedIn: 'root' })
export class ServerAddress {
  private readonly repositories = inject(RepositoriesApi);

  private known: string | null = null;
  private pending: Promise<string | null> | null = null;

  /** The base URL without a trailing slash, or `null`. A `null` is not remembered. */
  baseUrl(): Promise<string | null> {
    if (this.known !== null) return Promise.resolve(this.known);
    this.pending ??= this.resolve().finally(() => {
      this.pending = null;
    });
    return this.pending;
  }

  private async resolve(): Promise<string | null> {
    try {
      const base = await this.repositories.baseUrl();
      if (base) this.known = base.replace(/\/+$/, '');
    } catch {
      return null;
    }
    return this.known;
  }
}
