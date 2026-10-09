// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectGo, collectWeb, command, root, web, evidence, saveJSON, saveManifest } from './collect.mjs'
import { validate, select, shard, key, counts, exactPattern, webGraph, measuredWeights, tierManifestPattern, knownFlakyOwners } from './core.mjs'
import { reportCases, goOutcomes, browserOutcomes } from './report.mjs'
import { goFailures, nodeFailures, vitestFailures, outputTail, printFailures, printFlakeWarnings } from './failures.mjs'
import { runPlaywright } from '../playwright-safe.mjs'
import { loadManifest as loadBrowserPolicy, tierWeights } from '../../web/scripts/ci-web-shard.mjs'
import { changedPaths, schedulingDecision, eventBase, sourceTree, promotionsBetween } from './diff.mjs'
import { boundedText } from './inputs.mjs'
import { manifestsMain, classifyManifest } from './manifests.mjs'
import { generateOpenAPI } from '../../api/generate.mjs'
import { nightlyBrowserMap, gitTree, coverageSummary } from './browser-impact.mjs'

export const manifestFile = kind => resolve(root,`scripts/ci/${kind}-test-tiers.json`)
export const load = kind => JSON.parse(readFileSync(manifestFile(kind),'utf8'))
// Collection spawns the Vitest, Node and Playwright listers. One process may
// plan repeatedly (tests, multi-step runs), so collect each inventory once.
const inventories = new Map()
const target = kind => {
  if (!inventories.has(kind)) inventories.set(kind, kind==='go' ? collectGo() : collectWeb())
  return inventories.get(kind)
}

// Planner input is authoritative. Candidate uncertainty can widen it to full,
// but candidate graph additions can never narrow the base planner's full gate.
export function runnerDecision(candidate,{mode,layout}={}) {
  if (mode === undefined) return candidate
  if (!['full','essential','static','spec-only'].includes(mode) ||
      layout !== undefined && !['full','static'].includes(layout))
    return {mode:'full',layout:'full',reason:'invalid supplied planner decision'}
  if (mode === 'full' || candidate.mode === 'full')
    return {mode:'full',layout:'full',reason:mode === 'full'?'trusted planner requires full':candidate.reason}
  const plannedLayout=layout ?? (mode === 'static'?'static':undefined)
  if (plannedLayout !== undefined && plannedLayout !== candidate.layout)
    return {mode:'full',layout:'full',reason:'candidate and planner layouts disagree'}
  return {...candidate,layout:plannedLayout??candidate.layout}
}

export function runnerSelection(tests,options,candidate,planner) {
  const decision=runnerDecision(candidate,planner)
  const selection=select(tests,{...options,forceFull:options.forceFull||decision.mode==='full'})
  selection.layout=selection.full?'full':decision.layout
  return selection
}

