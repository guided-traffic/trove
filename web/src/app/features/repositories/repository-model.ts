import { FormatDescriptor, Repository } from '../../api/repositories-api';
import { protocolPath } from '../../core/server-address';
import { JsonObject } from '../../shared/merge-patch';
import { SettingSchema, pruneDefaults } from './settings-schema';

/** The key pattern of R-REPO-002 (INV-5), checked live in the wizard; the server decides. */
export const KEY_PATTERN = /^[a-z0-9][a-z0-9._-]{1,63}$/;

export type HideExistence = 'inherit' | 'true' | 'false';

/** `remote.mode` (R-REPO-050). */
export type RemoteMode = 'pullThrough' | 'mirror';

/**
 * The defaults of `remote.mirror` (R-REPO-050, `MirrorSettings` of the API). The form writes a member
 * only where it differs from these (Q-UI-80); a `null` default means "no value".
 */
export const MIRROR_DEFAULTS: Readonly<Record<string, unknown>> = {
  paused: false,
  intervalSeconds: 3600,
  fullPassIntervalSeconds: 604800,
  discovery: 'auto',
  concurrency: 4,
  maxVersionsPerPackage: 0,
  includePrereleases: true,
  maxBytes: null,
};

export type UpstreamAuthMode = 'none' | 'basic' | 'bearer';

/**
 * The upstream settings of a remote repository, in the shape the form controls hold them (R-UI-021,
 * R-REPO-024, R-REPO-033). The password is write-only: a document never returns it, so `password` holds
 * only what the user typed in this session and `passwordStored` only that the document holds one.
 */
export interface UpstreamForm {
  /** The upstream URL; empty means the format's well-known upstream. */
  readonly url: string;
  readonly authMode: UpstreamAuthMode;
  readonly username: string;
  /** What the user typed; sent as `{"value": ...}` and never read back (R-REPO-033). */
  readonly password: string;
  /** The document holds a sealed value (`{"set": true}`) or a reference. */
  readonly passwordStored: 'none' | 'sealed' | 'reference';
  /** Remove the stored password (`"password": null`). */
  readonly clearPassword: boolean;
  /** `remote.cache.metadataTtlSeconds`; `null` follows the format's default. A text that is no number stays text for the server to reject. */
  readonly metadataTtl: number | string | null;
  readonly notFoundTtl: number | string | null;
  /** `remote.availability.mode: offline` (INV-11, R-REPO-030). */
  readonly offline: boolean;
}

export const EMPTY_UPSTREAM: UpstreamForm = {
  url: '',
  authMode: 'none',
  username: '',
  password: '',
  passwordStored: 'none',
  clearPassword: false,
  metadataTtl: null,
  notFoundTtl: null,
  offline: false,
};

/** What a format proposes for its remote repositories (`FormatDescriptor`): the values a form leaves unwritten. */
export interface UpstreamDefaults {
  readonly url: string | null;
  readonly metadataTtlSeconds: number | null;
  readonly notFoundTtlSeconds: number | null;
}

export function upstreamDefaultsOf(format: FormatDescriptor | null | undefined): UpstreamDefaults {
  return {
    url: format?.defaultUpstreamUrl ?? null,
    metadataTtlSeconds: format?.defaultMetadataTtlSeconds ?? null,
    notFoundTtlSeconds: format?.defaultNotFoundTtlSeconds ?? null,
  };
}

/** The upstream a new remote repository of the format starts with: its preset URL, nothing else. */
export function initialUpstream(format: FormatDescriptor | null | undefined): UpstreamForm {
  const url = format?.defaultUpstreamUrl;
  return url ? { ...EMPTY_UPSTREAM, url } : EMPTY_UPSTREAM;
}

/** `https://Host:443/path` and `https://host/path` name the same upstream: compare as the server stores it. */
export function normalizedUrl(url: string): string {
  try {
    const u = new URL(url);
    return `${u.protocol}//${u.host}${u.pathname === '' ? '/' : u.pathname}${u.search}`;
  } catch {
    return url;
  }
}

