// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { tmpdir } from 'node:os'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { validate, select, shard, key, exactPattern, webGraph, counts, measuredWeights } from './core.mjs'
import { reportCases, goOutcomes, browserOutcomes } from './report.mjs'
import { aggregate, jobMinutes, readReports } from './measure.mjs'
import { checkFull } from './check-full.mjs'
import { changedPaths, schedulingMode } from './diff.mjs'
import { tierWeights } from '../../web/scripts/ci-web-shard.mjs'

const g=(pkg,name,tier='NIGHTLY')=>({kind:'go',package:pkg,name,tier,active:true})
const w=(file,name,tier='NIGHTLY')=>({kind:'node',file,name,tier})
const cases=[g('internal/auth','TestCeiling','ESSENTIAL'),g('internal/auth','TestRotation'),
  g('internal/nodes','TestCRUD','ESSENTIAL'),g('internal/nodes','TestUndo'),g('cmd/aeon','TestRoutes'),
  w('tests/scope.test.ts','ceiling','ESSENTIAL'),w('tests/scope.test.ts','revoked'),w('tests/tree.test.ts','tree')]
const fixture=()=>({version:1,tests:structuredClone(cases)})
const noFlaky={version:1,entries:[]}

test('AEON-648 pending writes and identity fences stay protected without promoting ordinary display tests',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  for(const [file,names] of [
    ['tests/parent-benefit-generation.unit.test.ts',[
      'retry captures its generation/revision and discards a result after navigation',
      'tenant changes fence an old status read even for the same node id',
      'an old provider save cannot restore another person’s opt-in or drafts',
    ]],
    ['tests/work-vocabulary-card.unit.test.ts',['serializes a deferred Reload with Save and leaves the saved revision visible']],
  ]) for(const name of names) {
    const row=manifest.tests.find(row=>row.file===file&&row.name===name)
    assert.equal(row?.tier,'ESSENTIAL',`${file}: ${name}`)
    for(const event of ['pull_request','merge_group'])
      assert.deepEqual(select([row],{event,paths:['README.md']}).tests,[row])
  }
  const display=manifest.tests.find(row=>row.file==='tests/work-benefit-visibility.unit.test.ts'&&row.name==='work offers its benefit reading section in the workspace')
  assert.equal(display?.tier,'NIGHTLY')
})

test('runtime reconciliation defaults new cases to NIGHTLY and drops stale cases with named warnings',()=>{
  const stale=cases[0],added={kind:'go',package:'internal/auth',name:'TestNew',active:false}
  const discovered=[...cases.slice(1),added],warnings=[],manifest=fixture(),before=structuredClone(manifest)
  const rows=validate(manifest,discovered,noFlaky,{warn:message=>warnings.push(message)})
  assert.deepEqual(rows.map(key),discovered.map(key))
  assert.deepEqual(rows.at(-1),{...added,tier:'NIGHTLY'})
  assert.equal(rows.find(row=>row.name==='TestCRUD').tier,'ESSENTIAL')
  assert.deepEqual(warnings,[`::warning::Unclassified test (defaults to NIGHTLY; classify these): ${key(added)}`,
    `::warning::Stale manifest entry (dropped): ${key(stale)}`])
  for(const event of ['pull_request','merge_group']) {
    assert.ok(select(rows,{event,paths:['internal/auth/key.go']}).tests.some(row=>key(row)===key(added)))
    assert.ok(!select(rows,{event,paths:['README.md']}).tests.some(row=>key(row)===key(added)))
  }
  assert.deepEqual(select(rows,{event:'schedule',paths:[]}).tests,rows)
  assert.deepEqual(manifest,before, 'Reconciliation must leave the stored manifest unchanged')
})

test('runtime reconciliation escapes warning titles and retains browser configuration validation',()=>{
  const row={kind:'browser',file:'tests/new.spec.ts',name:'new\ncase%',config:'playwright.ui.config.ts',project:'',line:12,id:'native'}
  const warnings=[]
  const rows=validate(fixture(),[...cases,row],noFlaky,{warn:message=>warnings.push(message)})
  assert.deepEqual(rows.at(-1),{...row,tier:'NIGHTLY',active:true})
  assert.deepEqual(warnings,[`::warning::Unclassified test (defaults to NIGHTLY; classify these): ${key(row).replaceAll('%','%25').replaceAll('\n','%0A')}`])
  for(const strict of [false,true]) assert.throws(()=>validate({version:1,tests:[{...row,config:'old.config.ts',tier:'ESSENTIAL'}]},[row],noFlaky,{strict}),/Changed browser configuration/)
})

test('reconciliation never hides malformed, duplicate or known-flaky ESSENTIAL classifications',()=>{
  for(const strict of [false,true]) {
    assert.throws(()=>validate({...fixture(),tests:[{...cases[0],tier:'UNKNOWN'}]},[],noFlaky,{strict}),/Invalid classification/)
    assert.throws(()=>validate({...fixture(),tests:[cases[0],cases[0]]},[],noFlaky,{strict}),/Duplicate manifest entry/)
    assert.throws(()=>validate(fixture(),[cases[0],cases[0]],noFlaky,{strict}),/Duplicate collected identity/)
    assert.throws(()=>validate(fixture(),[],{version:1,entries:[{key:key(cases[0]),owner:'AEON-675'}]},{strict}),/Known-flaky case cannot be ESSENTIAL/)
  }
})

test('strict checks reject unknown and stale registrations; tagging never removes a nightly test',()=>{
  assert.throws(()=>validate(fixture(),[...cases,g('internal/auth','TestNew')],noFlaky,{strict:true}),/Unclassified test.*TestNew/)
  assert.throws(()=>validate(fixture(),cases.slice(1),noFlaky,{strict:true}),/Stale manifest entry.*TestCeiling/)
  const manifest=fixture();manifest.tests[1].tags=['delete-candidate']
  const rows=validate(manifest,cases)
  const full=select(rows,{event:'schedule',paths:[]})
  assert.equal(full.tests.length,cases.length)
  assert.ok(full.tests.find(row=>row.name==='TestRotation').tags.includes('delete-candidate'))
  manifest.tests[0].tags=['delete-candidate']
  assert.throws(()=>validate(manifest,cases),/Invalid classification/)
})

test('known-flaky cases cannot enter ESSENTIAL, but remain in changed-area and nightly execution',()=>{
  for (const row of [g('internal/importer','TestSourceRequestCapAndDelay'),
    {kind:'browser',file:'tests/clip-tip.spec.ts',name:'touch disclosure',config:'playwright.ui.config.ts',project:'',tier:'NIGHTLY'}]) {
    const manifest={version:1,tests:[{...row,tier:'ESSENTIAL'}]}
    const known={version:1,entries:[{key:key(row),owner:row.kind==='go'?'AEON-675':'AEON-676'}]}
    assert.throws(()=>validate(manifest,[row],known),error=>
      error.message===`Known-flaky case cannot be ESSENTIAL: ${key(row)} (${known.entries[0].owner})`)
    manifest.tests[0].tier='NIGHTLY'
    const retained=validate(manifest,[row],known)
    assert.deepEqual(select(retained,{event:'pull_request',paths:['README.md']}).tests,[])
    assert.deepEqual(select(retained,{event:'schedule',paths:[]}).tests,retained)
    const paths=row.kind==='go'?['internal/importer/source.go']:[`web/${row.file}`]
    assert.deepEqual(select(retained,{event:'merge_group',paths,webImports:{[row.file]:[]}}).tests,retained)
  }
})

