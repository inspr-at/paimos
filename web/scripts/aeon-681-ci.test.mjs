// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { run, browserList, plan, main } from '../../scripts/test-tiers/cli.mjs'
import { command, root, web, evidence, flattenBrowser } from '../../scripts/test-tiers/collect.mjs'
import { resolve, relative } from 'node:path'
import { pathToFileURL } from 'node:url'
import { registerHooks } from 'node:module'
import { validate, key, select } from '../../scripts/test-tiers/core.mjs'

// The planner assertions concern browser gates. Collect every browser case
// natively once, and use declared unit identities for the unrelated unit lane.
// Native unit collection/execution has its own selector regression below and
// the static web-tiers check reconciles the complete catalogue independently.
let collected
const inventory=()=>{
  if(!collected) {
    const manifest=JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json'),'utf8'))
    const tests=manifest.tests.filter(row=>row.kind!=='browser')
    for(const config of ['playwright.ui.config.ts','playwright.perf.config.ts']) {
      const rows=flattenBrowser(JSON.parse(command(process.execPath,['node_modules/@playwright/test/cli.js','test','-c',config,'--list','--reporter=json'],{cwd:web})),config)
      tests.push(...rows.filter(row=>config!=='playwright.ui.config.ts'||row.file!=='tests/performance.spec.ts'))
    }
    collected={tests}
  }
  return collected
}

test('native full CI planning retains the OPS-257 gate and essential promotions without gating the optional catalogue',async()=>{
  // All modes describe this same checkout. Collect its native registrations
  // once, then exercise the unchanged planner and CLI against that snapshot.
  // Production CLI calls still collect fresh; there is no persistent cache.
  const snapshot=inventory(), before=structuredClone(snapshot)
  let collections=0
  const dependencies={collect:kind=>{assert.equal(kind,'web');collections++;return snapshot}}
  const policy=JSON.parse(readFileSync(resolve(web,'ci-web-shards.json'),'utf8'))
  const gated=new Set(policy.groups.filter(group=>group.gate!==false).flatMap(group=>group.specs.map(spec=>spec.file)))
  const options={event:'pull_request',paths:['.github/workflows/ci.yml']}
  const selection=plan('web',options,dependencies)
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
    assert.equal(await main(['plan','web','--event','pull_request','--paths',JSON.stringify(options.paths)],dependencies),0)
    assert.equal(output.at(-1).kinds.browser,expected.length)
    assert.equal(output.at(-1).deferredBrowserCases,browser.length-expected.length)
    assert.equal(await main(['plan','web','--full','--event','pull_request','--paths',JSON.stringify(options.paths)],dependencies),0)
    assert.equal(output.at(-1).kinds.browser,expected.length)
    assert.equal(output.at(-1).scope,'gated-full')
    assert.equal(await main(['plan','web','--all','--event','workflow_dispatch','--paths','[]'],dependencies),0)
    assert.equal(output.at(-1).kinds.browser,browser.length)
    assert.equal(output.at(-1).scope,'catalogue')
  } finally {
    console.log=log
    if(original===undefined) delete process.env.AEON_TEST_TIER_MODE
    else process.env.AEON_TEST_TIER_MODE=original
  }
  const nightly=plan('web',{...options,event:'schedule'},dependencies)
  assert.deepEqual(nightly.tests.map(key).sort(),browser.map(key).sort())
  assert.equal(nightly.scope,'catalogue')
  assert.equal(nightly.deferredBrowserCases,0)
  assert.equal(collections,5,'Every planning mode must consume the one native snapshot')
  assert.deepEqual(snapshot,before,'Planning must not mutate the shared discovery snapshot')
})

test('production CLI planning reuses one native snapshot without a supplied collector',async()=>{
  // Every planner call sees the same unchanged tree. Collect it natively once,
  // then reuse that snapshot while exercising the unchanged planner and CLI.
  // The selector tests below still run their requested native lists separately.
  const snapshot=inventory()
  const collector=pathToFileURL(resolve(root,'scripts/test-tiers/collect.mjs')).href
  const wrapper=`export * from ${JSON.stringify(`${collector}?native-planning-snapshot`)};
export const collectWeb = () => (${JSON.stringify(snapshot)});`
  const hooks=registerHooks({resolve(specifier,context,next) {
    const result=next(specifier,context)
    if(result.url===collector)return {url:`data:text/javascript,${encodeURIComponent(wrapper)}`,shortCircuit:true}
    return result
  }})
  try {
    const {plan,main}=await import('../../scripts/test-tiers/cli.mjs?native-planning-snapshot')
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
  } finally { hooks.deregister() }
})

