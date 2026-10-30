import { DOCUMENT, NgTemplateOutlet } from '@angular/common';
import {
  Component,
  DestroyRef,
  computed,
  effect,
  inject,
  input,
  resource,
  signal,
  untracked,
} from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { MatTabsModule } from '@angular/material/tabs';
import { Router, RouterLink } from '@angular/router';
import { newIdempotencyKey } from '../../api/client';
import { ClientSetupApi, ClientSnippet } from '../../api/packages-api';
import { ApiError } from '../../api/problem';
import { RepositoriesApi, RepositorySummary } from '../../api/repositories-api';
import { CreateTokenRequest, TokensApi } from '../../api/tokens-api';
import { SessionStore } from '../../core/session-store';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { clientsOf, FORMAT_CLIENTS, snippetExplanation } from './clients';
import { checkTokenShape, TokenShape } from './token-shape';

/** How the snippets get their credentials (R-UI-016 step 4). */
export type CredentialMode = 'env' | 'create' | 'paste';

/** The literal the server puts into a snippet where the browser inserts the token (R-API-030). */
export const TOKEN_PLACEHOLDER = '{{token}}';

/** Replaces every placeholder in the browser. The replacement is literal: `$&` in a token means nothing. */
export function fillToken(content: string, token: string | null): string {
  return token === null ? content : content.split(TOKEN_PLACEHOLDER).join(token);
}

/**
 * The client setup page (R-UI-016, R-UI-017): format, client, repository, credentials, then the
 * snippets of the server (R-API-030) in the tabs Configure, Install and Publish. The choices of the
 * first three steps live in the query parameters, so a link from a repository or a package opens the
 * page prepared.
 *
 * Secrets (R-UI-043): the server never fills a token. The browser replaces `{{token}}` in this
 * component only. A created or pasted token lives in a component signal, is never put into a
 * service, the URL or browser storage, and is gone when the route is left. A pasted token is checked
 * for its prefix, length and CRC-32 locally and is sent nowhere.
 */
@Component({
  selector: 'trove-client-setup',
  imports: [MatButtonModule, MatTabsModule, NgTemplateOutlet, RouterLink, CopyButton, ErrorNotice],
  templateUrl: './client-setup.html',
  styleUrl: './client-setup.scss',
})
export class ClientSetupPage {
  private readonly setupApi = inject(ClientSetupApi);
  private readonly repositories = inject(RepositoriesApi);
  private readonly tokens = inject(TokensApi);
  private readonly router = inject(Router);
  private readonly document = inject(DOCUMENT);
  protected readonly session = inject(SessionStore);

  /** Query parameters, bound by the router's component input binding. */
  readonly format = input<string | undefined>();
  readonly client = input<string | undefined>();
  readonly repository = input<string | undefined>();

  protected readonly formats = resource({ loader: () => this.repositories.formats() });
  protected readonly allRepositories = resource({ loader: () => this.repositories.listAll() });

  protected readonly anonymous = computed(() => this.session.state() === 'anonymous');
  protected readonly mode = signal<CredentialMode>('env');
  /** The token of this setup: created or pasted. Component-local, never shared or stored. */
  private readonly token = signal<string | null>(null);
  protected readonly created = signal(false);
  protected readonly pasted = signal('');
  protected readonly reveal = signal(false);
  protected readonly withPublish = signal(false);
  protected readonly creating = signal(false);
  protected readonly createFailure = signal<ApiError | null>(null);
  private idempotencyKey = newIdempotencyKey();
  private generation = 0;

