// SPDX-License-Identifier: AGPL-3.0-only
// Offline native-catalogue replay; collection never launches browsers.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'
import { select, validate, webGraph } from './core.mjs'
import { collectGo, collectWeb } from './collect.mjs'
import { schedulingDecision, effectiveLane, sourceTree } from './diff.mjs'
import { boundedText } from './inputs.mjs'
import { classifyPaths } from '../ci-pr-plan.mjs'

const fixture=JSON.parse(readFileSync(new URL('./affected-replay.json',import.meta.url)))
const legacy=await import(`data:text/javascript;base64,${Buffer.from(fixture.legacySelector).toString('base64')}`)
const inventories = {go: collectGo(), web: collectWeb()}
const rows=['go','web'].flatMap(kind=>validate(JSON.parse(readFileSync(new URL(`../ci/${kind}-test-tiers.json`,import.meta.url))), inventories[kind].tests))
const root=fileURLToPath(new URL('../../',import.meta.url))
const graph=webGraph(resolve(root,'web'))
// Replay reads consumers and migrations from the current tree. Historical
// manifest promotions are not reconstructed: manifest edits replay as
// registration-only (no promoted cases), which hosted runs confirm per PR.
const tree=sourceTree(root)
const promotions={keys:new Set(),kinds:new Set(),count:0}
const readFile=path=>boundedText(root,path)
const exists=path=>readFile(path)!==undefined
const replay=fixture.prs.map(({number,paths})=>{
  const options={event:'pull_request',paths,imports:fixture.goImports,webImports:graph}
  const classifiedLane=classifyPaths(paths).lane
  const before=schedulingDecision('pull_request',paths,undefined,{graph,tests:rows})
  const after=schedulingDecision('pull_request',paths,exists,{affectedLane:'on',graph,tree,promotions,tests:rows})
  const oldLane=effectiveLane(classifiedLane,{...before,event:'pull_request',affectedLane:'off'})
  const lane=effectiveLane(classifiedLane,{...after,event:'pull_request',affectedLane:'on'})
  const old=legacy.select(rows,{...options,forceFull:before.mode==='full'})
  const next=select(rows,{...options,affectedLane:'on',forceFull:after.mode==='full',tree,promotions,readFile})
  const risk=select(rows,{...options,affectedLane:'on',tree,promotions,readFile})
  const kinds=selection=>Object.fromEntries(['go','node','vitest','browser'].map(kind=>[kind,selection.tests.filter(row=>row.kind===kind).length]))
  // All-success PR path in today's ci.yml. Reusable route contributes one
  // hosted job; skipped tree-reuse/cache-prime do not contribute runner jobs.
  // Static layout drops go-test (7), go-timing, web-shard (12) and e2e-run.
  const jobs=(lane,mode,layout)=>lane==='docs-only'?9:lane==='spec-only'?12:mode!=='essential'?37:layout==='static'?16:22
  const label=decision=>decision.mode==='essential'&&decision.layout==='static'?'static':decision.mode
  return {number,classifiedLane,oldLane,lane,old:before.mode,new:label(after),oldCases:old.tests.length,newCases:next.tests.length,
    oldKinds:kinds(old),newKinds:kinds(next),oldJobs:jobs(oldLane,before.mode,'full'),newJobs:jobs(lane,after.mode,after.layout),
    reason:risk.full?risk.reason:after.reason}
})
const rule=reason=>/^(?:R\d|CI machinery|consumer fan-out|unnarrowed|unmapped|missing|web module|browser fan-out|invalid)/.exec(reason)?.[0]??'other'
const fullReasons={}
for(const row of replay) if(row.new==='full') fullReasons[rule(row.reason)]=(fullReasons[rule(row.reason)]??0)+1
const transitions={}
for(const row of replay) {
  const label=`${row.old}->${row.new}`
  transitions[label]=(transitions[label]??0)+1
}
const narrowed=replay.filter(row=>row.new!=='full').length
const summary={prs:replay.length,oldEssential:replay.filter(row=>row.old==='essential').length,newNarrowed:narrowed,
  newStatic:replay.filter(row=>row.new==='static').length,percentNarrowed:Math.round(100*narrowed/replay.length),
  oldJobs:replay.reduce((sum,row)=>sum+row.oldJobs,0),newJobs:replay.reduce((sum,row)=>sum+row.newJobs,0),fullReasons}
if(process.argv[2]==='--json')console.log(JSON.stringify({base:fixture.base,transitions,summary,replay},null,2))
else {
  console.table(replay.map(row=>({PR:row.number,lane:row.lane,old:row.old,new:row.new,
    cases:`${row.oldCases}->${row.newCases}`,jobs:`${row.oldJobs}->${row.newJobs}`,reason:row.reason.slice(0,110)})))
  console.log(JSON.stringify({transitions,summary}))
}
