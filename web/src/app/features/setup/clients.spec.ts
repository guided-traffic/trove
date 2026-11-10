import { formatIcon } from '../../shared/format-icon';
import { clientsOf, snippetExplanation } from './clients';
import { RENDERED_SNIPPET_IDS } from './snippet-ids.fixture';

describe('Snippet explanations', () => {
  for (const [client, ids] of Object.entries(RENDERED_SNIPPET_IDS)) {
    it(`R-UI-016 R-API-030 every snippet id the API renders for ${client} has an explanation`, () => {
      expect(ids.filter((id) => snippetExplanation(id) === null)).toEqual([]);
    });
  }
});

describe('Ansible Galaxy client setup', () => {
  it('R-ANS-025 R-ANS-026 the format offers ansible-galaxy as its one client', () => {
    expect(clientsOf('ansible')).toEqual([{ id: 'ansible-galaxy', label: 'Ansible Galaxy (ansible-galaxy)' }]);
  });

  it('R-ANS-025 the snippets of every shell for the token variable are explained, and the API renders exactly the ones the UI knows', () => {
    const ids = RENDERED_SNIPPET_IDS['ansible:ansible-galaxy'];
    expect(ids).toEqual([
      'ansible-galaxy.configure.ansible-cfg',
      'ansible-galaxy.configure.netrc',
      'ansible-galaxy.configure.token-env',
      'ansible-galaxy.configure.token-env-cmd',
      'ansible-galaxy.configure.token-env-powershell',
      'ansible-galaxy.install.collection',
      'ansible-galaxy.install.requirements',
      'ansible-galaxy.publish.collection',
    ]);
    expect(snippetExplanation('ansible-galaxy.configure.netrc')).toContain('roles');
    expect(snippetExplanation('ansible-galaxy.configure.ansible-cfg')).toContain('does not expand variables');
  });

  it('R-UI-006 the format has an icon of its own', () => {
    expect(formatIcon('ansible')).toBe('terminal');
    expect(formatIcon('deb')).not.toBe(formatIcon('ansible'));
  });
});