test('known-flaky registry fails closed on missing ownership and duplicate case keys',()=>{
  for(const entry of [{key:key(cases[0])},{key:'',owner:'AEON-675'},{key:key(cases[0]),owner:'675'}])
    assert.throws(()=>validate(fixture(),cases,{version:1,entries:[entry]}),/case key and owner ticket/)
  const entry={key:key(cases[1]),owner:'AEON-675'}
  assert.throws(()=>validate(fixture(),cases,{version:1,entries:[entry,entry]}),/Duplicate known-flaky entry/)
  assert.throws(()=>validate(fixture(),cases,{version:2,entries:[]}),/registry version 1/)
})

test('known-flaky committed cases retain exact owners and cannot be promoted by the CLI default validator',()=>{
  const known=JSON.parse(readFileSync(new URL('../ci/known-flaky.json',import.meta.url)))
  const manifests=['go','web'].map(kind=>JSON.parse(readFileSync(new URL(`../ci/${kind}-test-tiers.json`,import.meta.url))))
  assert.equal(known.entries.length,9)
  assert.deepEqual(Object.fromEntries(['AEON-675','AEON-676','AEON-683'].map(owner=>
    [owner,known.entries.filter(entry=>entry.owner===owner).length])),{'AEON-675':1,'AEON-676':1,'AEON-683':7})
  for(const entry of known.entries) {
    const manifest=manifests.find(manifest=>manifest.tests.some(row=>key(row)===entry.key))
    assert.ok(manifest,`Stale known-flaky case: ${entry.key}`)
    const row=manifest.tests.find(row=>key(row)===entry.key)
    assert.equal(row.tier,'GATED-FULL',entry.key)
    assert.ok(select(manifest.tests,{event:'schedule',paths:[]}).tests.some(row=>key(row)===entry.key))
    const promoted=structuredClone(manifest)
    promoted.tests.find(row=>key(row)===entry.key).tier='ESSENTIAL'
    assert.throws(()=>validate(promoted,manifest.tests),error=>
      error.message===`Known-flaky case cannot be ESSENTIAL: ${entry.key} (${entry.owner})`)
  }
})

test('PR and merge group take essential union changed package and reverse dependencies; docs retain core',()=>{
  const imports={'cmd/aeon':['internal/auth'],'internal/auth':[]}
  for(const event of ['pull_request','merge_group']) {
    const docs=select(cases,{event,paths:['README.md','docs/RELEASE.md'],imports})
    assert.equal(docs.full,false)
    assert.deepEqual(docs.tests.map(row=>row.name),['TestCeiling','TestCRUD','ceiling'])
    const picked=select(cases,{event,paths:['docs/RELEASE.md','internal/auth/key.go'],imports})
    assert.equal(picked.full,false)
    assert.deepEqual(picked.tests.map(row=>row.name),['TestCeiling','TestRotation','TestCRUD','TestRoutes','ceiling'])
  }
})

test('large optional reverse-dependency expansion keeps all changed-package tests within the core union',()=>{
  const many=[...cases,...Array.from({length:301},(_,i)=>g('internal/dependant',`TestExtra${i}`))]
  const selected=select(many,{event:'pull_request',paths:['internal/auth/key.go'],imports:{'internal/dependant':['internal/auth']}})
  assert.equal(selected.full,false)
  assert.ok(selected.tests.some(row=>row.name==='TestRotation'))
  assert.ok(selected.tests.some(row=>row.name==='TestCRUD'))
  assert.ok(!selected.tests.some(row=>row.package==='internal/dependant'))
  assert.match(selected.reason,/optional Go reverse dependencies exceed 300/)
})

test('uncertain and missing metadata select full on every premerge event',()=>{
  for(const path of ['go.mod','go.sum','api/openapi.yaml','.github/workflows/ci.yml',
    'internal/db/migrations/9999.sql','internal/auth/testdata/token.json','web/tests/access-fixtures.ts',
    'web/vite.config.ts','web/package-lock.json','scripts/test-tiers/core.mjs','unexpected.input']) {
    for(const event of ['pull_request','merge_group']) assert.equal(select(cases,{event,paths:[path]}).full,true,path)
  }
  assert.equal(select(cases,{event:'merge_group'}).full,true)
  assert.equal(select(cases,{event:'pull_request',paths:['web/src/deleted.ts'],webImports:{}}).full,true)
})

test('fan-out chooses full shard counts for uncertain and deleted paths, small counts for mapped changes',()=>{
  for(const event of ['pull_request','merge_group']) {
    assert.equal(schedulingMode(event,['README.md']), 'essential')
    assert.equal(schedulingMode(event,['internal/auth/flow.go'],()=>true),'essential')
    assert.equal(schedulingMode(event,['.github/workflows/ci.yml']), 'full')
    assert.equal(schedulingMode(event,['internal/removed/file.go'],()=>false),'full')
    assert.equal(schedulingMode(event,undefined),'full')
  }
  assert.equal(schedulingMode('schedule',['README.md']),'full')
})

test('uncertain infrastructure changes preserve the classified gate plus promoted essentials',()=>{
  const browser=(file,name,tier='NIGHTLY')=>({kind:'browser',file,name,tier})
  const rows=[...cases.map(row=>({...row,tier:row.tier==='NIGHTLY'?'GATED-FULL':row.tier})),browser('tests/gated.spec.ts','guard','GATED-FULL'),browser('tests/optional.spec.ts','core','ESSENTIAL'),
    browser('tests/optional.spec.ts','gallery')]
  for(const event of ['pull_request','merge_group','push']) {
    const picked=select(rows,{event,paths:['.github/workflows/ci.yml','web/package.json','web/playwright.ui.config.ts']})
    assert.equal(picked.full,true)
    assert.deepEqual(picked.tests,rows.slice(0,-1))
    assert.equal(picked.scope,'gated-full')
    assert.equal(picked.deferredBrowserCases,1)
  }
  for(const options of [{event:'schedule'},{event:'workflow_dispatch',forceAll:true},{event:'pull_request',forceAll:true}]) {
    const picked=select(rows,{...options,paths:['.github/workflows/ci.yml']})
    assert.deepEqual(picked.tests,rows)
    assert.equal(picked.scope,'catalogue')
    assert.equal(picked.deferredBrowserCases,0)
  }
})

test('optional changed browser cases run in changed-area selection; uncertain impact widens only to the gate',()=>{
  const rows=[{kind:'browser',file:'tests/optional.spec.ts',name:'race',tier:'NIGHTLY'},
    {kind:'browser',file:'tests/other.spec.ts',name:'other',tier:'GATED-FULL'}]
  const webImports={'src/store.ts':[],'tests/optional.spec.ts':['src/store.ts'],'tests/other.spec.ts':[]}
  for(const path of ['web/tests/optional.spec.ts','web/src/store.ts']) {
    const picked=select(rows,{event:'merge_group',paths:[path],webImports})
    assert.deepEqual(picked.tests,[rows[0]])
    assert.equal(picked.full,false)
  }
  for(const path of ['web/src/deleted.ts','web/src/Unmapped.vue']) {
    const picked=select(rows,{event:'pull_request',paths:[path],webImports:{...webImports,'src/Unmapped.vue':[]}})
    assert.deepEqual(picked.tests,[rows[1]],path)
    assert.equal(picked.deferredBrowserCases,1,path)
  }
})

