import { FormatDescriptor, Repository } from '../../api/repositories-api';
import { protocolPath } from '../../core/server-address';
import { JsonObject } from '../../shared/merge-patch';
import { SettingSchema, pruneDefaults } from './settings-schema';

/** The key pattern of R-REPO-002 (INV-5), checked live in the wizard; the server decides. */
export const KEY_PATTERN = /^[a-z0-9][a-z0-9._-]{1,63}$/;

export type HideExistence = 'inherit' | 'true' | 'false';

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
}

/** The format member of a document: its name and the schema that says which values are defaults. */
export interface FormatMemberOf {
  readonly name: string;
  readonly schema: SettingSchema | null | undefined;
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
  };
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
  return {
    description: form.description,
    adminNotes: form.adminNotes,
    includePatterns: lines(form.includePatterns),
    excludePatterns: lines(form.excludePatterns),
    disabled: form.disabled,
    requireAuthentication: form.requireAuthentication,
    hideExistence: form.hideExistence,
    ...(format && Object.keys(member).length > 0 ? { [format.name]: member } : {}),
  };
}

/** The document a create sends. */
export function documentOf(
  cls: Repository['class'],
  format: FormatMemberOf,
  form: RepositoryForm,
): Repository {
  return { key: form.key, class: cls, format: format.name, ...settingsOf(form, format) };
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
