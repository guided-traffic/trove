import { JsonObject } from '../../shared/merge-patch';

/**
 * The part of JSON Schema 2020-12 that the settings schemas of the formats use (`settingsSchema` of
 * `GET /api/v1/formats`, Q-ARCH-229) and that the repository form renders. The server compiles and
 * enforces the full schema; the form only needs enough of it to offer the right control.
 */
export interface SettingSchema {
  readonly type?: string | readonly string[];
  readonly title?: string;
  readonly description?: string;
  readonly default?: unknown;
  readonly enum?: readonly unknown[];
  readonly format?: string;
  readonly properties?: Readonly<Record<string, SettingSchema>>;
  readonly required?: readonly string[];
  /** The repository classes a member applies to; absent means every class. */
  readonly 'x-trove-classes'?: readonly string[];
}

interface NodeBase {
  /** The member name, the last segment of `path`. */
  readonly name: string;
  /** The members from the format member down to this one. */
  readonly path: readonly string[];
  /** The JSON Pointer in the repository document (`/npm/latestGuard`), as the server's field errors name it. */
  readonly pointer: string;
  /** The id of the control, derived from the pointer. */
  readonly id: string;
  /** The schema's `title`, else the member name. */
  readonly label: string;
  readonly description: string | null;
}

/** One member of a settings schema as the form renders it. */
export type SettingNode =
  | (NodeBase & { readonly kind: 'group'; readonly children: readonly SettingNode[] })
  | (NodeBase & { readonly kind: 'boolean'; readonly fallback: boolean | undefined })
  | (NodeBase & {
      readonly kind: 'choice';
      readonly options: readonly unknown[];
      readonly fallback: unknown;
    })
  | (NodeBase & { readonly kind: 'text'; readonly url: boolean; readonly fallback: unknown })
  | (NodeBase & { readonly kind: 'number'; readonly integer: boolean; readonly fallback: unknown })
  /** A construct the form does not render (an array, a composition): shown, not edited. */
  | (NodeBase & { readonly kind: 'other' });

