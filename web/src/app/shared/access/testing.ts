import { Provider, Type } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { SessionApi } from '../../api/session-api';
import { FakeSessionApi, provideFakeApis } from '../../api/testing/fakes';
import { SessionStore } from '../../core/session-store';
import { BrowserStorage } from '../../core/storage';

export interface Rendered<T> {
  readonly fixture: ComponentFixture<T>;
  readonly root: HTMLElement;
  /** Looks up a service of the test injector (the fakes, the stores). */
  readonly get: <S>(token: Type<S> | (abstract new (...args: never[]) => S)) => S;
}

export interface RenderOptions {
  /** Component inputs, set before the first change detection (route parameters, query parameters). */
  readonly inputs?: Readonly<Record<string, unknown>>;
  readonly providers?: readonly Provider[];
  /** Runs after the fakes exist and before the component is created. */
  readonly setup?: (get: Rendered<unknown>["get"]) => unknown;
  /** Capabilities of the signed-in caller; the default is every administrator capability. */
  readonly capabilities?: readonly string[];
}

/**
 * Lets pending promises of the fakes resolve and the zoneless change detection run. `ms` waits
 * first, for a dialog that closes after its exit animation.
 */
export async function settle(fixture: ComponentFixture<unknown>, ms = 0): Promise<void> {
  for (let i = 0; i < 6; i++) {
    await new Promise<void>((resolve) => setTimeout(resolve, i === 0 ? ms : 0));
    fixture.detectChanges();
    await fixture.whenStable();
  }
}

/** Renders a standalone screen against the in-memory fakes with a signed-in caller. */
export async function renderScreen<T>(component: Type<T>, options: RenderOptions = {}): Promise<Rendered<T>> {
  TestBed.resetTestingModule();
  await TestBed.configureTestingModule({
    imports: [component],
    providers: [provideRouter([]), ...provideFakeApis(), ...(options.providers ?? [])],
  }).compileComponents();
  const get: Rendered<T>['get'] = (token) => TestBed.inject(token as Type<never>) as never;
  if (options.capabilities) {
    (get(SessionApi) as FakeSessionApi).meResult = { capabilities: [...options.capabilities] };
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
export function textOf(el: Element): string {
  return (el.textContent ?? '').replace(/\s+/g, ' ').trim();
}

/** The button whose text contains `label`, searched under `root`. */
export function buttonByText(root: ParentNode, label: string): HTMLButtonElement | HTMLAnchorElement {
  const found = [...root.querySelectorAll<HTMLButtonElement | HTMLAnchorElement>('button, a')].find((b) =>
    textOf(b).includes(label),
  );
  if (!found) throw new Error(`no button with text "${label}"`);
  return found;
}

/** Sets the value of the input labelled `label` and fires the `input` event. */
export function typeInto(root: ParentNode, label: string, value: string): void {
  const field = [...root.querySelectorAll('mat-form-field')].find((f) => textOf(f.querySelector('mat-label') ?? f).startsWith(label));
  const input = field?.querySelector<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>('input, textarea, select');
  if (!input) throw new Error(`no field labelled "${label}"`);
  input.value = value;
  input.dispatchEvent(new Event(input instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }));
}

/** Browser storage through the one wrapper that may touch it (R-UI-046). */
export const browserStorage = new BrowserStorage();

/** Every key and value of both storage areas as one string, to prove that a secret is not in it. */
export function storedText(): string {
  return JSON.stringify([...browserStorage.entries('local'), ...browserStorage.entries('session')]);
}
