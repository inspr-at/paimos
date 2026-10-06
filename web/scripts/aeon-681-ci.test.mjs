// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdirSync, mkdtempSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { registerHooks } from 'node:module'
import { run, browserList } from '../../scripts/test-tiers/cli.mjs'
import { command, root, web, evidence, flattenBrowser } from '../../scripts/test-tiers/collect.mjs'
import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { validate, key, select } from '../../scripts/test-tiers/core.mjs'

test('native full CI planning retains the OPS-257 gate and essential promotions without gating the optional catalogue',async t=>{
  // Exercise every unchanged planner/CLI path against the same native snapshot.
  // Recollecting the entire catalogue for each mode made this one regression
  // exceed the static gate's guard before it could finish its assertions.
  const collector=pathToFileURL(resolve(root,'scripts/test-tiers/collect.mjs')).href
  const entry=pathToFileURL(resolve(root,'scripts/test-tiers/cli.mjs')).href+'?aeon691-native-planning'
  const wrapper=`export * from ${JSON.stringify(collector)};
import { collectWeb as nativeCollectWeb } from ${JSON.stringify(collector)};
let inventory;
export let collections = 0;
export const collectWeb = () => { if (!inventory) { collections++; inventory = nativeCollectWeb(); } return inventory; };`
  const snapshot='data:text/javascript,'+encodeURIComponent(wrapper)
  const hook=registerHooks({resolve(specifier,context,next){
    if(context.parentURL===entry&&specifier==='./collect.mjs') return {url:snapshot,shortCircuit:true}
    return next(specifier,context)
  }})
  t.after(()=>hook.deregister())
  const {plan,main}=await import(entry)
  const policy=JSON.parse(readFileSync(resolve(web,'ci-web-shards.json'),'utf8'))
  const gated=new Set(policy.groups.filter(group=>group.gate!==false).flatMap(group=>group.specs.map(spec=>spec.file)))
  const options={event:'pull_request',paths:['.github/workflows/ci.yml']}
  const selection=plan('web',options)
  const browser=selection.all.filter(row=>row.kind==='browser')
  const declared=new Map(JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json'),'utf8')).tests.map(row=>[key(row),row]))
  const expected=browser.filter(row=>row.tier==='ESSENTIAL'||(gated.has(row.file)&&declared.get(key(row))?.tier==='GATED-FULL'))
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
  assert.equal((await import(snapshot)).collections,1,'all planner modes must use one real native inventory')
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
  // Full and catalogue modes often produce identical inputs. Collect each
  // distinct native selector once, while retaining every per-mode assertion.
  const nativeSelections=new Map()
  for(const mode of ['essential','full','all']) {
    let total=0
    for(const group of groups) {
      const included=row=>mode==='all'||tiers.get(key(row))==='ESSENTIAL'||(mode==='full'&&tiers.get(key(row))==='GATED-FULL')
      const rows=all.filter(row=>group.specs.some(spec=>spec.file===row.file)&&included(row))
      if(!rows.length) continue
      const list=resolve(evidence,`regression-${mode}-${group.id}.txt`)
      const selectors=browserList(rows,all).join('\n')+'\n'
      writeFileSync(list,selectors)
      const env={...process.env,...Object.fromEntries(Object.entries(group.env).map(([k,v])=>[k,v.replaceAll('${RUNNER_TEMP}',evidence)]))}
      const identity=JSON.stringify([group.config,group.flags,group.project,group.env,selectors])
      let listed=nativeSelections.get(identity)
      if(!listed) {
        listed=flattenBrowser(JSON.parse(command(process.execPath,['node_modules/@playwright/test/cli.js','test','--config',group.config,...group.flags,
          ...(group.project?['--project',group.project]:[]),'--workers=1','--retries=0','--test-list',list,'--list','--reporter=json'],{cwd:web,env})),group.config)
        nativeSelections.set(identity,listed)
      }
      assert.deepEqual(listed.map(row=>`${row.id}:${row.project}`).sort(),rows.map(row=>`${row.id}:${row.project}`).sort(),group.id)
      total+=listed.length
    }
    assert.equal(total,mode==='all'?all.length:reconciled.filter(row=>row.tier==='ESSENTIAL'||(mode==='full'&&row.tier==='GATED-FULL')).length)
  }
})

test('AEON-648 work Node registrations have explicit tiers and remain in changed-area and nightly runs',()=>{
  const manifest=JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json')))
  for(const file of ['tests/done-gate.test.ts','tests/release-membership.test.ts','tests/work-aggregates.test.ts',
    'tests/work-surfaces-fix4.test.ts','tests/work-vocabulary.test.ts']) {
    const collected=JSON.parse(command(process.execPath,['--import',resolve(root,'scripts/test-tiers/node-collect-hook.mjs'),
      resolve(root,'scripts/test-tiers/node-collect.mjs'),resolve(web,file)],{cwd:web}))
      .map(row=>({...row,kind:'node',file}))
    assert.ok(collected.length>0,file)
    const declared={version:1,tests:manifest.tests.filter(row=>row.file===file)}
    const rows=validate(declared,collected,undefined,{strict:true})
    for(const event of ['pull_request','merge_group']) {
      const changed=select(rows,{event,paths:[`web/${file}`],webImports:{[file]:[]}})
      const expected=event==='merge_group'?rows.filter(row=>row.tier==='ESSENTIAL'||row.tier==='GATED-FULL'):collected
      assert.equal(changed.full,event==='merge_group')
      assert.deepEqual(changed.tests.map(key).sort(),expected.map(key).sort(),`${event}: ${file}`)
    }
    assert.deepEqual(select(rows,{event:'schedule',paths:[]}).tests.map(key).sort(),collected.map(key).sort(),file)
  }
  for(const [file,name] of [
    ['tests/release-membership.test.ts','ambiguous parent create replay never confirms a backlog leaf as added'],
    ['tests/work-vocabulary.test.ts','workspace names depend on leaf shape first, then project-relative depth'],
  ]) {
    const row=manifest.tests.find(row=>row.file===file&&row.name===name)
    assert.equal(row?.tier,'NIGHTLY',`${file}: ${name}`)
    assert.equal(select([row],{event:'pull_request',paths:['README.md']}).tests.length,0)
  }
})

test('default check command warns once per unclassified native work-node case and exits zero', () => {
  const manifest = JSON.parse(readFileSync(resolve(root, 'scripts/ci/web-test-tiers.json'), 'utf8'))
  const cases = []
  for (const file of ['tests/done-gate.test.ts', 'tests/release-membership.test.ts', 'tests/work-aggregates.test.ts',
    'tests/work-surfaces-fix4.test.ts', 'tests/work-vocabulary.test.ts']) {
    const native = JSON.parse(command(process.execPath, ['--import', resolve(root, 'scripts/test-tiers/node-collect-hook.mjs'),
      resolve(root, 'scripts/test-tiers/node-collect.mjs'), resolve(web, file)], { cwd: web }))
    assert.ok(native.length > 0, file)
    cases.push(...native.map(row => ({ ...row, kind: 'node', file })))
  }
  const known = manifest.tests.find(row => row.file === 'tests/planning.test.ts')
  assert.ok(known)
  assert.equal(new Set(cases.map(key)).size, cases.length)
  const directory = mkdtempSync(resolve(tmpdir(), 'aeon-648-default-tier-'))
  const hook = resolve(directory, 'fixture.mjs')
  const collector = pathToFileURL(resolve(root, 'scripts/test-tiers/collect.mjs')).href
  // Native registration identities stay real; only collection breadth and the
  // manifest read are isolated. Execute the unchanged command/main/validator.
  const wrapper = `export * from ${JSON.stringify(`${collector}?aeon648-fixture`)};
export const collectWeb = () => (${JSON.stringify({ tests: [known, ...cases] })});`
  writeFileSync(hook, `import fs from 'node:fs';
import { registerHooks, syncBuiltinESMExports } from 'node:module';
const read = fs.readFileSync;
fs.readFileSync = function(path, ...args) {
  if (String(path) === ${JSON.stringify(resolve(root, 'scripts/ci/web-test-tiers.json'))}) return ${JSON.stringify(JSON.stringify({ version: 1, tests: [known] }))};
  return read.call(this, path, ...args);
};
syncBuiltinESMExports();
registerHooks({ resolve(specifier, context, next) {
  const result = next(specifier, context);
  if (result.url === ${JSON.stringify(collector)}) return { url: ${JSON.stringify(`data:text/javascript,${encodeURIComponent(wrapper)}`)}, shortCircuit: true };
  return result;
}});
`)
  const env = { ...process.env }
  delete env.NODE_TEST_CONTEXT
  const result = spawnSync(process.execPath, ['--import', hook, resolve(root, 'scripts/test-tiers/cli.mjs'), 'check', 'web'],
    { cwd: web, env, encoding: 'utf8', timeout: 30_000, maxBuffer: 4 * 1024 * 1024 })
  assert.ifError(result.error)
  assert.equal(result.status, 0, result.stderr)
  const warnings = result.stderr.trim().split('\n').filter(Boolean)
  assert.equal(warnings.length, cases.length, result.stderr)
  const expected = cases.map(row => `::warning::Unclassified test (defaults to NIGHTLY; classify these): ${key(row)}`
    .replaceAll('%', '%25').replaceAll('\r', '%0D').replaceAll('\n', '%0A'))
  assert.deepEqual(warnings.sort(), expected.sort())
  const summary = JSON.parse(result.stdout)
  assert.equal(summary.inventory.NIGHTLY, cases.length + (known.tier === 'NIGHTLY' ? 1 : 0))
  assert.equal(Object.values(summary.inventory).reduce((sum, n) => sum + n, 0), cases.length + 1)
})
