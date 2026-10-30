import { expect } from 'vitest';
import { Provider, Type } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { MATERIAL_ANIMATIONS } from '@angular/material/core';
import { provideRouter } from '@angular/router';
import { provideFakeApis } from '../api/testing/fakes';
import { SessionApi } from '../api/session-api';
import { FakeSessionApi } from '../api/testing/fakes';
import { SessionStore } from '../core/session-store';
import { BrowserStorage } from '../core/storage';
import { seriousViolations } from './axe';

export interface RenderedScreen<T> {
  readonly fixture: ComponentFixture<T>;
  readonly root: HTMLElement;
  /** A service of the test injector: the fake bound to an abstract service class, or a store. */
  get<A>(token: abstract new (...args: never[]) => A): A;
}

export interface RenderOptions {
  /** Component inputs, set as the router would bind route and query parameters. */
  readonly inputs?: Readonly<Record<string, unknown>>;
  /** Capabilities of `authz/me`; the default is the administrator's. */
  readonly capabilities?: readonly string[];
  /** Further providers of the test module. */
  readonly providers?: readonly Provider[];
  /** Runs after the providers exist and before the component is created; may be asynchronous. */
  readonly setup?: (get: <A>(token: abstract new (...args: never[]) => A) => A) => unknown;
}

/** Lets promises, resources and effects settle: a few rounds of `whenStable`. */
export async function settle(fixture: ComponentFixture<unknown>, rounds = 6): Promise<void> {
  for (let i = 0; i < rounds; i++) {
    await fixture.whenStable();
    fixture.detectChanges();
  }
}

/** Waits for real timers and macrotasks (file reads, hashing) that `whenStable` does not see. */
export async function sleep(fixture: ComponentFixture<unknown>, ms = 60): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, ms));
  await settle(fixture);
}

/** Renders one screen against the in-memory fakes (R-UI-047 component tier). */
export async function renderScreen<T>(
  component: Type<T>,
  options: RenderOptions = {},
): Promise<RenderedScreen<T>> {
  TestBed.resetTestingModule();
  await TestBed.configureTestingModule({
    imports: [component],
    // Dialogs close at once: jsdom plays no CSS animation, so `afterClosed` would never fire.
    providers: [
      // A catch-all: a screen that navigates after a save (a dialog closes at once here) must find a route.
      provideRouter([{ path: '**', children: [] }]),
      ...provideFakeApis(),
      ...(options.providers ?? []),
      { provide: MATERIAL_ANIMATIONS, useValue: { animationsDisabled: true } },
    ],
  }).compileComponents();
  const get = <A>(token: abstract new (...args: never[]) => A): A =>
    TestBed.inject(token as never) as A;
  if (options.capabilities) {
    (TestBed.inject(SessionApi) as FakeSessionApi).meResult = {
      capabilities: [...options.capabilities],
    };
  }
  await options.setup?.(get);
  await TestBed.inject(SessionStore).load();
  const fixture = TestBed.createComponent(component);
  for (const [name, value] of Object.entries(options.inputs ?? {})) {
    fixture.componentRef.setInput(name, value);
  }
  await settle(fixture);
  return { fixture, root: fixture.nativeElement as HTMLElement, get };
}

/** The text content of an element with whitespace collapsed. */
export const text = (root: Element): string => root.textContent?.replace(/\s+/g, ' ').trim() ?? '';

/** Fails the spec on a serious or critical axe violation in `root` (R-UI-044). */
export async function expectAccessible(root: Element): Promise<void> {
  expect(await seriousViolations(root)).toEqual([]);
}

export function button(root: ParentNode, label: string | RegExp): HTMLButtonElement {
  const found = [...root.querySelectorAll('button')].find((b) =>
    typeof label === 'string'
      ? (b.textContent ?? '').trim() === label
      : label.test(b.textContent ?? ''),
  );
  if (!found) throw new Error(`no button "${String(label)}"`);
  return found;
}

/** Types into an input or textarea the way the browser reports it. */
export function type(
  el: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement,
  value: string,
): void {
  el.value = value;
  el.dispatchEvent(
    new Event(el instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }),
  );
}

/** Opens the filter panel of a list screen (a no-op when an applied filter opened it already). */
export async function openFilters(fixture: ComponentFixture<unknown>, root: ParentNode): Promise<void> {
  if (root.querySelector('trove-filter-panel form')) return;
  button(root, /^\s*filter_list\s*Filters/).click();
  await settle(fixture);
}

/** The button or link whose text contains `label`, searched under `root`. */
export function buttonByText(root: ParentNode, label: string): HTMLButtonElement | HTMLAnchorElement {
  const found = [...root.querySelectorAll<HTMLButtonElement | HTMLAnchorElement>('button, a')].find((b) =>
    text(b).includes(label),
  );
  if (!found) throw new Error(`no button with text "${label}"`);
  return found;
}

/** Sets the value of the Material field labelled `label` and fires the matching event. */
export function typeInto(root: ParentNode, label: string, value: string): void {
  const field = [...root.querySelectorAll('mat-form-field')].find((f) =>
    text(f.querySelector('mat-label') ?? f).startsWith(label),
  );
  const input = field?.querySelector<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>(
    'input, textarea, select',
  );
  if (!input) throw new Error(`no field labelled "${label}"`);
  type(input, value);
}

/** Browser storage through the one wrapper that may touch it (R-UI-046). */
export const browserStorage = new BrowserStorage();

/** Every key and value of both storage areas as one string, to prove that a secret is not in it. */
export function storedText(): string {
  return JSON.stringify([...browserStorage.entries('local'), ...browserStorage.entries('session')]);
}
