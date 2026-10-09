// SPDX-License-Identifier: AGPL-3.0-only
// Fixed go-static selector-regression lane, imported by test-tiers.test.mjs.
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, symlinkSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { buildBrowserMap, browserImpact, browserIdentity, coverageSource, nightlyBrowserMap } from './browser-impact.mjs'
import { select, webGraph, graphIncomplete, goImpact, reverseDependants, key } from './core.mjs'
import { schedulingDecision } from './diff.mjs'
import { captureBrowser } from './browser-coverage.mjs'
import { EventEmitter } from 'node:events'
import { fileURLToPath } from 'node:url'

const tree='a'.repeat(40),runId='42',attempt='1'
const browser=(file,name,tier='GATED-FULL')=>({kind:'browser',file,name,tier,config:'playwright.ui.config.ts',project:''})
const rows=[browser('tests/importing.spec.ts','imports source'),browser('tests/navigation.spec.ts','navigates through HTML'),browser('tests/unrelated.spec.ts','unrelated')]
const graph={'src/screen.ts':[],'tests/importing.spec.ts':['src/screen.ts'],'tests/navigation.spec.ts':[],'tests/unrelated.spec.ts':[]}
const observations=rows.map((row,i)=>({...row,status:'passed',complete:true,sources:i<2?['src/screen.ts']:[]}))
const report={version:1,tree,runId,attempt,complete:true,cases:observations}
const map=()=>buildBrowserMap([report],{tree,runId,attempt,catalogue:rows})

test('AEON-1019 complete V8 coverage and imports select the importing and non-importing browser specs with zero misses',()=>{
  const browserMap=map()
  assert.deepEqual(browserMap.modules['src/screen.ts'],['tests/importing.spec.ts','tests/navigation.spec.ts'])
  const options={event:'pull_request',paths:['web/src/screen.ts'],webImports:graph,browserMap,baseTree:tree}
  for(const affectedLane of [undefined,'off','on']) {
    const selected=select(rows,{...options,affectedLane})
    assert.equal(selected.full,false)
    assert.deepEqual(selected.tests,rows.slice(0,2))
    const required=new Set(observations.filter(row=>row.sources.includes('src/screen.ts')).map(browserIdentity))
    assert.equal([...required].filter(id=>!selected.tests.some(row=>browserIdentity(row)===id)).length,0)
    const planned=schedulingDecision('pull_request',options.paths,()=>true,{graph,browserMap,baseTree:tree,tests:rows,affectedLane,tree:{complete:true,go:new Map(),webTests:new Map()}})
    assert.deepEqual([planned.mode,planned.layout],['essential','full'])
  }
  // Coverage alone must not erase an additional static importer.
  const staticGraph={...graph,'tests/unrelated.spec.ts':['src/screen.ts']}
  assert.deepEqual(select(rows,{...options,webImports:staticGraph}).tests,rows)
})

test('AEON-1019 maps reject incomplete catalogue, failed/skipped cases, duplicate observations and wrong tree/run/attempt',()=>{
  const altered=[{...report,complete:false},{...report,cases:observations.slice(1)},
    {...report,cases:[...observations,observations[0]]},{...report,tree:'b'.repeat(40)},
    {...report,runId:'41'},{...report,attempt:'2'},
    ...['failed','skipped','timedOut'].map(status=>({...report,cases:[{...observations[0],status},...observations.slice(1)]})),
    {...report,cases:[{...observations[0],complete:false},...observations.slice(1)]},
    {...report,cases:[{...observations[0],sources:['../src/screen.ts']},...observations.slice(1)]}]
  for(const value of altered)assert.equal(buildBrowserMap([value],{tree,runId,attempt,catalogue:rows}),undefined)
  assert.equal(buildBrowserMap([],{tree,runId,attempt,catalogue:rows}),undefined)
  const split=observations.map(row=>({...report,cases:[row]}))
  assert.deepEqual(buildBrowserMap(split,{tree,runId,attempt,catalogue:rows}),map())
})

