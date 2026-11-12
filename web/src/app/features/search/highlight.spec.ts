import { describe, expect, it } from 'vitest';
import { highlight, termsOf } from './highlight';

const bold = (name: string, q: string): string =>
  highlight(name, q)
    .map((s) => (s.match ? `[${s.text}]` : s.text))
    .join('');

describe('The highlight of a suggestion (R-UI-097, Q-UI-409)', () => {
  it('R-UI-097 every occurrence of each term is bold, case-insensitively, and the rest of the name stays as it is', () => {
    expect(bold('npm-vendor', 'vend')).toBe('npm-[vend]or');
    expect(bold('NPM-Vendor', 'npm VEND')).toBe('[NPM]-[Vend]or');
    expect(bold('npm-npm', 'npm')).toBe('[npm]-[npm]');
    expect(bold('collections', 'xyz')).toBe('collections');
  });

  it('R-UI-097 overlapping and touching marks merge into one', () => {
    expect(bold('vendor', 'ven endo')).toBe('[vendo]r');
    expect(bold('abcdef', 'abc def')).toBe('[abcdef]');
  });

  it('R-UI-097 keys with dot, hyphen, at sign and slash are matched literally', () => {
    expect(bold('@web/ui-kit', '@web/ui')).toBe('[@web/ui]-kit');
    expect(bold('platform.v2-npm', '.v2-')).toBe('platform[.v2-]npm');
  });

  it('R-UI-097 the terms are the text between white space, lower-cased, without empty parts', () => {
    expect(termsOf('  Npm   Vend ')).toEqual(['npm', 'vend']);
    expect(termsOf('   ')).toEqual([]);
  });
});
