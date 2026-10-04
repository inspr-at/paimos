// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, mkdtempSync, mkdirSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { tmpdir } from 'node:os'
import { validate, select, shard, key, exactPattern, webGraph } from './core.mjs'
import { reportCases, goOutcomes, browserOutcomes } from './report.mjs'
import { aggregate, jobMinutes } from './measure.mjs'
import { checkFull } from './check-full.mjs'
import { schedulingMode } from './diff.mjs'

const g=(pkg,name,tier='NIGHTLY')=>({kind:'go',package:pkg,name,tier,active:true})
const w=(file,name,tier='NIGHTLY')=>({kind:'node',file,name,tier})
const cases=[g('internal/auth','TestCeiling','ESSENTIAL'),g('internal/auth','TestRotation'),
  g('internal/nodes','TestCRUD','ESSENTIAL'),g('internal/nodes','TestUndo'),g('cmd/aeon','TestRoutes'),
  w('tests/scope.test.ts','ceiling','ESSENTIAL'),w('tests/scope.test.ts','revoked'),w('tests/tree.test.ts','tree')]
const fixture=()=>({version:1,tests:structuredClone(cases)})

test('unknown and stale registrations fail; tagging never removes a nightly test',()=>{
  assert.throws(()=>validate(fixture(),[...cases,g('internal/auth','TestNew')]),/Unclassified test.*TestNew/)
  assert.throws(()=>validate(fixture(),cases.slice(1)),/Stale manifest entry.*TestCeiling/)
  const manifest=fixture();manifest.tests[1].tags=['delete-candidate']
  const rows=validate(manifest,cases)
  const full=select(rows,{event:'schedule',paths:[]})
  assert.equal(full.tests.length,cases.length)
  assert.ok(full.tests.find(row=>row.name==='TestRotation').tags.includes('delete-candidate'))
  manifest.tests[0].tags=['delete-candidate']
  assert.throws(()=>validate(manifest,cases),/Invalid classification/)
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

test('committed allowlists preserve 332/60/155, helpers, and AEON-541 behavioral guards',()=>{
  const go=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const web=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  assert.equal(go.tests.filter(row=>row.tier==='ESSENTIAL').length,332)
  assert.equal(web.tests.filter(row=>row.tier==='ESSENTIAL'&&row.kind==='browser').length,60)
  assert.equal(web.tests.filter(row=>row.tier==='ESSENTIAL'&&row.kind!=='browser').length,155)
  for(const helper of ['TestFakeVendorProcess','TestNoOutboundServerHelper'])assert.equal(go.tests.find(row=>row.name===helper).tier,'ESSENTIAL')
  assert.equal(web.tests.filter(row=>row.tier==='ESSENTIAL'&&row.file==='tests/no-shift.spec.ts').length,5)
  assert.equal(go.tests.filter(row=>row.tags?.includes('delete-candidate')).length,158)
  assert.equal(go.tests.filter(row=>row.lane==='timing').length,4)
  assert.ok([...go.tests,...web.tests].filter(row=>row.tags?.includes('delete-candidate')).every(row=>row.tier==='NIGHTLY'))
})

test('nightly runs every tier and fixed gate; PR/MQ aggregates and compatibility remain',()=>{
  const read=name=>readFileSync(new URL(`../../.github/workflows/${name}`,import.meta.url),'utf8')
  const nightly=read('nightly-full.yml'),ci=read('ci.yml')
  assert.match(nightly,/schedule:\n\s+- cron:/);assert.match(nightly,/workflow_dispatch:/)
  assert.doesNotMatch(nightly,/pull_request:|merge_group:|runner-route:/)
  for(const command of ['run go --full --shard','run web --full --unit','run web --full --shard'])assert.ok(nightly.includes(command))
  for(const id of ['go-static','go-timing','migration-compat','e2e','release-check'])assert.ok(nightly.includes(`  nightly-${id}:`))
  assert.match(ci,/  go:\n\s+if: always\(\)\n\s+needs: \[go-test, go-static, go-timing, tree-reuse, cache-prime\]/)
  assert.match(ci,/  web:\n\s+name: web\n\s+if: always\(\)\n\s+needs: \[web-setup, web-shard, tree-reuse, cache-prime\]/)
  assert.match(ci,/  migration-compat:\n\s+permissions:/)
  assert.doesNotMatch(ci,/ci-flake-guard\.mjs --kind/)
})

test('full-execution proof binds actual complete outcomes to this run, attempt and SHA',()=>{
  const report={...reportCases(cases,cases.map(row=>({key:key(row),status:'passed',started:true})),1,'go-test-1'),full:true,exitCode:0,runId:'123',attempt:'2',sha:'a'}
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
