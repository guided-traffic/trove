import { FormatDescriptor, Repository } from '../../api/repositories-api';

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
  };
}

/** The settings members of a document (everything but the identity). */
export function settingsOf(form: RepositoryForm): Record<string, unknown> {
  return {
    description: form.description,
    adminNotes: form.adminNotes,
    includePatterns: lines(form.includePatterns),
    excludePatterns: lines(form.excludePatterns),
    disabled: form.disabled,
    requireAuthentication: form.requireAuthentication,
    hideExistence: form.hideExistence,
  };
}

/** The document a create sends. */
export function documentOf(
  cls: Repository['class'],
  format: string,
  form: RepositoryForm,
): Repository {
  return { key: form.key, class: cls, format, ...settingsOf(form) };
}

/** The client address of a repository: the base URL plus the format's `protocolBasePath`. */
export function clientAddress(
  baseUrl: string,
  format: FormatDescriptor | undefined,
  key: string,
): string {
  if (!format) return '';
  return (
    baseUrl.replace(/\/$/, '') + format.protocolBasePath.replace('{key}', encodeURIComponent(key))
  );
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
