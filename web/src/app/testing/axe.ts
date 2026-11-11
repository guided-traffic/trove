import axe from 'axe-core';

/**
 * Accessibility check for component specs (R-UI-044, Q-UI-11): axe-core on the rendered element
 * in jsdom. Returns the violations of impact `serious` or `critical`, formatted for the failure
 * message; an empty list passes.
 *
 * jsdom has no layout engine, so `color-contrast` cannot be evaluated and is switched off here;
 * contrast, the document-level rules (title, language) and the keyboard journeys belong to the
 * browser tier (Playwright with axe-core, R-UI-047), which is not set up yet.
 */
export async function seriousViolations(root: Element): Promise<string[]> {
  const results = await axe.run(root, {
    rules: { 'color-contrast': { enabled: false } },
    resultTypes: ['violations'],
  });
  return results.violations
    .filter((v) => v.impact === 'serious' || v.impact === 'critical')
    .map((v) => `${v.id} (${v.impact}): ${v.help} -- ${v.nodes.map((n) => n.target.join(' ')).join('; ')}`);
}

/**
 * What assistive technology reads for `element`'s content, as axe-core computes a text alternative (accname): an image
 * with an empty `alt` and an `aria-hidden` subtree add nothing, the text does. Whitespace is collapsed. For a spec that
 * checks that a name is said once, with nothing before or after it.
 */
export function spokenText(element: Element): string {
  axe.setup(element.ownerDocument);
  try {
    return axe.commons.text.accessibleText(element, { inLabelledByContext: true }).replace(/\s+/g, ' ').trim();
  } finally {
    axe.teardown();
  }
}