/** The URL the repository would use: the entered one, else the format's well-known upstream, else none. */
export function effectiveUrl(upstream: UpstreamForm, defaults: UpstreamDefaults): string | null {
  const entered = upstream.url.trim();
  return entered !== '' ? entered : defaults.url;
}

function sameNumber(value: number | string | null, fallback: number | null): boolean {
  return value === null || value === '' || value === fallback;
}

/**
 * The members of `remote` that the upstream settings write (R-UI-021): only what differs from its
 * default (Q-UI-80). The URL is always written when entered on the edit form (`structural`), so a
 * patch never relies on how the server reads a removed URL. A secret is written only when typed, or
 * removed on request; an unchanged stored password is not part of any request (R-REPO-033).
 */
export function upstreamOf(
  form: UpstreamForm,
  defaults: UpstreamDefaults,
  write: RemoteWrite,
): JsonObject {
  const out: JsonObject = {};
  const url = form.url.trim();
  if (
    url !== '' &&
    (write === 'structural' || defaults.url === null || normalizedUrl(url) !== normalizedUrl(defaults.url))
  ) {
    out['url'] = url;
  }
  if (form.authMode !== 'none') {
    out['authMode'] = form.authMode;
    if (form.authMode === 'basic' && form.username.trim() !== '') out['username'] = form.username.trim();
    if (form.password !== '') out['password'] = { value: form.password };
    else if (form.clearPassword && form.passwordStored !== 'none') out['password'] = null;
  }
  const cache: JsonObject = {};
  if (!sameNumber(form.metadataTtl, defaults.metadataTtlSeconds)) cache['metadataTtlSeconds'] = form.metadataTtl;
  if (!sameNumber(form.notFoundTtl, defaults.notFoundTtlSeconds)) cache['notFoundTtlSeconds'] = form.notFoundTtl;
  // The edit form names both objects even when empty, so that a member set back to its default is
  // cleared with `null` inside them and the patch never nulls the whole object, which holds members
  // the form does not edit (`storeArtifacts`, `failureThreshold`, ...).
  if (write === 'structural' || Object.keys(cache).length > 0) out['cache'] = cache;
  if (form.offline) out['availability'] = { mode: 'offline' };
  else if (write === 'structural') out['availability'] = {};
  return out;
}

/**
 * The body of `POST /api/v1/upstream-tests` for the form: the format and the unsaved `remote` section,
 * with the URL the repository would use. The password is the typed one; a stored one is not part of an
 * unsaved test (R-REPO-024).
 */
export function upstreamTestBody(
  form: UpstreamForm,
  format: FormatDescriptor,
): { format: string; remote: JsonObject } {
  const defaults = upstreamDefaultsOf(format);
  const remote = upstreamOf(form, defaults, 'structural');
  const url = effectiveUrl(form, defaults);
  if (url !== null) remote['url'] = url;
  if (remote['password'] === null) delete remote['password'];
  for (const name of ['cache', 'availability']) {
    const member = remote[name] as JsonObject | undefined;
    if (member && Object.keys(member).length === 0) delete remote[name];
  }
  return { format: format.name, remote };
}

/** The request as shown on screen: a typed password is replaced, never displayed (R-UI-043). */
export function maskSecrets<T>(value: T): T {
  if (Array.isArray(value)) return value.map((v) => maskSecrets(v)) as T;
  if (typeof value !== 'object' || value === null) return value;
  const out: Record<string, unknown> = {};
  for (const [name, v] of Object.entries(value)) {
    out[name] =
      name === 'password' && typeof v === 'object' && v !== null && 'value' in v
        ? { value: '<the password>' }
        : maskSecrets(v);
  }
  return out as T;
}

