// SPDX-License-Identifier: AGPL-3.0-only
// Offline native-catalogue replay; collection never launches browsers.
import { readFileSync, mkdtempSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'
import { select, validate, webGraph } from './core.mjs'
import { collectGo, collectWeb } from './collect.mjs'
import { schedulingDecision, effectiveLane, sourceTree } from './diff.mjs'
import { boundedText } from './inputs.mjs'
import { classifyPaths } from '../ci-pr-plan.mjs'
import { planningWebCases, browserCaseLimit } from './planning-web.mjs'

const fixture=JSON.parse(readFileSync(new URL('./affected-replay.json',import.meta.url)))
const legacy=await import(`data:text/javascript;base64,${Buffer.from(fixture.legacySelector).toString('base64')}`)
const inventories = {go: collectGo(), web: collectWeb()}
const rows=['go','web'].flatMap(kind=>validate(JSON.parse(readFileSync(new URL(`../ci/${kind}-test-tiers.json`,import.meta.url))), inventories[kind].tests))
const root=fileURLToPath(new URL('../../',import.meta.url))
const graph=webGraph(resolve(root,'web'))
const compareIndex=process.argv.indexOf('--compare-base')
let baseline, comparisonBase
if(compareIndex>=0) {
  const requested=process.argv[compareIndex+1]
  if(!/^[a-f0-9]{7,40}$/.test(requested??''))throw new Error('Expected local base commit SHA')
  comparisonBase=execFileSync('git',['rev-parse',`${requested}^{commit}`],{cwd:root,encoding:'utf8',timeout:30_000}).trim()
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-replay-base-'))
  const archive=execFileSync('git',['archive',comparisonBase,'scripts/test-tiers','scripts/ci'],{cwd:root,timeout:30_000,maxBuffer:64*1024*1024})
  execFileSync('tar',['-x','-C',directory],{input:archive,timeout:30_000})
  baseline=(await import(resolve(directory,'scripts/test-tiers/diff.mjs'))).schedulingDecision
}
// Replay reads consumers and migrations from the current tree. Historical
// manifest promotions are not reconstructed: manifest edits replay as
// registration-only (no promoted cases), which hosted runs confirm per PR.
const tree=sourceTree(root)
const nativeBrowserCounts=new Map()
for(const row of inventories.web.tests.filter(row=>row.kind==='browser'))nativeBrowserCounts.set(row.file,(nativeBrowserCounts.get(row.file)??0)+1)
const bounds=[...nativeBrowserCounts].map(([file,native])=>({file,native,bound:planningWebCases(tree.webTests.get(file))}))
const undercounts=bounds.filter(row=>row.bound<=browserCaseLimit&&row.bound<row.native)
if(undercounts.length)throw new Error(`Browser source bound undercounts native inventory: ${JSON.stringify(undercounts)}`)
const browserBounds={specs:bounds.length,boundedSpecs:bounds.filter(row=>row.bound<=browserCaseLimit).length,
  overLimitOrUnsupported:bounds.filter(row=>row.bound>browserCaseLimit).length,
  unsupportedSpecs:bounds.filter(row=>row.bound>browserCaseLimit).map(row=>row.file).sort(),undercounts}
// Cost-only counterfactual: omit unsupported spec registrations to identify
// which mode differences they cause. Never used for real planning/selection.
const supportedOnlyTree={...tree,webTests:new Map(tree.webTests)}
for(const file of browserBounds.unsupportedSpecs) supportedOnlyTree.webTests.set(file,'')
const promotions={keys:new Set(),kinds:new Set(),count:0}
const readFile=path=>boundedText(root,path)
const exists=path=>readFile(path)!==undefined
const replay=fixture.prs.map(({number,paths})=>{
  const options={event:'pull_request',paths,imports:fixture.goImports,webImports:graph}
  const classifiedLane=classifyPaths(paths).lane
  const before=schedulingDecision('pull_request',paths,undefined,{graph,tree})
  const after=schedulingDecision('pull_request',paths,exists,{affectedLane:'on',graph,tree,promotions,tests:rows})
  const unsupportedOmitted=schedulingDecision('pull_request',paths,exists,{graph,tree:supportedOnlyTree})
  const l4UnsupportedOmitted=schedulingDecision('pull_request',paths,exists,{affectedLane:'on',graph,tree:supportedOnlyTree,promotions})
  const plannerAfter=schedulingDecision('pull_request',paths,exists,{affectedLane:'on',graph,tree,promotions})
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
  const baseDecision=baseline?.('pull_request',paths,exists,{graph})
  return {number,classifiedLane,oldLane,lane,old:before.mode,unsetReason:before.reason,unsupportedOmittedMode:unsupportedOmitted.mode,l4UnsupportedOmittedMode:l4UnsupportedOmitted.mode,new:label(after),sourceBoundL4Mode:plannerAfter.mode,sourceBoundL4Reason:plannerAfter.reason,oldCases:old.tests.length,newCases:next.tests.length,
    ...(baseDecision?{baselineMode:baseDecision.mode,baselineLane:effectiveLane(classifiedLane,{...baseDecision,event:'pull_request'})}:{}),
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
const laneComparison=baseline?{base:comparisonBase,affectedLane:'unset',prs:replay.length,
  matching:replay.filter(row=>row.old===row.baselineMode).length,
  scope:'Mode only on recorded file lists with current tree/common graph; unset effectiveLane comparison is tautological. L4 new uses native rows.',
  essentialAtBase:replay.filter(row=>row.baselineMode==='essential').length,
  differences:replay.filter(row=>row.old!==row.baselineMode)
    .map(row=>({number:row.number,baseMode:row.baselineMode,currentMode:row.old,baseLane:row.baselineLane,currentLane:row.oldLane}))}:undefined
const sourceBoundComparison={
  scope:'Current source-bound planner vs base mode (unset) and vs current native L4 mode; no historical tree or source-bound selection equivalence claim.',
  unsetModeChanges:replay.filter(row=>row.baselineMode&&row.old!==row.baselineMode).map(row=>({number:row.number,base:row.baselineMode,current:row.old})),
  unsupportedUnsetModeChanges:replay.filter(row=>row.baselineMode&&row.old!==row.baselineMode&&row.old==='full'&&row.unsupportedOmittedMode===row.baselineMode)
    .map(row=>row.number),
  l4UnsupportedSpecModeChanges:replay.filter(row=>row.sourceBoundL4Mode==='full'&&row.l4UnsupportedOmittedMode!=='full').map(row=>row.number),
  unsupportedCostMethod:'Counterfactual omits the listed unsupported specs from source estimates; all other planner inputs stay unchanged. This measures cost only and is never a runnable fallback.',
  l4NativeDifferences:replay.filter(row=>row.sourceBoundL4Mode!==(row.new==='static'?'essential':row.new))
    .map(row=>({number:row.number,native:row.new,planner:row.sourceBoundL4Mode,reason:row.sourceBoundL4Reason})),
}
if(process.argv.includes('--json'))console.log(JSON.stringify({base:fixture.base,transitions,summary,browserBounds,sourceBoundComparison,...(laneComparison?{laneComparison}:{}),replay},null,2))
else {
  console.table(replay.map(row=>({PR:row.number,lane:row.lane,old:row.old,new:row.new,
    cases:`${row.oldCases}->${row.newCases}`,jobs:`${row.oldJobs}->${row.newJobs}`,reason:row.reason.slice(0,110)})))
  console.log(JSON.stringify({transitions,summary}))
}
