// Regenerates prepared.json: for every case of corpus.json the manifest as
// npm's prepare() step of @npmcli/package-json normalizes it in a directory that
// holds the case's files. The Go test requires that the request manifest and
// this output compare equal after the normalization of R-NPM-007.
//
//   node generate.js <path to node_modules/@npmcli/package-json> <output file>
//
// The package ships inside npm (npm/node_modules/@npmcli/package-json); the
// result is committed, nothing here runs in the build.
'use strict'

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

const PackageJson = require(path.resolve(process.argv[2]))
const corpus = JSON.parse(fs.readFileSync(path.join(__dirname, 'corpus.json'), 'utf8'))

async function main () {
  const out = {}
  for (const c of corpus) {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'prepare-'))
    for (const f of c.files) {
      fs.mkdirSync(path.dirname(path.join(dir, f)), { recursive: true })
      fs.writeFileSync(path.join(dir, f), '')
    }
    fs.writeFileSync(path.join(dir, 'package.json'), JSON.stringify(c.manifest))
    const pkg = await PackageJson.load(dir)
    await pkg.prepare()
    const keep = ['name', 'version', 'dependencies', 'optionalDependencies', 'peerDependencies',
      'peerDependenciesMeta', 'bundleDependencies', 'bin', 'os', 'cpu', 'engines', 'scripts']
    const m = {}
    for (const k of keep) {
      if (k in pkg.content) m[k] = pkg.content[k]
    }
    out[c.name] = m
    fs.rmSync(dir, { recursive: true, force: true })
  }
  fs.writeFileSync(process.argv[3], JSON.stringify(out, null, 2) + '\n')
}

main().catch((e) => { console.error(e); process.exit(1) })
