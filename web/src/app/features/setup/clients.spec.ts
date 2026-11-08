import { snippetExplanation } from './clients';
import { RENDERED_SNIPPET_IDS } from './snippet-ids.fixture';

describe('Snippet explanations', () => {
  for (const [client, ids] of Object.entries(RENDERED_SNIPPET_IDS)) {
    it(`R-UI-016 R-API-030 every snippet id the API renders for ${client} has an explanation`, () => {
      expect(ids.filter((id) => snippetExplanation(id) === null)).toEqual([]);
    });
  }
});
