// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { run, browserList, plan, main } from '../../scripts/test-tiers/cli.mjs'
import { command, root, web, evidence, flattenBrowser } from '../../scripts/test-tiers/collect.mjs'
import { resolve } from 'node:path'
import { validate, key } from '../../scripts/test-tiers/core.mjs'

test('native full CI planning retains the OPS-257 gate and essential promotions without gating the optional catalogue',async()=>{
  const policy=JSON.parse(readFileSync(resolve(web,'ci-web-shards.json'),'utf8'))
  const gated=new Set(policy.groups.filter(group=>group.gate!==false).flatMap(group=>group.specs.map(spec=>spec.file)))
  const options={event:'pull_request',paths:['.github/workflows/ci.yml']}
  const selection=plan('web',options)
  const browser=selection.all.filter(row=>row.kind==='browser')
  const declared=new Map(JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json'),'utf8')).tests.map(row=>[key(row),row]))
  const expected=browser.filter(row=>row.tier==='ESSENTIAL'||(gated.has(row.file)&&declared.has(key(row))))
  assert.ok(browser.length>expected.length,'Fixture must include optional nonessential registrations')
  assert.deepEqual(selection.tests.map(key).sort(),expected.map(key).sort())
  assert.equal(selection.full,true)
  assert.equal(selection.scope,'gated-full')
  assert.equal(selection.deferredBrowserCases,browser.length-expected.length)
  // Workflow fan-out and explicit --full both retain the established gate.
  const original=process.env.AEON_TEST_TIER_MODE,log=console.log,output=[]
  try {
    process.env.AEON_TEST_TIER_MODE='full'
    console.log=value=>output.push(JSON.parse(value))
    assert.equal(await main(['plan','web','--event','pull_request','--paths',JSON.stringify(options.paths)]),0)
    assert.equal(output.at(-1).kinds.browser,expected.length)
    assert.equal(output.at(-1).deferredBrowserCases,browser.length-expected.length)
    assert.equal(await main(['plan','web','--full','--event','pull_request','--paths',JSON.stringify(options.paths)]),0)
    assert.equal(output.at(-1).kinds.browser,expected.length)
    assert.equal(output.at(-1).scope,'gated-full')
    assert.equal(await main(['plan','web','--all','--event','workflow_dispatch','--paths','[]']),0)
    assert.equal(output.at(-1).kinds.browser,browser.length)
    assert.equal(output.at(-1).scope,'catalogue')
  } finally {
    console.log=log
    if(original===undefined) delete process.env.AEON_TEST_TIER_MODE
    else process.env.AEON_TEST_TIER_MODE=original
  }
  const nightly=plan('web',{...options,event:'schedule'})
  assert.deepEqual(nightly.tests.map(key).sort(),browser.map(key).sort())
  assert.equal(nightly.scope,'catalogue')
  assert.equal(nightly.deferredBrowserCases,0)
})

test('native Node and Vitest selectors execute the requested registrations, rather than skip them', async () => {
  const manifest=JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json'),'utf8'))
  const node=manifest.tests.find(row=>row.tier==='ESSENTIAL'&&row.file==='tests/access.test.ts')
  const vitest=manifest.tests.find(row=>row.tier==='ESSENTIAL'&&row.file==='tests/authz.unit.test.ts')
  const nested=manifest.tests.find(row=>row.file==='tests/kind-convert.test.ts')
  assert.ok(node);assert.ok(vitest);assert.ok(nested)
  // Collection must evaluate parameter registrations without executing bodies.
  const collected=JSON.parse(command(process.execPath,['--import',resolve(root,'scripts/test-tiers/node-collect-hook.mjs'),
    resolve(root,'scripts/test-tiers/node-collect.mjs'),resolve(web,node.file)],{cwd:web}))
  const reconciled=validate({version:1,tests:manifest.tests.filter(row=>row.file===node.file)},collected.map(row=>({...row,kind:'node',file:node.file})))
  assert.deepEqual(reconciled.map(key).sort(),collected.map(row=>key({...row,kind:'node',file:node.file})).sort())
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
  assert.equal(report.classes['GATED-FULL'].passed,1)
  assert.equal(report.classes['GATED-FULL'].notRun,0)
  assert.equal(report.classes.NIGHTLY.selected,0)
})

// --list imports registration code but never opens a browser. This exercises
// the exact native test-list syntax used by both narrowed and full CI runs.
test('native browser selectors cover reconciled essentials and every collected full-suite registration',()=>{
  mkdirSync(evidence,{recursive:true})
  const manifest=JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json'),'utf8'))
  const all=[]
  for(const config of ['playwright.ui.config.ts','playwright.perf.config.ts']) {
    const rows=flattenBrowser(JSON.parse(command(process.execPath,['node_modules/@playwright/test/cli.js','test','-c',config,'--list','--reporter=json'],{cwd:web})),config)
    all.push(...rows.filter(row=>config!=='playwright.ui.config.ts'||row.file!=='tests/performance.spec.ts'))
  }
  const reconciled=validate({version:1,tests:manifest.tests.filter(row=>row.kind==='browser')},all)
  assert.deepEqual(reconciled.map(key).sort(),all.map(key).sort())
  const tiers=new Map(reconciled.map(row=>[key(row),row.tier]))
  const declared=new Map(manifest.tests.filter(row=>row.kind==='browser').map(row=>[key(row),row]))
  for(const row of reconciled) assert.equal(row.tier,declared.get(key(row))?.tier??'NIGHTLY',key(row))
  const groups=JSON.parse(readFileSync(resolve(web,'ci-web-shards.json'),'utf8')).groups
  for(const mode of ['essential','full','all']) {
    let total=0
    for(const group of groups) {
      const included=row=>mode==='all'||tiers.get(key(row))==='ESSENTIAL'||(mode==='full'&&tiers.get(key(row))==='GATED-FULL')
      const rows=all.filter(row=>group.specs.some(spec=>spec.file===row.file)&&included(row))
      if(!rows.length) continue
      const list=resolve(evidence,`regression-${mode}-${group.id}.txt`)
      writeFileSync(list,browserList(rows,all).join('\n')+'\n')
      const env={...process.env,...Object.fromEntries(Object.entries(group.env).map(([k,v])=>[k,v.replaceAll('${RUNNER_TEMP}',evidence)]))}
      const listed=flattenBrowser(JSON.parse(command(process.execPath,['node_modules/@playwright/test/cli.js','test','--config',group.config,...group.flags,
        ...(group.project?['--project',group.project]:[]),'--workers=1','--retries=0','--test-list',list,'--list','--reporter=json'],{cwd:web,env})),group.config)
      assert.deepEqual(listed.map(row=>`${row.id}:${row.project}`).sort(),rows.map(row=>`${row.id}:${row.project}`).sort(),group.id)
      total+=listed.length
    }
    assert.equal(total,mode==='all'?all.length:reconciled.filter(row=>row.tier==='ESSENTIAL'||(mode==='full'&&row.tier==='GATED-FULL')).length)
  }
})
