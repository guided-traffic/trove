import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { RepositoriesApi } from '../api/repositories-api';
import { SessionApi } from '../api/session-api';
import { SystemApi } from '../api/system-api';
import {
  FakeSessionApi,
  FakeSystemApi,
  provideFakeApis,
  sampleInfo,
} from '../api/testing/fakes';
import { FakeRepositoriesApi, GENERIC_FORMAT, PACKAGE_FORMAT } from '../api/testing/fakes-catalog';
import { ServerAddress, protocolPath } from './server-address';

/** The protocol paths are the format registry's data; the spec spells them from this prefix. */
const API = '/api';

function setup(capabilities: readonly string[]) {
  TestBed.resetTestingModule();
  TestBed.configureTestingModule({
    providers: [provideRouter([]), ...provideFakeApis()],
  });
  (TestBed.inject(SessionApi) as FakeSessionApi).meResult = { capabilities: [...capabilities] };
  return {
    address: TestBed.inject(ServerAddress),
    system: TestBed.inject(SystemApi) as FakeSystemApi,
    repos: TestBed.inject(RepositoriesApi) as FakeRepositoriesApi,
  };
}

describe('ServerAddress', () => {
  it('R-UI-021 a holder of settings:read gets the base URL of the installation facts, not the origin of the page', async () => {
    const { address, system } = setup(['settings:read']);
    system.infoResult = sampleInfo({ baseUrl: 'https://packages.example.org/' });
    expect(await address.baseUrl()).toBe('https://packages.example.org');
    expect(location.origin).not.toBe('https://packages.example.org');
  });

  it('R-UI-021 any other caller reads the base URL from the absolute address of a repository it may see', async () => {
    const { address, system, repos } = setup([]);
    system.infoResult = sampleInfo({ baseUrl: 'https://must-not-be-asked.example' });
    repos.formatsResult = [GENERIC_FORMAT, PACKAGE_FORMAT];
    expect(await address.baseUrl()).toBe('https://trove.example.net');
  });

  it('R-UI-021 an unreadable system info falls back to the repository addresses', async () => {
    const { address, system } = setup(['settings:read']);
    system.info = async () => {
      throw new Error('forbidden');
    };
    expect(await address.baseUrl()).toBe('https://trove.example.net');
  });

  it('R-UI-021 no readable repository and no facts answer null, and null is not remembered', async () => {
    const { address, repos } = setup([]);
    const all = repos.repositories;
    repos.repositories = [];
    expect(await address.baseUrl()).toBeNull();
    repos.repositories = all;
    expect(await address.baseUrl()).toBe('https://trove.example.net');
  });

  it('R-UI-021 a known base URL is asked for once', async () => {
    const { address, system } = setup(['settings:read']);
    const info = vi.spyOn(system, 'info');
    await Promise.all([address.baseUrl(), address.baseUrl()]);
    await address.baseUrl();
    expect(info).toHaveBeenCalledTimes(1);
  });

  it('R-REPO-002 the protocol path fills the key, percent-encoded', () => {
    expect(protocolPath(PACKAGE_FORMAT, 'web-libs')).toBe(API + '/npm/web-libs/');
  });
});