test('AEON-1019 importer-only, stale, renamed, missing, CSS, asset and data changes use the full browser gate',()=>{
  const variants=[['src/screen.ts',undefined,tree],['src/screen.ts',map(),'b'.repeat(40)],
    ['src/screen.ts',{...map(),complete:false},tree],['src/new.ts',map(),tree],
    ['src/renamed.ts',map(),tree],['src/style.css',map(),tree],['src/icon.svg',map(),tree],
    ['src/data.json',map(),tree],['src/empty.ts',map(),tree]]
  for(const [file,browserMap,baseTree] of variants) {
    const webImports={...graph,'src/empty.ts':[]}
    const impact=browserImpact([file],webImports,browserMap,baseTree)
    assert.equal(impact.full,true,file)
    const selected=select(rows,{event:'pull_request',paths:[`web/${file}`],webImports,browserMap,baseTree})
    assert.equal(selected.full,true,file)
    assert.deepEqual(selected.tests,rows,file)
    assert.equal(schedulingDecision('pull_request',[`web/${file}`],()=>true,{graph:webImports,browserMap,baseTree,tests:rows}).mode,'full',file)
  }
})

test('AEON-1019 browser and Go over-cap boundaries preserve every gated case',()=>{
  const many=Array.from({length:301},(_,i)=>browser('tests/importing.spec.ts',`case ${i}`))
  const options={event:'pull_request',paths:['web/src/screen.ts'],webImports:graph,browserMap:map(),baseTree:tree}
  assert.equal(select(many.slice(0,300),options).full,false)
  const selected=select(many,options)
  assert.equal(selected.full,true);assert.deepEqual(selected.tests,many)
  assert.equal(schedulingDecision('pull_request',options.paths,()=>true,{graph,browserMap:map(),baseTree:tree,tests:many}).mode,'full')
  const go=[{kind:'go',package:'internal/leaf',name:'TestLeaf',tier:'GATED-FULL'},
    ...Array.from({length:301},(_,i)=>({kind:'go',package:'internal/parent',name:`Test${i}`,tier:'GATED-FULL'}))]
  const imports={'internal/parent':['internal/leaf']},paths=['internal/leaf/leaf.go']
  assert.equal(goImpact(go.slice(0,301),paths,imports).full,false)
  assert.equal(goImpact(go,paths,imports).full,true)
  assert.deepEqual(select(go,{event:'pull_request',paths,imports}).tests,go)
  assert.equal(schedulingDecision('pull_request',paths,()=>true,{tests:go,imports,tree:{complete:true}}).mode,'full')
})

test('AEON-1019 graph scans bound sizes before reads and discard symlink or truncated results',()=>{
  const root=mkdtempSync(resolve(tmpdir(),'aeon-browser-graph-'))
  for(const dir of ['src','tests','e2e'])mkdirSync(resolve(root,dir))
  writeFileSync(resolve(root,'src/small.ts'),'export {}')
  writeFileSync(resolve(root,'src/large.ts'),'x'.repeat(64))
  let reads=0
  const failed=webGraph(root,{bounds:{files:100,entries:100,totalBytes:1000,fileBytes:64},read:()=>{reads++;return ''}})
  assert.match(failed[graphIncomplete],/per-file byte bound/);assert.equal(reads,0)
  symlinkSync(resolve(root,'src/small.ts'),resolve(root,'src/link.ts'))
  assert.match(webGraph(root)[graphIncomplete],/symlink/)
  assert.equal(select(rows,{event:'pull_request',paths:['web/src/small.ts'],webImports:webGraph(root)}).full,true)
  assert.equal(schedulingDecision('pull_request',['web/src/small.ts'],()=>true,{graph:webGraph(root),tests:rows}).mode,'full')
  assert.equal(coverageSource('http://localhost:5173/src/screen.vue?vue&type=script'),'src/screen.vue')
  assert.equal(coverageSource('http://localhost:5173/src/data.json'),undefined)
})

test('AEON-1019 nightly map fetch is exact-SHA and refuses missing, stale or truncated artifacts',()=>{
  const calls=[]
  const exec=(command,args)=>{
    calls.push([command,args])
    if(command==='git')return tree+'\n'
    if(args[0]==='repo')return JSON.stringify({nameWithOwner:'fixture/aeon'})
    return JSON.stringify({workflow_runs:[{head_sha:'b'.repeat(40),conclusion:'success',event:'schedule',id:42,run_attempt:1}]})
  }
  const result=nightlyBrowserMap('.', 'c'.repeat(40),rows,{exec})
  assert.equal(result.browserMap,undefined)
  assert.ok(calls.some(([,args])=>args[1]?.includes(`head_sha=${'c'.repeat(40)}`)))
  assert.ok(!calls.some(([,args])=>args[1]?.includes('/artifacts')))
})

