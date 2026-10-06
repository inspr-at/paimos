// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { resolve, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
export const root = fileURLToPath(new URL('../../', import.meta.url))
export const web = resolve(root, 'web')
export const evidence = resolve(root, 'tmp/test-tiers')
export function command(bin, args, { cwd = root, env = process.env, timeout = 180_000 } = {}) {
  const result = spawnSync(bin, args, { cwd, env, encoding: 'utf8', timeout, maxBuffer: 32*1024*1024 })
  if (result.error || result.status !== 0) throw new Error(`${bin} ${args.slice(0,4).join(' ')} failed (${result.status ?? result.error?.code}); ${result.stderr?.slice(-2000) ?? ''}`)
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
export function collectWeb() {
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
  for (const file of nodeFiles.sort()) {
    const rows = JSON.parse(command(process.execPath,['--import',resolve(root,'scripts/test-tiers/node-collect-hook.mjs'),
      resolve(root,'scripts/test-tiers/node-collect.mjs'),resolve(web,file)],{cwd:web}))
    unit.push(...rows.map(row=>({...row,kind:'node',file})))
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
  const {tests,...metadata}=manifest
  const header=JSON.stringify(metadata,null,2).slice(0,-2)
  writeFileSync(path,`${header},\n  "tests": [\n${tests.map(row=>'    '+JSON.stringify(row)).join(',\n')}\n  ]\n}\n`)
}
