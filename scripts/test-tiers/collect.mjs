// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
import { resolve, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { formatManifest } from './manifests.mjs'
import { goFailures } from './failures.mjs'
export const root = fileURLToPath(new URL('../../', import.meta.url))
export const web = resolve(root, 'web')
export const evidence = resolve(root, 'tmp/test-tiers')
export function command(bin, args, { cwd = root, env = process.env, timeout = 180_000 } = {}) {
  const result = spawnSync(bin, args, { cwd, env, encoding: 'utf8', timeout, maxBuffer: 32*1024*1024 })
  if (result.error || result.status !== 0) {
    let output = result.stdout
    if (bin === 'go' && args.includes('-json')) {
      try {
        const failures = goFailures(output ?? '')
        if (failures.length) output = failures.map(row => `${row.owner}: ${row.output}`).join('\n')
      } catch { /* Interrupted JSON falls back to the captured output tail. */ }
    }
    const diagnostic = [output, result.stderr].map(text => text?.slice(-2000)).filter(Boolean).join('\n')
    throw new Error(`${bin} ${args.slice(0,4).join(' ')} failed (${result.status ?? result.error?.code}); ${diagnostic}`)
  }
  return result.stdout
}
export function collectGo() {
  const data = JSON.parse(command('go', ['run','./scripts/test-tier-go'], { env: { ...process.env, GOMAXPROCS: '2' } }))
  const tests = new Map()
  for (const row of data.tests) {
    const id = `${row.package}:${row.name}`
    const previous = tests.get(id)
    tests.set(id, { ...row, kind: 'go', active: row.active || previous?.active || false })
  }
  return { tests: [...tests.values()], imports: data.imports }
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
export const collections = { web: 0 }
const webMemo = {}
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
export function collectWeb({ fresh = false } = {}) {
  return reuse(webMemo, treeStamp(web, webStampEntries), () => { collections.web += 1; return collectWebNow() }, { fresh })
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
  const browser = flattenBrowser(JSON.parse(command(process.execPath,['node_modules/@playwright/test/cli.js','test','-c','playwright.ui.config.ts','--list','--reporter=json'],{cwd:web})), 'playwright.ui.config.ts')
  const performance = flattenBrowser(JSON.parse(command(process.execPath,['node_modules/@playwright/test/cli.js','test','-c','playwright.perf.config.ts','--list','--reporter=json'],{cwd:web})), 'playwright.perf.config.ts')
  const tests=[...unit,...browser.filter(row=>row.file !== 'tests/performance.spec.ts'),...performance]
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
