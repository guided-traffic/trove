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