test('lean planner fetches only the exact event base before diffing and widens on fetch failure',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-tier-planner-'))
  const eventPath=resolve(directory,'event.json'),base='a'.repeat(40)
  for(const event of ['pull_request','merge_group']) {
    writeFileSync(eventPath,JSON.stringify(event==='pull_request'?{pull_request:{base:{sha:base}}}:{merge_group:{base_sha:base}}))
    const calls=[]
    const exec=(bin,args,options)=>{calls.push({bin,args,options});return args[0]==='diff'?'README.md\0internal/auth/key.go\0':''}
    assert.deepEqual(changedPaths(event,{GITHUB_EVENT_PATH:eventPath},{fetchBase:true,exec}),['README.md','internal/auth/key.go'])
    assert.deepEqual(calls,[
      {bin:'git',args:['fetch','--no-tags','--depth=1','origin',base],options:{timeout:30_000}},
      {bin:'git',args:['diff','--name-only','-z','--no-renames',base,'HEAD'],options:{timeout:30_000}}])
    const failed=changedPaths(event,{GITHUB_EVENT_PATH:eventPath},{fetchBase:true,exec:()=>{throw new Error('fetch refused')}})
    assert.equal(failed,undefined)
    assert.equal(schedulingMode(event,failed),'full')
  }
  writeFileSync(eventPath,JSON.stringify({pull_request:{base:{sha:'--invalid'}}}))
  const invalidCalls=[]
  assert.equal(changedPaths('pull_request',{GITHUB_EVENT_PATH:eventPath},{fetchBase:true,exec:(...args)=>{invalidCalls.push(args);return ''}}),undefined)
  assert.deepEqual(invalidCalls,[], 'Invalid base must never be fetched')
})

test('lean tier-plan skips setup, deep checkout and non-premerge checkout work',()=>{
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  const planner=ci.slice(ci.indexOf('\n  tier-plan:'),ci.indexOf('\n  runner-route:'))
  assert.match(planner,/timeout-minutes: 2/)
  assert.match(planner,/fetch-depth: 1\n/)
  assert.doesNotMatch(planner,/setup-node|setup-go|npm ci|fetch-depth: 0/)
  assert.match(planner,/if: contains\(fromJSON\('\["pull_request","merge_group"\]'\), github.event_name\)/)
  assert.match(planner,/pull_request\|merge_group\) node scripts\/test-tiers\/diff.mjs/)
  assert.match(planner,/\*\) echo 'mode=full' >> "\$GITHUB_OUTPUT"/)
})

test('changed modules select dependent specs/units; equal file basenames do not broaden impact',()=>{
  const graph={'src/ceiling.ts':[],'tests/scope.test.ts':['src/ceiling.ts'],'tests/tree.test.ts':[]}
  const selected=select(cases,{event:'pull_request',paths:['web/src/ceiling.ts'],webImports:graph})
  assert.deepEqual(selected.tests.filter(row=>row.kind==='node').map(row=>row.name),['ceiling','revoked'])
  assert.deepEqual(select(cases,{event:'merge_group',paths:['web/tests/tree.test.ts'],webImports:graph}).tests.filter(row=>row.kind==='node').map(row=>row.name),['ceiling','tree'])
})

test('a mapped Go change does not widen unrelated web tests; unmapped rendered components widen web',()=>{
  const goOnly=cases.filter(row=>row.kind==='go'),webOnly=cases.filter(row=>row.kind!=='go')
  assert.equal(select(webOnly,{event:'pull_request',paths:['internal/auth/flow.go']}).full,false)
  assert.deepEqual(select(webOnly,{event:'pull_request',paths:['internal/auth/flow.go']}).tests.map(row=>row.name),['ceiling'])
  assert.equal(select(goOnly,{event:'pull_request',paths:['web/src/ceiling.ts']}).full,false)
  assert.equal(select(webOnly,{event:'pull_request',paths:['web/src/Unmapped.vue'],webImports:{'src/Unmapped.vue':[]}}).full,true)
})

test('web dependency graph follows Vue, TS, export and index imports transitively',()=>{
  const root=mkdtempSync(resolve(tmpdir(),'aeon-tier-graph-'))
  for(const directory of ['src','src/lib','tests','e2e'])mkdirSync(resolve(root,directory),{recursive:true})
  writeFileSync(resolve(root,'src/Screen.vue'),'<template/><script setup>import { grant } from "./lib"</script>')
  writeFileSync(resolve(root,'src/lib/index.ts'),'export { grant } from "./grant"')
  writeFileSync(resolve(root,'src/lib/grant.ts'),'export const grant=true')
  writeFileSync(resolve(root,'tests/grant.spec.ts'),'import "../src/Screen.vue"')
  const graph=webGraph(root)
  assert.deepEqual(graph['src/Screen.vue'],['src/lib/index.ts'])
  const row={kind:'browser',file:'tests/grant.spec.ts',name:'grant',tier:'NIGHTLY'}
  assert.equal(select([row],{event:'pull_request',paths:['web/src/lib/grant.ts'],webImports:graph}).tests.length,1)
})

test('two tier shards contain every selected identity exactly once, including equal printed titles',()=>{
  const duplicate=[w('tests/param.test.ts','undefined'),w('tests/param.test.ts','undefined')].map((row,i)=>({...row,occurrence:i+1}))
  const rows=[...cases,...duplicate],all=[...shard(rows,1,2),...shard(rows,2,2)]
  assert.deepEqual(all.map(key).sort(),rows.map(key).sort())
  assert.equal(new Set(all.map(key)).size,rows.length)
  const pattern=new RegExp(exactPattern(['test [1]','ends.$']))
  assert.ok(pattern.test('test [1]'));assert.ok(!pattern.test('test 1'));assert.ok(!pattern.test('other ends.$'))
})

for(const [kind,maxShards,options] of [['go',7,{}],['browser',12,{}],['node',4,{}],['node',4,{firstShardLast:true}]]) {
  test(`${kind}${options.firstShardLast?' (first shard last)':''} zero-weight owners fill shards before reusing tied bins for counts 1..${maxShards}`,()=>{
    for(let count=1;count<=maxShards;count++) {
      for(const ownerCount of new Set([0,1,Math.max(0,count-1),count,count+1,2*count+1])) {
        for(const distribution of ['mixed','all-zero','equal-positive']) {
          const owners=Array.from({length:ownerCount},(_,i)=>kind==='go'
            ? `internal/owner-${String(i).padStart(2,'0')}`
            : `tests/owner-${String(i).padStart(2,'0')}.${kind==='browser'?'spec':'test'}.ts`)
          // Different registration counts ensure the tie-break counts owners,
          // rather than splitting owners or comparing their number of tests.
          const rows=owners.flatMap((owner,i)=>Array.from({length:1+i%3},(_,j)=>kind==='go'
            ? g(owner,`TestCase${j}`) : {...w(owner,`case ${j}`),kind}))
          const weights=Object.fromEntries(owners.map((owner,i)=>[owner,
            distribution==='all-zero'?0:distribution==='mixed'?(i===0?1:0):1]))
          const before={rows:structuredClone(rows),weights:{...weights}}
          const bins=Array.from({length:count},(_,i)=>shard(rows,i+1,count,weights,options))
          const ownerOf=row=>kind==='go'?row.package:row.file
          const ownerBins=bins.map(bin=>[...new Set(bin.map(ownerOf))])
          const context=`${kind}${options.firstShardLast?' (first shard last)':''}: ${count} shards, ${ownerCount} owners, ${distribution}`
          assert.deepEqual(bins.flat().map(key).sort(),rows.map(key).sort(),context)
          assert.equal(new Set(bins.flat().map(key)).size,rows.length,context)
          assert.deepEqual(ownerBins.flat().sort(),owners,`${context}: owners stay whole`)
          assert.equal(ownerBins.filter(bin=>bin.length).length,Math.min(count,ownerCount),`${context}: empty shards only when owners are fewer`)
          for(let i=0;i<count;i++) {
            assert.deepEqual(shard([...rows].reverse(),i+1,count,weights,options).map(key).sort(),bins[i].map(key).sort(),`${context}: input order cannot change assignment`)
          }
          if(distribution==='all-zero'&&!options.firstShardLast) {
            // Equal weights and owner counts resolve by path then shard index.
            assert.deepEqual(ownerBins,Array.from({length:count},(_,i)=>owners.filter((_,j)=>j%count===i)),context)
          }
          assert.deepEqual({rows,weights},before,`${context}: inputs remain unchanged`)
        }
      }
    }
  })
}

