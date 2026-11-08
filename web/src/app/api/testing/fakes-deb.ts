import { Provider } from '@angular/core';
import { DebApi, DebIndexState, DebSigningKey, DebSigningKeyCreate } from '../deb-api';
import { JobReference } from '../jobs-api';
import { ApiError } from '../problem';

/*
 * The in-memory fake of the Debian operations (R-UI-047 component tier). It follows the state rules of R-DEB-015 and R-DEB-017:
 * one primary, at most one next key, a primary cannot be retired directly, a withheld key is confirmed or reinstated. It holds
 * no private key; a created key gets a made-up fingerprint and certificate.
 */

export const DEB_REPOSITORY = 'web/deb-local';

export function debKey(fingerprint: string, state: DebSigningKey['state'], over: Partial<DebSigningKey> = {}): DebSigningKey {
  return {
    fingerprint,
    algorithm: 22,
    bits: 256,
    keyCreatedAt: '2026-11-01T08:00:00Z',
    boundSince: '2026-11-01T08:00:00Z',
    state,
    origin: 'generated',
    publicKey: `-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nfake-${fingerprint}\n-----END PGP PUBLIC KEY BLOCK-----\n`,
    createdAt: '2026-11-01T09:00:00Z',
    ...over,
  };
}

export const DEB_PRIMARY = debKey('3F2A0000000000000000000000000000000090C0', 'primary');

function problem(status: number, code: string, detail: string): ApiError {
  return new ApiError(status, { code, detail }, null);
}

export class FakeDebApi extends DebApi {
  keys: DebSigningKey[] = [DEB_PRIMARY];
  index: DebIndexState = {
    suites: [
      {
        distribution: 'stable',
        changeSeq: 42,
        publishedSeq: 42,
        generation: 17,
        releaseDate: '2026-11-08T10:00:00Z',
        state: 'current',
        signedBy: [DEB_PRIMARY.fingerprint],
      },
      {
        distribution: 'testing',
        changeSeq: 9,
        publishedSeq: 8,
        generation: 5,
        releaseDate: '2026-11-08T09:58:12Z',
        state: 'pending',
      },
    ],
  };
  /** The refusal the next call of any operation throws once (for the failure paths). */
  failure: ApiError | null = null;
  calls: string[] = [];
  /** The job a reindex queues; a failure to set instead answers 409 `reindex_running`. */
  reindexJob: JobReference | null = null;
  private serial = 0;

  private enter(call: string): void {
    this.calls.push(call);
    if (this.failure) {
      const e = this.failure;
      this.failure = null;
      throw e;
    }
  }

  private find(fingerprint: string): DebSigningKey {
    const k = this.keys.find((x) => x.fingerprint === fingerprint);
    if (!k) throw problem(404, 'not_found', 'no such key');
    return k;
  }

  private put(k: DebSigningKey): void {
    this.keys = this.keys.map((x) => (x.fingerprint === k.fingerprint ? k : x));
  }

  private has(state: DebSigningKey['state']): boolean {
    return this.keys.some((k) => k.state === state);
  }

  override async signingKeys(): Promise<readonly DebSigningKey[]> {
    this.enter('signingKeys');
    return this.keys;
  }

  override async createSigningKey(_ref: string, body: DebSigningKeyCreate): Promise<DebSigningKey> {
    this.enter(body.generate ? 'create:generate' : 'create:import');
    if (this.has('next')) throw problem(409, 'deb_key_state', 'a next key exists');
    if (!body.generate && !body.privateKey) throw problem(422, 'deb_key_unsupported', 'no key given');
    const n = String(++this.serial).padStart(4, '0');
    const key = debKey(`A1B2${n}000000000000000000000000000000FF`.slice(0, 40), 'next', {
      origin: body.generate ? 'generated' : 'imported',
      createdAt: '2026-11-09T09:00:00Z',
    });
    this.keys = [...this.keys, key];
    return key;
  }

  override async promoteSigningKey(): Promise<DebSigningKey> {
    this.enter('promote');
    const next = this.keys.find((k) => k.state === 'next');
    if (!next) throw problem(409, 'deb_no_next_key', 'there is no next key');
    this.keys = this.keys.map((k) =>
      k.state === 'primary'
        ? { ...k, state: 'retired', retiredAt: '2026-11-09T10:00:00Z' }
        : k.fingerprint === next.fingerprint
          ? { ...k, state: 'primary', promotedAt: '2026-11-09T10:00:00Z' }
          : k,
    );
    return this.find(next.fingerprint);
  }

  override async retireSigningKey(_ref: string, fingerprint: string): Promise<void> {
    this.enter(`retire:${fingerprint}`);
    const k = this.find(fingerprint);
    if (k.state !== 'next' && k.state !== 'withheld') throw problem(409, 'deb_key_state', 'the key cannot be retired');
    this.put({ ...k, state: 'retired', retiredAt: '2026-11-09T10:00:00Z' });
  }

  override async reinstateSigningKey(_ref: string, fingerprint: string): Promise<DebSigningKey> {
    this.enter(`reinstate:${fingerprint}`);
    const k = this.find(fingerprint);
    if (k.state !== 'withheld') throw problem(409, 'deb_key_state', 'the key is not withheld');
    const state = !this.has('primary') ? 'primary' : !this.has('next') ? 'next' : null;
    if (!state) throw problem(409, 'deb_key_state', 'the repository has a primary and a next key');
    const back: DebSigningKey = { ...k, state, withheldAt: null };
    this.put(back);
    return back;
  }

  override async indexState(): Promise<DebIndexState> {
    this.enter('indexState');
    return this.index;
  }

  override async reindex(): Promise<JobReference> {
    this.enter('reindex');
    if (!this.reindexJob) throw problem(409, 'reindex_running', 'a reindex is queued or running');
    return this.reindexJob;
  }
}

export function provideDebFakes(): Provider[] {
  return [{ provide: DebApi, useClass: FakeDebApi }];
}
