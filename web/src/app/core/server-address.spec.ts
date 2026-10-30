import { TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { RepositoriesApi } from '../api/repositories-api';
import { SessionApi } from '../api/session-api';
import { FakeSessionApi, provideFakeApis } from '../api/testing/fakes';
import { FakeRepositoriesApi, PACKAGE_FORMAT } from '../api/testing/fakes-catalog';
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
    repos: TestBed.inject(RepositoriesApi) as FakeRepositoriesApi,
  };
}

describe('ServerAddress', () => {
  it('R-UI-021 the base URL is the one the server configured, not the origin of the page', async () => {
    const { address, repos } = setup([]);
    repos.baseUrlResult = 'https://packages.example.org/';
    expect(await address.baseUrl()).toBe('https://packages.example.org');
    expect(location.origin).not.toBe('https://packages.example.org');
  });

  it('R-REPO-002 a caller without settings:read and without a readable repository still has an address (Q-UI-75)', async () => {
    const { address, repos } = setup(['repositories:write-config']);
    repos.repositories = [];
    expect(await address.baseUrl()).toBe('https://trove.example.net');
  });

  it('R-UI-021 a server without a configured base URL answers null, and null is not remembered', async () => {
    const { address, repos } = setup([]);
    repos.baseUrlResult = null;
    expect(await address.baseUrl()).toBeNull();
    repos.baseUrlResult = 'https://trove.example.net';
    expect(await address.baseUrl()).toBe('https://trove.example.net');
  });

  it('R-UI-021 an unreadable answer is null and is asked again', async () => {
    const { address, repos } = setup([]);
    repos.baseUrl = async () => {
      throw new Error('offline');
    };
    expect(await address.baseUrl()).toBeNull();
    repos.baseUrl = async () => 'https://trove.example.net/';
    expect(await address.baseUrl()).toBe('https://trove.example.net');
  });

  it('R-UI-021 a known base URL is asked for once', async () => {
    const { address, repos } = setup([]);
    await Promise.all([address.baseUrl(), address.baseUrl()]);
    await address.baseUrl();
    expect(repos.baseUrlCalls).toBe(1);
  });

  it('R-REPO-002 the protocol path fills the key, percent-encoded', () => {
    expect(protocolPath(PACKAGE_FORMAT, 'web-libs')).toBe(API + '/npm/web-libs/');
  });
});