test('plan and the CLI reuse a supplied inventory instead of recollecting the catalogue',async()=>{
  const sentinel={kind:'vitest',file:'tests/plan-owner.unit.test.ts',name:'AEON-706 supplied inventory sentinel (never registered on disk)'}
  const supplied={tests:[...inventory().tests,sentinel]}
  assert.ok(!inventory().tests.some(row=>key(row)===key(sentinel)))
  const warnings=[],warn=console.warn
  console.warn=message=>warnings.push(String(message))
  let selection,output=[]
  const log=console.log
  try {
    selection=plan('web',{event:'pull_request',paths:['.github/workflows/ci.yml'],inventory:supplied})
    console.log=value=>output.push(JSON.parse(value))
    assert.equal(await main(['plan','web','--unit','--event','pull_request','--paths','[]'],{inventory:supplied}),0)
  } finally { console.warn=warn;console.log=log }
  // The sentinel only exists in the supplied inventory: a recollection drops it.
  const planned=selection.all.find(row=>key(row)===key(sentinel))
  assert.equal(planned?.tier,'NIGHTLY')
  assert.equal(selection.all.length,supplied.tests.length)
  assert.equal(Object.values(output.at(-1).inventory).reduce((sum,n)=>sum+n,0),supplied.tests.length)
  assert.ok(warnings.some(line=>line.includes(key(sentinel))),warnings.join('\n'))
  assert.throws(()=>plan('web',{event:'pull_request',paths:[],inventory:{tests:'not a list'}}),/Supplied inventory needs a tests array/)
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
  // The shared snapshot already lists both browser configs natively. Keep the
  // same complete registration identities without listing the catalogue twice.
  const all=inventory().tests.filter(row=>row.kind==='browser')
  const reconciled=validate({version:1,tests:manifest.tests.filter(row=>row.kind==='browser')},all)
  assert.deepEqual(reconciled.map(key).sort(),all.map(key).sort())
  const tiers=new Map(reconciled.map(row=>[key(row),row.tier]))
  const declared=new Map(manifest.tests.filter(row=>row.kind==='browser').map(row=>[key(row),row]))
  for(const row of reconciled) assert.equal(row.tier,declared.get(key(row))?.tier??'NIGHTLY',key(row))
  const groups=JSON.parse(readFileSync(resolve(web,'ci-web-shards.json'),'utf8')).groups
  const listings=new Map()
  for(const mode of ['essential','full','all']) {
    let total=0
    for(const group of groups) {
      const included=row=>mode==='all'||tiers.get(key(row))==='ESSENTIAL'||(mode==='full'&&tiers.get(key(row))==='GATED-FULL')
      const rows=all.filter(row=>group.specs.some(spec=>spec.file===row.file)&&included(row))
      if(!rows.length) continue
      const list=resolve(evidence,`regression-${mode}-${group.id}.txt`)
      const selection=browserList(rows,all).join('\n')+'\n'
      writeFileSync(list,selection)
      const env={...process.env,...Object.fromEntries(Object.entries(group.env).map(([k,v])=>[k,v.replaceAll('${RUNNER_TEMP}',evidence)]))}
      // Several groups select exactly the same cases in full and all modes.
      // Reuse only identical native requests within this unchanged snapshot;
      // each mode still asserts every identity and its independent total.
      const request=JSON.stringify([group.config,group.flags,group.project,group.env,selection])
      if(!listings.has(request)) listings.set(request,flattenBrowser(JSON.parse(command(process.execPath,['node_modules/@playwright/test/cli.js','test','--config',group.config,...group.flags,
        ...(group.project?['--project',group.project]:[]),'--workers=1','--retries=0','--test-list',list,'--list','--reporter=json'],{cwd:web,env})),group.config))
      const listed=listings.get(request)
      assert.deepEqual(listed.map(row=>`${row.id}:${row.project}`).sort(),rows.map(row=>`${row.id}:${row.project}`).sort(),group.id)
      total+=listed.length
    }
    assert.equal(total,mode==='all'?all.length:reconciled.filter(row=>row.tier==='ESSENTIAL'||(mode==='full'&&row.tier==='GATED-FULL')).length)
  }
})