/** The editable members of a repository document, in the shape the form controls hold them. */
export interface RepositoryForm {
  readonly key: string;
  readonly description: string;
  readonly adminNotes: string;
  /** One pattern per line. */
  readonly includePatterns: string;
  readonly excludePatterns: string;
  readonly disabled: boolean;
  readonly requireAuthentication: boolean;
  readonly hideExistence: HideExistence;
  /** The content of the member named after the format (`npm`), edited by the schema-rendered form. */
  readonly formatMember: JsonObject;
  /** `remote.mode`; only a remote repository of a format that can mirror edits it. */
  readonly remoteMode: RemoteMode;
  /** The members of `remote.mirror` that were set (R-REPO-050); an unset member follows its default. */
  readonly mirror: JsonObject;
  /** The upstream settings of a remote repository (R-UI-021). */
  readonly upstream: UpstreamForm;
}

/**
 * How the form writes the `remote` member of the mirror section (R-UI-021): `sparse` for a create
 * (only what differs from the defaults, and nothing in mode `pullThrough`), `structural` for the edit
 * form (the mode and the mirror object are always present, so a merge patch never replaces the whole
 * `remote` section and members at their defaults are cleared with `null`).
 */
export type RemoteWrite = 'sparse' | 'structural';

/** The format member of a document: its name and the schema that says which values are defaults. */
export interface FormatMemberOf {
  readonly name: string;
  readonly schema: SettingSchema | null | undefined;
  /** Set when the form edits the `remote` section (a remote repository). */
  readonly remote?: RemoteWrite;
  /** Whether the form edits `remote.mode` and `remote.mirror` (a format with `mirror: true`); true when `remote` is set and this is not. */
  readonly mirror?: boolean;
  /** Set when the form edits the upstream settings; the defaults say which values are not written. */
  readonly upstream?: UpstreamDefaults;
}

/** Defaults of R-REPO-004. */
export const EMPTY_FORM: RepositoryForm = {
  key: '',
  description: '',
  adminNotes: '',
  includePatterns: '',
  excludePatterns: '',
  disabled: false,
  requireAuthentication: false,
  hideExistence: 'inherit',
  formatMember: {},
  remoteMode: 'pullThrough',
  mirror: {},
  upstream: EMPTY_UPSTREAM,
};

export function lines(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l.length > 0);
}

export function formOf(repo: Repository): RepositoryForm {
  return {
    key: repo.key,
    description: repo.description ?? '',
    adminNotes: repo.adminNotes ?? '',
    includePatterns: (repo.includePatterns ?? []).join('\n'),
    excludePatterns: (repo.excludePatterns ?? []).join('\n'),
    disabled: repo.disabled ?? false,
    requireAuthentication: repo.requireAuthentication ?? false,
    hideExistence: repo.hideExistence ?? 'inherit',
    formatMember: memberOf(repo),
    remoteMode: repo.remote?.mode === 'mirror' ? 'mirror' : 'pullThrough',
    mirror: { ...(repo.remote?.mirror ?? {}) } as JsonObject,
    upstream: upstreamFormOf(repo),
  };
}

function upstreamFormOf(repo: Repository): UpstreamForm {
  const r = repo.remote;
  if (!r) return EMPTY_UPSTREAM;
  const password = r.password as { set?: boolean; secretRef?: unknown } | null | undefined;
  return {
    url: r.url ?? '',
    authMode: r.authMode ?? 'none',
    username: r.username ?? '',
    password: '',
    passwordStored: password?.secretRef ? 'reference' : password?.set ? 'sealed' : 'none',
    clearPassword: false,
    metadataTtl: r.cache?.metadataTtlSeconds ?? null,
    notFoundTtl: r.cache?.notFoundTtlSeconds ?? null,
    offline: r.availability?.mode === 'offline',
  };
}

/** The members of `remote.mirror` that differ from their defaults. */
export function mirrorOf(form: RepositoryForm): JsonObject {
  const out: JsonObject = {};
  for (const [name, fallback] of Object.entries(MIRROR_DEFAULTS)) {
    const value = form.mirror[name];
    if (value === undefined || value === null) continue;
    if (JSON.stringify(value) !== JSON.stringify(fallback)) out[name] = value;
  }
  return out;
}

/** Whether the mirror part of the form is as a new repository has it. */
export function sameMirror(form: RepositoryForm): boolean {
  return form.remoteMode === 'pullThrough' && Object.keys(form.mirror).length === 0;
}

