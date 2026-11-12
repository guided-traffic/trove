import { FormatDescriptor, UpstreamPreset } from '../../api/repositories-api';
import { JsonObject } from '../../shared/merge-patch';

/*
 * The upstream of a remote repository as the forms hold it and write it (R-UI-021, R-UI-082, R-REPO-024, R-REPO-033): the form
 * state, the defaults a format proposes, and the members of `remote` a create, an edit and Test connection send.
 */

export type UpstreamAuthMode = 'none' | 'basic' | 'bearer';

/** One entry of `remote.extraHosts` (R-REPO-024): a host pattern and whether the repository's credential is sent to it. */
export interface UpstreamHost {
  readonly pattern: string;
  readonly sendCredentials: boolean;
}

/**
 * How the form writes the `remote` member (R-UI-021): `sparse` for a create (only what differs from the defaults, and nothing
 * in mode `pullThrough`), `structural` for the edit form (the mode and the mirror object are always present, so a merge patch
 * never replaces the whole `remote` section and members at their defaults are cleared with `null`).
 */
export type RemoteWrite = 'sparse' | 'structural';

/**
 * The upstream settings of a remote repository, in the shape the form controls hold them (R-UI-021,
 * R-REPO-024, R-REPO-033). The password is write-only: a document never returns it, so `password` holds
 * only what the user typed in this session and `passwordStored` only that the document holds one.
 */
export interface UpstreamForm {
  /** The upstream URL; empty means the format's default preset. */
  readonly url: string;
  /**
   * The further hosts (`remote.extraHosts`) once edited; null follows the URL: the hosts of the preset whose URL it is, none for
   * any other (R-REPO-062, R-UI-082). A stored repository's list is its own and is never null.
   */
  readonly extraHosts: readonly UpstreamHost[] | null;
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
  extraHosts: null,
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
  /** The format's presets, whose hosts a create of a preset's URL stores without being told (R-REPO-062). */
  readonly presets: readonly UpstreamPreset[];
}

/** The format's default upstream preset (R-REPO-062), or null for a format without presets. */
export function defaultPreset(format: FormatDescriptor | null | undefined): UpstreamPreset | null {
  return format?.upstreamPresets.find((p) => p.default) ?? null;
}

/**
 * The defaults of a format's remote: the default preset's URL and the format's cache periods. `remote` is the `remote` member
 * of `getRepositoryDefaults` (R-REPO-063) when the form has it: the server's resolved periods win over the descriptor's.
 */
export function upstreamDefaultsOf(
  format: FormatDescriptor | null | undefined,
  remote?: Readonly<Record<string, unknown>> | null,
): UpstreamDefaults {
  const cache = (remote?.['cache'] ?? null) as { metadataTtlSeconds?: number; notFoundTtlSeconds?: number } | null;
  return {
    url: defaultPreset(format)?.url ?? null,
    metadataTtlSeconds: cache?.metadataTtlSeconds ?? format?.defaultMetadataTtlSeconds ?? null,
    notFoundTtlSeconds: cache?.notFoundTtlSeconds ?? format?.defaultNotFoundTtlSeconds ?? null,
    presets: format?.upstreamPresets ?? [],
  };
}

/** The preset whose URL is url, compared as the server stores URLs; null for any other URL (R-REPO-062). */
export function presetOf(presets: readonly UpstreamPreset[], url: string | null): UpstreamPreset | null {
  if (url === null || url.trim() === '') return null;
  const n = normalizedUrl(url.trim());
  return presets.find((p) => normalizedUrl(p.url) === n) ?? null;
}

/** The hosts the server stores for url when a create names no extraHosts: its preset's, else none (Q-REPO-322). */
export function hostsOfUrl(defaults: UpstreamDefaults, url: string | null): readonly UpstreamHost[] {
  return (presetOf(defaults.presets, url)?.extraHosts ?? []).map((h) => ({
    pattern: h.pattern,
    sendCredentials: h.sendCredentials,
  }));
}

/** The further hosts the form shows and a test or an edit sends: the edited list, else the hosts that follow the URL. */
export function shownHosts(form: UpstreamForm, defaults: UpstreamDefaults): readonly UpstreamHost[] {
  return form.extraHosts ?? hostsOfUrl(defaults, effectiveUrl(form, defaults));
}

/** The hosts that receive the credential: the upstream's host and every further host with sendCredentials (R-REPO-033). */
export function credentialHosts(form: UpstreamForm, defaults: UpstreamDefaults): readonly string[] {
  const out: string[] = [];
  const url = effectiveUrl(form, defaults);
  if (url) {
    try {
      out.push(new URL(url).hostname);
    } catch {
      // not a URL yet: the server names the problem
    }
  }
  for (const h of shownHosts(form, defaults)) {
    if (h.sendCredentials && h.pattern.trim() !== '' && !out.includes(h.pattern.trim())) out.push(h.pattern.trim());
  }
  return out;
}

function sameHosts(a: readonly UpstreamHost[], b: readonly UpstreamHost[]): boolean {
  return (
    a.length === b.length &&
    a.every((h, i) => h.pattern.trim().toLowerCase() === b[i]!.pattern.trim().toLowerCase() && h.sendCredentials === b[i]!.sendCredentials)
  );
}

function hostsJson(hosts: readonly UpstreamHost[]): JsonObject[] {
  return hosts.map((h) => ({ pattern: h.pattern.trim().toLowerCase(), sendCredentials: h.sendCredentials }));
}

/** The upstream a new remote repository of the format starts with: its default preset's URL, nothing else. */
export function initialUpstream(format: FormatDescriptor | null | undefined): UpstreamForm {
  const url = defaultPreset(format)?.url;
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

/** The URL the repository would use: the entered one, else the format's default preset, else none. */
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
  // A create names the further hosts only where they differ from what the server stores for the URL without them (R-UI-082,
  // Q-REPO-322); the settings page names them always, so that its patch keeps the list as shown.
  const hosts = shownHosts(form, defaults);
  if (write === 'structural' || !sameHosts(hosts, hostsOfUrl(defaults, effectiveUrl(form, defaults)))) {
    out['extraHosts'] = hostsJson(hosts);
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
