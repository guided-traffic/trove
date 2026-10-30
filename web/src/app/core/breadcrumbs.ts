import { Injectable, computed, inject } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { ActivatedRouteSnapshot, NavigationEnd, Router } from '@angular/router';
import { filter, map, startWith } from 'rxjs';

/** One step of the trail above the page title. */
export interface Crumb {
  readonly label: string;
  /** Router commands of the target; null for a step that is only a name (an area without a page). */
  readonly commands: readonly string[] | null;
  /** The label is data (a repository key, a user name): not translated, not hyphenated by the browser. */
  readonly literal: boolean;
}

/** A crumb declared on a route's `data.crumbs` (the area names and wrapper routes that have no title). */
export interface CrumbSpec {
  readonly label: string;
  readonly link: boolean;
}

/**
 * The breadcrumb trail of the current route (R-UI-002, R-UI-003), derived from the route
 * configuration and never maintained by screens:
 *
 * - `data.crumbs` of a route adds the named steps of an area ("Administration"), with or without a link;
 * - a route with a string `title` adds a step with that title;
 * - a parameter segment (`:key`) adds a step with the parameter's value, so a detail screen reads
 *   "Repositories > my-repo";
 * - the file tree's matcher route adds one step per path segment;
 * - a matcher route with `data.crumbParam` adds one step with that parameter's value (a package
 *   name such as `@team-a/ui`, whose slash belongs to the name).
 *
 * A trail of one step repeats the page title and is not shown.
 */
@Injectable({ providedIn: 'root' })
export class Breadcrumbs {
  private readonly router = inject(Router);

  // A fresh wrapper per navigation, so that the signal notices even a navigation that keeps the
  // same route objects.
  private readonly snapshot = toSignal(
    this.router.events.pipe(
      filter((e): e is NavigationEnd => e instanceof NavigationEnd),
      map(() => ({ root: this.router.routerState.snapshot.root })),
      startWith({ root: this.router.routerState.snapshot.root }),
    ),
    { requireSync: true },
  );

  /** The trail; empty when it would only repeat the page title. */
  readonly trail = computed<readonly Crumb[]>(() => {
    const crumbs = collect(this.snapshot().root);
    return crumbs.length > 1 ? crumbs : [];
  });
}

/** Builds the trail from a snapshot tree (exported for the unit tests). */
export function collect(root: ActivatedRouteSnapshot): Crumb[] {
  const out: Crumb[] = [];
  const add = (crumb: Crumb): void => {
    const last = out[out.length - 1];
    const same =
      last?.label === crumb.label && (last.commands?.join('/') ?? null) === (crumb.commands?.join('/') ?? null);
    if (!same) out.push(crumb);
  };

  let route = root.firstChild;
  let commands: string[] = ['/'];
  while (route) {
    const config = route.routeConfig;
    const own = route.url.map((s) => s.path);
    const before = commands;
    commands = [...commands, ...own];

    const specs = config?.data?.['crumbs'] as readonly CrumbSpec[] | undefined;
    for (const spec of specs ?? []) {
      add({ label: spec.label, commands: spec.link ? commands : null, literal: false });
    }

    const title = typeof config?.title === 'string' ? config.title : undefined;
    const crumbParam = config?.data?.['crumbParam'] as string | undefined;
    if (config && config.path === undefined && config.matcher && crumbParam) {
      // A matcher route whose parameter names the thing (a package name with its slash): one step.
      add({ label: route.params[crumbParam] ?? crumbParam, commands: [...before, ...own], literal: true });
    } else if (config && config.path === undefined && config.matcher) {
      // The file tree: the first segment is the repository, the rest is the path inside it.
      own.forEach((segment, i) => add({ label: segment, commands: [...before, ...own.slice(0, i + 1)], literal: true }));
    } else if (config && title && config.path !== undefined) {
      const pieces = config.path.split('/');
      pieces.forEach((piece, i) => {
        const here = [...before, ...own.slice(0, i + 1)];
        if (piece.startsWith(':')) {
          add({ label: route!.params[piece.slice(1)] ?? piece, commands: here, literal: true });
        } else if (i === pieces.length - 1) {
          add({ label: title, commands: here, literal: false });
        }
      });
    }
    route = route.firstChild;
  }
  return out;
}