/** What of `remote` a form edits: the mirror section (default) and the upstream settings (when given). */
export interface RemoteParts {
  readonly mirror?: boolean;
  readonly upstream?: UpstreamDefaults;
}

/** The `remote` member the form writes, or undefined when a create has nothing to write. */
export function remoteOf(
  form: RepositoryForm,
  write: RemoteWrite,
  parts: RemoteParts = {},
): JsonObject | undefined {
  const out: JsonObject = parts.upstream ? upstreamOf(form.upstream, parts.upstream, write) : {};
  if (parts.mirror ?? true) {
    if (write === 'structural') {
      out['mode'] = form.remoteMode;
      out['mirror'] = mirrorOf(form);
    } else if (form.remoteMode === 'mirror') {
      out['mode'] = 'mirror';
      const mirror = mirrorOf(form);
      if (Object.keys(mirror).length > 0) out['mirror'] = mirror;
    }
  }
  return write === 'structural' || Object.keys(out).length > 0 ? out : undefined;
}

function memberOf(repo: Repository): JsonObject {
  const raw = (repo as unknown as JsonObject)[repo.format];
  return typeof raw === 'object' && raw !== null && !Array.isArray(raw) ? (raw as JsonObject) : {};
}

/**
 * The settings members of a document (everything but the identity). The format member carries only
 * the settings that differ from the schema's defaults and is left out when none does (Q-UI-80).
 */
export function settingsOf(form: RepositoryForm, format?: FormatMemberOf): Record<string, unknown> {
  const member = format ? pruneDefaults(format.schema, form.formatMember) : {};
  const remote = format?.remote
    ? remoteOf(form, format.remote, {
        mirror: format.mirror ?? true,
        ...(format.upstream ? { upstream: format.upstream } : {}),
      })
    : undefined;
  return {
    description: form.description,
    adminNotes: form.adminNotes,
    includePatterns: lines(form.includePatterns),
    excludePatterns: lines(form.excludePatterns),
    disabled: form.disabled,
    requireAuthentication: form.requireAuthentication,
    hideExistence: form.hideExistence,
    ...(format && Object.keys(member).length > 0 ? { [format.name]: member } : {}),
    ...(remote ? { remote } : {}),
  };
}

/** The document a create sends; `project` assigns the repository to a project in the same transaction (R-AUTHZ-026). */
export function documentOf(
  cls: Repository['class'],
  format: FormatMemberOf,
  form: RepositoryForm,
  project?: string | null,
): Repository {
  return {
    key: form.key,
    class: cls,
    format: format.name,
    ...(project ? { project } : {}),
    ...settingsOf(form, format),
  };
}

/**
 * The client address of a repository: the server's base URL (ServerAddress, Q-UI-69) plus the
 * format's `protocolBasePath`. Empty while the base URL is unknown; the page's own location is
 * never a substitute.
 */
export function clientAddress(
  baseUrl: string | null | undefined,
  format: FormatDescriptor | undefined,
  key: string,
): string {
  if (!format || !baseUrl) return '';
  return baseUrl.replace(/\/+$/, '') + protocolPath(format, key);
}

/** The management API address of a path, or the path with a marker while the base URL is unknown. */
export function apiAddress(baseUrl: string | null | undefined, path: string): string {
  return (baseUrl ? baseUrl.replace(/\/+$/, '') : '<base URL>') + path;
}

/** The `curl` command that sends the same request (INV-17). */
export function curlOf(
  method: 'POST' | 'PATCH',
  url: string,
  body: unknown,
  extraHeaders: readonly string[] = [],
): string {
  const contentType = method === 'PATCH' ? 'application/merge-patch+json' : 'application/json';
  const headers = [`Content-Type: ${contentType}`, ...extraHeaders];
  const quoted = JSON.stringify(body, null, 2).replace(/'/g, `'\\''`);
  return [
    `curl -X ${method} '${url}'`,
    `  -H 'Authorization: Bearer $TROVE_TOKEN'`,
    ...headers.map((h) => `  -H '${h}'`),
    `  --data '${quoted}'`,
  ].join(' \\\n');
}
