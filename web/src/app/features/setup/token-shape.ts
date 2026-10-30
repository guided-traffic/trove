/*
 * The offline shape check of a pasted trove token (INV-8, R-AUTH-022): a known prefix, 57
 * characters in all, base62 digits, and a trailing 6-digit base62 CRC-32 (IEEE) of everything
 * before it. It is no security mechanism: it only catches a mistyped or truncated paste before the
 * value is put into a configuration file. The value never leaves the browser.
 */

const ALPHABET = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';
const PREFIXES = ['trv_pat_', 'trv_sat_', 'trv_cit_'] as const;
const TOKEN_LENGTH = 57;
const CHECKSUM_DIGITS = 6;

export type TokenShape = 'ok' | 'empty' | 'prefix' | 'length' | 'alphabet' | 'checksum';

const CRC_TABLE: Uint32Array = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    table[n] = c >>> 0;
  }
  return table;
})();

/** CRC-32/IEEE of an ASCII string, as an unsigned 32-bit number. */
export function crc32(text: string): number {
  let crc = 0xffffffff;
  for (let i = 0; i < text.length; i++) {
    crc = (CRC_TABLE[(crc ^ text.charCodeAt(i)) & 0xff] ?? 0) ^ (crc >>> 8);
  }
  return (crc ^ 0xffffffff) >>> 0;
}

/** `value` in base 62, most significant digit first, left-padded with `0` to `width`. */
export function base62(value: number, width: number): string {
  let out = '';
  let rest = value;
  while (rest > 0) {
    out = ALPHABET.charAt(rest % 62) + out;
    rest = Math.floor(rest / 62);
  }
  return out.padStart(width, '0');
}

/** Why a pasted value is not a trove token; `ok` when it passes. Surrounding space is ignored. */
export function checkTokenShape(input: string): TokenShape {
  const token = input.trim();
  if (token === '') return 'empty';
  if (!PREFIXES.some((p) => token.startsWith(p))) return 'prefix';
  if (token.length !== TOKEN_LENGTH) return 'length';
  if (![...token.slice(PREFIXES[0].length)].every((c) => ALPHABET.includes(c))) return 'alphabet';
  const body = token.slice(0, TOKEN_LENGTH - CHECKSUM_DIGITS);
  return base62(crc32(body), CHECKSUM_DIGITS) === token.slice(TOKEN_LENGTH - CHECKSUM_DIGITS)
    ? 'ok'
    : 'checksum';
}