  protected readonly formatOptions = computed(() =>
    (valueOf(this.formats) ?? []).filter((f) => f.name in FORMAT_CLIENTS),
  );
  protected readonly repositoryOptionsAll = computed(() => valueOf(this.allRepositories) ?? []);
  protected readonly activeFormat = computed<string | null>(() => {
    const names = this.formatOptions().map((f) => f.name);
    const given = this.format();
    if (given && names.includes(given)) return given;
    const repoFormat = this.repositoryOptionsAll().find((r) => r.key === this.repository())?.format;
    if (repoFormat && names.includes(repoFormat)) return repoFormat;
    return names[0] ?? null;
  });
  protected readonly clients = computed(() => clientsOf(this.activeFormat() ?? ''));
  protected readonly activeClient = computed<string | null>(() => {
    const ids = this.clients().map((c) => c.id);
    const given = this.client();
    return given && ids.includes(given) ? given : (ids[0] ?? null);
  });
  protected readonly repositoryOptions = computed<readonly RepositorySummary[]>(() =>
    this.repositoryOptionsAll()
      .filter((r) => r.format === this.activeFormat() && !r.disabled)
      // Virtual repositories first: they are the recommended address (R-UI-016).
      .sort((a, b) => Number(b.class === 'virtual') - Number(a.class === 'virtual') || a.key.localeCompare(b.key)),
  );
  protected readonly activeRepository = computed<string | null>(() => {
    const given = this.repository();
    return given && this.repositoryOptions().some((r) => r.key === given) ? given : null;
  });
  /** `deploy` per repository key, from one batched permission check. */
  protected readonly deployable = resource({
    params: () => this.repositoryOptions().map((r) => r.key),
    loader: async ({ params }) => {
      if (params.length === 0 || this.anonymous()) return new Map<string, boolean>();
      const answers = await this.session.check(params.map((repository) => ({ repository, action: 'deploy' })));
      return new Map(params.map((key, i) => [key, answers[i] ?? false] as const));
    },
  });

  protected readonly credentials = computed<'none' | 'env' | 'placeholder'>(() =>
    this.anonymous() ? 'none' : this.mode() === 'env' ? 'env' : 'placeholder',
  );
  protected readonly answer = resource({
    params: () => {
      const repository = this.activeRepository();
      const client = this.activeClient();
      return repository && client ? { repository, client, credentials: this.credentials() } : undefined;
    },
    loader: ({ params }) =>
      this.setupApi.get(params.repository, { client: params.client, credentials: params.credentials }),
  });
  protected readonly setup = computed(() => valueOf(this.answer));
  protected readonly loadFailure = computed(() => {
    const e = this.answer.error() ?? this.formats.error() ?? this.allRepositories.error();
    return e ? asApiError(e) : null;
  });

  /** The state of the pasted value, or null while the field is empty or untouched. */
  protected readonly pasteShape = computed<TokenShape | null>(() =>
    this.pasted() === '' ? null : checkTokenShape(this.pasted()),
  );
  protected readonly composed = computed<readonly ClientSnippet[]>(() => {
    const s = this.setup();
    if (!s) return [];
    const token = this.mode() === 'env' || this.anonymous() ? null : this.token();
    return s.snippets.map((x) => ({ ...x, content: fillToken(x.content, token) }));
  });
  protected readonly needsToken = computed(
    () => !this.anonymous() && this.mode() !== 'env' && this.token() === null,
  );
  protected readonly createdToken = computed(() => (this.created() ? this.token() : null));
  protected readonly byTab = computed(() => ({
    configure: this.composed().filter((s) => s.tab === 'configure'),
    install: this.composed().filter((s) => s.tab === 'install'),
    publish: this.composed().filter((s) => s.tab === 'publish'),
  }));
  protected readonly canOfferPublish = computed(() => this.setup()?.publishTarget != null);
  protected readonly isVirtual = computed(
    () => this.repositoryOptions().find((r) => r.key === this.activeRepository())?.class === 'virtual',
  );
  protected readonly canReadConfig = computed(() => this.session.has('repositories:read-config'));
  protected readonly explanation = snippetExplanation;
  protected readonly placeholder = TOKEN_PLACEHOLDER;
  protected readonly copyLabel = $localize`:@@setup.copy:Copy snippet`;
  protected readonly copyTokenLabel = $localize`:@@setup.copy-token:Copy token`;

  constructor() {
    // A token is scoped to a repository: another repository or another way of signing in drops it.
    effect(() => {
      this.activeRepository();
      this.mode();
      untracked(() => this.forgetToken());
    });
    // A pasted value that passes the check becomes the token; any other value is not kept.
    effect(() => {
      const shape = this.pasteShape();
      const value = this.pasted().trim();
      untracked(() => {
        if (this.mode() !== 'paste') return;
        this.token.set(shape === 'ok' ? value : null);
      });
    });
    inject(DestroyRef).onDestroy(() => this.forgetToken());
  }