// A caller that already holds a native collection passes it as `inventory`.
// Programmatic comparisons can share one native snapshot of an unchanged
// checkout through `collect`. Otherwise every plan collects the catalogue
// (minutes of Playwright and Vitest listing when several plans run in one
// process). Normal CLI invocations always use fresh discovery.
export function plan(kind,{event=process.env.GITHUB_EVENT_NAME??'pull_request',paths, affectedLane=process.env.CI_AFFECTED_LANE,plannerMode=process.env.AEON_TEST_TIER_MODE,plannerLayout=process.env.AEON_TEST_TIER_LAYOUT,index=1,count=1,unit=false,full=false,all:catalogue=false,timing=false,inventory:supplied}={}, { collect=target }={}) {
  if(supplied!==undefined&&!Array.isArray(supplied?.tests)) throw new Error('Supplied inventory needs a tests array')
  const inventory=supplied??collect(kind)
  const manifest=load(kind)
  const all=validate(manifest,inventory.tests)
  const browserPolicy=kind==='web'?loadBrowserPolicy():undefined
  const graph=kind==='web'||affectedLane==='on'?webGraph(web):{}
  // Consumer rules read the candidate tree; manifest promotions compare the
  // fetched event base with the checkout. Both are unused unless the switch is on.
  const on=affectedLane==='on'&&event==='pull_request'&&Array.isArray(paths)
  const tree=on?sourceTree(root):undefined
  const promotions=on&&paths.some(path=>tierManifestPattern.test(path))?promotionsBetween(eventBase(),{cwd:root}):undefined
  const readFile=path=>boundedText(root,path)
  const coverage=paths?.some(path=>path.startsWith('web/src/'))?nightlyBrowserMap(root,eventBase(),load('web').tests):{}
  const candidate=schedulingDecision(event,paths,undefined,{affectedLane,graph:kind==='web'||affectedLane==='on'?graph:undefined,tree,promotions,imports:inventory.imports,...coverage})
  const selection=runnerSelection(all,{event,paths,imports:inventory.imports,affectedLane,
    forceFull:full,forceAll:catalogue,webImports:graph,tree,promotions,readFile,...coverage},candidate,
    {mode:plannerMode,layout:plannerLayout})
  const filtered=kind==='web'?selection.tests.filter(row=>unit?row.kind!=='browser':row.kind==='browser'):selection.tests.filter(row=>timing?row.lane==='timing':row.lane!=='timing')
  const weights={}
  if(kind==='web'&&!unit) Object.assign(weights,tierWeights(browserPolicy,filtered))
  if(kind==='go') {
    for(const line of readFileSync(resolve(root,'scripts/ci/go-shards.txt'),'utf8').split('\n')) {
      const match=/^\d+ (\d+) github\.com\/inspr-at\/paimos\/(\S+)/.exec(line)
      if(match) weights[match[2]]=(weights[match[2]]??0)+Number(match[1])/1000
    }
    for(const pkg of Object.keys(weights)) weights[pkg]*=filtered.filter(row=>row.package===pkg).length/all.filter(row=>row.package===pkg).length
  }
  if(kind==='go'||unit) Object.assign(weights,measuredWeights(manifest,filtered))
  return {...selection,all,tests:shard(filtered,index,count,weights,{firstShardLast:unit,ownerTimings:kind==='go'?manifest.timingWeights?.owners:undefined})}
}

export function browserList(rows,all) {
  const files=new Set(rows.map(row=>row.file))
  const whole=new Set([...files].filter(file=>rows.filter(row=>row.file===file).length===all.filter(row=>row.kind==='browser'&&row.file===file).length))
  const descriptions=[...whole].map(file=>`[${rows.find(row=>row.file===file).project}] › ${file.replace(/^tests\//,'')}`)
  descriptions.push(...rows.filter(row=>!whole.has(row.file)).map(row=>`[${row.project}] › ${row.file.replace(/^tests\//,'')}:${row.line} › ${row.name}`))
  return descriptions
}

function execute(bin,args,path,{cwd=root,env=process.env}={}) {
  mkdirSync(resolve(path,'..'),{recursive:true})
  const result=spawnSync(bin,args,{cwd,env,encoding:'utf8',timeout:30*60*1000,maxBuffer:64*1024*1024})
  writeFileSync(path,result.stdout??'')
  writeFileSync(`${path}.stderr`,result.stderr??'')
  if(result.error) console.error(`${bin}: ${result.error.code}`)
  return {code:result.status??1,output:result.stdout??'',stderr:result.stderr??'',error:result.error?.code,signal:result.signal}
}

function diagnostics(kind,owner,result,failures,env) {
  if(result.code&&!failures.length)failures.push({kind,owner,name:'(runner)',
    output:outputTail(`${result.output}\n${result.stderr}`)||`Runner exited with code ${result.code}`})
  printFailures(failures.map(failure=>({...failure,output:failure.output||outputTail(result.stderr)})),{summary:env.GITHUB_STEP_SUMMARY})
}