test('routing and login registrations keep explicit gate tiers after native collection', t => {
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-686-routing-tiers-'))
  t.after(()=>rmSync(directory,{recursive:true,force:true}))
  const file='tests/settings-routing.unit.test.ts', output=resolve(directory,'routing.json')
  command(process.execPath,['node_modules/vitest/vitest.mjs','list',file,`--json=${output}`,
    '--no-staticParse','--maxWorkers=1','--no-fileParallelism',
    ...(process.env.VITE_CACHE_DIR?['--configLoader=runner']:[])],{cwd:web})
  const routing=JSON.parse(readFileSync(output,'utf8')).map(row=>({kind:'vitest',file:relative(web,row.file),name:row.name}))
  const loginFile='tests/agent-login.test.ts'
  const login=JSON.parse(command(process.execPath,['--import',resolve(root,'scripts/test-tiers/node-collect-hook.mjs'),
    resolve(root,'scripts/test-tiers/node-collect.mjs'),resolve(web,loginFile)],{cwd:web}))
    .map(row=>({...row,kind:'node',file:loginFile}))
  assert.ok(routing.length>=35,'native collection expands every moved Workspace bookmark')
  assert.ok(login.some(row=>row.name==='CLI login names the instance after the workspace and never passes --instance (AEON-730)'))
  const manifest=JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json'),'utf8'))
  const native=[...routing,...login]
  const declared={version:1,tests:manifest.tests.filter(row=>[file,loginFile].includes(row.file))}
  const rows=validate(declared,native,undefined,{strict:true})
  for(const row of rows) assert.equal(row.tier,row.kind==='node'?'ESSENTIAL':'GATED-FULL',key(row))
  for(const event of ['pull_request','merge_group']) {
    const selected=select(rows,{event,paths:['.github/workflows/ci.yml']}).tests
    assert.deepEqual(selected.map(key).sort(),native.map(key).sort(),`${event} retains every routing and login regression`)
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

// Collection spawns the Vitest, Node and Playwright listers; repeating it per
// plan made the static pre-filter exceed its budget on a loaded workstation.
test('one process collects the web inventory once across repeated plans', () => {
  const manifest = JSON.parse(readFileSync(resolve(root, 'scripts/ci/web-test-tiers.json'), 'utf8'))
  const known = manifest.tests.find(row => row.file === 'tests/planning.test.ts')
  assert.ok(known)
  const directory = mkdtempSync(resolve(tmpdir(), 'aeon-734-collect-once-'))
  const hook = resolve(directory, 'fixture.mjs'), script = resolve(directory, 'plans.mjs')
  const collector = pathToFileURL(resolve(root, 'scripts/test-tiers/collect.mjs')).href
  // The real module stays loaded under a query so only collectWeb is counted.
  const wrapper = `export * from ${JSON.stringify(`${collector}?aeon734-fixture`)};
export let collections = 0;
export const collectWeb = () => { collections += 1; return (${JSON.stringify({ tests: [known] })}); };`
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
  writeFileSync(script, `import { plan, main } from ${JSON.stringify(pathToFileURL(resolve(root, 'scripts/test-tiers/cli.mjs')).href)};
import { collections } from ${JSON.stringify(collector)};
const options = { event: 'pull_request', paths: ['README.md'] };
const first = plan('web', options), second = plan('web', { ...options, event: 'schedule' });
const log = console.log; console.log = () => {};
let code;
try { code = await main(['plan', 'web', '--event', 'pull_request', '--paths', '["README.md"]']); } finally { console.log = log; }
console.log(JSON.stringify({ collections, code, plans: [first.all.length, second.all.length] }));
`)
  const env = { ...process.env }
  delete env.NODE_TEST_CONTEXT
  const result = spawnSync(process.execPath, ['--import', hook, script], { cwd: web, env, encoding: 'utf8', timeout: 60_000, maxBuffer: 4 * 1024 * 1024 })
  assert.ifError(result.error)
  assert.equal(result.status, 0, result.stderr)
  const summary = JSON.parse(result.stdout.trim().split('\n').at(-1))
  assert.equal(summary.code, 0)
  assert.deepEqual(summary.plans, [1, 1], 'every plan still sees the collected inventory')
  assert.equal(summary.collections, 1, 'three plans in one process must collect once')
})