test('fewer owners than shards retain successful empty-shard reports in Go, unit and browser callers',async()=>{
  const {run}=await import('./cli.mjs')
  const env={RUNNER_ENVIRONMENT:'github-hosted',GITHUB_RUN_ID:'empty-shard',GITHUB_RUN_ATTEMPT:'1',GITHUB_SHA:'a'.repeat(40)}
  for(const [kind,unit,count,row] of [['go',false,7,cases[0]],['web',true,4,cases[5]],
    ['web',false,12,{kind:'browser',file:'tests/only.spec.ts',name:'only',tier:'ESSENTIAL'}]]) {
    const owner=row.kind==='go'?row.package:row.file
    const tests=shard([row],count,count,{[owner]:0})
    assert.deepEqual(tests,[])
    const job=`ops257r3-empty-${kind}-${unit?'unit':'cases'}`
    assert.equal(await run(kind,{tests,all:[row],full:true,scope:'gated-full',reason:'fewer owners than shards'},
      {unit,job,env}),0)
    const report=JSON.parse(readFileSync(new URL(`../../tmp/test-tiers/${job}-measurement.json`,import.meta.url)))
    assert.equal(checkFull(report,env),true)
    assert.ok(Object.values(report.classes).every(tier=>tier.selected===0&&tier.run===0&&tier.passed===0&&tier.notRun===0&&tier.failed===0))
  }
})

test('reports distinguish missing, skipped and failed outcomes; nested Go subtests cannot inflate totals',()=>{
  const text=[{Package:'github.com/inspr-at/paimos/internal/auth',Test:'TestCeiling',Action:'run'},
    {Package:'github.com/inspr-at/paimos/internal/auth',Test:'TestCeiling/sub',Action:'pass'},
    {Package:'github.com/inspr-at/paimos/internal/auth',Test:'TestCeiling',Action:'pass'}].map(JSON.stringify).join('\n')
  const report=reportCases(cases.slice(0,3),goOutcomes(text),60,'go-test-1')
  assert.equal(report.classes.ESSENTIAL.passed,1)
  assert.equal(report.classes.ESSENTIAL.notRun,1)
  assert.equal(report.classes.NIGHTLY.notRun,1)
  assert.equal(report.executionMinutes,1);assert.equal(report.runnerMinutes,null)
  const inactive=reportCases([{...cases[0],active:false}],[],0,'darwin-only')
  assert.equal(inactive.classes.ESSENTIAL.platformInactive,1)
  assert.equal(inactive.classes.ESSENTIAL.passed,0)
  const browser={kind:'browser',file:'tests/a.spec.ts',name:'action',tier:'ESSENTIAL',id:'id',project:''}
  assert.throws(()=>browserOutcomes({suites:[{specs:[{id:'id',tests:[{results:[{status:'failed'},{status:'passed'}]}]}]}]},[browser]),/Automatic retries forbidden/)
})

test('job accounting includes setup in minutes and reports missing artifacts rather than zero-case success',()=>{
  const job={name:'go-test (1)',status:'completed',conclusion:'failure',started_at:'2026-10-04T01:00:00Z',completed_at:'2026-10-04T01:02:00Z'}
  assert.equal(jobMinutes(job),2)
  assert.equal(jobMinutes({...job,status:'in_progress'}),null)
  const measurement=aggregate([], [job])
  assert.equal(measurement.measured.goRunnerMinutes,2)
  assert.equal(measurement.coverage,'incomplete')
  assert.deepEqual(measurement.missingEvidence,['go-test-1'])
})

test('Actions skipped jobs with zero completed_at never invalidate PR, queue, push or nightly accounting',()=>{
  // Exact shape returned for tree-reuse on the failing PR/merge-group runs.
  const skipped={name:'tree-reuse',status:'completed',conclusion:'skipped',
    started_at:'2026-10-05T10:00:00Z',completed_at:'0001-01-01T00:00:00Z',steps:[]}
  const lanes={
    pull_request:{names:['tree-reuse','cache-prime','go-test (1)','go-timing','web-setup','web-shard (1)'],options:{lane:'docs-only'},missing:[]},
    merge_group:{names:['tree-reuse','cache-prime'],options:{lane:'full'},missing:[]},
    push:{names:['go-test (1)','go-timing','web-setup','web-shard (1)','release-check-run','e2e-run'],options:{lane:'full',reusedFrom:'123'},missing:[]},
    workflow_dispatch:{names:['tree-reuse','cache-prime'],options:{lane:'full'},missing:[]},
    schedule:{names:['nightly-go-test (1)','nightly-go-timing','nightly-web-setup','nightly-web-shard (1)'],options:{},missing:['go-test-1','go-timing','web-unit','web-shard-1']},
  }
  for(const [event,{names,options,missing}] of Object.entries(lanes)) for(const timestamps of [
    {},{started_at:'0001-01-01T00:00:00Z'},
    {started_at:null,completed_at:null},{started_at:0,completed_at:0},
  ]) {
    const jobs=names.map(name=>({...skipped,name,...timestamps}))
    const report=aggregate([],jobs,options)
    assert.ok(report.jobs.every(job=>job.conclusion==='skipped'&&job.runnerMinutes===null),event)
    assert.deepEqual(report.missingEvidence,missing,event)
    assert.equal(report.coverage,missing.length?'incomplete':options.reusedFrom?'reused':'reported',event)
    assert.equal(report.measured.goRunnerMinutes,0,event)
    assert.equal(report.measured.webRunnerMinutes,0,event)
    assert.deepEqual(report.classes,{},'Skipped jobs must not fabricate case passes')
  }
  assert.equal(jobMinutes({...skipped,status:'queued',conclusion:null}),null)
  assert.equal(jobMinutes({...skipped,conclusion:'cancelled',runner_id:0}),null)
  assert.throws(()=>jobMinutes({...skipped,conclusion:'cancelled',runner_id:1}),/Invalid job timestamps/)
  assert.throws(()=>jobMinutes({...skipped,conclusion:'cancelled',runner_id:0,steps:[{name:'Checkout',conclusion:'success'}]}),/Invalid job timestamps/)
})

