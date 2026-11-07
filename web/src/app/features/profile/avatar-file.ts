import { InjectionToken } from '@angular/core';

/** What the server accepts (R-API-055); the UI refuses the rest before it sends anything. */
export const AVATAR_TYPES: readonly string[] = ['image/png', 'image/jpeg', 'image/webp'];
export const AVATAR_MAX_BYTES = 1_048_576;
export const AVATAR_MIN_SIDE = 32;
export const AVATAR_MAX_PIXELS = 4_194_304;

/** The codes of the server's refusals; the UI's own checks use the same ones, so one text per case serves both. */
export type AvatarRefusal =
  | 'malformed_request'
  | 'unsupported_media_type'
  | 'payload_too_large'
  | 'invalid_dimensions'
  | 'undecodable_image';

/** The pixel size of an image file, or null when the browser cannot decode it. */
export type ImageSizer = (file: Blob) => Promise<{ width: number; height: number } | null>;

export const IMAGE_SIZER = new InjectionToken<ImageSizer>('IMAGE_SIZER', {
  providedIn: 'root',
  factory: () => async (file) => {
    try {
      const bitmap = await createImageBitmap(file);
      const size = { width: bitmap.width, height: bitmap.height };
      bitmap.close();
      return size;
    } catch {
      return null;
    }
  },
});

/**
 * The checks that are cheap in the browser, in the order of the server's (R-UI-051): not empty, the type, the size, then
 * the pixel size read by decoding. The server repeats all of them and decides the format from the bytes; an animated
 * image is only found there. Returns the refusal, or null when the file may be sent.
 */
export async function refuseAvatar(file: File, sizer: ImageSizer): Promise<AvatarRefusal | null> {
  if (file.size === 0) return 'malformed_request';
  if (!AVATAR_TYPES.includes(file.type)) return 'unsupported_media_type';
  if (file.size > AVATAR_MAX_BYTES) return 'payload_too_large';
  const size = await sizer(file);
  if (size === null) return 'undecodable_image';
  if (
    size.width < AVATAR_MIN_SIDE ||
    size.height < AVATAR_MIN_SIDE ||
    size.width * size.height > AVATAR_MAX_PIXELS
  ) {
    return 'invalid_dimensions';
  }
  return null;
}
