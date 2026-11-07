import { IdentityApi } from '../../api/identity-api';
import { FakeIdentityApi, problem } from '../../api/testing/access-fakes';
import { AvatarStore } from '../../core/avatar-store';
import { seriousViolations } from '../../testing/axe';
import { buttonByText, renderScreen, settle, sleep, text } from '../../testing/screen';
import { robotDataUri } from '../../shared/ui/robot';
import { AVATAR_MAX_BYTES, IMAGE_SIZER, ImageSizer } from './avatar-file';
import { Profile } from './profile';

const overlay = (): HTMLElement => document.querySelector('.cdk-overlay-container') as HTMLElement;
const SEED = '00000000-0000-7000-8000-000000000001';

function png(bytes = 2048, type = 'image/png'): File {
  return new File([new Uint8Array(bytes)], 'me.png', { type });
}

describe('Profile avatar (R-UI-051)', () => {
  async function render(sizer: ImageSizer = async () => ({ width: 256, height: 256 })) {
    const screen = await renderScreen(Profile, { providers: [{ provide: IMAGE_SIZER, useValue: sizer }] });
    const api = screen.get(IdentityApi) as FakeIdentityApi;
    const store = screen.get(AvatarStore);
    const section = screen.root.querySelector('trove-profile-avatar') as HTMLElement;
    const choose = async (file: File): Promise<void> => {
      const input = section.querySelector('input[type="file"]') as HTMLInputElement;
      Object.defineProperty(input, 'files', { configurable: true, value: { item: () => file, length: 1 } });
      input.dispatchEvent(new Event('change'));
      await sleep(screen.fixture, 30);
    };
    const preview = () => section.querySelector('trove-avatar img') as HTMLImageElement;
    const sent = () => api.calls.filter((c) => c.startsWith('setAvatar'));
    return { ...screen, api, store, section, choose, preview, sent };
  }

  it('R-UI-051 shows the robot of the user ID without an avatar and offers no removal', async () => {
    const { section, preview, root } = await render();
    expect(preview().getAttribute('src')).toBe(robotDataUri(SEED));
    expect(buttonByText(section, 'Choose file…')).toBeTruthy();
    expect(text(section)).not.toContain('Remove avatar');
    expect(text(section)).toContain('at most 1 MiB');
    expect(text(section)).toContain('centred square');
    expect(await seriousViolations(root)).toEqual([]);
  });

  it('R-UI-051 an upload sends the file, shows the returned avatar and offers the removal', async () => {
    const { api, store, section, preview, choose, sent } = await render();
    await choose(png());
    expect(sent()).toEqual(['setAvatar image/png 2048']);
    const url = store.url();
    expect(url).toMatch(/^\/api\/v1\/avatars\/[a-f0-9]{64}$/);
    expect(preview().getAttribute('src')).toBe(url);
    expect(text(section)).toContain('Remove avatar');
    expect((api.meResult as { avatarUrl: string }).avatarUrl).toBe(url);
  });

  it('R-UI-051 a replacement uses the new URL', async () => {
    const { store, preview, choose } = await render();
    await choose(png());
    const first = store.url();
    await choose(png(4096));
    expect(store.url()).not.toBe(first);
    expect(preview().getAttribute('src')).toBe(store.url());
  });

  it.each([
    [{ status: 415, code: 'unsupported_media_type', field: '' }, 'Only PNG, JPEG and WebP images are accepted.'],
    [{ status: 413, code: 'payload_too_large', field: '' }, 'The image is larger than 1 MiB.'],
    [{ status: 400, code: 'malformed_request', field: '' }, 'The file is empty.'],
    [{ status: 422, code: 'validation_failed', field: 'animated_image' }, 'Animated images are not accepted.'],
    [{ status: 422, code: 'validation_failed', field: 'invalid_dimensions' }, 'at least 32 by 32 pixels'],
    [{ status: 422, code: 'validation_failed', field: 'undecodable_image' }, 'could not be read as an image'],
    [{ status: 503, code: 'server_busy', field: '' }, 'The server is busy processing other images.'],
  ])('R-UI-051 the answer %j is shown in words and the avatar stays', async (answer, expected) => {
    const { api, store, section, choose } = await render();
    api.failure = problem(
      answer.status,
      answer.code,
      answer.field ? { errors: [{ pointer: '', code: answer.field }] } : {},
    );
    await choose(png());
    expect(section.querySelector('[role="alert"]')?.textContent).toContain(expected);
    expect(store.url()).toBeNull();
  });

  it('R-UI-051 an answer without an avatar text keeps the general notice with the request ID', async () => {
    const { api, section, choose } = await render();
    api.failure = problem(403, 'forbidden');
    await choose(png());
    expect(section.querySelector('.field-error')).toBeNull();
    expect(text(section)).toContain('You do not have the permission for this.');
  });

  it.each([
    ['a type that is not an image the server takes', () => png(2048, 'image/gif'), undefined, 'Only PNG, JPEG and WebP'],
    ['a file over 1 MiB', () => png(AVATAR_MAX_BYTES + 1), undefined, 'larger than 1 MiB'],
    ['an empty file', () => png(0), undefined, 'The file is empty.'],
    ['an image under 32 pixels', () => png(), async () => ({ width: 31, height: 400 }), 'at least 32 by 32'],
    ['an image over 4,194,304 pixels', () => png(), async () => ({ width: 2049, height: 2049 }), 'at most 4,194,304'],
    ['a file the browser cannot decode', () => png(), async () => null, 'could not be read as an image'],
  ])('R-UI-051 %s is refused before anything is sent', async (_name, file, sizer, expected) => {
    const { section, choose, sent } = await render(sizer as ImageSizer | undefined);
    await choose(file());
    expect(sent()).toEqual([]);
    expect(section.querySelector('[role="alert"]')?.textContent).toContain(expected);
  });

  it('R-UI-051 the limits themselves are accepted: 1 MiB, 32 by 32 and 4,194,304 pixels', async () => {
    const edge = await render(async () => ({ width: 32, height: 32 }));
    await edge.choose(png(AVATAR_MAX_BYTES));
    expect(edge.sent()).toHaveLength(1);
    const big = await render(async () => ({ width: 2048, height: 2048 }));
    await big.choose(png());
    expect(big.sent()).toHaveLength(1);
  });

  it('R-UI-051 removing asks first, then shows the robot again', async () => {
    const { fixture, api, store, section, preview, choose } = await render();
    await choose(png());
    buttonByText(section, 'Remove avatar').click();
    await settle(fixture);
    const confirm = overlay().querySelector('trove-confirm-dialog') as HTMLElement;
    expect(confirm.textContent).toContain('Remove your avatar?');
    buttonByText(confirm, 'Remove avatar').click();
    await sleep(fixture, 300);
    expect(api.calls).toContain('removeAvatar');
    expect(store.url()).toBeNull();
    expect(preview().getAttribute('src')).toBe(robotDataUri(SEED));
    expect(text(section)).not.toContain('Remove avatar');
  });
});
