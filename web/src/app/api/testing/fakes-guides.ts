import { Provider } from '@angular/core';
import { Guide, GuideSection, GuidesApi } from '../guides-api';
import { ApiError } from '../problem';
import { FakeClientSetupApi, NPM_REPOSITORIES } from './fakes-packages';

/*
 * The in-memory fake of the guide operations (R-UI-047 component tier). Its sections have the ids,
 * topics and order of npm's guide, and its snippets are the setup fake's answer for the same
 * repository and client, as the server attaches them (R-API-053).
 */

const BASE = 'https://trove.example.net';

const CLIENTS = ['npm', 'pnpm', 'yarn', 'yarn-classic'];

function section(id: string, topic: GuideSection['topic'], title: string, html: string): GuideSection {
  return { id, topic, title, html, snippets: [] };
}

export class FakeGuidesApi extends GuidesApi {
  calls: { repository?: string; format?: string; cls?: string; client: string }[] = [];
  failure: ApiError | null = null;
  /** The reader is not signed in: snippets without a credential and no publishing sections. */
  anonymous = false;
  /** The endpoint serves under exposure rules. */
  exposureRules = false;
  private readonly setup = new FakeClientSetupApi();

  override async repository(key: string, client: string): Promise<Guide> {
    this.calls.push({ repository: key, client });
    this.fail();
    const repo = NPM_REPOSITORIES.find((r) => r.key === key);
    if (!repo) throw new ApiError(400, { code: 'malformed_request', detail: 'no guide for this repository' }, null);
    return this.build(key, repo.class, client);
  }

  override async format(format: string, cls: string, client: string): Promise<Guide> {
    this.calls.push({ format, cls, client });
    this.fail();
    if (format !== 'npm') throw new ApiError(404, { code: 'not_found', detail: 'no such format' }, null);
    return this.build(null, cls as Guide['class'], client);
  }

  private fail(): void {
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
  }

  private async build(key: string | null, cls: Guide['class'], client: string): Promise<Guide> {
    if (!CLIENTS.includes(client)) {
      throw new ApiError(400, { code: 'malformed_request', detail: 'unknown client' }, null);
    }
    const credentials = this.anonymous ? 'none' : 'env';
    const setup = await this.setup.get(key ?? 'npm-all', { client, credentials });
    const registryUrl = `${BASE}/api/npm/${key ?? '<repository>'}/`;
    const of = (tab: string) =>
      setup.snippets
        .filter((s) => s.tab === tab)
        .map((s) => ({ ...s, content: key ? s.content : s.content.replaceAll('/npm-all/', '/<repository>/') }));
    const sections: GuideSection[] = [
      section(`npm.${cls}.what`, 'what', `What a ${cls} repository is`, `<p>A ${cls} repository.</p><h3>Details</h3><p>More.</p>`),
      { ...section('npm.configure', 'configure', 'Point the client at the repository', '<p>Save the snippet.</p>'), snippets: of('configure') },
      section(
        'npm.authenticate',
        'authenticate',
        'Sign the client in',
        this.anonymous ? '<p>No credential is needed.</p>' : '<p>Keep the token in <code>TROVE_TOKEN</code>.</p>',
      ),
      { ...section('npm.install', 'install', 'Install packages', '<p>Install a package.</p>'), snippets: of('install') },
    ];
    if (cls !== 'remote' && !this.anonymous) {
      sections.push(section('npm.publish', 'publish', 'Publish a package', '<p><code>npm publish</code></p>'));
    }
    if (this.exposureRules) {
      sections.push(section('npm.endpoint', 'endpoint', 'On this endpoint', '<p><code>endpoint_read_only</code></p>'));
    }
    sections.push(
      section('npm.troubleshooting', 'troubleshooting', 'Troubleshooting', '<table><tr><td>404</td></tr></table>'),
    );
    return {
      format: 'npm',
      class: cls,
      client,
      repository: key,
      registryUrl,
      publishTarget: key && setup.publishTarget && !this.anonymous ? setup.publishTarget : null,
      credentials,
      exposureRules: this.exposureRules,
      sections,
    };
  }
}

export function provideGuideFakes(): Provider[] {
  return [{ provide: GuidesApi, useClass: FakeGuidesApi }];
}
