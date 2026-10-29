export type JsonObject = Record<string, unknown>;

function isObject(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function equal(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

/**
 * The JSON merge patch (RFC 7396) that turns `before` into `after`: objects are compared member
 * by member, everything else (arrays included) is replaced as a whole, a member that is gone
 * becomes `null`. An empty object means that nothing changed.
 */
export function mergePatchOf(before: JsonObject, after: JsonObject): JsonObject {
  const patch: JsonObject = {};
  for (const key of Object.keys(after)) {
    const a = after[key];
    const b = before[key];
    if (a === undefined) continue;
    if (isObject(a) && isObject(b)) {
      const nested = mergePatchOf(b, a);
      if (Object.keys(nested).length > 0) patch[key] = nested;
    } else if (!equal(a, b)) {
      patch[key] = a;
    }
  }
  for (const key of Object.keys(before)) {
    if (!(key in after) || after[key] === undefined) patch[key] = null;
  }
  return patch;
}

/** Applies a merge patch (RFC 7396) to a copy of `target`. */
export function applyMergePatch(target: JsonObject, patch: JsonObject): JsonObject {
  const out: JsonObject = { ...target };
  for (const [key, value] of Object.entries(patch)) {
    if (value === null) delete out[key];
    else if (isObject(value)) out[key] = applyMergePatch(isObject(out[key]) ? out[key] : {}, value);
    else out[key] = value;
  }
  return out;
}
