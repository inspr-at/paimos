// SPDX-License-Identifier: AGPL-3.0-only
import { readdirSync, readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { command } from './collect.mjs'
import { measurementCauses, measurementErrorCause } from './measurement-causes.mjs'

export function jobMinutes(job) {
  // Actions returns completed/skipped jobs with a real started_at and Go's
  // zero completed_at (0001-01-01T00:00:00Z). They never acquired a runner.
  if(job.conclusion==='skipped'||job.status!=='completed') return null
  // A cancelled queued job has explicit evidence that it never ran. A missing
  // timestamp alone is insufficient to exempt an executed/cancelled job.
  if(job.conclusion==='cancelled'&&job.runner_id===0&&Array.isArray(job.steps)&&job.steps.length===0) return null
  const start=Date.parse(job.started_at),end=Date.parse(job.completed_at)
  const seconds=(end-start)/1000
  if(typeof job.started_at!=='string'||typeof job.completed_at!=='string'||!Number.isFinite(seconds)||start<=0||end<=0||seconds<0) throw new Error(`Invalid job timestamps: ${job.name}`)
  return seconds/60
}
export function aggregate(reports,jobs,{runId,attempt,sha,reusedFrom,lane,layout,freshTiming=false}={}) {
  if(layout!==undefined&&!['full','static',''].includes(layout)) throw new Error('Invalid tier layout')
  if(reusedFrom!==undefined && !/^[1-9][0-9]*$/.test(String(reusedFrom))) throw new Error("Invalid reused source run")
  if(reusedFrom!==undefined && reports.length && !(freshTiming&&reports.every(report=>report.job==='go-timing'))) throw new Error("Reused execution must not report fresh test passes")
  const names=new Set(),classes={},excludedEvidence=[],flakeLedger=[],flakeCounts=new Map()
  reports=reports.filter(report=>{
    const mismatched=Object.entries({runId,attempt,sha}).some(([field,value])=>value!==undefined&&String(report[field])!==String(value))
    if(mismatched)excludedEvidence.push({job:report.job,reason:'different or missing run, attempt or SHA'})
    return !mismatched
  })
  for(const report of reports) {
    if(report.version!==1||names.has(report.job)) throw new Error(`Invalid or duplicate measurement: ${report.job}`)
    names.add(report.job)
    if(report.flakeLedger!==undefined&&!Array.isArray(report.flakeLedger)) throw new Error('Invalid flake ledger')
    for(const entry of report.flakeLedger??[]) {
      if(typeof entry.key!=='string'||!entry.key||!['flaky','quarantined'].includes(entry.status)||
        entry.status==='quarantined'&&!/^AEON-\d+$/.test(entry.owner??'')) throw new Error('Invalid flake ledger entry')
      flakeLedger.push({...entry,job:report.job})
      if(entry.status==='flaky') flakeCounts.set(entry.key,(flakeCounts.get(entry.key)??0)+1)
    }
    for(const [tier,counts] of Object.entries(report.classes)) {
      classes[tier]??={selected:0,run:0,passed:0,skipped:0,failed:0,notRun:0,platformInactive:0,flaky:0,quarantined:0}
      for(const field of Object.keys(classes[tier])) {
        const value=counts[field]??(['flaky','quarantined'].includes(field)?0:undefined)
        if(!Number.isSafeInteger(value)||value<0) throw new Error('Invalid case count')
        classes[tier][field]+=value
      }
    }
  }
  // Spec-only uses the existing native unit/exact-spec commands, not tier
  // selection. Report that untiered scope without inventing case evidence.
  // The static layout (PR-only affected lane) schedules no Go shards, timing
  // or browser shards; unit evidence is still required and reported.
  const expectedSkip=job=>!job.name.startsWith('nightly-')&&
    (lane==='docs-only'||lane==='spec-only'&&/^go-/.test(job.name)||layout==='static'&&/^(?:go-test|go-timing|web-shard)(?: \(\d+\))?$/.test(job.name))
  const required=jobs.filter(job=>
    /^(?:nightly-)?(?:go-test|web-unit|web-shard)(?: \(\d+\))?$|^nightly-web-setup$|^(?:nightly-)?go-timing$/.test(job.name)&&
    !(job.conclusion==='skipped'&&expectedSkip(job))&&
    !(lane==='spec-only'&&/^web-(?:unit|shard)(?: \(\d+\))?$/.test(job.name)))
  const evidenceName=name=>name.replace(/^nightly-/,'').replace(/^web-setup$/,'web-unit').replace(/ \((\d+)\)$/,'-$1')
  // A skipped required job has no execution evidence, even if an artifact with
  // its name is present. Full/nightly skips must never become zero-case success.
  const executionRequired=reusedFrom!==undefined?(freshTiming?required.filter(job=>job.name==='go-timing'):[]):required
  const missingEvidence=executionRequired
    .filter(job=>job.conclusion==='skipped'||!names.has(evidenceName(job.name))).map(job=>evidenceName(job.name))
  const upstreamFailures=reusedFrom!==undefined?[]:jobs.filter(job=>
    (job.name==='nightly-web-setup'||lane!=='docs-only'&&/^(?:tier-plan|web-setup)$/.test(job.name))&&
    (job.status!=='completed'||job.conclusion!=='success'))
    .map(({name,status,conclusion})=>({name,status,conclusion}))
  if(reusedFrom!==undefined&&freshTiming&&!jobs.some(job=>job.name==='go-timing'))missingEvidence.push('go-timing')
  const skippedJobs=executionRequired.filter(job=>job.conclusion==='skipped').map(job=>job.name)
  const coverageReason=skippedJobs.length?(upstreamFailures.length?'skipped after upstream failure':'required tier jobs skipped'):
    upstreamFailures.length?'upstream jobs did not succeed':missingEvidence.length?'missing case evidence':undefined
  const runners=jobs.map(job=>({name:job.name,status:job.status,conclusion:job.conclusion,runnerMinutes:jobMinutes(job)}))
  const minutes=prefix=>runners.filter(job=>new RegExp(`^(?:nightly-)?${prefix}(?:-| |$)`).test(job.name)).reduce((sum,job)=>sum+(job.runnerMinutes??0),0)
  return {version:1,runId,attempt,sha,classes,reports,flakeLedger,flakeCounts:Object.fromEntries(flakeCounts),jobs:runners,missingEvidence,excludedEvidence,upstreamFailures,skippedJobs,coverageReason,
    reusedFrom,
    coverage:missingEvidence.length||upstreamFailures.length?'incomplete':reusedFrom!==undefined?'reused':lane==='spec-only'?'untiered':'reported',
    baselines:{goRunnerMinutes:22.5,webRunnerMinutes:42.17},
    measured:{goRunnerMinutes:minutes('go'),webRunnerMinutes:minutes('web'),sharedPlanningRunnerMinutes:minutes('tier-plan')},
    caseScope:reusedFrom!==undefined&&freshTiming?`Only freshly executed timing guard cases are counted; verified full PR run ${reusedFrom} supplies skipped tier execution. Current runner minutes include guards and proof overhead.`:reusedFrom!==undefined?`No tier tests executed in this run; verified full merge-group run ${reusedFrom} supplies execution evidence. Current runner minutes include reuse/cache overhead.`:lane==='spec-only'?'Spec-only: native unit checks and exact changed specs execute without tier case accounting; runner costs are reported, no tier passes are claimed.':'Top-level Go Test/Fuzz including isolated timing, and web unit/UI registrations selected by tiers; fixed static, release and e2e gates retain separate runner costs.'}
}

export function readReports(directory) {
  const reports=[]
  if(!existsSync(directory))return reports
  for(const entry of readdirSync(directory,{withFileTypes:true})) {
    const path=resolve(directory,entry.name)
    if(entry.isDirectory()) reports.push(...readReports(path))
    else if(/^(?:go-test-\d+|go-timing|web-unit(?:-\d+)?|web-shard-\d+)-measurement\.json$/.test(entry.name)) reports.push(JSON.parse(readFileSync(path,'utf8')))
  }
  return reports
}
export function main(directory,env=process.env) {
  if(!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(env.GITHUB_REPOSITORY??'')||! /^[1-9][0-9]*$/.test(env.GITHUB_RUN_ID??'')||! /^[1-9][0-9]*$/.test(env.GITHUB_RUN_ATTEMPT??'')) throw new Error('Missing Actions run identity')
  const jobs=[]
  for(let page=1;page<=20;page++) {
    const result=JSON.parse(command('gh',['api',`repos/${env.GITHUB_REPOSITORY}/actions/runs/${env.GITHUB_RUN_ID}/attempts/${env.GITHUB_RUN_ATTEMPT}/jobs?per_page=100&page=${page}`]))
    jobs.push(...result.jobs)
    if(jobs.length>=result.total_count) break
    if(page===20) throw new Error('Job inventory exceeds measurement bound')
  }
  const report=aggregate(readReports(directory),jobs,{runId:env.GITHUB_RUN_ID,attempt:env.GITHUB_RUN_ATTEMPT,sha:env.GITHUB_SHA,lane:env.CI_LANE,layout:env.TIER_LAYOUT,reusedFrom:['merge_group','pull_request'].includes(env.REUSE)?env.SOURCE_RUN:undefined,freshTiming:env.REUSE==='pull_request'})
  report.measurementCauses=measurementCauses(report)
  mkdirSync('tmp',{recursive:true})
  writeFileSync('tmp/test-tier-run-measurement.json',JSON.stringify(report,null,2)+'\n')
  console.log(JSON.stringify({classes:report.classes,flakeCounts:report.flakeCounts,flakeLedger:report.flakeLedger,measured:report.measured,coverage:report.coverage,coverageReason:report.coverageReason,missingEvidence:report.missingEvidence,upstreamFailures:report.upstreamFailures,skippedJobs:report.skippedJobs,measurementCauses:report.measurementCauses}))
  const coverageLabel=report.coverage+(report.coverageReason?` — ${report.coverageReason}`:'')
  if(env.GITHUB_STEP_SUMMARY) writeFileSync(env.GITHUB_STEP_SUMMARY,`\nTest tiers: ${coverageLabel}; Go ${report.measured.goRunnerMinutes.toFixed(2)} / web ${report.measured.webRunnerMinutes.toFixed(2)} runner-min. Baselines: 22.50 / 42.17.\n\n\`\`\`json\n${JSON.stringify({classes:report.classes,flakeCounts:report.flakeCounts,flakeLedger:report.flakeLedger},null,2)}\n\`\`\`\n${report.reusedFrom?`Verified execution source run: ${report.reusedFrom}. No fresh tier passes are claimed. `:''}Missing case evidence: ${report.missingEvidence.join(', ')||'none'}.\nUpstream jobs that did not succeed: ${report.upstreamFailures.map(job=>`${job.name} (${job.conclusion??job.status})`).join(', ')||'none'}.\n`,{flag:'a'})
  // Missing artifacts remain visibly incomplete, even after a failed setup.
  return report.coverage==='incomplete'?1:0
}
if(process.argv[1]&&resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try { process.exitCode=main(process.argv[2]) } catch(error) {
    mkdirSync('tmp',{recursive:true})
    writeFileSync('tmp/test-tier-run-measurement.json',JSON.stringify({version:1,coverage:'incomplete',measurementCauses:[measurementErrorCause(error)]})+'\n')
    console.error(JSON.stringify({metric:'tier_measurements_failure',cause:measurementErrorCause(error)}));process.exitCode=1
  }
}