// OPS-279 integration: export route outputs as runner_class and JSON runs_on. The
// labels must match this job's event and the runner's own platform identity;
// opting into a class alone never permits an arbitrary self-hosted runner.
export function browserRunnerIdentity(env=process.env) {
  const identity={environment:env.RUNNER_ENVIRONMENT??null,name:env.RUNNER_NAME??null,
    os:env.RUNNER_OS??null,arch:env.RUNNER_ARCH??null}
  if(env.RUNNER_ENVIRONMENT==='github-hosted') return {class:'hosted',...identity,labels:[]}
  const refuse=()=>{throw new Error('Tier browser execution requires GitHub-hosted CI or the approved mbp2606 runner identity (runner_class, runs_on, platform and event); collection is safe locally')}
  const eventClass=new Map([['pull_request','mbp2606-pr'],['merge_group','mbp2606-mq'],
    ['push','mbp2606-push'],['workflow_dispatch','mbp2606-dispatch']]).get(env.GITHUB_EVENT_NAME)
  if(env.GITHUB_ACTIONS!=='true'||env.RUNNER_ENVIRONMENT!=='self-hosted'||env.runner_class!=='mbp2606'||
    env.RUNNER_OS!=='Linux'||env.RUNNER_ARCH!=='ARM64'||!eventClass||
    typeof env.runs_on!=='string'||env.runs_on.length>1024) refuse()
  let labels
  try { labels=JSON.parse(env.runs_on) } catch { refuse() }
  const expected=['self-hosted','Linux','ARM64','mbp2606',eventClass]
  if(!Array.isArray(labels)||labels.length!==expected.length||expected.some(label=>!labels.includes(label))) refuse()
  return {class:'mbp2606',...identity,labels:expected}
}

