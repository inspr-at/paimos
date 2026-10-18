// SPDX-License-Identifier: AGPL-3.0-only
import { key, tiers } from './core.mjs'

// Per-case identity makes partial Actions reruns composable without counting
// a case twice or allowing an old pass to hide a newer failure/interruption.
export function countCaseResults(results) {
  const classes = Object.fromEntries(tiers.map(tier => [tier,
    {selected:0,run:0,passed:0,skipped:0,failed:0,notRun:0,platformInactive:0,flaky:0,quarantined:0}]))
  const seen = new Set()
  for (const row of results) {
    if (!row || typeof row.key !== 'string' || !row.key || seen.has(row.key) || !tiers.includes(row.tier) ||
        !['passed','failed','skipped','notRun','platformInactive','quarantined'].includes(row.status) ||
        typeof row.started !== 'boolean' || typeof row.flaky !== 'boolean') throw new Error('Invalid case result')
    seen.add(row.key)
    const counts=classes[row.tier]
    counts.selected++
    if(row.started)counts.run++
    if(row.flaky)counts.flaky++
    if(row.status==='platformInactive') { counts.platformInactive++;counts.skipped++ }
    else counts[row.status]++
  }
  return classes
}

export function reportCases(selected, outcomes, seconds, job) {
  const seen = new Map()
  for (const outcome of outcomes) {
    if (seen.has(outcome.key)) throw new Error(`Duplicate result: ${outcome.key}`)
    seen.set(outcome.key,outcome)
  }
  const caseResults=selected.map(row=>{
    const outcome=seen.get(key(row)),inactive=!row.active&&row.kind==='go'
    return {key:key(row),tier:row.tier,status:inactive?'platformInactive':outcome?.status??'notRun',
      started:!inactive&&!!outcome?.started,flaky:!inactive&&!!outcome?.flaky}
  })
  const classes=countCaseResults(caseResults)
  return {version:1,job,executionMinutes:seconds/60,runnerMinutes:null,
    runnerMinutesNote:'Complete job duration comes from Actions job metadata; executionMinutes excludes setup.',classes,caseResults}
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
        if(!attempts.length) continue // registration blocked before execution: report as notRun
        const status=attempts[0]?.status
        if(!['passed','skipped','failed','timedOut'].includes(status)) throw new Error(`Incomplete browser attempt: ${spec.title}`)
        outcomes.push({key:key(row),started:!!status && status!=='skipped',status:status==='passed'?'passed':status==='skipped'?'skipped':'failed'})
      }
      visit(suite.suites)
    }
  }
  visit(report.suites)
  return outcomes
}
