import { Component, computed, effect, inject, input, model, resource, signal, untracked } from '@angular/core';
import { ClientSetupApi } from '../../api/packages-api';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { ClientChoice, clientsOf } from '../setup/clients';

/** A command longer than this many lines is collapsed behind an expander (R-UI-009). */
export const COLLAPSE_AFTER_LINES = 3;

/**
 * The install command of one version in one repository, with its client picker (R-UI-009): the command is the install
 * snippet of the client setup answer (R-API-030) for the chosen client, with the package and the version filled in and quoted
 * by the server. It asks with `credentials=none`, so it never holds a token. The package detail's install panel (a version in
 * several repositories) and the repository's content page (one repository, R-UI-052) share it.
 */
@Component({
  selector: 'trove-install-command',
  imports: [CopyButton, ErrorNotice],
  templateUrl: './install-command.html',
  styleUrl: './install-panel.scss',
})
export class InstallCommand {
  private readonly setup = inject(ClientSetupApi);

  readonly format = input.required<string>();
  readonly name = input.required<string>();
  /** The version string; null while none is selected. */
  readonly version = input.required<string | null>();
  /** The qualified name of the repository the command installs through; null while none is chosen. */
  readonly repository = input.required<string | null>();
  /** The chosen client, a two-way binding so that the host can follow it. */
  readonly client = model<string | null>(null);

  protected readonly clients = computed<readonly ClientChoice[]>(() => clientsOf(this.format()));
  protected readonly selectedClient = computed(() => this.client() ?? this.clients()[0]?.id ?? null);
  protected readonly expanded = signal(false);

  protected readonly answer = resource({
    params: () => {
      const repository = this.repository();
      const client = this.selectedClient();
      const version = this.version();
      return repository && client && version !== null
        ? { repository, client, name: this.name(), version }
        : undefined;
    },
    loader: ({ params }) =>
      this.setup.get(params.repository, {
        client: params.client,
        credentials: 'none',
        package: params.name,
        version: params.version,
      }),
  });

  protected readonly command = computed(
    () => valueOf(this.answer)?.snippets.find((s) => s.tab === 'install')?.content.trimEnd() ?? '',
  );
  protected readonly lines = computed(() => this.command().split('\n'));
  protected readonly collapsible = computed(() => this.lines().length > COLLAPSE_AFTER_LINES);
  /** How many lines the command shows; the height follows it with the animation of the style guide. */
  protected readonly shownLines = computed(() =>
    this.collapsible() && !this.expanded() ? COLLAPSE_AFTER_LINES : this.lines().length,
  );
  protected readonly failure = computed(() => {
    const e = this.answer.error();
    return e ? asApiError(e) : null;
  });
  protected readonly copyLabel = $localize`:@@pkg.install.copy:Copy install command`;

  constructor() {
    // Another version or another package starts collapsed again.
    effect(() => {
      this.version();
      untracked(() => this.expanded.set(false));
    });
  }

  protected pickClient(event: Event): void {
    this.client.set((event.target as HTMLSelectElement).value);
  }
}