// Classification: fixed go-static/nightly-go-static CI checks, outside the
// dynamically collected Go/web tier and browser-shard inventories.
test('AEON-732 full and nightly skipped tier fixtures remain incomplete after upstream failures',()=>{
  const finished={status:'completed',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'}
  const skipped={...finished,conclusion:'skipped',completed_at:'0001-01-01T00:00:00Z',steps:[]}
  for(const lane of [undefined,'full']) for(const prerequisite of ['tier-plan','web-setup']) {
    const jobs=[{...finished,name:prerequisite,conclusion:'failure'},
      ...['go-test (1)','go-timing','web-unit (1)','web-shard (1)'].map(name=>({...skipped,name}))]
    const report=aggregate([],jobs,{lane})
    assert.equal(report.coverage,'incomplete')
    assert.equal(report.coverageReason,'skipped after upstream failure')
    assert.deepEqual(report.missingEvidence,['go-test-1','go-timing','web-unit-1','web-shard-1'])
    assert.deepEqual(report.upstreamFailures,[{name:prerequisite,status:'completed',conclusion:'failure'}])
    assert.deepEqual(report.skippedJobs,jobs.slice(1).map(job=>job.name))
    assert.ok(report.jobs.slice(1).every(job=>job.runnerMinutes===null))
    assert.equal(report.measured.goRunnerMinutes,0)
    assert.equal(report.measured.webRunnerMinutes,prerequisite==='web-setup'?2:0)
    assert.deepEqual(report.classes,{})
    // Required skips remain incomplete even without prerequisite metadata.
    const noPrerequisite=aggregate([],jobs.slice(1),{lane})
    assert.equal(noPrerequisite.coverage,'incomplete')
    assert.equal(noPrerequisite.coverageReason,'required tier jobs skipped')
  }
  const nightly=aggregate([], [{...finished,name:'nightly-web-setup',conclusion:'failure'},
    {...skipped,name:'nightly-web-shard (1)'}])
  assert.equal(nightly.coverage,'incomplete')
  assert.equal(nightly.coverageReason,'skipped after upstream failure')
  assert.deepEqual(nightly.missingEvidence,['web-unit','web-shard-1'])
  assert.deepEqual(nightly.upstreamFailures,[{name:'nightly-web-setup',status:'completed',conclusion:'failure'}])
  for(const name of ['tier-plan','web-setup','nightly-web-setup']) for(const conclusion of ['failure','cancelled','skipped']) {
    const job={...finished,name,conclusion,...(conclusion==='skipped'?{completed_at:'0001-01-01T00:00:00Z'}:{})}
    // Even a retained unit artifact cannot conceal a later setup/build failure.
    const report=aggregate([reportCases([],[],0,'web-unit')],[job])
    const skippedUnit=name==='nightly-web-setup'&&conclusion==='skipped'
    assert.deepEqual(report.missingEvidence,skippedUnit?['web-unit']:[])
    assert.equal(report.coverage,'incomplete')
    assert.equal(report.coverageReason,skippedUnit?'skipped after upstream failure':'upstream jobs did not succeed')
    assert.deepEqual(report.upstreamFailures,[{name,status:'completed',conclusion}])
  }
  const jobs=['go-test (1)','go-timing','web-unit (1)','web-shard (1)'].map(name=>({...skipped,name}))
  const exact=aggregate([],jobs,{lane:'spec-only'})
  assert.equal(exact.coverage,'untiered')
  assert.deepEqual(exact.missingEvidence,[])
  assert.deepEqual(exact.classes,{})
  // Actions may skip a matrix before it expands, leaving the family name.
  const unexpanded=aggregate([],jobs.map(job=>({...job,name:job.name.replace(/ \(\d+\)$/,'')})))
  assert.equal(unexpanded.coverage,'incomplete')
  assert.deepEqual(unexpanded.missingEvidence,['go-test','go-timing','web-unit','web-shard'])
  const unexpected=aggregate([], [{...finished,name:'go-test (1)',conclusion:'success'}],{lane:'docs-only'})
  assert.equal(unexpected.coverage,'incomplete','Executed docs-only jobs still require evidence')
  const successful=aggregate([reportCases([],[],0,'go-test-1')],[{...finished,name:'go-test (1)',conclusion:'success'}],{lane:'full'})
  assert.equal(successful.coverage,'reported')
  assert.deepEqual(successful.missingEvidence,[])
  const artifactForSkipped=aggregate([reportCases([],[],0,'go-test-1')],[jobs[0]],{lane:'full'})
  assert.equal(artifactForSkipped.coverage,'incomplete')
  assert.deepEqual(artifactForSkipped.missingEvidence,['go-test-1'])
})

test('AEON-732 measurement CLI fixtures expose upstream skips in JSON, summary and exit status',()=>{
  const root=mkdtempSync(resolve(tmpdir(),'aeon-tier-measurement-'))
  const finished={status:'completed',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'}
  const skipped={...finished,conclusion:'skipped',completed_at:'0001-01-01T00:00:00Z',steps:[]}
  const jobs=[{...finished,name:'tier-plan',conclusion:'failure'},
    ...['go-test (1)','go-timing','web-setup','web-unit (1)','web-shard (1)'].map(name=>({...skipped,name}))]
  const evidence=resolve(root,'evidence'),summary=resolve(root,'summary.txt')
  mkdirSync(evidence)
  for(const fixture of [
    {lane:'full',status:1,coverage:'incomplete',reason:'skipped after upstream failure'},
    {lane:'docs-only',planned:true,status:0,coverage:'reported'},
    {lane:'full',planned:true,reuse:'merge_group',status:0,coverage:'reused'},
    {lane:'full',planned:true,reuse:'pull_request',status:0,coverage:'reused'},
    {lane:'spec-only',planned:true,status:0,coverage:'untiered'},
    {lane:'spec-only',status:1,coverage:'incomplete',reason:'upstream jobs did not succeed'},
  ]) {
    const fixtureEvidence=mkdtempSync(resolve(root,'case-evidence-'))
    if(fixture.reuse==='pull_request')writeFileSync(resolve(fixtureEvidence,'go-timing-measurement.json'),JSON.stringify({version:1,job:'go-timing',classes:{},runId:'123',attempt:'2',sha:'a'.repeat(40)}))
    const fixtureJobs=jobs.map(job=>fixture.reuse==='pull_request'&&job.name==='go-timing'?{...job,...finished,conclusion:'success'}:fixture.planned&&(job.name==='tier-plan'||fixture.lane==='spec-only'&&job.name.startsWith('web-'))
      ? {...job,...finished,conclusion:'success'}:job)
    writeFileSync(resolve(root,'gh'),`#!${process.execPath}\nif (process.argv[2] !== 'api' || process.argv[3] !== 'repos/example/aeon/actions/runs/123/attempts/2/jobs?per_page=100&page=1') process.exit(2)\nprocess.stdout.write(${JSON.stringify(JSON.stringify({total_count:fixtureJobs.length,jobs:fixtureJobs}))})\n`,{mode:0o755})
    writeFileSync(summary,'')
    const result=spawnSync(process.execPath,[fileURLToPath(new URL('./measure.mjs',import.meta.url)),fixtureEvidence],{
      cwd:root,encoding:'utf8',timeout:10_000,
      env:{PATH:root,GITHUB_REPOSITORY:'example/aeon',GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'.repeat(40),
        GITHUB_STEP_SUMMARY:summary,CI_LANE:fixture.lane,REUSE:fixture.reuse??'',SOURCE_RUN:fixture.reuse?'456':''},
    })
    assert.ifError(result.error)
    assert.equal(result.status,fixture.status,result.stderr)
    assert.equal(result.stderr,'')
    const report=JSON.parse(readFileSync(resolve(root,'tmp/test-tier-run-measurement.json'),'utf8'))
    const output=JSON.parse(result.stdout)
    for(const evidence of [report,output]) {
      assert.equal(evidence.coverage,fixture.coverage)
      assert.equal(evidence.coverageReason,fixture.reason)
      assert.deepEqual(evidence.classes,{})
    }
    assert.ok(readFileSync(summary,'utf8').includes(`Test tiers: ${fixture.coverage}${fixture.reason?` — ${fixture.reason}`:''};`))
    if(fixture.lane==='full'&&!fixture.reuse) {
      assert.deepEqual(report.missingEvidence,['go-test-1','go-timing','web-unit-1','web-shard-1'])
      assert.ok(readFileSync(summary,'utf8').includes('tier-plan (failure), web-setup (skipped)'))
      assert.equal(report.measured.sharedPlanningRunnerMinutes,2)
      assert.equal(report.measured.goRunnerMinutes,0)
      assert.equal(report.measured.webRunnerMinutes,0)
    } else assert.deepEqual(report.missingEvidence,[])
  }
})

test('completed jobs that ran still reject absent, zero, invalid and reversed timestamps',()=>{
  const job={name:'go-test (1)',status:'completed',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'}
  for(const conclusion of ['success','failure','cancelled']) for(const timestamps of [
    {started_at:null},{completed_at:null},{started_at:undefined},{completed_at:undefined},
    {started_at:''},{completed_at:''},{started_at:0},{completed_at:0},
    {started_at:'0001-01-01T00:00:00Z'},{completed_at:'0001-01-01T00:00:00Z'},
    {started_at:'invalid'},{completed_at:'2026-10-05T09:59:59Z'},
  ]) assert.throws(()=>aggregate([], [{...job,conclusion,...timestamps}]),/Invalid job timestamps: go-test \(1\)/)
})

test('four unit shards partition the existing ledger by file with every identity selected once',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  for(const event of ['pull_request','merge_group','push','workflow_dispatch','schedule']) {
    const selected=select(manifest.tests,{event,paths:['.github/workflows/ci.yml']}).tests.filter(row=>row.kind!=='browser')
    const bins=Array.from({length:4},(_,i)=>shard(selected,i+1,4))
    assert.deepEqual(bins.flat().map(key).sort(),selected.map(key).sort(),event)
    const owners=new Map()
    bins.forEach((rows,i)=>rows.forEach(row=>{
      if(owners.has(row.file))assert.equal(owners.get(row.file),i,'A file must not run twice')
      owners.set(row.file,i)
    }))
    const sizes=bins.map(rows=>rows.length).sort((a,b)=>a-b)
    // Nightly retains a large parameterized file; it is intentionally kept
    // whole and does not participate in the four full-gate unit jobs.
    if(event!=='schedule')assert.ok(sizes[3]<=1.3*(sizes[1]+sizes[2])/2,`Unit registration balance: ${sizes}`)
  }
})

test('serial tier weights rebalance the twelve browser shards without changing full or changed-area coverage',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../../web/ci-web-shards.json',import.meta.url)))
  const rows=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url))).tests
  const graph=Object.fromEntries([...new Set(rows.map(row=>row.file))].map(file=>[file,[]]))
  for(const options of [
    {event:'pull_request',paths:['.github/workflows/ci.yml']},
    {event:'merge_group',paths:['web/tests/clip-tip.spec.ts'],webImports:graph},
    {event:'push'}, {event:'workflow_dispatch'}, {event:'schedule'},
  ]) {
    const selected=select(rows,options).tests.filter(row=>row.kind==='browser')
    const weights=tierWeights(manifest,selected)
    const bins=Array.from({length:12},(_,i)=>shard(selected,i+1,12,weights))
    assert.deepEqual(bins.flat().map(key).sort(),selected.map(key).sort(),options.event)
    if(options.paths?.[0]==='.github/workflows/ci.yml'||options.event==='push') {
      const loads=bins.map(bin=>[...new Set(bin.map(row=>row.file))].reduce((n,file)=>n+weights[file],0)).sort((a,b)=>a-b)
      assert.ok(loads[11]<=1.3*(loads[5]+loads[6])/2,`Serial scheduling estimates: ${loads}`)
      assert.ok(loads[11]<=1.25*(loads[5]+loads[6])/2,`Hosted scheduling estimates: ${loads}`)
    }
  }
})