// Inject native runners for policy tests; production retains the supervised
// browser runner and the same bounded native commands/evidence paths.
export async function run(kind,selection,{unit=false,job='local',env=process.env}={},
  {execute:executeCases=execute,command:nativeCommand=command,runPlaywright:browserRunner=runPlaywright,
    loadBrowserPolicy:browserPolicy=loadBrowserPolicy,knownFlaky,saveJSON:save=saveJSON,log=console.log}={}) {
  const begin=performance.now(),batches=[],ledger=[],coverageReports=[]
  const runner=kind==='web'&&!unit?browserRunnerIdentity(env):undefined
  if(kind==='go') generateOpenAPI()
  const mergeGroup=env.GITHUB_EVENT_NAME==='merge_group'
  const quarantines=knownFlakyOwners(knownFlaky)
  for(const row of selection.all) if(row.tier==='ESSENTIAL'&&quarantines.has(key(row)))
    throw new Error(`Known-flaky case cannot be ESSENTIAL: ${key(row)} (${quarantines.get(key(row))})`)
  mkdirSync(evidence,{recursive:true})

  const finish=(owner,rows,result,outcomes,failures,reportError=false)=>{
    const expected=new Set(rows.map(key)),seen=new Set()
    let runnerFailure=reportError||!!(result.error||result.signal||result.metrics?.signal)||![0,1].includes(result.code)
    for(const outcome of outcomes) {
      if(!expected.has(outcome.key)||seen.has(outcome.key)) runnerFailure=true
      if(outcome.status!=='skipped'&&!outcome.started) runnerFailure=true
      seen.add(outcome.key)
    }
    if(rows.some(row=>!seen.has(key(row)))) runnerFailure=true
    const failed=outcomes.some(outcome=>outcome.status==='failed')
    if(result.code===1&&!failed||result.code===0&&failed) runnerFailure=true
    if(failures.some(failure=>failure.name==='(package)')) runnerFailure=true
    diagnostics(rows[0].kind,owner,result,failures,env)
    return {rows,result,outcomes,runnerFailure}
  }
  const executeOwner=(owner,rows,stem)=>{
    let result,outcomes=[],failures=[],reportError=false
    if(kind==='go') {
      result=executeCases('go',['test','-p','2','-count=1','-timeout=25m','-json','-run',exactPattern(rows.map(row=>row.name)),`./${owner}`],`${stem}.jsonl`,{env:{...env,GOMAXPROCS:'2'}})
      outcomes=goOutcomes(result.output)
      failures=goFailures(result.output)
    } else if(rows[0].kind==='node') {
      result=executeCases(process.execPath,['--test','--test-concurrency=1',`--test-reporter=${resolve(root,'scripts/test-tiers/node-reporter.mjs')}`,
        '--test-name-pattern',exactPattern(rows.map(row=>row.name)),owner],`${stem}.jsonl`,{cwd:web,env})
      failures=nodeFailures(result.output,owner,result.stderr)
      const consumed=new Set()
      for(const line of result.output.split('\n').filter(Boolean)) {
        const event=JSON.parse(line)
        if(!['test:pass','test:fail'].includes(event.type))continue
        const row=rows.find(row=>row.name===event.fullName&&!consumed.has(key(row)))
        if(!row) {
          if(event.type==='test:fail'&&!event.skip) reportError=true
          continue
        }
        consumed.add(key(row))
        outcomes.push({key:key(row),status:event.skip?'skipped':event.type==='test:pass'?'passed':'failed',started:!event.skip})
      }
    } else if(rows[0].kind==='vitest') {
      const reportPath=`${stem}.json`
      writeFileSync(reportPath,'')
      result=executeCases(process.execPath,['node_modules/vitest/vitest.mjs','run',owner,'--maxWorkers=1','--no-fileParallelism','--retry=0',
        '--testNamePattern',exactPattern(rows.map(row=>row.name)),'--reporter=json',`--outputFile=${reportPath}`],`${stem}.log`,{cwd:web,env})
      try {
        const report=JSON.parse(readFileSync(reportPath,'utf8'))
        reportError=!!(report.numRuntimeErrorTestSuites||report.unhandledErrors?.length)
        failures=vitestFailures(report,owner,`${result.output}\n${result.stderr}`)
        const consumed=new Set()
        for(const file of report.testResults) for(const result of file.assertionResults) {
          const nativeName=[...result.ancestorTitles,result.title].join(' > ')
          const row=rows.find(row=>row.name===nativeName&&!consumed.has(key(row)))
          if(!row) continue
          consumed.add(key(row))
          outcomes.push({key:key(row),status:['pending','skipped','todo','disabled'].includes(result.status)?'skipped':result.status==='passed'?'passed':'failed',started:result.status==='passed'||result.status==='failed'})
        }
      } catch { reportError=true }
    }
    return finish(owner,rows,result,outcomes,failures,reportError)
  }
  const owners=new Map()
  for(const row of selection.tests.filter(row=>row.active!==false&&row.kind!=='browser')) {
    const owner=row.kind==='go'?row.package:row.file
    if(!owners.has(owner)) owners.set(owner,[])
    owners.get(owner).push(row)
  }
  for(const [owner,rows] of owners) {
    const stem=resolve(evidence,`${job}-${owner.replaceAll('/','-')}`)
    if(kind==='go') {
      // Native enumeration proves the AST/current-platform inventory is exact.
      const listed=nativeCommand('go',['test','-p','2','-list','^(Test|Fuzz)',`./${owner}`],{env:{...env,GOMAXPROCS:'2'}})
        .split('\n').filter(name=>/^(?:Test|Fuzz)\w+$/.test(name)).sort()
      const wanted=selection.all.filter(row=>row.package===owner&&row.active).map(row=>row.name).sort()
      if(JSON.stringify(listed)!==JSON.stringify(wanted)) throw new Error(`Native Go inventory mismatch: ${owner}`)
    }
    const batch=executeOwner(owner,rows,stem)
    batch.retry=failed=>executeOwner(owner,failed,`${stem}-retry`)
    batches.push(batch)
  }
  if(kind==='web'&&!unit) {
    // Original OPS-257 configuration/env owns launches. Only the case list is
    // replaced. Nightly/changed-area cases retain their original launch policy.
    const policy=browserPolicy()
    for(const group of policy.groups) {
      const files=new Set(group.specs.map(spec=>spec.file))
      const rows=selection.tests.filter(row=>row.kind==='browser'&&row.active!==false&&files.has(row.file))
      if(!rows.length) continue
      const executeBrowser=async(rows,retry=false)=>{
        const stem=resolve(evidence,`${job}-${group.id}${retry?'-retry':''}`),listPath=`${stem}.txt`,reportPath=`${stem}.json`
        saveJSON(reportPath,{})
        // A retry always identifies individual cases, even if all cases in a
        // spec failed. Native collection checks line/title/project identity.
        const descriptions=retry?rows.map(row=>`[${row.project}] › ${row.file.replace(/^tests\//,'')}:${row.line} › ${row.name}`):browserList(rows,selection.all)
        writeFileSync(listPath,descriptions.join('\n')+'\n')
        const groupEnv=Object.fromEntries(Object.entries(group.env).map(([k,v])=>[k,v.replaceAll('${RUNNER_TEMP}',env.RUNNER_TEMP??evidence)]))
        const browserEnv={...env,...groupEnv,PW_RETRIES:'0'}
        const observeCoverage=selection.scope==='catalogue'&&!retry&&env.GITHUB_WORKFLOW==='Nightly full'
        const coverageDirectory=resolve(evidence,`browser-coverage-${job}-${group.id}-${env.GITHUB_RUN_ID}-${env.GITHUB_RUN_ATTEMPT}`)
        if(observeCoverage) {
          const casesPath=resolve(evidence,`browser-coverage-${job}-${group.id}-cases.json`)
          saveJSON(casesPath,rows)
          Object.assign(browserEnv,{AEON_BROWSER_IMPACT_CASES:casesPath,AEON_BROWSER_IMPACT_DIRECTORY:coverageDirectory,
            NODE_OPTIONS:`${env.NODE_OPTIONS??''} --import=${new URL('./browser-coverage-hook.mjs',import.meta.url).href}`})
        }
        const args=['--config',group.config,...group.flags,...(group.project?['--project',group.project]:[]),'--test-list',listPath,'--workers=1','--retries=0','--forbid-only']
        const collected=JSON.parse(nativeCommand(process.execPath,['node_modules/@playwright/test/cli.js','test',...args,'--list','--reporter=json'],{cwd:web,env:browserEnv}))
        if(collected.errors?.length) throw new Error(`Browser collection failed: ${group.id}`)
        const actual=collected.suites.flatMap(function walk(suite) { return [...(suite.specs??[]).flatMap(spec=>(spec.tests??[]).map(test=>`${spec.id}:${test.projectName??''}`)),...(suite.suites??[]).flatMap(walk)] }).sort()
        if(JSON.stringify(actual)!==JSON.stringify(rows.map(row=>`${row.id}:${row.project}`).sort())) throw new Error(`Browser selection mismatch: ${group.id}`)
        const result=await browserRunner([...args,
          '--reporter=line,json','--output',`test-results/test-tiers/${job}/${group.id}${retry?'-retry':''}`],{cwd:web,env:{...browserEnv,PLAYWRIGHT_JSON_OUTPUT_FILE:reportPath}})
        if(observeCoverage) {
          const summary=coverageSummary(coverageDirectory,rows,{tree:gitTree(root),runId:env.GITHUB_RUN_ID,attempt:env.GITHUB_RUN_ATTEMPT})
          coverageReports.push(summary)
        }
        let outcomes=[],reportError=false
        try {
          const report=JSON.parse(readFileSync(reportPath,'utf8'))
          reportError=!!report.errors?.length
          outcomes=browserOutcomes(report,rows)
        } catch(error) { console.error(JSON.stringify(error.message));reportError=true }
        const failures=outcomes.filter(outcome=>outcome.status==='failed').map(outcome=>{
          const row=rows.find(row=>key(row)===outcome.key)
          return {kind:'browser',owner:row.file,name:row.name,output:'See supervised Playwright output and JSON evidence.'}
        })
        return finish(group.id,rows,{output:'',stderr:'',...result},outcomes,failures,reportError)
      }
      const batch=await executeBrowser(rows)
      batch.retry=failed=>executeBrowser(failed,true)
      batches.push(batch)
      if([129,130,143].includes(batch.result.code)) break
    }
  }

  // Count first-pass failures across the whole job, before any retry. Subtests
  // belong to their top-level Go case; quarantine does not shrink the cap.
  const failedCount=batches.reduce((sum,batch)=>sum+batch.outcomes.filter(outcome=>outcome.status==='failed').length,0)
  let retryAllowed=mergeGroup&&failedCount>0&&failedCount<=3&&!batches.some(batch=>batch.result.signal||batch.result.metrics?.signal||[129,130,143].includes(batch.result.code))
  const outcomes=[]
  let code=0
  for(const batch of batches) {
    const final=new Map(batch.outcomes.map(outcome=>[outcome.key,outcome]))
    const failed=batch.rows.filter(row=>final.get(key(row))?.status==='failed')
    let retry
    if(retryAllowed&&failed.length&&!batch.runnerFailure) {
      retry=await batch.retry(failed)
      if(retry.result.signal||retry.result.metrics?.signal||[129,130,143].includes(retry.result.code)) retryAllowed=false
      for(const outcome of retry.outcomes) {
        // Only a completed, clean retry that passed proves a flake. A skipped
        // or absent retry never converts the original failure into success.
        if(!retry.runnerFailure&&outcome.status==='passed') {
          final.set(outcome.key,{...outcome,flaky:true})
          ledger.push({key:outcome.key,status:'flaky',runId:env.GITHUB_RUN_ID,sha:env.GITHUB_SHA,
            attempt:env.GITHUB_RUN_ATTEMPT,testAttempt:2})
        }
      }
    }
    if(batch.runnerFailure||retry?.runnerFailure) code ||= batch.result.code||retry?.result.code||1
    for(const row of failed) {
      const id=key(row),outcome=final.get(id)
      if(mergeGroup&&outcome.status==='failed'&&quarantines.has(id)) {
        final.set(id,{...outcome,status:'quarantined'})
        ledger.push({key:id,status:'quarantined',owner:quarantines.get(id),runId:env.GITHUB_RUN_ID,
          sha:env.GITHUB_SHA,attempt:env.GITHUB_RUN_ATTEMPT,testAttempt:retry?2:1})
      }
    }
    if(!mergeGroup) code ||= batch.result.code
    outcomes.push(...final.values())
  }
  printFlakeWarnings(ledger,{log})
  if(kind==='web'&&!unit&&selection.scope==='catalogue'&&env.GITHUB_WORKFLOW==='Nightly full') {
    save(resolve(evidence,`browser-impact-${job}.json`),{version:1,tree:gitTree(root),runId:env.GITHUB_RUN_ID,attempt:env.GITHUB_RUN_ATTEMPT,
      complete:coverageReports.length>0&&coverageReports.every(row=>row.complete),cases:coverageReports.flatMap(row=>row.cases)})
  }
  const report=reportCases(selection.tests,outcomes,(performance.now()-begin)/1000,job)
  report.runId=env.GITHUB_RUN_ID
  report.attempt=env.GITHUB_RUN_ATTEMPT
  report.sha=env.GITHUB_SHA
  if(runner) {
    report.runner_class=runner.class
    report.runner=runner
  }
  report.full=selection.full
  report.reason=selection.reason
  report.scope=selection.scope??'changed-area'
  report.deferredBrowserCases=selection.deferredBrowserCases??0
  report.flakeLedger=ledger
  report.exitCode=code
  if(Object.values(report.classes).some(c=>c.notRun||c.failed)) report.exitCode ||= 1
  save(resolve(evidence,`${job}-measurement.json`),report)
  log(JSON.stringify(report))
  if(env.GITHUB_STEP_SUMMARY) writeFileSync(env.GITHUB_STEP_SUMMARY,`\nTest tier measurement (${job}):\n\n\`\`\`json\n${JSON.stringify(report,null,2)}\n\`\`\`\n`,{flag:'a'})
  return report.exitCode
}

