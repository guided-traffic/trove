/** One piece of a banner message: plain text, or a link the message wrote in Markdown inline syntax (R-OPS-025). */
export type BannerPart =
  | { readonly kind: 'text'; readonly text: string }
  | { readonly kind: 'link'; readonly text: string; readonly href: string };

/** `[text](url)` with no nesting and no line break, the only markup a banner message knows. */
const LINK = /\[([^\]\n]{1,200})\]\(([^)\s]{1,2000})\)/g;

/** The URL of a link, or null when it is not one the banner may link to: only `https`, with no user name or password. */
export function safeHref(raw: string): string | null {
  try {
    const url = new URL(raw);
    return url.protocol === 'https:' && url.username === '' && url.password === '' ? url.href : null;
  } catch {
    return null;
  }
}

/**
 * Splits a banner message into text and `https` links (R-OPS-025). The message is plain text: nothing else is interpreted, and the
 * result is bound as text and as `href`, never as HTML. A link to anything but `https` (`javascript:`, `data:`, `http:`, a relative
 * address) stays in the text exactly as it was written, so the reader sees what the administrator typed and nothing runs.
 */
export function parseBannerText(message: string): readonly BannerPart[] {
  const parts: BannerPart[] = [];
  const text = (s: string): void => {
    if (s === '') return;
    const last = parts[parts.length - 1];
    if (last?.kind === 'text') parts[parts.length - 1] = { kind: 'text', text: last.text + s };
    else parts.push({ kind: 'text', text: s });
  };
  let at = 0;
  for (const m of message.matchAll(LINK)) {
    text(message.slice(at, m.index));
    const href = safeHref(m[2] ?? '');
    if (href === null) text(m[0]);
    else parts.push({ kind: 'link', text: m[1] ?? '', href });
    at = m.index + m[0].length;
  }
  text(message.slice(at));
  return parts;
}
