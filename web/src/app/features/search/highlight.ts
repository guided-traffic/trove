/** A part of a shown name: matched text is bold (Q-UI-409). */
export interface Segment {
  readonly text: string;
  readonly match: boolean;
}

/** The terms of a search text: lower-cased, split at white space, empty parts dropped (R-API-062). */
export function termsOf(q: string): string[] {
  return q
    .trim()
    .toLowerCase()
    .split(/\s+/)
    .filter((t) => t !== '');
}

/**
 * Marks every occurrence of each term of `q` in `name`, case-insensitively; overlapping and touching marks merge (R-UI-097,
 * Q-UI-409). Only the client's text decides what is bold: a package found by a word prefix shows that prefix.
 */
export function highlight(name: string, q: string): Segment[] {
  const lower = name.toLowerCase();
  const marked = new Array<boolean>(name.length).fill(false);
  for (const term of termsOf(q)) {
    for (let at = lower.indexOf(term); at >= 0; at = lower.indexOf(term, at + 1)) {
      for (let i = at; i < at + term.length && i < name.length; i++) marked[i] = true;
    }
  }
  const out: Segment[] = [];
  for (let i = 0; i < name.length; i++) {
    const last = out[out.length - 1];
    if (last && last.match === marked[i]) out[out.length - 1] = { text: last.text + name[i], match: last.match };
    else out.push({ text: name[i]!, match: marked[i]! });
  }
  return out;
}
