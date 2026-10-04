// SPDX-License-Identifier: AGPL-3.0-only
import { key } from './core.mjs'

export function reportCases(selected, outcomes, seconds, job) {
  const seen = new Map()
  for (const outcome of outcomes) {
    if (seen.has(outcome.key)) throw new Error(`Duplicate result: ${outcome.key}`)
    seen.set(outcome.key,outcome)
  }
  const classes = {}
  for (const tier of ['ESSENTIAL','NIGHTLY']) {
    const rows = selected.filter(row=>row.tier===tier)
    const tally = { selected:rows.length,run:0,passed:0,skipped:0,failed:0,notRun:0,platformInactive:0 }
    for (const row of rows) {
      if (!row.active && row.kind==='go') { tally.platformInactive++;tally.skipped++;continue }
      const outcome = seen.get(key(row))
      if (!outcome) { tally.notRun++;continue }
      if (outcome.started) tally.run++
      if (outcome.status==='passed') tally.passed++
      else if (outcome.status==='skipped') tally.skipped++
      else tally.failed++
    }
    classes[tier]=tally
  }
  return {version:1,job,executionMinutes:seconds/60,runnerMinutes:null,
    runnerMinutesNote:'Complete job duration comes from Actions job metadata; executionMinutes excludes setup.',classes}
}

export function goOutcomes(text) {
  const started = new Set(), terminal = new Map()
  for (const line of text.split('\n')) {
    if (!line.trim()) continue
    const event=JSON.parse(line)
    if (!event.Test || event.Test.includes('/')) continue
    const pkg=event.Package.replace(/^github\.com\/inspr-at\/paimos\//,'')
    const id=`${pkg}:${event.Test}`
    if(event.Action==='run') started.add(id)
    if(['pass','skip','fail'].includes(event.Action)) terminal.set(id,{key:id,status:{pass:'passed',skip:'skipped',fail:'failed'}[event.Action],started:started.has(id)})
  }
  return [...terminal.values()]
}
export function browserOutcomes(report, rows) {
  const outcomes=[]
  const visit=suites=>{
    for(const suite of suites??[]) {
      for(const spec of suite.specs??[]) for(const test of spec.tests??[]) {
        const row=rows.find(row=>row.id===spec.id && row.project===(test.projectName??''))
        if(!row) throw new Error(`Unexpected browser result: ${spec.title}`)
        const attempts=test.results??[]
        if(attempts.length>1) throw new Error(`Automatic retries forbidden: ${spec.title}`)
        const status=attempts[0]?.status
        outcomes.push({key:key(row),started:!!status && status!=='skipped',status:status==='passed'?'passed':status==='skipped'?'skipped':'failed'})
      }
      visit(suite.suites)
    }
  }
  visit(report.suites)
  return outcomes
}
