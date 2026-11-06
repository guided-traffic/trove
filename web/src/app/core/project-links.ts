import { splitRef } from '../api/repositories-api';

/**
 * The router commands of a repository's own screen, given its qualified name `<project>/<key>` (R-REPO-002): a
 * repository is shown inside its project (Q-UI-129).
 */
export function repositoryCommands(ref: string): string[] {
  const { project, key } = splitRef(ref);
  return ['/projects', project, 'repositories', key];
}

/** The router commands of a repository's file tree at `path` (empty for the repository itself). */
export function browseCommands(ref: string, path: readonly string[] = []): string[] {
  const { project, key } = splitRef(ref);
  return ['/projects', project, 'browse', key, ...path.filter((s) => s !== '')];
}
