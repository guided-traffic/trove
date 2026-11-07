import { Avatar, Style } from '@dicebear/core';
import botttsDefinition from '@dicebear/styles/bottts.json';

// The DiceBear "Bottts" style, bundled with the UI (R-UI-002). The style is validated once, when the module loads.
const bottts = new Style(botttsDefinition);

// Rendering is pure in the seed, so a seed is rendered once for the life of the page.
const rendered = new Map<string, string>();

/**
 * The generated robot of a user: DiceBear's Bottts style rendered in the browser from the seed and returned as a data URI,
 * which the UI's CSP admits for images (`img-src 'self' data:`). Nothing is requested from another host. The background is
 * left transparent, so the host's fill (a theme token) shows behind the robot in both colour schemes.
 *
 * This module is the only one that imports the library; the avatar component loads it with a dynamic import, so the
 * library and the style (about 250 kB of script) are a lazy chunk and not part of the initial bundle.
 */
export function robotDataUri(seed: string): string {
  let uri = rendered.get(seed);
  if (uri === undefined) {
    uri = new Avatar(bottts, { seed, size: 64, idRandomization: false }).toDataUri();
    rendered.set(seed, uri);
  }
  return uri;
}
