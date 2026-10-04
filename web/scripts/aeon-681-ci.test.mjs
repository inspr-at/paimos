// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { run, browserList } from '../../scripts/test-tiers/cli.mjs'
import { command, root, web, evidence, flattenBrowser } from '../../scripts/test-tiers/collect.mjs'
import { resolve } from 'node:path'

test('native Node and Vitest selectors execute the requested registrations, rather than skip them', async () => {
  const manifest=JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json'),'utf8'))
  const node=manifest.tests.find(row=>row.tier==='ESSENTIAL'&&row.file==='tests/access.test.ts')
  const vitest=manifest.tests.find(row=>row.tier==='ESSENTIAL'&&row.file==='tests/authz.unit.test.ts')
  const nested=manifest.tests.find(row=>row.file==='tests/kind-convert.test.ts')
  assert.ok(node);assert.ok(vitest);assert.ok(nested)
  // Collection must evaluate parameter registrations without executing bodies.
  const collected=JSON.parse(command(process.execPath,['--import',resolve(root,'scripts/test-tiers/node-collect-hook.mjs'),
    resolve(root,'scripts/test-tiers/node-collect.mjs'),resolve(web,node.file)],{cwd:web}))
  assert.equal(collected.length,manifest.tests.filter(row=>row.file===node.file).length)
  assert.ok(collected.some(row=>row.name===node.name))
  const rows=[node,vitest,nested]
  const job='native-selection-regression'
  const env={...process.env,GITHUB_STEP_SUMMARY:undefined}
  // This launches an independent CLI, not a recursive node:test run.
  delete env.NODE_TEST_CONTEXT
  assert.equal(await run('web',{tests:rows,all:rows,full:false,reason:'runner regression'},
    {unit:true,job,env}),0)
  const report=JSON.parse(readFileSync(resolve(root,`tmp/test-tiers/${job}-measurement.json`),'utf8'))
  assert.equal(report.classes.ESSENTIAL.passed,2)
  assert.equal(report.classes.ESSENTIAL.skipped,0)
  assert.equal(report.classes.ESSENTIAL.notRun,0)
  assert.equal(report.classes.NIGHTLY.passed,1)
  assert.equal(report.classes.NIGHTLY.notRun,0)
})

// --list imports registration code but never opens a browser. This exercises
// the exact native test-list syntax used by both narrowed and full CI runs.
test('native browser selectors cover the 52 stable essentials and every retained full-suite registration',()=>{
  mkdirSync(evidence,{recursive:true})
  const manifest=JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json'),'utf8'))
  const all=[]
  for(const config of ['playwright.ui.config.ts','playwright.perf.config.ts']) {
    const rows=flattenBrowser(JSON.parse(command(process.execPath,['node_modules/@playwright/test/cli.js','test','-c',config,'--list','--reporter=json'],{cwd:web})),config)
    all.push(...rows.filter(row=>config!=='playwright.ui.config.ts'||row.file!=='tests/performance.spec.ts'))
  }
  const tiers=new Map(manifest.tests.filter(row=>row.kind==='browser').map(row=>[`${row.file}:${row.name}`,row.tier]))
  assert.equal(all.length,tiers.size)
  for(const row of all) assert.ok(tiers.has(`${row.file}:${row.name}`),`Unclassified ${row.name}`)
  const groups=JSON.parse(readFileSync(resolve(web,'ci-web-shards.json'),'utf8')).groups
  for(const full of [false,true]) {
    let total=0
    for(const group of groups) {
      const rows=all.filter(row=>group.specs.some(spec=>spec.file===row.file)&&(full||tiers.get(`${row.file}:${row.name}`)==='ESSENTIAL'))
      if(!rows.length) continue
      const list=resolve(evidence,`regression-${full?'full':'essential'}-${group.id}.txt`)
      writeFileSync(list,browserList(rows,all).join('\n')+'\n')
      const env={...process.env,...Object.fromEntries(Object.entries(group.env).map(([k,v])=>[k,v.replaceAll('${RUNNER_TEMP}',evidence)]))}
      const listed=flattenBrowser(JSON.parse(command(process.execPath,['node_modules/@playwright/test/cli.js','test','--config',group.config,...group.flags,
        ...(group.project?['--project',group.project]:[]),'--workers=1','--retries=0','--test-list',list,'--list','--reporter=json'],{cwd:web,env})),group.config)
      assert.deepEqual(listed.map(row=>`${row.id}:${row.project}`).sort(),rows.map(row=>`${row.id}:${row.project}`).sort(),group.id)
      total+=listed.length
    }
    assert.equal(total,full?all.length:52)
  }
})