function isObject(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isSchema(value: unknown): value is SettingSchema {
  return isObject(value);
}

function sameJson(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

/** RFC 6901: `~` and `/` inside a member name are escaped. */
function pointerSegment(name: string): string {
  return name.replace(/~/g, '~0').replace(/\//g, '~1');
}

/** The id of the control of a document pointer: `/npm/latestGuard` is `f-npm-latestGuard`. */
export function controlIdOf(pointer: string): string {
  return 'f' + pointer.replace(/[^A-Za-z0-9_]+/g, '-');
}

/** The one type a schema names, ignoring `null` (a nullable member is edited as its other type). */
function typeOf(s: SettingSchema): string | null {
  if (typeof s.type === 'string') return s.type;
  if (Array.isArray(s.type)) {
    const types = (s.type as readonly string[]).filter((t) => t !== 'null');
    return types.length === 1 ? (types[0] ?? null) : null;
  }
  return s.properties ? 'object' : null;
}

function appliesTo(s: SettingSchema, cls: string): boolean {
  const classes = s['x-trove-classes'];
  return !Array.isArray(classes) || classes.includes(cls);
}

function node(
  name: string,
  s: SettingSchema,
  cls: string,
  path: readonly string[],
  pointer: string,
): SettingNode | null {
  const base: NodeBase = {
    name,
    path,
    pointer,
    id: controlIdOf(pointer),
    label: typeof s.title === 'string' && s.title !== '' ? s.title : name,
    description: typeof s.description === 'string' && s.description !== '' ? s.description : null,
  };
  const fallback = s.default;
  if (Array.isArray(s.enum)) return { ...base, kind: 'choice', options: s.enum, fallback };
  switch (typeOf(s)) {
    case 'object': {
      if (!isObject(s.properties)) return { ...base, kind: 'other' };
      const children = nodesOf(s, cls, path, pointer);
      return children.length > 0 ? { ...base, kind: 'group', children } : null;
    }
    case 'boolean':
      return {
        ...base,
        kind: 'boolean',
        fallback: typeof fallback === 'boolean' ? fallback : undefined,
      };
    case 'string':
      return { ...base, kind: 'text', url: s.format === 'uri', fallback };
    case 'integer':
    case 'number':
      return { ...base, kind: 'number', integer: typeOf(s) === 'integer', fallback };
    default:
      return { ...base, kind: 'other' };
  }
}

function nodesOf(
  s: SettingSchema,
  cls: string,
  path: readonly string[],
  pointer: string,
): SettingNode[] {
  const out: SettingNode[] = [];
  for (const [name, child] of Object.entries(s.properties ?? {})) {
    if (!isSchema(child) || !appliesTo(child, cls)) continue;
    const n = node(name, child, cls, [...path, name], `${pointer}/${pointerSegment(name)}`);
    if (n) out.push(n);
  }
  return out;
}

/**
 * The members of a format's settings schema that apply to a repository of `cls`, in schema order,
 * for the member named `format` of the document. An object whose members all belong to other
 * classes is left out. Empty for a format without a schema.
 */
export function settingNodes(
  schema: SettingSchema | null | undefined,
  cls: string,
  format: string,
): readonly SettingNode[] {
  return isSchema(schema) ? nodesOf(schema, cls, [], `/${pointerSegment(format)}`) : [];
}

/** The value at `path` of the member, or undefined. */
export function valueAt(member: JsonObject, path: readonly string[]): unknown {
  let current: unknown = member;
  for (const name of path) {
    if (!isObject(current)) return undefined;
    current = current[name];
  }
  return current;
}

/** A copy of the member with `value` at `path`; `undefined` removes the member there. */
export function withValue(member: JsonObject, path: readonly string[], value: unknown): JsonObject {
  const [name, ...rest] = path;
  if (name === undefined) return member;
  const out: JsonObject = { ...member };
  if (rest.length > 0) {
    out[name] = withValue(isObject(member[name]) ? member[name] : {}, rest, value);
  } else if (value === undefined) {
    delete out[name];
  } else {
    out[name] = value;
  }
  return out;
}

/**
 * The member as it is written into the document: a setting equal to its schema default is left out,
 * so the repository follows the format's default, and so is an object left empty by that. A
 * required member and a member the schema does not know stay as they are.
 */
export function pruneDefaults(
  schema: SettingSchema | null | undefined,
  member: JsonObject,
): JsonObject {
  if (!isSchema(schema)) return member;
  const properties = schema.properties ?? {};
  const required = schema.required ?? [];
  const out: JsonObject = {};
  for (const [name, value] of Object.entries(member)) {
    if (value === undefined) continue;
    const s = properties[name];
    const keep = required.includes(name);
    if (s && isObject(value) && isObject(s.properties)) {
      const inner = pruneDefaults(s, value);
      if (keep || Object.keys(inner).length > 0) out[name] = inner;
    } else if (keep || !s || !('default' in s) || !sameJson(s.default, value)) {
      out[name] = value;
    }
  }
  return out;
}

/**
 * What a number field sends: a number when the text is one as JSON writes it, otherwise the text
 * itself, which the server refuses at the field (the server validates, the form does not guess).
 */
export function numberOrText(text: string): unknown {
  const t = text.trim();
  if (t === '') return undefined;
  const n = Number(t);
  return Number.isFinite(n) && String(n) === t ? n : t;
}

/** The members set in the member, flattened (`audit.timeout`), with their values as JSON writes them. */
export function settingEntries(
  member: JsonObject,
  prefix = '',
): readonly (readonly [string, string])[] {
  const out: (readonly [string, string])[] = [];
  for (const [name, value] of Object.entries(member)) {
    if (isObject(value)) out.push(...settingEntries(value, `${prefix}${name}.`));
    else out.push([`${prefix}${name}`, typeof value === 'string' ? value : JSON.stringify(value)]);
  }
  return out;
}

/**
 * The id of the control a field error of the repository document belongs to: inside the format
 * member (`section`, `/npm`) by `targetOf`, else the generic member's field (`/includePatterns/0` is
 * `f-includePatterns`).
 */
export function errorTarget(
  pointer: string,
  nodes: readonly SettingNode[],
  section: string,
): string {
  if (pointer === section || pointer.startsWith(`${section}/`)) {
    return targetOf(nodes, pointer, controlIdOf(section));
  }
  return `f-${pointer.split('/')[1] ?? ''}`;
}

/**
 * The control a field error at `pointer` belongs to: the deepest rendered member the pointer is at
 * or below, else the section itself (a member the form does not render, or the member as a whole).
 */
export function targetOf(
  nodes: readonly SettingNode[],
  pointer: string,
  sectionId: string,
): string {
  for (const n of nodes) {
    if (pointer === n.pointer || pointer.startsWith(`${n.pointer}/`)) {
      return n.kind === 'group' ? targetOf(n.children, pointer, n.id) : n.id;
    }
  }
  return sectionId;
}
