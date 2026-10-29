// Verifies that the semantic-release dependency set in package.json can still produce a release:
// it drives the two plugins whose contract breaks between versions (commit-analyzer and
// release-notes-generator, each loading the conventionalcommits preset) through the exact plugin
// configuration in .releaserc.json, against a synthetic commit set.
//
// Why this exists: the release job is the only place npm dependencies are exercised, and it runs
// on pushes to main, after a Renovate bump has merged. A preset and a writer that disagree make
// the release fail with a handlebars "Missing helper" error while every pull request check is
// green. This script renders the release notes with the installed writer, so an incompatible
// bump goes red in the pull request's release-tooling job instead of on main.
//
// Run via: make test-release-tooling (CI: the release-tooling job).

import { readFile } from "node:fs/promises";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import { pathToFileURL } from "node:url";

const RELEASERC_URL = new URL("../.releaserc.json", import.meta.url);

// semantic-release loads a plugin from its own install first and from the project only when that
// finds nothing (lib/plugins/utils.js: importFrom.silent(__dirname, name) || importFrom(cwd, name)).
// Its own dependency on a plugin therefore wins over the project's pin whenever the two ranges
// disagree and npm nests a second copy under node_modules/semantic-release. This check resolves
// the plugins the same way, or it renders with a copy the release job never loads.
const projectRequire = createRequire(import.meta.url);
const semanticReleaseRequire = createRequire(
  join(
    dirname(projectRequire.resolve("semantic-release/package.json")),
    "lib",
    "plugins",
    "utils.js",
  ),
);

async function loadPlugin(name) {
  let path;
  try {
    path = semanticReleaseRequire.resolve(name);
  } catch {
    path = projectRequire.resolve(name);
  }
  const { version } = JSON.parse(
    await readFile(join(dirname(path), "package.json"), "utf8"),
  );
  console.log(`    ${name} ${version} from ${path.replace(process.cwd() + "/", "")}`);
  return import(pathToFileURL(path).href);
}

// One commit per release type the analyzer must distinguish, subjects chosen so the rendered
// notes can be asserted on.
const COMMITS = [
  {
    hash: "1111111111111111111111111111111111111111",
    message: "fix(web): keep the session after a token refresh",
  },
  {
    hash: "2222222222222222222222222222222222222222",
    message: "feat(api): add a list endpoint for repositories",
  },
  {
    hash: "3333333333333333333333333333333333333333",
    message:
      "feat(api)!: rename the search parameter\n\nBREAKING CHANGE: the q parameter is now query",
  },
  {
    hash: "4444444444444444444444444444444444444444",
    message: "chore(deps): update dependency example to v2",
  },
];

const logger = {
  log: () => {},
  error: () => {},
  success: () => {},
  warn: () => {},
};

function fail(message, cause) {
  console.error(`FAIL: ${message}`);
  if (cause) console.error(cause);
  process.exit(1);
}

// Take the plugin configs from .releaserc.json itself so this check cannot drift from what the
// release job actually loads.
function pluginConfigFrom(releaserc, pluginName) {
  for (const entry of releaserc.plugins) {
    if (entry === pluginName) return {};
    if (Array.isArray(entry) && entry[0] === pluginName) return entry[1] ?? {};
  }
  return fail(`${pluginName} is not configured in .releaserc.json`);
}

const releaserc = JSON.parse(await readFile(RELEASERC_URL, "utf8"));

const context = {
  commits: COMMITS,
  logger,
  cwd: process.cwd(),
  options: {
    repositoryUrl: "https://github.com/guided-traffic/trove.git",
  },
  lastRelease: { gitTag: "v0.1.0", gitHead: COMMITS[0].hash, version: "0.1.0" },
  nextRelease: { gitTag: "v0.2.0", gitHead: COMMITS[3].hash, version: "0.2.0" },
};

// Step 1: the analyzer must load the preset and classify the commit set. The breaking change is
// a minor release while trove stays on major version 0 (releaseRules in .releaserc.json).
const { analyzeCommits } = await loadPlugin("@semantic-release/commit-analyzer");
const analyzerConfig = pluginConfigFrom(releaserc, "@semantic-release/commit-analyzer");

let releaseType;
try {
  releaseType = await analyzeCommits(analyzerConfig, context);
} catch (error) {
  fail("analyzeCommits threw - the commit-analyzer/preset pair is broken", error);
}
if (releaseType !== "minor") {
  fail(
    `analyzeCommits returned "${releaseType}" for a commit set with a BREAKING CHANGE - expected "minor" (major version 0)`,
  );
}

// Step 2: the notes generator must render the notes with the writer it ships. This is the step
// that breaks when the preset's template calls a helper the installed writer does not register.
const { generateNotes } = await loadPlugin("@semantic-release/release-notes-generator");
const notesConfig = pluginConfigFrom(releaserc, "@semantic-release/release-notes-generator");

let notes;
try {
  notes = await generateNotes(notesConfig, context);
} catch (error) {
  fail("generateNotes threw - the release-notes-generator/preset/writer set is broken", error);
}

// A render that silently drops sections is as broken as one that throws: assert the type sections
// and the commit subjects actually appear.
const mustContain = [
  "Features",
  "Bug Fixes",
  "keep the session after a token refresh",
  "add a list endpoint for repositories",
  "rename the search parameter",
];
for (const needle of mustContain) {
  if (!notes.includes(needle)) {
    fail(
      `rendered release notes are missing "${needle}" - the writer/preset pair renders incomplete notes\n--- rendered notes ---\n${notes}`,
    );
  }
}

console.log("OK: release tooling renders release notes");
console.log(`    analyzeCommits -> ${releaseType}`);
console.log(`    generateNotes  -> ${notes.length} chars, all sections present`);
