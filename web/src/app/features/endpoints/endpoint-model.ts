import { EndpointConfiguration, ExposureRule } from '../../api/endpoints-api';
import { isFilesAddress } from '../../api/files-api';
import { FormatDescriptor } from '../../api/repositories-api';
import { lines } from '../repositories/repository-model';

/** The editable members of an exposure rule, in the shape the form controls hold them. */
export interface RuleForm {
  readonly name: string;
  readonly description: string;
  readonly repository: string;
  readonly enabled: boolean;
  /** One pattern per line in the `*` fields. */
  readonly packagesInclude: string;
  readonly packagesExclude: string;
  readonly pathsInclude: string;
  readonly pathsExclude: string;
  readonly versionsInclude: string;
  readonly versionsExclude: string;
  readonly range: string;
  readonly prereleases: boolean;
  readonly tagsInclude: string;
  readonly tagsExclude: string;
}

export const EMPTY_RULE_FORM: RuleForm = {
  name: '',
  description: '',
  repository: '',
  enabled: true,
  packagesInclude: '',
  packagesExclude: '',
  pathsInclude: '',
  pathsExclude: '',
  versionsInclude: '',
  versionsExclude: '',
  range: '',
  prereleases: false,
  tagsInclude: '',
  tagsExclude: '',
};

/** The rule name syntax of R-EP-012. */
export const RULE_NAME_PATTERN = /^[a-z0-9][a-z0-9._-]{0,63}$/;

export function ruleFormOf(rule: ExposureRule): RuleForm {
  const text = (list: readonly string[] | undefined): string => (list ?? []).join('\n');
  return {
    name: rule.name,
    description: rule.description ?? '',
    repository: rule.repository,
    enabled: rule.enabled,
    packagesInclude: text(rule.packages?.include),
    packagesExclude: text(rule.packages?.exclude),
    pathsInclude: text(rule.paths?.include),
    pathsExclude: text(rule.paths?.exclude),
    versionsInclude: text(rule.versions?.include),
    versionsExclude: text(rule.versions?.exclude),
    range: rule.versions?.range ?? '',
    prereleases: rule.prereleases,
    tagsInclude: text(rule.distTags?.include),
    tagsExclude: text(rule.distTags?.exclude),
  };
}

/** The rule document that a create, a replace and the dry run send. */
export function ruleOf(form: RuleForm): ExposureRule {
  return {
    name: form.name,
    description: form.description,
    repository: form.repository,
    enabled: form.enabled,
    packages: { include: lines(form.packagesInclude), exclude: lines(form.packagesExclude) },
    paths: { include: lines(form.pathsInclude), exclude: lines(form.pathsExclude) },
    versions: {
      include: lines(form.versionsInclude),
      exclude: lines(form.versionsExclude),
      range: form.range.trim() === '' ? null : form.range.trim(),
    },
    prereleases: form.prereleases,
    distTags: { include: lines(form.tagsInclude), exclude: lines(form.tagsExclude) },
  };
}

/** The members of a rule as flat, comparable fields for the diff and the conflict dialog. */
export function flatRule(rule: ExposureRule): Record<string, unknown> {
  return {
    description: rule.description ?? '',
    enabled: rule.enabled,
    'packages.include': rule.packages?.include ?? [],
    'packages.exclude': rule.packages?.exclude ?? [],
    'paths.include': rule.paths?.include ?? [],
    'paths.exclude': rule.paths?.exclude ?? [],
    'versions.include': rule.versions?.include ?? [],
    'versions.exclude': rule.versions?.exclude ?? [],
    'versions.range': rule.versions?.range ?? null,
    prereleases: rule.prereleases,
    'distTags.include': rule.distTags?.include ?? [],
    'distTags.exclude': rule.distTags?.exclude ?? [],
  };
}

/** The route family a format's client address belongs to, to tell whether an endpoint serves it. */
export function familyOfAddress(protocolBasePath: string): 'files' | 'packages' {
  return isFilesAddress(protocolBasePath) ? 'files' : 'packages';
}

export interface ClientAddress {
  readonly url: string;
  /** False when the endpoint's surface does not include the family of the address. */
  readonly served: boolean;
}

/** The client address of a repository on an endpoint: its base URL plus the format's path (R-EP-017). */
export function endpointAddress(
  configuration: EndpointConfiguration,
  format: FormatDescriptor | undefined,
  key: string,
): ClientAddress | null {
  if (!format) return null;
  const template = format.protocolBasePath;
  return {
    url:
      configuration.baseUrl.replace(/\/$/, '') + template.replace('{key}', encodeURIComponent(key)),
    served: configuration.surface.includes(familyOfAddress(template)),
  };
}

/** A one-line summary of a rule's pattern dimensions for the table. */
export function patternSummary(rule: ExposureRule): string {
  const parts: string[] = [];
  const add = (
    label: string,
    include: readonly string[] | undefined,
    exclude: readonly string[] | undefined,
  ): void => {
    if (include?.length) parts.push(`${label}: ${include.join(', ')}`);
    if (exclude?.length) parts.push(`${label} ≠ ${exclude.join(', ')}`);
  };
  add('packages', rule.packages?.include, rule.packages?.exclude);
  add('paths', rule.paths?.include, rule.paths?.exclude);
  return parts.join('; ');
}

/** A one-line summary of the version dimension. */
export function versionSummary(rule: ExposureRule): string {
  const v = rule.versions;
  const parts: string[] = [];
  if (v?.range) parts.push(v.range);
  if (v?.include?.length) parts.push(`+ ${v.include.join(', ')}`);
  if (v?.exclude?.length) parts.push(`− ${v.exclude.join(', ')}`);
  return parts.join(' ');
}