test('hosted unit and Go weights preserve files/packages and every selected identity',()=>{
  for(const kind of ['web','go']) {
    const manifest=JSON.parse(readFileSync(new URL(`../ci/${kind}-test-tiers.json`,import.meta.url)))
    for(const event of ['pull_request','merge_group','push','schedule']) {
      const rows=select(manifest.tests,{event,paths:['.github/workflows/ci.yml']}).tests.filter(row=>kind==='web'?row.kind!=='browser':row.lane!=='timing')
      const weights=measuredWeights(manifest,rows),count=kind==='web'?4:7
      const bins=Array.from({length:count},(_,i)=>shard(rows,i+1,count,weights,{firstShardLast:kind==='web'}))
      assert.deepEqual(bins.flat().map(key).sort(),rows.map(key).sort())
      const owners=new Map()
      bins.forEach((bin,i)=>bin.forEach(row=>{
        const owner=kind==='go'?row.package:row.file
        if(owners.has(owner))assert.equal(owners.get(owner),i,'An owner cannot run twice')
        owners.set(owner,i)
      }))
    }
  }
})

test('unit tie preference keeps the heaviest file away from shard 1 extra checks without losing cases',()=>{
  const rows=[...Array(4)].flatMap((_,i)=>[1,2].map(n=>({kind:'node',file:`tests/unit-${i}.test.ts`,name:`case ${n}`})))
  const weights=Object.fromEntries(rows.map(row=>[row.file, row.file.includes('unit-0')?44:30]))
  const bins=Array.from({length:4},(_,i)=>shard(rows,i+1,4,weights,{firstShardLast:true}))
  assert.deepEqual(bins.flat().map(key).sort(),rows.map(key).sort())
  assert.deepEqual(bins[1],rows.slice(0,2))
  assert.ok(bins[0].every(row=>row.file!=='tests/unit-0.test.ts'))
  assert.deepEqual(shard(rows,1,1,weights,{firstShardLast:true}).map(key).sort(),rows.map(key).sort())
  assert.deepEqual(shard(rows,1,4,weights),rows.slice(0,2),'Go/browser default tie policy stays unchanged')
})

test('measured weights scale the selected slice, retain zero elapsed and reject invalid data',()=>{
  const manifest={timingWeights:{owners:{'tests/scope.test.ts':{seconds:10,selectedTests:2}}}}
  assert.deepEqual(measuredWeights(manifest,[cases[5]]),{'tests/scope.test.ts':5})
  assert.deepEqual(measuredWeights(manifest,cases),{'tests/scope.test.ts':10})
  manifest.timingWeights.owners['tests/scope.test.ts'].seconds=0
  assert.deepEqual(measuredWeights(manifest,cases),{'tests/scope.test.ts':0})
  for(const timing of [{seconds:-1,selectedTests:2},{seconds:NaN,selectedTests:2},{seconds:1,selectedTests:0},{seconds:1,selectedTests:1.5}]) {
    manifest.timingWeights.owners['tests/scope.test.ts']=timing
    assert.throws(()=>measuredWeights(manifest,cases),/Invalid measured timing: tests\/scope.test.ts/)
  }
})