export function classifyArgs(flags) {
  const options = {}, legacy = ['go', 'web'].includes(flags[0])
  let rest = flags
  if (legacy) { options.kind = flags[0]; rest = flags.slice(1) }
  for (let i = 0; i < rest.length; i++) {
    const flag = rest[i]
    if (!['--tier', '--kind', '--only'].includes(flag) || Object.hasOwn(options, flag.slice(2))) throw new Error(`Unknown or repeated classify flag: ${flag}`)
    const value = rest[++i]
    if (!value || value.startsWith('--')) throw new Error(`Missing value: ${flag}`)
    options[flag.slice(2)] = value
  }
  if (options.kind !== undefined && !['go', 'web'].includes(options.kind)) throw new Error('Expected --kind go|web')
  if (options.only !== undefined && options.only.length > 1024) throw new Error('Expected --only pattern of at most 1024 characters')
  if (legacy && options.tier === undefined) options.tier = 'NIGHTLY'
  if (!['ESSENTIAL', 'GATED-FULL', 'NIGHTLY'].includes(options.tier)) throw new Error('classify requires --tier ESSENTIAL|GATED-FULL|NIGHTLY')
  return { ...options, strict: legacy && options.only === undefined }
}

export function classify(flags, { collect = target, read = load, save = saveManifest } = {}) {
  const options = classifyArgs(flags), outputs = []
  for (const kind of options.kind ? [options.kind] : ['go', 'web']) {
    const inventory = collect(kind), manifest = read(kind)
    validate(manifest, inventory.tests, undefined, { warn: () => {} })
    const result = classifyManifest(manifest, inventory.tests, options)
    validate(result.manifest, inventory.tests, undefined, { strict: options.strict, warn: () => {} })
    outputs.push({ kind, ...result })
  }
  for (const { kind, manifest, added } of outputs) {
    save(manifestFile(kind), manifest)
    console.log(JSON.stringify({ kind, classified: added.length, tier: options.tier }))
  }
  return 0
}

