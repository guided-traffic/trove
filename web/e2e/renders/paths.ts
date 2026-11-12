import { join, resolve } from 'node:path';

/** The repository root: web/e2e/renders/../../.. */
export const root = resolve(__dirname, '..', '..', '..');
/** The edge of the UI development profile: the dev server, which proxies everything outside /ui/ to trove. */
export const edge = process.env['UI_RENDERS_EDGE'] ?? 'https://localhost:4200';
/** The generated state of the profile (deploy/ui-dev/up.sh): the break-glass password. */
export const stateDir = join(root, '.dev', 'ui-dev');
/** Where the pictures, the contact sheet and the report go. */
export const outDir = join(root, '.out', 'ui-renders');
/** The session of one kind of user, kept by the setup. */
export const authFile = (kind: string): string => join(outDir, '.auth', `${kind}.json`);
/** One line of JSON per picture and per failure, written by the renders and read by the contact sheet. */
export const manifestDir = join(outDir, '.manifest');