test('AEON-1019 V8 starts before a page is returned and retains source observations across navigation and close',async()=>{
  let ready, release
  const reached=new Promise(resolve=>{ready=resolve}),barrier=new Promise(resolve=>{release=resolve})
  const session=new EventEmitter(),page=new EventEmitter(),context=new EventEmitter()
  page.isClosed=()=>true
  session.send=async method=>{
    if(method==='Profiler.startPreciseCoverage') { ready();await barrier }
    if(method==='Profiler.takePreciseCoverage')throw new Error('page already closed')
  }
  session.detach=async()=>{}
  Object.assign(context,{pages:()=>[],serviceWorkers:()=>[],newPage:async()=>page,newCDPSession:async()=>session})
  const original=async()=>context
  const browser={browserType:()=>({name:()=> 'chromium'}),contexts:()=>[],newContext:original}
  const capture=captureBrowser(browser),created=await browser.newContext()
  let returned=false
  const pending=created.newPage().then(value=>{returned=true;return value})
  await reached;assert.equal(returned,false);release();assert.equal(await pending,page)
  session.emit('Debugger.scriptParsed',{url:'http://localhost/src/screen.ts'})
  session.emit('Debugger.scriptParsed',{url:'http://localhost/src/second.vue?vue&type=script'})
  const result=await capture.finish()
  assert.deepEqual(result,{complete:true,sources:['src/screen.ts','src/second.vue']})
  assert.equal(browser.newContext,original)
  const uncertain=captureBrowser(browser)
  await (await browser.newContext()).newPage()
  page.emit('popup',new EventEmitter())
  assert.equal((await uncertain.finish()).complete,false)
})

test('AEON-1019 recorded PR 202 source replay keeps both real importing and non-importing navigation specs',()=>{
  const changes=JSON.parse(readFileSync(new URL('./affected-replay.json',import.meta.url)))
  const source='src/lib/releases.ts'
  assert.ok(changes.prs.find(row=>row.number===202).paths.includes(`web/${source}`))
  const realGraph=webGraph(fileURLToPath(new URL('../../web',import.meta.url)))
  const importers=reverseDependants(new Set([source]),realGraph)
  assert.ok(importers.has('tests/aeon-635.spec.ts'))
  assert.ok(!importers.has('tests/releases.spec.ts'),'navigation cannot be found using the import graph')
  const catalogue=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url))).tests
  const replayRows=['tests/aeon-635.spec.ts','tests/releases.spec.ts','tests/agent-indicator.spec.ts'].map(file=>catalogue.find(row=>row.kind==='browser'&&row.file===file))
  assert.ok(replayRows.every(Boolean))
  // Controlled coverage observations; historical nightly V8 did not exist.
  // The real recorded source edit and spec identities reproduce the missed edge.
  const cases=replayRows.map((row,i)=>({...row,complete:true,status:'passed',sources:i<2?[source]:[]}))
  const browserMap=buildBrowserMap([{...report,cases}],{tree,runId,attempt,catalogue:replayRows})
  const selected=select(replayRows,{event:'pull_request',paths:[`web/${source}`],webImports:realGraph,browserMap,baseTree:tree})
  assert.equal(selected.full,false)
  assert.deepEqual(selected.tests,replayRows.slice(0,2))
  assert.equal(cases.filter(row=>row.sources.includes(source)&&!selected.tests.some(picked=>key(picked)===key(row))).length,0)
  assert.equal(select(replayRows,{event:'pull_request',paths:[`web/${source}`],webImports:realGraph}).full,true)
})

test('AEON-1019 artifact loader accepts a complete exact-base nightly and refuses a partial shard',()=>{
  const checkout=mkdtempSync(resolve(tmpdir(),'aeon-browser-map-load-')),base='c'.repeat(40)
  let partial=false
  const exec=(command,args)=>{
    if(command==='git')return tree+'\n'
    if(command==='unzip')return args[0]==='-Z1'?'browser-impact-web-shard-1.json\n':JSON.stringify(partial?{...report,cases:observations.slice(1)}:report)
    if(args[0]==='repo')return JSON.stringify({nameWithOwner:'fixture/aeon'})
    if(args[1].includes('/workflows/'))return JSON.stringify({workflow_runs:[{head_sha:base,conclusion:'success',event:'schedule',id:42,run_attempt:1}]})
    if(args[1].endsWith('/zip'))return Buffer.from('mock zip bytes')
    return JSON.stringify({total_count:1,artifacts:[{id:12,name:'web-shard-1-tier-measurements',expired:false,size_in_bytes:10}]})
  }
  assert.deepEqual(nightlyBrowserMap(checkout,base,rows,{exec}),{baseTree:tree,browserMap:map()})
  partial=true
  assert.deepEqual(nightlyBrowserMap(checkout,base,rows,{exec}),{baseTree:tree,browserMap:undefined})
})
