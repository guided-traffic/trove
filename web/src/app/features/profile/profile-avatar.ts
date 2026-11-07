import { Component, computed, inject, input, signal } from '@angular/core';
import { IdentityApi } from '../../api/identity-api';
import { ApiError } from '../../api/problem';
import { AvatarStore } from '../../core/avatar-store';
import { Notifier } from '../../core/notifier';
import { Confirm } from '../../shared/confirm-dialog';
import { ErrorNotice } from '../../shared/error-notice';
import { asApiError } from '../../shared/paged-resource';
import { TroveAvatar } from '../../shared/ui/avatar';
import { AVATAR_TYPES, IMAGE_SIZER, refuseAvatar } from './avatar-file';
import { avatarErrorText, avatarRefusalText } from './avatar-messages';

/**
 * "Public avatar" of the profile (R-UI-051): the current avatar, a "Choose file…" button, what is accepted, and
 * "Remove avatar" while one is set. A chosen file is checked in the browser (type, size, pixel size), then sent as it
 * is; the server decodes it, centre-crops it to a square and stores a new PNG, so there is no cropper here. The new
 * `avatarUrl` of the answer goes to the `AvatarStore`, which the top bar shows without a reload; the old URL is 404 after
 * a replacement and is never used again.
 */
@Component({
  selector: 'trove-profile-avatar',
  imports: [TroveAvatar, ErrorNotice],
  templateUrl: './profile-avatar.html',
  styleUrls: ['../../shared/access/access.scss', './profile-avatar.scss'],
})
export class ProfileAvatar {
  /** The principal ID: the seed of the robot shown while no image is set. */
  readonly seed = input.required<string>();

  private readonly api = inject(IdentityApi);
  private readonly store = inject(AvatarStore);
  private readonly confirmer = inject(Confirm);
  private readonly notifier = inject(Notifier);
  private readonly sizer = inject(IMAGE_SIZER);

  protected readonly url = this.store.url;
  protected readonly accept = AVATAR_TYPES.join(',');
  protected readonly busy = signal(false);
  /** The refusal to show: a text of the UI's own check or of an answer that has an avatar text. */
  protected readonly message = signal<string | null>(null);
  /** An answer without an avatar text (a 403, a network failure): the general error notice. */
  protected readonly failure = signal<ApiError | null>(null);
  protected readonly hasAvatar = computed(() => this.url() !== null);

  protected async chosen(event: Event): Promise<void> {
    const input = event.target as HTMLInputElement;
    const file = input.files?.item(0) ?? null;
    // The same file chosen again after a failure must fire `change` again.
    input.value = '';
    if (file === null) return;
    this.message.set(null);
    this.failure.set(null);
    const refusal = await refuseAvatar(file, this.sizer);
    if (refusal !== null) {
      this.message.set(avatarRefusalText(refusal));
      return;
    }
    this.busy.set(true);
    try {
      const user = await this.api.setAvatar(file);
      this.store.set(user.avatarUrl);
      this.notifier.status($localize`:@@avatar.saved:Avatar saved.`);
    } catch (e) {
      this.fail(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected async remove(): Promise<void> {
    const answer = await this.confirmer.ask({
      title: $localize`:@@avatar.remove-title:Remove your avatar?`,
      lines: [$localize`:@@avatar.remove-text:Your avatar is replaced by the generated one.`],
      confirmLabel: $localize`:@@avatar.remove-confirm:Remove avatar`,
    });
    if (answer === null) return;
    this.message.set(null);
    this.failure.set(null);
    this.busy.set(true);
    try {
      await this.api.removeAvatar();
      this.store.set(null);
      this.notifier.status($localize`:@@avatar.removed:Avatar removed.`);
    } catch (e) {
      this.fail(asApiError(e));
    } finally {
      this.busy.set(false);
    }
  }

  private fail(error: ApiError): void {
    // The codes the avatar texts cover are shown in place; the rest keep the notice with the request ID.
    if (this.coveredBy(error)) this.message.set(avatarErrorText(error));
    else this.failure.set(error);
  }

  private coveredBy(error: ApiError): boolean {
    const field = error.status === 422 ? (error.problem?.errors?.[0]?.code ?? null) : null;
    return avatarRefusalText(field) !== null || avatarRefusalText(error.code) !== null;
  }
}
