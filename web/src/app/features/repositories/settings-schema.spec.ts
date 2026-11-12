import { DEB_SETTINGS_SCHEMA, RICH_SETTINGS_SCHEMA } from '../../api/testing/fakes-catalog';
import {
  SettingSchema,
  errorTarget,
  numberOrText,
  pruneDefaults,
  settingEntries,
  settingNodes,
  targetOf,
  valueAt,
  widthOf,
  withValue,
} from './settings-schema';

const SCHEMA = RICH_SETTINGS_SCHEMA as SettingSchema;

describe('Settings schema model', () => {
  it('Q-UI-335 a member takes the field width of its kind: a period a duration, a size bytes, a count a number, a URL or a long string the column, any other string a name', () => {
    expect(widthOf('metadataTtlSeconds', { type: 'integer' }, 'number')).toBe('duration');
    expect(widthOf('timeout', { type: 'string' }, 'text')).toBe('duration');
    expect(widthOf('maxBodySize', { type: 'string' }, 'text')).toBe('bytes');
    expect(widthOf('maxBytes', { type: 'integer' }, 'number')).toBe('bytes');
    expect(widthOf('concurrency', { type: 'integer' }, 'number')).toBe('number');
    expect(widthOf('providerURL', { type: 'string', format: 'uri' }, 'text')).toBe('full');
    expect(widthOf('authUrl', { type: 'string', maxLength: 2048 }, 'text')).toBe('full');
    expect(widthOf('authUrl', { type: 'string', pattern: '^https?://' }, 'text')).toBe('full');
    const [url] = settingNodes({ type: 'object', properties: { providerURL: { type: ['string', 'null'], format: 'uri', maxLength: 2048 } } }, 'remote', 'npm');
    expect(url).toMatchObject({ kind: 'text', url: true, multiline: false, width: 'full' });
    expect(widthOf('origin', { type: 'string', maxLength: 64 }, 'text')).toBe('name');
    const nodes = settingNodes(RICH_SETTINGS_SCHEMA, 'remote', 'npm');
    const lines = nodes.flatMap(function flat(n): { kind: string; width?: string }[] {
      return n.kind === 'group' ? n.children.flatMap(flat) : [n];
    });
    expect(lines.filter((n) => n.kind === 'text' || n.kind === 'number').every((n) => typeof n.width === 'string')).toBe(true);
  });

  it('R-UI-021 R-ARCH-004 only the members whose x-trove-classes contain the class are rendered, each by its type, in schema order', () => {
    expect(settingNodes(SCHEMA, 'local', 'npm').map((n) => [n.kind, n.pointer])).toEqual([
      ['boolean', '/npm/latestGuard'],
    ]);
    expect(settingNodes(SCHEMA, 'virtual', 'npm').map((n) => [n.kind, n.name])).toEqual([
      ['choice', 'latestStrategy'],
    ]);

    const remote = settingNodes(SCHEMA, 'remote', 'npm');
    expect(remote.map((n) => n.name)).toEqual(['serveRemovedVersions', 'audit']);
    const audit = remote[1];
    if (audit?.kind !== 'group') throw new Error('audit is no group');
    expect(audit.id).toBe('f-npm-audit');
    expect(audit.description).toBe('The security audit.');
    expect(audit.children.map((n) => [n.name, n.kind, n.id, n.label])).toEqual([
      ['mode', 'choice', 'f-npm-audit-mode', 'mode'],
      ['providerURL', 'text', 'f-npm-audit-providerURL', 'providerURL'],
      ['timeout', 'text', 'f-npm-audit-timeout', 'timeout'],
      ['maxBodySize', 'text', 'f-npm-audit-maxBodySize', 'maxBodySize'],
      ['retries', 'number', 'f-npm-audit-retries', 'Retries'],
      ['hosts', 'other', 'f-npm-audit-hosts', 'hosts'],
    ]);
    expect(audit.children[0]).toMatchObject({ options: ['forward', 'disabled'], labels: ['Forward to an advisory service', 'disabled'] });
    expect(audit.children[1]).toMatchObject({
      url: true,
      fallback: null,
      path: ['audit', 'providerURL'],
    });
    expect(audit.children[4]).toMatchObject({ integer: true, fallback: 2 });
  });

  it('R-UI-021 a format without a schema, and an object whose members all belong to other classes, render nothing', () => {
    expect(settingNodes(null, 'local', 'generic')).toEqual([]);
    const onlyRemote = {
      type: 'object',
      properties: {
        audit: {
          type: 'object',
          properties: { mode: { type: 'string', 'x-trove-classes': ['remote'] } },
        },
      },
    } as SettingSchema;
    expect(settingNodes(onlyRemote, 'local', 'npm')).toEqual([]);
  });

  it('R-UI-021 Q-UI-80 the written member leaves out what equals the default and the objects that empties; required and unknown members stay', () => {
    expect(
      pruneDefaults(SCHEMA, {
        latestGuard: true,
        audit: { mode: 'forward', timeout: '45s', providerURL: null },
        future: 1,
      }),
    ).toEqual({ audit: { timeout: '45s' }, future: 1 });
    expect(pruneDefaults(SCHEMA, { audit: { mode: 'forward' }, latestGuard: false })).toEqual({
      latestGuard: false,
    });
    const required = {
      type: 'object',
      required: ['mode'],
      properties: { mode: { type: 'string', default: 'a' } },
    } as SettingSchema;
    expect(pruneDefaults(required, { mode: 'a' })).toEqual({ mode: 'a' });
    expect(pruneDefaults(null, { anything: true })).toEqual({ anything: true });
  });

  it('R-UI-021 a value is set and removed at its path without changing the original', () => {
    const before = { latestGuard: false };
    const set = withValue(before, ['audit', 'timeout'], '45s');
    expect(set).toEqual({ latestGuard: false, audit: { timeout: '45s' } });
    expect(before).toEqual({ latestGuard: false });
    expect(valueAt(set, ['audit', 'timeout'])).toBe('45s');
    expect(valueAt(set, ['audit', 'mode'])).toBeUndefined();
    expect(withValue(set, ['latestGuard'], undefined)).toEqual({ audit: { timeout: '45s' } });
  });

  it('R-UI-021 a number field sends a number only for a number as JSON writes it, otherwise the text for the server to refuse', () => {
    expect(numberOrText('')).toBeUndefined();
    expect(numberOrText(' 30 ')).toBe(30);
    expect(numberOrText('1.5')).toBe(1.5);
    expect(numberOrText('01')).toBe('01');
    expect(numberOrText('ten')).toBe('ten');
  });

  it('R-UI-021 a server field error goes to its control, else to the nearest group, else to the section', () => {
    const nodes = settingNodes(SCHEMA, 'remote', 'npm');
    expect(targetOf(nodes, '/npm/audit/timeout', 'f-npm')).toBe('f-npm-audit-timeout');
    expect(targetOf(nodes, '/npm/audit/unknown', 'f-npm')).toBe('f-npm-audit');
    expect(targetOf(nodes, '/npm/latestGuard', 'f-npm')).toBe('f-npm');
    expect(targetOf(nodes, '/npm', 'f-npm')).toBe('f-npm');
    expect(errorTarget('/npm/audit/timeout', nodes, '/npm')).toBe('f-npm-audit-timeout');
    expect(errorTarget('/includePatterns/0', nodes, '/npm')).toBe('f-includePatterns');
  });

  it('R-UI-021 the review lists the written settings flattened', () => {
    expect(settingEntries({ latestGuard: false, audit: { timeout: '45s', retries: 3 } })).toEqual([
      ['latestGuard', 'false'],
      ['audit.timeout', '45s'],
      ['audit.retries', '3'],
    ]);
  });

  it('R-DEB-008 R-DEB-024 an array of strings is a list, one with an enum of items offers its options, a long string is an area, an array of anything else stays unedited', () => {
    const local = settingNodes(DEB_SETTINGS_SCHEMA as SettingSchema, 'local', 'deb');
    expect(local.map((n) => [n.name, n.kind])).toEqual([
      ['architectures', 'list'],
      ['compression', 'list'],
      ['origin', 'text'],
      ['label', 'text'],
    ]);
    expect(local[0]).toMatchObject({ options: null, fallback: ['amd64', 'arm64'] });
    expect(local[1]).toMatchObject({ options: ['xz'], fallback: [] });

    const virtual = settingNodes(DEB_SETTINGS_SCHEMA as SettingSchema, 'virtual', 'deb');
    expect(virtual.map((n) => n.name)).toEqual(['architectures', 'compression', 'origin', 'label', 'suites', 'components']);

    const remote = settingNodes(DEB_SETTINGS_SCHEMA as SettingSchema, 'remote', 'deb');
    expect(remote.map((n) => [n.name, n.kind])).toEqual([
      ['upstreamKeys', 'text'],
      ['mirror', 'group'],
    ]);
    expect(remote[0]).toMatchObject({ multiline: true, url: false });
    const mirror = remote[1];
    if (mirror?.kind !== 'group') throw new Error('mirror is no group');
    expect(mirror.children.map((n) => [n.name, n.kind, n.pointer])).toEqual([
      ['suites', 'list', '/deb/mirror/suites'],
      ['components', 'list', '/deb/mirror/components'],
      ['architectures', 'list', '/deb/mirror/architectures'],
      ['translations', 'list', '/deb/mirror/translations'],
      ['sources', 'boolean', '/deb/mirror/sources'],
    ]);
    expect(settingNodes(SCHEMA, 'remote', 'npm')[1]).toMatchObject({ kind: 'group' });
  });

  it('R-DEB-008 the written member leaves out a list equal to its default and keeps one that differs', () => {
    expect(
      pruneDefaults(DEB_SETTINGS_SCHEMA as SettingSchema, {
        architectures: ['amd64', 'arm64'],
        compression: [],
        origin: 'acme',
      }),
    ).toEqual({ origin: 'acme' });
    expect(pruneDefaults(DEB_SETTINGS_SCHEMA as SettingSchema, { architectures: ['arm64'] })).toEqual({ architectures: ['arm64'] });
    expect(
      pruneDefaults(DEB_SETTINGS_SCHEMA as SettingSchema, { mirror: { components: ['main'], suites: ['stable'] } }),
    ).toEqual({ mirror: { suites: ['stable'] } });
  });
});
