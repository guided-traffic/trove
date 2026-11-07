import { ApiError } from '../../api/problem';
import { describeProblem } from '../../core/http/problem-messages';

/**
 * The text for a refusal of an avatar, from the UI's own check or the server's answer (R-UI-042, R-UI-051). The server
 * names the reason of a 422 in `errors[0].code`; the other refusals are told by the problem code.
 */
export function avatarRefusalText(code: string | null): string | null {
  switch (code) {
    case 'malformed_request':
      return $localize`:@@avatar.err.empty:The file is empty.`;
    case 'unsupported_media_type':
      return $localize`:@@avatar.err.type:Only PNG, JPEG and WebP images are accepted.`;
    case 'payload_too_large':
      return $localize`:@@avatar.err.size:The image is larger than 1 MiB.`;
    case 'animated_image':
      return $localize`:@@avatar.err.animated:Animated images are not accepted. Choose an image with a single frame.`;
    case 'invalid_dimensions':
      return $localize`:@@avatar.err.dimensions:The image must be at least 32 by 32 pixels and at most 4,194,304 pixels in all.`;
    case 'undecodable_image':
      return $localize`:@@avatar.err.undecodable:The file could not be read as an image. It may be damaged or cut off.`;
    case 'server_busy':
      return $localize`:@@avatar.err.busy:The server is busy processing other images. Try again in a moment.`;
    default:
      return null;
  }
}

/** The message of a failed upload or removal: the avatar texts first, then the general ones of `describeProblem`. */
export function avatarErrorText(error: ApiError): string {
  const field = error.status === 422 ? (error.problem?.errors?.[0]?.code ?? null) : null;
  return avatarRefusalText(field) ?? avatarRefusalText(error.code) ?? describeProblem(error).text;
}