  protected labelOfClass(c: RepositorySummary['class']): string {
    switch (c) {
      case 'local':
        return $localize`:@@repo.class.local:Local`;
      case 'remote':
        return $localize`:@@repo.class.remote:Remote`;
      case 'virtual':
        return $localize`:@@repo.class.virtual:Virtual`;
    }
  }

  /** What the repository list says about publishing to a repository, before the answer decides. */
  protected publishHint(r: RepositorySummary): string {
    if (r.class === 'remote') return $localize`:@@setup.publish.read-only:Read only`;
    if (r.class === 'virtual') {
      return $localize`:@@setup.publish.by-target:Publishing goes to its default deployment target`;
    }
    return valueOf(this.deployable)?.get(r.key)
      ? $localize`:@@setup.publish.yes:You can publish`
      : $localize`:@@setup.publish.no:You cannot publish`;
  }

  protected pick(change: {
    format?: string | null;
    client?: string | null;
    repository?: string | null;
  }): void {
    void this.router.navigate([], { queryParams: change, queryParamsHandling: 'merge', replaceUrl: true });
  }

  protected pickFormat(format: string): void {
    this.pick({ format, client: null, repository: null });
  }

  protected setMode(mode: CredentialMode): void {
    this.mode.set(mode);
  }

  protected onPaste(value: string): void {
    this.pasted.set(value);
  }

  /** Creates a personal token for this setup and keeps its value in this component only. */
  protected async createToken(): Promise<void> {
    const setup = this.setup();
    const repository = this.activeRepository();
    if (!setup || !repository || this.creating()) return;
    this.creating.set(true);
    this.createFailure.set(null);
    const target = setup.publishTarget;
    const publish = this.withPublish() && target !== null;
    const grants: NonNullable<NonNullable<CreateTokenRequest['scope']>['repositories']> = [
      { repositories: [repository], actions: ['read'] },
      ...(publish && target ? [{ repositories: [target], actions: ['read' as const, 'deploy' as const] }] : []),
    ];
    const request: CreateTokenRequest = {
      description: $localize`:@@setup.token.description:Client setup: ${setup.client}:client:, ${repository}:repository:, ${new Date().toISOString().slice(0, 10)}:date:`,
      ttlSeconds: setup.tokenTtlSeconds,
      scope: { repositories: grants },
      selfRotatable: false,
    };
    // forgetToken() counts a generation: an answer that arrives after the repository or the way
    // of signing in changed belongs to a setup the page no longer shows (R-UI-042, R-UI-043).
    const generation = this.generation;
    try {
      const made = await this.createOnce(request);
      if (generation !== this.generation) {
        // Nobody can copy this token any more: it must not stay alive.
        await this.tokens.revoke(made.id).catch(() => undefined);
        return;
      }
      this.token.set(made.token);
      this.created.set(true);
    } catch (e) {
      if (generation === this.generation) this.createFailure.set(asApiError(e));
    } finally {
      this.creating.set(false);
    }
  }

  private async createOnce(request: CreateTokenRequest) {
    try {
      return await this.tokens.create(request, this.idempotencyKey);
    } catch (e) {
      // The first attempt created a token whose value was lost with its answer: revoke it and
      // create again under a new key, so that exactly one working token exists (R-UI-042).
      const lost = e instanceof ApiError && e.code === 'token_already_issued' ? e.problem?.['tokenId'] : null;
      if (typeof lost !== 'string') throw e;
      await this.tokens.revoke(lost);
      this.idempotencyKey = newIdempotencyKey();
      return this.tokens.create(request, this.idempotencyKey);
    }
  }

  protected forgetToken(): void {
    this.generation++;
    this.token.set(null);
    this.created.set(false);
    this.pasted.set('');
    this.reveal.set(false);
    this.idempotencyKey = newIdempotencyKey();
  }

  /**
   * Offers a configuration file for download through a Blob URL that is revoked right after the
   * click (R-UI-043).
   */
  protected download(snippet: ClientSnippet): void {
    if (!snippet.fileName) return;
    const url = URL.createObjectURL(new Blob([snippet.content], { type: 'text/plain;charset=utf-8' }));
    try {
      const a = this.document.createElement('a');
      a.href = url;
      a.download = snippet.fileName;
      a.click();
    } finally {
      URL.revokeObjectURL(url);
    }
  }
}
