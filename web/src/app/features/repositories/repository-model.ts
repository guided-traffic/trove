import { FormatDescriptor, Repository, RepositoryVisibility } from '../../api/repositories-api';
import { protocolPath } from '../../core/server-address';
import { JsonObject } from '../../shared/merge-patch';
import { SettingSchema, pruneDefaults } from './settings-schema';
import { EMPTY_UPSTREAM, RemoteWrite, UpstreamDefaults, UpstreamForm, upstreamOf } from './upstream-model';

/** The key pattern of R-REPO-002 (INV-5), checked in the creation form; the server decides. */
export const KEY_PATTERN = /^(?=.{2,64}$)[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$/;

/** Whether a key has the shape of R-REPO-002: the pattern, no `..`, no trailing dot. */
export function keyShapeValid(key: string): boolean {
  return KEY_PATTERN.test(key) && !key.includes('..') && !key.endsWith('.');
}

export type HideExistence = 'inherit' | 'true' | 'false';

/** `remote.mode` (R-REPO-050). */
export type RemoteMode = 'pullThrough' | 'mirror';

/**
 * The defaults of `remote.mirror` as the server resolves them (R-REPO-050, R-REPO-063): the `remote.mirror` of the document of
 * `getRepositoryDefaults`. The client keeps no copy of them; null while they are not known.
 */
export type MirrorDefaults = Readonly<Record<string, unknown>> | null;

/** The `remote.mirror` of a document of `getRepositoryDefaults` (R-REPO-063), or null when it has none. */
export function mirrorDefaultsOf(document: Readonly<Record<string, unknown>> | null | undefined): MirrorDefaults {
  const remote = document?.['remote'];
  const mirror = typeof remote === 'object' && remote !== null ? (remote as Record<string, unknown>)['mirror'] : undefined;
  return typeof mirror === 'object' && mirror !== null && !Array.isArray(mirror) ? (mirror as Record<string, unknown>) : null;
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
  /** The format's schema has a `mirror` object for the class, which the section Mirror saves (R-UI-087). */
  readonly formatMirror?: boolean;
  /** The server's defaults of `remote.mirror` (R-REPO-063), which the form leaves unwritten. */
  readonly mirrorDefaults?: MirrorDefaults;
}

/** Defaults of R-REPO-004. */
export const EMPTY_FORM: RepositoryForm = {
  key: '',
  description: '',
  adminNotes: '',
  includePatterns: '',
  excludePatterns: '',
  disabled: false,
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
    extraHosts: (r.extraHosts ?? []).map((h) => ({ pattern: h.pattern, sendCredentials: h.sendCredentials ?? false })),
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

/**
 * The members of `remote.mirror` the form writes: those set and different from the server's default (Q-UI-80). While the defaults
 * are not known every set member is written, which stores the same values.
 */
export function mirrorOf(form: RepositoryForm, defaults: MirrorDefaults = null): JsonObject {
  const out: JsonObject = {};
  for (const [name, value] of Object.entries(form.mirror)) {
    if (value === undefined || value === null) continue;
    if (defaults && JSON.stringify(value) === JSON.stringify(defaults[name] ?? null)) continue;
    out[name] = value;
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
  /** The server's defaults of `remote.mirror`, which the form leaves unwritten. */
  readonly mirrorDefaults?: MirrorDefaults;
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
      out['mirror'] = mirrorOf(form, parts.mirrorDefaults ?? null);
    } else if (form.remoteMode === 'mirror') {
      out['mode'] = 'mirror';
      const mirror = mirrorOf(form, parts.mirrorDefaults ?? null);
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
        mirrorDefaults: format.mirrorDefaults ?? null,
        ...(format.upstream ? { upstream: format.upstream } : {}),
      })
    : undefined;
  return {
    description: form.description,
    adminNotes: form.adminNotes,
    includePatterns: lines(form.includePatterns),
    excludePatterns: lines(form.excludePatterns),
    disabled: form.disabled,
    hideExistence: form.hideExistence,
    ...(format && Object.keys(member).length > 0 ? { [format.name]: member } : {}),
    ...(remote ? { remote } : {}),
  };
}

/** The members and the deployment target a virtual repository is created with (R-UI-081). */
export interface VirtualMembersOf {
  readonly members: readonly string[];
  readonly deployTarget: string | null;
}

/**
 * The document a create sends (R-UI-081, R-UI-084, Q-UI-80): the identity, and of every other member only what differs from the
 * default a create without it would store, so the server applies the rest. The project is named by the path of the request
 * (R-REPO-001); a new repository is never created disabled (R-UI-083). `visibility` is written only when the user chose a level
 * other than the defaults' (R-REPO-063).
 */
export function createBodyOf(
  cls: Repository['class'],
  format: FormatMemberOf,
  form: RepositoryForm,
  extras: { readonly visibility?: RepositoryVisibility; readonly virtual?: VirtualMembersOf } = {},
): Repository {
  const member = pruneDefaults(format.schema, form.formatMember);
  const remote = format.remote
    ? remoteOf(form, 'sparse', {
        mirror: format.mirror ?? true,
        mirrorDefaults: format.mirrorDefaults ?? null,
        ...(format.upstream ? { upstream: format.upstream } : {}),
      })
    : undefined;
  const includes = lines(form.includePatterns);
  const excludes = lines(form.excludePatterns);
  const virtual = extras.virtual;
  const withMembers = virtual !== undefined && (virtual.members.length > 0 || virtual.deployTarget !== null);
  return {
    key: form.key,
    class: cls,
    format: format.name,
    ...(form.description.trim() !== '' ? { description: form.description } : {}),
    ...(form.adminNotes.trim() !== '' ? { adminNotes: form.adminNotes } : {}),
    ...(includes.length > 0 ? { includePatterns: includes } : {}),
    ...(excludes.length > 0 ? { excludePatterns: excludes } : {}),
    ...(form.hideExistence !== 'inherit' ? { hideExistence: form.hideExistence } : {}),
    ...(Object.keys(member).length > 0 ? { [format.name]: member } : {}),
    ...(remote ? { remote } : {}),
    ...(withMembers
      ? {
          virtual: {
            members: [...virtual.members],
            ...(virtual.deployTarget !== null ? { deployTarget: virtual.deployTarget } : {}),
          },
        }
      : {}),
    ...(extras.visibility ? { visibility: extras.visibility } : {}),
  } as Repository;
}

/**
 * The client address of the repository `ref` (`<project>/<key>`): the server's base URL (ServerAddress, Q-UI-69) plus the
 * format's `protocolBasePath`. Empty while the base URL is unknown; the page's own location is
 * never a substitute.
 */
export function clientAddress(
  baseUrl: string | null | undefined,
  format: FormatDescriptor | undefined,
  ref: string,
): string {
  if (!format || !baseUrl) return '';
  return baseUrl.replace(/\/+$/, '') + protocolPath(format, ref);
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