test('unit measurement artifacts are required for every executed shard and legacy nightly units',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-unit-measurements-'))
  const job=name=>({name,status:'completed',conclusion:'success',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'})
  const names=['web-unit-1','web-unit-2','web-unit-3','web-unit-4']
  for(const name of names)writeFileSync(resolve(directory,`${name}-measurement.json`),JSON.stringify(reportCases([],[],0,name)))
  const jobs=names.map((_,i)=>job(`web-unit (${i+1})`))
  const reports=readReports(directory)
  assert.equal(reports.length,4)
  assert.equal(aggregate(reports,jobs).coverage,'reported')
  for(const name of names)assert.deepEqual(aggregate(reports.filter(r=>r.job!==name),jobs).missingEvidence,[name])
  assert.deepEqual(aggregate([], [job('nightly-web-setup')]).missingEvidence,['web-unit'])
  assert.deepEqual(aggregate([], [job('web-setup')]).missingEvidence,[],'Main setup no longer executes units')
  const exact=aggregate([], [job('web-unit (1)'),job('web-shard (1)')],{lane:'spec-only'})
  assert.equal(exact.coverage,'untiered')
  assert.deepEqual(exact.classes,{})
  assert.deepEqual(exact.missingEvidence,[])
  assert.match(exact.caseScope,/no tier passes are claimed/)
  assert.deepEqual(aggregate([], [job('web-unit (1)'),job('web-shard (1)')],{lane:'full'}).missingEvidence,['web-unit-1','web-shard-1'])
})

test('rerun measurements reject earlier-attempt artifacts instead of replaying case passes',()=>{
  const job={name:'go-test (1)',status:'completed',started_at:'2026-10-04T01:00:00Z',completed_at:'2026-10-04T01:01:00Z'}
  const earlier={...reportCases([cases[0]],[{key:key(cases[0]),status:'passed',started:true}],1,'go-test-1'),runId:'123',attempt:'1',sha:'a'}
  const report=aggregate([earlier],[job],{runId:'123',attempt:'2',sha:'a'})
  assert.equal(report.coverage,'incomplete');assert.deepEqual(report.classes,{})
  assert.deepEqual(report.missingEvidence,['go-test-1']);assert.equal(report.excludedEvidence.length,1)
  const fresh=aggregate([{...earlier,attempt:'2'}],[job],{runId:'123',attempt:'2',sha:'a'})
  assert.equal(fresh.coverage,'reported');assert.equal(fresh.classes.ESSENTIAL.passed,1)
})

test('exact-SHA reuse records provenance and current costs without fabricating fresh case passes',()=>{
  const jobs=[{name:'go-test (1)',status:'completed',started_at:'2026-10-04T01:00:00Z',completed_at:'2026-10-04T01:00:30Z'}]
  const reused=aggregate([],jobs,{reusedFrom:'123'})
  assert.equal(reused.coverage,'reused');assert.equal(reused.reusedFrom,'123')
  assert.deepEqual(reused.classes,{});assert.deepEqual(reused.missingEvidence,[])
  assert.equal(reused.measured.goRunnerMinutes,0.5)
  assert.throws(()=>aggregate([],jobs,{reusedFrom:'invalid'}),/Invalid reused source/)
  assert.throws(()=>aggregate([{}],jobs,{reusedFrom:'123'}),/must not report fresh test passes/)
})

test('committed allowlists preserve classifications, helpers and reviewed AEON-541 demotions without fixed inventory sizes',()=>{
  const go=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const web=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  for(const manifest of [go,web]) {
    const reconciled=validate(manifest,manifest.tests)
    assert.deepEqual(reconciled.map(key).sort(),manifest.tests.map(key).sort())
    for(const tier of ['ESSENTIAL','GATED-FULL']) assert.ok(reconciled.some(row=>row.tier===tier),tier)
  }
  for(const helper of ['TestFakeVendorProcess','TestNoOutboundServerHelper'])assert.equal(go.tests.find(row=>row.name===helper).tier,'ESSENTIAL')
  const known=JSON.parse(readFileSync(new URL('../ci/known-flaky.json',import.meta.url)))
  const guards=web.tests.filter(row=>row.file==='tests/no-shift.spec.ts'&&known.entries.some(entry=>entry.key===key(row)))
  assert.deepEqual(guards.map(key).sort(),known.entries.filter(entry=>entry.key.startsWith('browser:tests/no-shift.spec.ts:')).map(entry=>entry.key).sort())
  assert.ok(guards.every(row=>row.tier==='GATED-FULL'))
  assert.ok(go.tests.some(row=>row.tags?.includes('delete-candidate')))
  assert.ok(go.tests.some(row=>row.lane==='timing'))
  assert.ok([...go.tests,...web.tests].filter(row=>row.tags?.includes('delete-candidate')).every(row=>row.tier==='GATED-FULL'||row.tier==='NIGHTLY'))
})

test('strict classification maintenance is scheduled separately and never a required PR or nightly test dependency',()=>{
  const nightly=readFileSync(new URL('../../.github/workflows/nightly-full.yml',import.meta.url),'utf8')
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  const job=nightly.slice(nightly.indexOf('\n  nightly-tier-classification:'),nightly.indexOf('\n  nightly-web-shard:'))
  assert.match(job,/if: github.event_name == 'schedule'/)
  assert.match(job,/cli\.mjs check go --strict/)
  assert.match(job,/cli\.mjs check web --strict/)
  assert.match(job,/Report web cases needing classification\n\s+if: always\(\)/)
  assert.doesNotMatch(ci,/--strict/)
  assert.doesNotMatch(nightly,/needs:.*nightly-tier-classification/)
})

test('nightly runs every tier and fixed gate; PR/MQ aggregates and compatibility remain',()=>{
  const read=name=>readFileSync(new URL(`../../.github/workflows/${name}`,import.meta.url),'utf8')
  const nightly=read('nightly-full.yml'),ci=read('ci.yml')
  assert.match(nightly,/schedule:\n\s+- cron:/);assert.match(nightly,/workflow_dispatch:/)
  assert.doesNotMatch(nightly,/pull_request:|merge_group:|runner-route:/)
  for(const command of ['run go --all --shard','run web --all --unit','run web --all --shard'])assert.ok(nightly.includes(command))
  for(const id of ['go-static','go-timing','migration-compat','e2e','release-check'])assert.ok(nightly.includes(`  nightly-${id}:`))
  assert.match(ci,/  go:\n\s+if: always\(\)\n\s+needs: \[ci-plan, go-test, go-static, go-timing, tree-reuse, cache-prime, tier-plan\]/)
  assert.match(ci,/  web:\n\s+name: web\n\s+if: always\(\)\n\s+needs: \[ci-plan, web-setup, web-unit, web-shard, tree-reuse, cache-prime, tier-plan\]/)
  assert.match(ci,/  migration-compat:\n\s+permissions:/)
  assert.doesNotMatch(ci,/ci-flake-guard\.mjs --kind/)
})

test('full-execution proof binds actual complete outcomes to this run, attempt and SHA',()=>{
  const report={...reportCases(cases,cases.map(row=>({key:key(row),status:'passed',started:true})),1,'go-test-1'),full:true,scope:'catalogue',exitCode:0,runId:'123',attempt:'2',sha:'a'}
  const env={GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'}
  assert.equal(checkFull(report,env),true)
  for(const change of [{full:false},{exitCode:1},{attempt:'1'},{sha:'b'},{classes:{...report.classes,NIGHTLY:{...report.classes.NIGHTLY,notRun:1}}}])assert.throws(()=>checkFull({...report,...change},env),/Full execution/)
})

test('browser registrations blocked before execution remain notRun rather than counted as failed runs',()=>{
  const row={kind:'browser',file:'tests/a.spec.ts',name:'action',tier:'ESSENTIAL',id:'id',project:''}
  const outcomes=browserOutcomes({suites:[{specs:[{id:'id',tests:[{results:[]}]}]}]},[row])
  const report=reportCases([row],outcomes,1,'web-shard-1')
  assert.equal(report.classes.ESSENTIAL.notRun,1)
  assert.equal(report.classes.ESSENTIAL.run,0)
  assert.equal(report.classes.ESSENTIAL.failed,0)
})

test('three-tier full is exactly ESSENTIAL plus GATED-FULL even for unknown impact and explicit full',()=>{
  const rows=[g('internal/auth','TestCore','ESSENTIAL'),g('internal/auth','TestExisting','GATED-FULL'),
    g('internal/auth','TestUnclassified'),
    {kind:'browser',file:'tests/gated.spec.ts',name:'gate',tier:'GATED-FULL'},
    {kind:'browser',file:'tests/optional.spec.ts',name:'gallery',tier:'NIGHTLY'},
    {kind:'browser',file:'tests/gated.spec.ts',name:'new case defaults to nightly',tier:'NIGHTLY'},
    {kind:'vitest',file:'tests/new.unit.test.ts',name:'new unit defaults to nightly',tier:'NIGHTLY'}]
  for(const event of ['pull_request','merge_group','push','workflow_dispatch']) {
    for(const paths of [undefined,['.github/workflows/ci.yml'],['web/src/deleted.ts'],['scripts/check.mjs','web/tests/optional.spec.ts']]) {
      for(const forceFull of [false,true]) {
        const selection=select(rows,{event,paths,forceFull})
        assert.deepEqual(selection.tests,[rows[0],rows[1],rows[3]],`${event} ${paths} explicit=${forceFull}`)
        assert.equal(selection.scope,'gated-full')
        assert.equal(selection.deferredBrowserCases,2)
      }
    }
  }
  for(const options of [{event:'schedule'},{event:'pull_request',forceAll:true},{event:'workflow_dispatch',forceAll:true}]) {
    const selection=select(rows,{...options,paths:['README.md']})
    assert.deepEqual(selection.tests,rows)
    assert.equal(selection.full,true)
    assert.equal(selection.scope,'catalogue')
    assert.equal(selection.deferredBrowserCases,0)
  }
  // The changed-area lane still exercises optional tests when their area changes.
  for(const event of ['pull_request','merge_group']) assert.deepEqual(
    select(rows,{event,paths:['web/tests/optional.spec.ts'],webImports:{'tests/optional.spec.ts':[]}}).tests,[rows[0],rows[4]])
  for(const event of ['pull_request','merge_group']) assert.deepEqual(
    select(rows,{event,paths:['web/tests/gated.spec.ts'],webImports:{'tests/gated.spec.ts':[]}}).tests,[rows[0],rows[3],rows[5]])
})

test('three-tier validation and reports retain GATED-FULL results and deletion candidates',()=>{
  const rows=[g('internal/auth','TestCore','ESSENTIAL'),{...g('internal/auth','TestExisting','GATED-FULL'),tags:['delete-candidate']},g('internal/auth','TestNew')]
  const manifest={version:1,tests:rows}
  assert.doesNotThrow(()=>validate(manifest,rows,noFlaky))
  assert.deepEqual(validate(manifest,rows,noFlaky),rows)
  assert.deepEqual(counts(rows),{ESSENTIAL:1,'GATED-FULL':1,NIGHTLY:1})
  const report=reportCases(rows,[{key:key(rows[0]),status:'passed',started:true},{key:key(rows[1]),status:'failed',started:true}],1,'go-test-1')
  assert.equal(report.classes['GATED-FULL'].failed,1)
  assert.equal(report.classes['GATED-FULL'].run,1)
  assert.equal(report.classes.NIGHTLY.notRun,1)
  const known={version:1,entries:[{key:key(rows[1]),owner:'AEON-675'}]}
  assert.deepEqual(validate(manifest,rows,known),rows)
  const promoted=rows.map(row=>({...row,tier:'ESSENTIAL',tags:[]}))
  assert.throws(()=>validate({version:1,tests:promoted},rows,known),/Known-flaky case cannot be ESSENTIAL/)
})

test('three-tier manifests allow new NIGHTLY cases and never promote ungated browser cases',()=>{
  const policy=JSON.parse(readFileSync(new URL('../../web/ci-web-shards.json',import.meta.url)))
  const gated=new Set(policy.groups.filter(group=>group.gate!==false).flatMap(group=>group.specs.map(spec=>spec.file)))
  const go=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const web=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  // Cases first classified after the old gate retain the NIGHTLY default.
  // An explicit inventory keeps the assertion strict for every legacy case.
  const postGateCases=new Set([...(go.postGateCases??[]),...(web.postGateCases??[])])
  assert.equal(postGateCases.size,(go.postGateCases??[]).length+(web.postGateCases??[]).length)
  for(const id of postGateCases) {
    const row=[...go.tests,...web.tests].find(row=>key(row)===id)
    assert.ok(row,`Unknown post-gate case: ${id}`)
    assert.equal(row.tier,'NIGHTLY',id)
  }
  for(const row of [...go.tests,...web.tests]) {
    if(postGateCases.has(key(row)))continue
    if(row.tier==='ESSENTIAL')continue
    assert.equal(row.tier,row.kind!=='browser'||gated.has(row.file)?'GATED-FULL':'NIGHTLY',key(row))
  }
  // This established Knowledge registration caused the merge-group regression:
  // its launch group is gated, so it must retain its full-gate classification.
  assert.equal(web.tests.find(row=>row.kind==='browser'&&row.file==='tests/knowledge.spec.ts'&&
    row.name==='the Knowledge tab groups by kind, filters, searches the text and moves with keys')?.tier,'GATED-FULL')
  assert.ok(web.tests.some(row=>row.kind==='browser'&&row.tier==='NIGHTLY'))
  assert.ok(web.tests.some(row=>row.kind==='browser'&&row.tier==='GATED-FULL'))
})

test('three-tier nightly explicitly requests all cases and remains outside required CI jobs',()=>{
  const nightly=readFileSync(new URL('../../.github/workflows/nightly-full.yml',import.meta.url),'utf8')
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  for(const command of ['run go --all --shard','run go --all --timing','run web --all --unit','run web --all --shard'])assert.ok(nightly.includes(command),command)
  assert.doesNotMatch(nightly,/cli\.mjs run (?:go|web) --full/)
  assert.doesNotMatch(ci,/cli\.mjs run (?:go|web) --all|needs:.*nightly-/)
})

test('three-tier full proof rejects incomplete gated results and old two-tier evidence',()=>{
  const empty={selected:0,run:0,passed:0,skipped:0,failed:0,notRun:0,platformInactive:0}
  const report={version:1,full:true,scope:'gated-full',exitCode:0,runId:'123',attempt:'2',sha:'a',
    classes:{ESSENTIAL:{...empty},'GATED-FULL':{...empty,selected:1,run:1,passed:1},NIGHTLY:{...empty}}}
  const env={GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'}
  assert.equal(checkFull(report,env),true)
  for(const scope of [undefined,'changed-area','browser-gate'])assert.throws(()=>checkFull({...report,scope},env),/Full execution/)
  const {['GATED-FULL']:gated,...oldClasses}=report.classes
  assert.throws(()=>checkFull({...report,classes:oldClasses},env),/Full execution/)
  for(const counts of [{...gated,notRun:1},{...gated,failed:1}])assert.throws(()=>checkFull({...report,classes:{...report.classes,'GATED-FULL':counts}},env),/Full execution/)
})


test('MG reuse flag forces both full fanout and full case selection on a lookup miss',()=>{
  const paths=['internal/nodes/module.go'],exists=()=>true
  for(const mgReuse of [undefined,'off','ON','true']) assert.equal(schedulingMode('merge_group',paths,exists,{mgReuse}),'essential')
  assert.equal(schedulingMode('merge_group',paths,exists,{mgReuse:'on'}),'full')
  assert.equal(schedulingMode('pull_request',paths,exists,{mgReuse:'on'}),'essential')
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  assert.match(ci,/env:\n  CI_MG_REUSE: \$\{\{ vars.CI_MG_REUSE \}\}/)
  const cli=readFileSync(new URL('./cli.mjs',import.meta.url),'utf8')
  assert.match(cli,/forceFull:full\|\|schedulingMode\(event,paths,undefined,\{mgReuse:process.env.CI_MG_REUSE\}\)/)
})


test('MG measurements count only fresh timing guards beside reused PR evidence',()=>{
  const selected=[g('internal/budget','TestClock','GATED-FULL')]
  const timing={...reportCases(selected,[{key:key(selected[0]),status:'passed',started:true}],1,'go-timing'),runId:'2',attempt:'1',sha:'a'}
  const jobs=[{name:'go-timing',status:'completed',conclusion:'success',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:01:00Z'},
    {name:'go-test (1)',status:'completed',conclusion:'skipped'}]
  const options={runId:'2',attempt:'1',sha:'a',reusedFrom:'123',freshTiming:true}
  const report=aggregate([timing],jobs,options)
  assert.equal(report.coverage,'reused')
  assert.equal(report.classes['GATED-FULL'].passed,1)
  assert.equal(report.reusedFrom,'123')
  assert.match(report.caseScope,/Only freshly executed timing guard cases/)
  assert.throws(()=>aggregate([{...timing,job:'go-test-1'}],jobs,options),/must not report fresh test passes/)
  assert.throws(()=>aggregate([timing],jobs,{...options,freshTiming:false}),/must not report fresh test passes/)
  for(const reports of [[],[{...timing,attempt:'2'}]]) {
    const missing=aggregate(reports,jobs,options)
    assert.equal(missing.coverage,'incomplete')
    assert.deepEqual(missing.missingEvidence,['go-timing'])
  }
})
