import { Component, computed, effect, inject, input, resource, signal, untracked } from '@angular/core';
import { MatButtonModule } from '@angular/material/button';
import { RouterLink } from '@angular/router';
import { ClientSetupApi } from '../../api/packages-api';
import { PackageVersion } from '../../api/packages-api';
import { CopyButton } from '../../shared/copy-button';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError, valueOf } from '../../shared/paged-resource';
import { ClientChoice, clientsOf } from '../setup/clients';

/** A command longer than this many lines is collapsed behind an expander (R-UI-009). */
export const COLLAPSE_AFTER_LINES = 3;

/**
 * The install command panel of a package version (R-UI-009). The command is the install snippet of
 * the client setup answer (R-API-030) for the chosen client and repository, with the package and
 * version filled in by the server, which also quotes them for a shell. The panel never asks for
 * credentials: the request carries `credentials=none`, and the link below the command leads to the
 * client setup that configures the client once.
 */
@Component({
  selector: 'trove-install-panel',
  imports: [MatButtonModule, RouterLink, CopyButton, ErrorNotice],
  templateUrl: './install-panel.html',
  styleUrl: './install-panel.scss',
})
export class InstallPanel {
  private readonly setup = inject(ClientSetupApi);

  readonly format = input.required<string>();
  readonly name = input.required<string>();
  readonly version = input.required<PackageVersion>();

  protected readonly clients = computed<readonly ClientChoice[]>(() => clientsOf(this.format()));
  protected readonly client = signal<string | null>(null);
  protected readonly chosenRepository = signal<string | null>(null);
  protected readonly expanded = signal(false);

  /** A virtual repository that contains the version, else the first local one, else the first. */
  protected readonly defaultRepository = computed<string | null>(() => {
    const repos = this.version().repositories;
    return (
      (repos.find((r) => r.class === 'virtual') ?? repos.find((r) => r.class === 'local') ?? repos[0])
        ?.key ?? null
    );
  });
  protected readonly repository = computed(() => this.chosenRepository() ?? this.defaultRepository());
  protected readonly selectedClient = computed(() => this.client() ?? this.clients()[0]?.id ?? null);

  protected readonly answer = resource({
    params: () => {
      const repository = this.repository();
      const client = this.selectedClient();
      return repository && client
        ? { repository, client, name: this.name(), version: this.version().version }
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
  protected readonly shown = computed(() =>
    this.collapsible() && !this.expanded()
      ? this.lines().slice(0, COLLAPSE_AFTER_LINES).join('\n')
      : this.command(),
  );
  protected readonly failure = computed(() => {
    const e = this.answer.error();
    return e ? asApiError(e) : null;
  });
  protected readonly copyLabel = $localize`:@@pkg.install.copy:Copy install command`;

  constructor() {
    // Another version or another package starts from the defaults again.
    effect(() => {
      this.version();
      untracked(() => {
        this.chosenRepository.set(null);
        this.expanded.set(false);
      });
    });
  }

  protected pickClient(event: Event): void {
    this.client.set((event.target as HTMLSelectElement).value);
  }

  protected pickRepository(event: Event): void {
    this.chosenRepository.set((event.target as HTMLSelectElement).value);
  }
}
