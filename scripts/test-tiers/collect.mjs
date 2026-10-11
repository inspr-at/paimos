// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { resolve, relative, dirname, basename } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { formatManifest } from './manifests.mjs'
import { goFailures } from './failures.mjs'
import { cachedListing, cachedGoList, cachedGoSource, cacheEnabled, inputSnapshot, listingKey, goPackageKeys } from './listing-cache.mjs'
export const root = fileURLToPath(new URL('../../', import.meta.url))
export const web = resolve(root, 'web')
export const evidence = resolve(root, 'tmp/test-tiers')
export function command(bin, args, { cwd = root, env = process.env, timeout = 180_000, input } = {}) {
  const result = spawnSync(bin, args, { cwd, env, encoding: 'utf8', timeout, input, maxBuffer: 32*1024*1024 })
  if (result.error || result.status !== 0) {
    let output = result.stdout
    if (bin === 'go' && args.includes('-json')) {
      try {
        const failures = goFailures(output ?? '')
        if (failures.length) output = failures.map(row => `${row.owner}: ${row.output}`).join('\n')
      } catch { /* Interrupted JSON falls back to the captured output tail. */ }
    }
    const diagnostic = [output, result.stderr].map(text => text?.slice(-2000)).filter(Boolean).join('\n')
    throw Object.assign(new Error(`${bin} ${args.slice(0,4).join(' ')} failed (${result.status ?? result.error?.code}); ${diagnostic}`), { stdout: result.stdout ?? '' })
  }
  return result.stdout
}
const identities = new Map()
function toolIdentity(kind) {
  if (!identities.has(kind)) identities.set(kind, kind === 'go' ? {
    version: command('go', ['version']).trim(),
    module: /^module\s+(\S+)/m.exec(readFileSync(resolve(root, 'go.mod'), 'utf8'))?.[1],
    target: JSON.parse(command('go', ['env', '-json', 'GOOS', 'GOARCH', 'GOFLAGS', 'CGO_ENABLED', 'GOVERSION', 'GOEXPERIMENT', 'GOWORK'])),
  } : { node: process.versions, packages: ['vitest', 'vite', '@playwright/test'].map(name =>
    [name, JSON.parse(readFileSync(resolve(web, `node_modules/${name}/package.json`), 'utf8')).version]) })
  return identities.get(kind)
}
export function collectGo({ fresh = false, cache = cacheEnabled() } = {}) {
  if (!cache) return collectGoNow()
  const identity = toolIdentity('go'), snapshot = inputSnapshot(root, command)
  // Local replace directives/workspaces can import files outside this tree.
  // Their contents are not covered by its key, so retain native discovery.
  if (externalGoInputs(identity)) return collectGoNow()
  const stamp = listingKey(snapshot, 'go-source', identity)
  const checked = value => {
    if (listingKey(inputSnapshot(root, command), 'go-source', identity) !== stamp) throw new Error('Go discovery inputs changed during collection; retry')
    return value
  }
  const data = cachedGoSource(snapshot, identity, { collectAll: () => checked(collectGoRaw()),
    collectPackage: owner => checked(collectGoPackage(owner, identity.module, snapshot)) }, { fresh })
  return normalizeGo(data)
}
function collectGoPackage(owner, module, snapshot) {
  const pkg = JSON.parse(command('go', ['list', '-e', '-json', owner === '.' ? '.' : `./${owner}`]))
  if (pkg.Error && !/^(?:no Go files in |build constraints exclude all Go files in )/.test(pkg.Error.Err)) throw new Error(`Go package discovery failed: ${pkg.Error.Err}`)
  const prefix = `${module}/`
  const imports = pkg.Error ? undefined : [...(pkg.Imports ?? []), ...(pkg.TestImports ?? []), ...(pkg.XTestImports ?? [])]
    .filter(path => path.startsWith(prefix)).map(path => path.slice(prefix.length))
  const active = new Set([...(pkg.TestGoFiles ?? []), ...(pkg.XTestGoFiles ?? [])])
  const files = snapshot.filter(([file]) => file.endsWith('_test.go') && dirname(file) === owner)
    .map(([file]) => ({ file, active: active.has(basename(file)) }))
  const tests = JSON.parse(command('go', ['run', './scripts/test-tiers/go-package'], { input: JSON.stringify({ package: owner, files }), env: { ...process.env, GOMAXPROCS: '2' } }))
  return { tests, imports }
}
function collectGoNow() {
  return normalizeGo(collectGoRaw())
}
function collectGoRaw() {
  return JSON.parse(command('go', ['run','./scripts/test-tier-go'], { env: { ...process.env, GOMAXPROCS: '2' } }))
}
function normalizeGo(data) {
  const tests = new Map()
  for (const row of (data.tests ?? []).sort((a, b) => Buffer.compare(Buffer.from(a.file + a.name), Buffer.from(b.file + b.name)))) {
    const id = `${row.package}:${row.name}`
    const previous = tests.get(id)
    tests.set(id, { ...row, kind: 'go', active: row.active || previous?.active || false })
  }
  return { tests: [...tests.values()], imports: data.imports }
}

