// SPDX-License-Identifier: AGPL-3.0-only
import { readdirSync, readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { command } from './collect.mjs'

export function jobMinutes(job) {
  if(job.status!=='completed'||!job.started_at||!job.completed_at) return null
  const seconds=(Date.parse(job.completed_at)-Date.parse(job.started_at))/1000
  if(!Number.isFinite(seconds)||seconds<0) throw new Error(`Invalid job timestamps: ${job.name}`)
  return seconds/60
}
export function aggregate(reports,jobs,{runId,attempt,sha}={}) {
  const names=new Set(),classes={}
  for(const report of reports) {
    if(report.version!==1||names.has(report.job)) throw new Error(`Invalid or duplicate measurement: ${report.job}`)
    names.add(report.job)
    for(const [tier,counts] of Object.entries(report.classes)) {
      classes[tier]??={selected:0,run:0,passed:0,skipped:0,failed:0,notRun:0,platformInactive:0}
      for(const field of Object.keys(classes[tier])) {
        const value=counts[field]
        if(!Number.isSafeInteger(value)||value<0) throw new Error('Invalid case count')
        classes[tier][field]+=value
      }
    }
  }
  const required=jobs.filter(job=>/^(?:nightly-)?(?:go-test|web-shard) \(\d+\)$|^(?:nightly-)?(?:web-setup|go-timing)$/.test(job.name))
  const evidenceName=name=>name.replace(/^nightly-/,'').replace(/^web-setup$/,'web-unit').replace(/ \((\d+)\)$/,'-$1')
  const missingEvidence=required.map(job=>evidenceName(job.name)).filter(name=>!names.has(name))
  const runners=jobs.map(job=>({name:job.name,status:job.status,conclusion:job.conclusion,runnerMinutes:jobMinutes(job)}))
  const minutes=prefix=>runners.filter(job=>new RegExp(`^(?:nightly-)?${prefix}(?:-| |$)`).test(job.name)).reduce((sum,job)=>sum+(job.runnerMinutes??0),0)
  return {version:1,runId,attempt,sha,classes,reports,jobs:runners,missingEvidence,
    coverage:missingEvidence.length?'incomplete':'reported',
    baselines:{goRunnerMinutes:22.5,webRunnerMinutes:42.17},
    measured:{goRunnerMinutes:minutes('go'),webRunnerMinutes:minutes('web'),sharedPlanningRunnerMinutes:minutes('tier-plan')},
    caseScope:'Top-level Go Test/Fuzz including isolated timing, and web unit/UI registrations selected by tiers; fixed static, release and e2e gates retain separate runner costs.'}
}

export function readReports(directory) {
  const reports=[]
  if(!existsSync(directory))return reports
  for(const entry of readdirSync(directory,{withFileTypes:true})) {
    const path=resolve(directory,entry.name)
    if(entry.isDirectory()) reports.push(...readReports(path))
    else if(/^(?:go-test-\d+|go-timing|web-unit|web-shard-\d+)-measurement\.json$/.test(entry.name)) reports.push(JSON.parse(readFileSync(path,'utf8')))
  }
  return reports
}
export function main(directory,env=process.env) {
  if(!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(env.GITHUB_REPOSITORY??'')||!/^\d+$/.test(env.GITHUB_RUN_ID??'')) throw new Error('Missing Actions run identity')
  const jobs=[]
  for(let page=1;page<=20;page++) {
    const result=JSON.parse(command('gh',['api',`repos/${env.GITHUB_REPOSITORY}/actions/runs/${env.GITHUB_RUN_ID}/jobs?filter=latest&per_page=100&page=${page}`]))
    jobs.push(...result.jobs)
    if(jobs.length>=result.total_count) break
    if(page===20) throw new Error('Job inventory exceeds measurement bound')
  }
  const report=aggregate(readReports(directory),jobs,{runId:env.GITHUB_RUN_ID,attempt:env.GITHUB_RUN_ATTEMPT,sha:env.GITHUB_SHA})
  mkdirSync('tmp',{recursive:true})
  writeFileSync('tmp/test-tier-run-measurement.json',JSON.stringify(report,null,2)+'\n')
  console.log(JSON.stringify({classes:report.classes,measured:report.measured,coverage:report.coverage,missingEvidence:report.missingEvidence}))
  if(env.GITHUB_STEP_SUMMARY) writeFileSync(env.GITHUB_STEP_SUMMARY,`\nTest tiers: ${report.coverage}; Go ${report.measured.goRunnerMinutes.toFixed(2)} / web ${report.measured.webRunnerMinutes.toFixed(2)} runner-min. Baselines: 22.50 / 42.17.\n\n\`\`\`json\n${JSON.stringify(report.classes,null,2)}\n\`\`\`\nMissing case evidence: ${report.missingEvidence.join(', ')||'none'}.\n`,{flag:'a'})
  // Missing artifacts remain visibly incomplete, even after a failed setup.
  return report.missingEvidence.length?1:0
}
if(process.argv[1]&&resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try { process.exitCode=main(process.argv[2]) } catch(error) { console.error(error.message);process.exitCode=1 }
}
