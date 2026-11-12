import { JsonObject, mergePatchOf } from '../../shared/merge-patch';
import { FormatMemberOf, RepositoryForm, settingsOf } from './repository-model';

/**
 * The sections of the repository's settings page that save members of the document (R-UI-087, Q-UI-351), each with its own
 * **Save changes**: the top (the description), the upstream of a remote, the mirror, the format's settings, the access details and
 * the administrator notes. The visibility and the members save through their own blocks.
 */
export type SettingsSection = 'general' | 'upstream' | 'mirror' | 'format' | 'access' | 'notes';

export const SETTINGS_SECTIONS: readonly SettingsSection[] = ['general', 'upstream', 'mirror', 'format', 'access', 'notes'];

function isObject(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function without(value: unknown, ...names: string[]): JsonObject {
  const out: JsonObject = isObject(value) ? { ...value } : {};
  for (const name of names) delete out[name];
  return out;
}

/**
 * The members of the settings document that `section` saves, in the shape `settingsOf` writes them. The format's member is split:
 * its `mirror` object belongs to the section Mirror, the rest to the format's section; a part that is empty while the member holds
 * no `mirror` is left out, so that emptying it removes the member, and a part beside a `mirror` stays an object, so that a patch
 * never removes what the other section holds.
 */
export function sectionMembers(section: SettingsSection, form: RepositoryForm, format: FormatMemberOf): JsonObject {
  const s = settingsOf(form, format) as JsonObject;
  const member = s[format.name];
  const remote = s['remote'];
  switch (section) {
    case 'general':
      return { description: s['description'] ?? '' };
    case 'notes':
      return { adminNotes: s['adminNotes'] ?? '' };
    case 'access':
      return {
        includePatterns: s['includePatterns'] ?? [],
        excludePatterns: s['excludePatterns'] ?? [],
        hideExistence: s['hideExistence'] ?? 'inherit',
      };
    case 'upstream':
      return isObject(remote) ? { remote: without(remote, 'mode', 'mirror') } : {};
    case 'mirror': {
      const out: JsonObject = {};
      if (isObject(remote) && 'mode' in remote) out['remote'] = { mode: remote['mode'], mirror: remote['mirror'] ?? {} };
      if (format.formatMirror) out[format.name] = { mirror: isObject(member) && isObject(member['mirror']) ? member['mirror'] : {} };
      return out;
    }
    case 'format': {
      if (format.name === '') return {};
      const rest = without(member, 'mirror');
      const holdsMirror = isObject(member) && member['mirror'] !== undefined;
      return Object.keys(rest).length === 0 && !holdsMirror ? {} : { [format.name]: rest };
    }
  }
}

/** The merge patch that saves `section`: only its members that changed between the stored form and the edited one. */
export function sectionPatch(section: SettingsSection, base: RepositoryForm, now: RepositoryForm, format: FormatMemberOf): JsonObject {
  return mergePatchOf(sectionMembers(section, base, format), sectionMembers(section, now, format));
}

/** `into` with the fields of `section` taken from `from`. */
export function withSection(section: SettingsSection, from: RepositoryForm, into: RepositoryForm): RepositoryForm {
  switch (section) {
    case 'general':
      return { ...into, description: from.description };
    case 'notes':
      return { ...into, adminNotes: from.adminNotes };
    case 'access':
      return {
        ...into,
        includePatterns: from.includePatterns,
        excludePatterns: from.excludePatterns,
        hideExistence: from.hideExistence,
      };
    case 'upstream':
      return { ...into, upstream: from.upstream };
    case 'mirror': {
      const formatMember = without(into.formatMember, 'mirror');
      if (from.formatMember['mirror'] !== undefined) formatMember['mirror'] = from.formatMember['mirror'];
      return { ...into, remoteMode: from.remoteMode, mirror: from.mirror, formatMember };
    }
    case 'format': {
      const formatMember = without(from.formatMember, 'mirror');
      if (into.formatMember['mirror'] !== undefined) formatMember['mirror'] = into.formatMember['mirror'];
      return { ...into, formatMember };
    }
  }
}

/**
 * The form after a write stored `next`: every section takes the stored values, except the sections the user has unsaved edits in
 * (against `before`), which keep them, and the section that was just saved, which takes the stored values whatever it held.
 */
export function rebaseForm(
  before: RepositoryForm,
  current: RepositoryForm,
  next: RepositoryForm,
  format: FormatMemberOf,
  saved: SettingsSection | null = null,
): RepositoryForm {
  let out = next;
  for (const section of SETTINGS_SECTIONS) {
    if (section === saved) continue;
    if (Object.keys(sectionPatch(section, before, current, format)).length > 0) out = withSection(section, current, out);
  }
  return out;
}
