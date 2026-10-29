import { signal } from '@angular/core';
import { ApiError } from '../../api/problem';

export type NodeKind = 'repository' | 'folder' | 'file';

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

  constructor(
    readonly repository: string,
    /** The path inside the repository; empty for the repository itself. */
    readonly path: string,
    readonly kind: NodeKind,
    readonly label: string,
    /** For a repository, the class and format, shown next to its key. */
    readonly meta: {
      readonly cls?: string;
      readonly format?: string;
      readonly disabled?: boolean;
    } = {},
  ) {}

  get id(): string {
    return this.path === '' ? this.repository : `${this.repository}/${this.path}`;
  }

  get expandable(): boolean {
    return this.kind !== 'file';
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
