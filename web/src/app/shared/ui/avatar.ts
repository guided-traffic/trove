import { ChangeDetectionStrategy, Component, computed, input, resource } from '@angular/core';

/**
 * A user's avatar: the uploaded picture when `imageUrl` is set, otherwise the robot generated from `seed`, the user's ID
 * (R-UI-002). The robot is rendered by ./robot, loaded on first use from the UI's own bundle; until it is there the host
 * shows its fill only. Decorative: the user's name is always next to it, so the image has an empty alternative text. The
 * host takes its size and shape from the place it is used in.
 */
@Component({
  selector: 'trove-avatar',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `@if (src(); as src) {
    <img [src]="src" alt="" width="64" height="64" draggable="false" />
  }`,
  styles: `
    :host {
      display: inline-block;
      overflow: hidden;
      line-height: 0;
    }
    img {
      display: block;
      inline-size: 100%;
      block-size: 100%;
      object-fit: cover;
    }
  `,
})
export class TroveAvatar {
  /** The stable, non-secret identifier of the user (the ID, which a rename does not change). */
  readonly seed = input.required<string>();
  /** The uploaded avatar, when the user has one; it wins over the generated robot. */
  readonly imageUrl = input<string | null | undefined>(null);

  // The robot is only generated while there is no uploaded picture, so the library is not loaded for a user who has one.
  private readonly robot = resource({
    params: () => (this.imageUrl() ? undefined : this.seed()),
    loader: async ({ params }) => (await import('./robot')).robotDataUri(params),
  });

  protected readonly src = computed(
    () => this.imageUrl() || (this.robot.hasValue() ? this.robot.value() : null),
  );
}
