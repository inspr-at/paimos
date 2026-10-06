// SPDX-License-Identifier: AGPL-3.0-only
// Offline manifest-level replay, without native test collection or browsers.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { select, webGraph } from './core.mjs'
import { schedulingDecision } from './diff.mjs'
import { classifyPaths } from '../ci-pr-plan.mjs'

const fixture=JSON.parse(readFileSync(new URL('./affected-replay.json',import.meta.url)))
const legacy=await import(`data:text/javascript;base64,${Buffer.from(fixture.legacySelector).toString('base64')}`)
const rows=['go','web'].flatMap(kind=>JSON.parse(readFileSync(new URL(`../ci/${kind}-test-tiers.json`,import.meta.url))).tests)
const graph=webGraph(fileURLToPath(new URL('../../web',import.meta.url)))
const replay=fixture.prs.map(({number,paths})=>{
  const options={event:'pull_request',paths,imports:fixture.goImports,webImports:graph}
  const lane=classifyPaths(paths).lane
  const before=schedulingDecision('pull_request',paths,undefined,{graph})
  const after=schedulingDecision('pull_request',paths,undefined,{affectedLane:'on',graph})
  const old=legacy.select(rows,{...options,forceFull:before.mode==='full'})
  const next=select(rows,{...options,affectedLane:'on',forceFull:after.mode==='full'})
  const risk=select(rows,{...options,affectedLane:'on'})
  const kinds=selection=>Object.fromEntries(['go','node','vitest','browser'].map(kind=>[kind,selection.tests.filter(row=>row.kind===kind).length]))
  // All-success PR path in today's ci.yml. Reusable route contributes one
  // hosted job; skipped tree-reuse/cache-prime do not contribute runner jobs.
  const jobs=mode=>lane==='docs-only'?9:lane==='spec-only'?12:mode==='essential'?22:37
  return {number,lane,old:before.mode,new:after.mode,oldCases:old.tests.length,newCases:next.tests.length,
    oldKinds:kinds(old),newKinds:kinds(next),oldJobs:jobs(before.mode),newJobs:jobs(after.mode),
    reason:risk.full?risk.reason:after.reason}
})
const transitions={}
for(const row of replay) {
  const label=`${row.old}->${row.new}`
  transitions[label]=(transitions[label]??0)+1
}
if(process.argv[2]==='--json')console.log(JSON.stringify({base:fixture.base,transitions,replay},null,2))
else console.table(replay.map(row=>({PR:row.number,lane:row.lane,old:row.old,new:row.new,
  cases:`${row.oldCases}->${row.newCases}`,jobs:`${row.oldJobs}->${row.newJobs}`,reason:row.reason})))