export function collectNativeGo(inventory, { fresh = false, cache = cacheEnabled() } = {}) {
  if (!cache) return nativeGoList(['./...'])
  const identity = toolIdentity('go'), snapshot = inputSnapshot(root, command)
  const keys = goPackageKeys(snapshot, inventory, identity)
  if (externalGoInputs(identity)) return nativeGoList(['./...'])
  const lists = cachedGoList(keys, owners => {
    const result = Object.fromEntries(owners.map(owner => [owner, []]))
    for (const entry of nativeGoList(owners.map(owner => owner === identity.module || owner === '.' ? '.' : `./${owner}`))) {
      const split = entry.lastIndexOf(':'), owner = entry.slice(0, split)
      if (!Object.hasOwn(result, owner)) throw new Error(`Unexpected Go listing package: ${owner}`)
      result[owner].push(entry.slice(split + 1))
    }
    if (JSON.stringify(goPackageKeys(inputSnapshot(root, command), inventory, identity)) !== JSON.stringify(keys)) throw new Error('Go discovery inputs changed during native listing; retry')
    return result
  }, { enabled: cache, fresh })
  return Object.entries(lists).flatMap(([owner, names]) => names.map(name => `${owner}:${name}`))
}
function nativeGoList(patterns) {
  const output = command('go', ['test', '-p', '2', '-json', '-list', '^(Test|Fuzz)', ...patterns],
    { env: { ...process.env, GOMAXPROCS: '2' }, timeout: 15 * 60_000 })
  const result = []
  for (const line of output.split('\n').filter(Boolean)) {
    const event = JSON.parse(line)
    if (event.Action === 'output' && /^(?:Test|Fuzz)\S*\n$/.test(event.Output ?? '')) result.push(`${event.Package.replace(/^github\.com\/inspr-at\/paimos\//, '')}:${event.Output.trim()}`)
  }
  return result
}
function externalGoInputs(identity) {
  return identity.target.GOWORK && identity.target.GOWORK !== 'off' || /=>\s*"?(?:\.{1,2}\/|\/|[A-Za-z]:)/.test(readFileSync(resolve(root, 'go.mod'), 'utf8'))
}
export function flattenBrowser(report, config) {
  if (report.errors?.length) throw new Error(`Playwright collection failed: ${report.errors.map(e=>e.message).join('\n')}`)
  const rows = []
  const visit = (suites, parents = []) => {
    for (const suite of suites ?? []) {
      const nesting = suite.title.endsWith('.spec.ts') ? parents : [...parents, suite.title]
      for (const spec of suite.specs ?? []) {
        for (const test of spec.tests) rows.push({ kind: 'browser', file: `tests/${spec.file.replace(/^tests\//,'')}`,
          name: [...nesting,spec.title].join(' › '), leaf: spec.title, line: spec.line, config, project: test.projectName ?? '', id: spec.id })
      }
      visit(suite.suites, nesting)
    }
  }
  visit(report.suites)
  return rows
}
// Web collection launches Vitest, one Node process per native test file and
// two Playwright lists; a planner process may ask for the same inventory
// several times (AEON-724 fix 6: the aeon-681 planning regression exceeded the
// static self-check budget). The inventory is reused while the stamp of the web
// tree is unchanged, so an edited test, source or config file re-collects.
export const collections = { web: 0, browser: 0 }
const webMemo = {}, browserMemo = {}
// Top-level web files plus the trees that registration code can import.
export const webStampEntries = ['.', 'src', 'tests']
export function treeStamp(base, entries) {
  const parts = []
  const visit = (directory, recurse) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      if (entry.name === 'node_modules') continue
      const path = resolve(directory, entry.name)
      if (entry.isDirectory()) { if (recurse) visit(path, true) }
      else if (entry.isFile()) {
        const stat = statSync(path)
        parts.push(`${relative(base, path)}\t${stat.size}\t${stat.mtimeMs}`)
      }
    }
  }
  for (const entry of entries) {
    const path = resolve(base, entry)
    let stat
    try { stat = statSync(path) } catch { parts.push(`${entry}\tabsent`); continue }
    if (stat.isDirectory()) visit(path, entry !== '.')
    else parts.push(`${relative(base, path)}\t${stat.size}\t${stat.mtimeMs}`)
  }
  return createHash('sha256').update(parts.sort().join('\n')).digest('hex')
}
// Returns a private copy of memo.value, recomputing when the stamp changed or
// fresh is requested. Callers can mutate the copy without poisoning the memo.
export function reuse(memo, stamp, compute, { fresh = false } = {}) {
  if (fresh || memo.value === undefined || memo.stamp !== stamp) {
    memo.value = compute()
    memo.stamp = stamp
  }
  return structuredClone(memo.value)
}
export function collectWeb({ fresh = false, browserOnly = false, cache = cacheEnabled() } = {}) {
  const identity = toolIdentity('web'), scope = browserOnly ? 'web-browser' : 'web-all'
  const stamp = listingKey(inputSnapshot(root, command), scope, identity)
  const compute = () => {
    const value = browserOnly ? { ...registrations(collectBrowserNow()), scope: 'browser' } : collectWebNow()
    if (listingKey(inputSnapshot(root, command), scope, identity) !== stamp) throw new Error('Web discovery inputs changed during collection; retry')
    return value
  }
  return reuse(browserOnly ? browserMemo : webMemo, `${stamp}:${cache ? 'cached' : 'native'}`, () => {
    collections[browserOnly ? 'browser' : 'web'] += 1
    return cachedListing(stamp, compute, { fresh, enabled: cache })
  }, { fresh })
}
// Browser shards must not pay for Vitest or native Node registration. Full
// collection remains the strict setup/classification authority for every kind.
export function collectBrowserNow({ run = command } = {}) {
  const list = config => flattenBrowser(JSON.parse(run(process.execPath,
    ['node_modules/@playwright/test/cli.js','test','-c',config,'--list','--reporter=json'], { cwd: web })), config)
  return [...list('playwright.ui.config.ts').filter(row => row.file !== 'tests/performance.spec.ts'),
    ...list('playwright.perf.config.ts')]
}
function collectWebNow() {
  mkdirSync(evidence,{recursive:true})
  // Native Vitest collection expands it.each. Static parsing would miss wire
  // vectors and parameter loops, so explicitly disable it.
  const vitestPath = resolve(evidence,'vitest-list.json')
  command(process.execPath,['node_modules/vitest/vitest.mjs','list','.unit.test.ts',`--json=${vitestPath}`,
    '--no-staticParse','--maxWorkers=1','--no-fileParallelism',
    // Avoid Vite's bundled-config writes into shared node_modules/.vite-temp.
    ...(process.env.VITE_CACHE_DIR ? ['--configLoader=runner'] : [])],{cwd:web})
  const unit = JSON.parse(readFileSync(vitestPath,'utf8')).map(row => ({ kind:'vitest',file:relative(web,row.file),name:row.name }))
  const nodeFiles=[]
  const visit=directory=>{
    for(const entry of readdirSync(resolve(web,directory),{withFileTypes:true})) {
      const file=`${directory}/${entry.name}`
      if(entry.isDirectory())visit(file)
      else if(entry.isFile()&&file.endsWith('.test.ts')&&!file.endsWith('.unit.test.ts'))nodeFiles.push(file)
    }
  }
  visit('tests')
  const files = nodeFiles.sort()
  const node = JSON.parse(command(process.execPath,['--import',resolve(root,'scripts/test-tiers/node-collect-hook.mjs'),
    resolve(root,'scripts/test-tiers/node-collect.mjs'),'--batch',...files.map(file=>resolve(web,file))],{cwd:web}))
  if (!Array.isArray(node) || node.length !== files.length) throw new Error('Node registration batch is incomplete')
  for (const [index, file] of files.entries()) {
    if (node[index]?.file !== resolve(web,file) || !Array.isArray(node[index]?.tests)) throw new Error(`Node registration batch mismatch: ${file}`)
    unit.push(...node[index].tests.map(row=>({...row,kind:'node',file})))
  }
  return registrations([...unit,...collectBrowserNow()])
}
function registrations(tests) {
  const totals=new Map(),seen=new Map()
  const id=row=>`${row.kind}:${row.file}:${row.name}`
  for(const row of tests) totals.set(id(row),(totals.get(id(row))??0)+1)
  for(const row of tests) if(totals.get(id(row))>1) {
    // Native parameter expansion can produce identical printed titles (NaN
    // and undefined, for example). Preserve every registration separately.
    row.occurrence=(seen.get(id(row))??0)+1
    seen.set(id(row),row.occurrence)
  }
  return { tests }
}
export function saveJSON(path,data) { mkdirSync(resolve(path,'..'),{recursive:true});writeFileSync(path,JSON.stringify(data,null,2)+'\n') }
export function saveManifest(path,manifest) {
  writeFileSync(path,formatManifest(manifest,relative(root,path)))
}