export async function main(args, dependencies={}) {
  if (args[0] === 'manifests') return manifestsMain(args.slice(1), root)
  if (args[0] === 'classify') return classify(args.slice(1))
  const [mode,kind,...flags]=args
  if(!['go','web'].includes(kind)||!['collect','check','classify','plan','run'].includes(mode)) throw new Error('Usage: cli.mjs collect|check|classify|plan|run go|web [--full | --all] [--unit] [--shard i/N] [--paths JSON] [check: --strict]')
  const options={unit:false,full:false,all:false,job:`${kind}-tiers`}
  for(let i=0;i<flags.length;i++) {
    const flag=flags[i]
    if(flag==='--full'||flag==='--all'||flag==='--unit') options[flag.slice(2)]=true
    else if(flag==='--paths') options.paths=JSON.parse(flags[++i])
    else if(flag==='--event') options.event=flags[++i]
    else if(flag==='--job') options.job=flags[++i]
    else if(flag==='--timing') options.timing=true
    else if(flag==='--strict'&&mode==='check') options.strict=true
    else if(flag==='--shard') {
      const match=/^(\d+)\/(\d+)$/.exec(flags[++i]??'')
      if(!match) throw new Error('Expected shard i/N')
      options.index=Number(match[1]);options.count=Number(match[2])
    } else throw new Error(`Unknown flag: ${flag}`)
  }
  if(mode==='collect') { saveJSON(resolve(evidence,`${kind}-inventory.json`),target(kind));return 0 }
  if(mode==='check') {
    const all=validate(load(kind),target(kind).tests,undefined,{strict:options.strict})
    const checked=load(kind)
    measuredWeights(checked,all) // malformed timing weights fail the manifest check, not a later plan
    shard(all,1,1,{}, {ownerTimings:kind==='go'?checked.timingWeights?.owners:undefined})
    if(kind==='web') { console.log(JSON.stringify({inventory:counts(all)}));return 0 }
    let output
    try {
      output=command('go',['test','-p','2','-json','-list','^(Test|Fuzz)','./...'],{env:{...process.env,GOMAXPROCS:'2'},timeout:15*60*1000})
    } catch(error) {
      let failures=[]
      try { failures=goFailures(error.stdout??'') } catch {}
      if(failures.length)printFailures(failures)
      else if(error.stdout)console.error(outputTail(error.stdout))
      throw error
    }
    const listed=[]
    for(const line of output.split('\n').filter(Boolean)) {
      const event=JSON.parse(line)
      if(event.Action==='output'&&/^(?:Test|Fuzz)\S*\n$/.test(event.Output??'')) listed.push(`${event.Package.replace(/^github\.com\/inspr-at\/paimos\//,'')}:${event.Output.trim()}`)
    }
    const expected=all.filter(row=>row.active).map(key).sort()
    if(JSON.stringify(listed.sort())!==JSON.stringify(expected))throw new Error('Native Go -list inventory differs from classified source')
    console.log(JSON.stringify({nativeGoCases:listed.length,inventory:counts(all)}));return 0
  }
  if(options.paths===undefined) options.paths=changedPaths(options.event??process.env.GITHUB_EVENT_NAME,process.env,{fetchBase:true})
  const selection=plan(kind,{...options,inventory:dependencies.inventory},dependencies)
  console.log(JSON.stringify({kind,full:selection.full,layout:selection.layout??'full',reason:selection.reason,scope:selection.scope??'changed-area',deferredBrowserCases:selection.deferredBrowserCases??0,inventory:counts(selection.all),selected:counts(selection.tests),kinds:Object.fromEntries(['go','node','vitest','browser'].map(kind=>[kind,selection.tests.filter(row=>row.kind===kind).length]))}))
  if(mode==='run') return run(kind,selection,options)
  return 0
}
if(process.argv[1]&&resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try { process.exitCode=await main(process.argv.slice(2)) }
  catch(error) {
    console.error(error.message)
    process.exitCode=process.argv[2]==='manifests'&&process.argv[3]==='merge-driver'?3:1
  }
}
