import { Injectable, inject } from '@angular/core';
import { FormatDescriptor, RepositoriesApi } from '../api/repositories-api';
import { SystemApi } from '../api/system-api';
import { SessionStore } from './session-store';

/** The path part of a client address: the format's `protocolBasePath` with the key filled in. */
export function protocolPath(format: FormatDescriptor, key: string): string {
  return format.protocolBasePath.replace('{key}', encodeURIComponent(key));
}

/**
 * The public base URL of the installation (Q-UI-69). Every address the UI shows or copies is built
 * from the server's configured base URL, never from the location of the page, which differs behind
 * a proxy or on another host name. A holder of `settings:read` asks the installation facts; any other
 * caller reads the base URL back from the absolute `addresses` of a repository it may see, which
 * the server builds on the same URL. `null` means that neither source answered (no readable
 * repository yet): the caller shows no address instead of a wrong one.
 */
@Injectable({ providedIn: 'root' })
export class ServerAddress {
  private readonly session = inject(SessionStore);
  private readonly system = inject(SystemApi);
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
    await this.session.load();
    let base: string | null = null;
    if (this.session.has('settings:read')) {
      base = await this.fromSystemInfo();
    }
    base ??= await this.fromAddresses();
    if (base !== null) this.known = base.replace(/\/+$/, '');
    return this.known;
  }

  private async fromSystemInfo(): Promise<string | null> {
    try {
      return (await this.system.info()).baseUrl || null;
    } catch {
      return null;
    }
  }

  /** Strips the format's path from a repository address; the rest is the base URL. */
  private async fromAddresses(): Promise<string | null> {
    try {
      const [formats, repos] = await Promise.all([
        this.repositories.formats(),
        this.repositories.listAll(),
      ]);
      for (const repo of repos) {
        const format = formats.find((f) => f.name === repo.format);
        if (!format) continue;
        const suffix = protocolPath(format, repo.key);
        const address = repo.addresses.find((a) => a.endsWith(suffix));
        if (address) return address.slice(0, address.length - suffix.length);
      }
    } catch {
      return null;
    }
    return null;
  }
}
