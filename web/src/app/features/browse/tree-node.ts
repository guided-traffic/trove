import { WritableSignal, signal } from '@angular/core';
import { ApiError } from '../../api/problem';

/** `member` is a member of a virtual repository, shown under it in resolution order (R-UI-010). */
export type NodeKind = 'repository' | 'folder' | 'file' | 'member';

/** What the tree shows next to a node: a repository's class and format, the size and time of an entry. */
export interface NodeMeta {
  readonly cls?: string;
  readonly format?: string;
  readonly disabled?: boolean;
  /** Files only, from the listing. */
  readonly size?: number;
  readonly modifiedAt?: string;
  /** For a member of a virtual repository: the key of the virtual repository, and the 1-based position. */
  readonly via?: string;
  readonly position?: number;
}

/** The id of a node: key and path joined by a slash, or `<via>><key>` for a virtual repository's member. */
export function nodeId(repository: string, path: string, via?: string): string {
  if (via) return `${via}>${repository}`;
  return path === '' ? repository : `${repository}/${path}`;
}

/** Prefix of the value of a "Load more" entry in the tree. */
export const MORE_PREFIX = 'more:';

/**
 * One node of the file tree: a repository, a folder or a file. The state of its children is
 * signals, so the tree template re-renders only what changed. A repository key cannot contain a
 * slash, so `id` (key and path joined by one) is unambiguous.
 */
export class TreeNode {
  readonly children = signal<readonly TreeNode[]>([]);
  readonly expanded = signal(false);
  readonly loading = signal(false);
  readonly loaded = signal(false);
  readonly hasMore = signal(false);
  readonly error = signal<ApiError | null>(null);
  /** The cursor of the next page of children, null when the last page was read. */
  cursor: string | null = null;
  /** The load of children that is in flight; a second request awaits it instead of starting another. */
  pending: Promise<void> | null = null;

  /** Class, format and flags of a repository node; absent until the list or the repository read says them. */
  readonly meta: WritableSignal<NodeMeta>;
  /** True when a virtual repository's members are not readable by the caller (consumer view). */
  readonly membersHidden = signal(false);

  constructor(
    readonly repository: string,
    /** The path inside the repository; empty for the repository itself. */
    readonly path: string,
    readonly kind: NodeKind,
    readonly label: string,
    meta: NodeMeta = {},
  ) {
    this.meta = signal(meta);
    this.id = nodeId(repository, path, meta.via);
  }

  /** The tree value of the node; a member of a virtual repository is `<virtual>><member>`. */
  readonly id: string;

  get expandable(): boolean {
    return this.kind === 'repository' || this.kind === 'folder';
  }
}

/** The ancestors of a path from the root down: `a/b/c.txt` gives `a`, `a/b`. */
export function ancestorsOf(path: string): string[] {
  const parts = path.split('/').filter((p) => p !== '');
  return parts.slice(0, -1).map((_, i) => parts.slice(0, i + 1).join('/'));
}

/** The parent folder of a path; empty for an item at the root of the repository. */
export function parentOf(path: string): string {
  const i = path.lastIndexOf('/');
  return i < 0 ? '' : path.slice(0, i);
}

/** The last segment of a path. */
export function nameOf(path: string): string {
  return path.slice(path.lastIndexOf('/') + 1);
}
