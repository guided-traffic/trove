// Q-UI-335: a number field is as wide as its number. A template's number field is a raw <input> with type="number",
// inputmode="numeric" or "decimal", or a pattern of digits, or a <trove-text-field> with type="number". It carries one of the
// widths number, duration or bytes: the class field-w-number, field-w-duration or field-w-bytes on a raw input, the attribute
// width="number|duration|bytes" on a text field, or a [class] or [width] binding that names the width at run time. Run by
// check-rules.mjs over src/app, tested by lint-rules.test.mjs.

const numericInput = /\btype="number"|\binputmode="(numeric|decimal)"|\bpattern="[^"]*(\[0-9\]|\\d)[^"]*"/;
const numberWidthClass = /\bfield-w-(number|duration|bytes)\b|\[class\]="'field-w-' \+/;
const numberWidthInput = /(?<![\w[-])width="(number|duration|bytes)"|\[width\]="/;

/**
 * The number fields of a template text that name no number width, each as its 1-based line and its tag.
 * @param {string} text
 * @returns {{ line: number, tag: string }[]}
 */
export function numberFieldsWithoutWidth(text) {
  const found = [];
  for (const m of text.matchAll(/<input\b[^>]*>/g)) {
    const tag = m[0];
    if (numericInput.test(tag) && !numberWidthClass.test(tag)) found.push({ line: lineOf(text, m.index), tag });
  }
  for (const m of text.matchAll(/<trove-text-field\b[^>]*>/g)) {
    const tag = m[0];
    if (/\btype="number"/.test(tag) && !numberWidthInput.test(tag)) found.push({ line: lineOf(text, m.index), tag });
  }
  return found;
}

function lineOf(text, index) {
  return text.slice(0, index).split('\n').length;
}
