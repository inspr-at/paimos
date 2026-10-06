// SPDX-License-Identifier: AGPL-3.0-only
import { spawnSync } from 'node:child_process'
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { collectGo, collectWeb, command, root, web, evidence, saveJSON, saveManifest } from './collect.mjs'
import { validate, select, shard, key, counts, exactPattern, webGraph, measuredWeights, tierManifestPattern } from './core.mjs'
import { reportCases, goOutcomes, browserOutcomes } from './report.mjs'
import { goFailures, nodeFailures, vitestFailures, outputTail, printFailures } from './failures.mjs'
import { runPlaywright } from '../playwright-safe.mjs'
import { loadManifest as loadBrowserPolicy, tierWeights } from '../../web/scripts/ci-web-shard.mjs'
import { changedPaths, schedulingDecision, eventBase, sourceTree, promotionsBetween } from './diff.mjs'
import { boundedText } from './inputs.mjs'
import { manifestsMain, classifyManifest } from './manifests.mjs'

export const manifestFile = kind => resolve(root,`scripts/ci/${kind}-test-tiers.json`)
export const load = kind => JSON.parse(readFileSync(manifestFile(kind),'utf8'))
const target = kind => kind==='go' ? collectGo() : collectWeb()

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

export function plan(kind,{event=process.env.GITHUB_EVENT_NAME??'pull_request',paths, affectedLane=process.env.CI_AFFECTED_LANE,plannerMode=process.env.AEON_TEST_TIER_MODE,plannerLayout=process.env.AEON_TEST_TIER_LAYOUT,index=1,count=1,unit=false,full=false,all:catalogue=false,timing=false}={}) {
  const inventory=target(kind)
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
  const candidate=schedulingDecision(event,paths,undefined,{affectedLane,graph:kind==='web'||affectedLane==='on'?graph:undefined,tree,promotions})
  const selection=runnerSelection(all,{event,paths,imports:inventory.imports,affectedLane,
    forceFull:full,forceAll:catalogue,webImports:graph,tree,promotions,readFile},candidate,
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
  return {...selection,all,tests:shard(filtered,index,count,weights,{firstShardLast:unit})}
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
  return {code:result.status??1,output:result.stdout??'',stderr:result.stderr??''}
}

function diagnostics(kind,owner,result,failures,env) {
  if(result.code&&!failures.length)failures.push({kind,owner,name:'(runner)',
    output:outputTail(`${result.output}\n${result.stderr}`)||`Runner exited with code ${result.code}`})
  printFailures(failures.map(failure=>({...failure,output:failure.output||outputTail(result.stderr)})),{summary:env.GITHUB_STEP_SUMMARY})
}

export async function run(kind,selection,{unit=false,job='local',env=process.env}={}) {
  const begin=performance.now(),outcomes=[]
  let code=0
  const owners=new Map()
  for(const row of selection.tests.filter(row=>row.active!==false)) {
    const owner=row.kind==='go'?row.package:row.file
    if(!owners.has(owner)) owners.set(owner,[])
    owners.get(owner).push(row)
  }
  for(const [owner,rows] of owners) {
    const stem=resolve(evidence,`${job}-${owner.replaceAll('/','-')}`)
    if(kind==='go') {
      // Native enumeration proves the AST/current-platform inventory is exact.
      const listed=command('go',['test','-p','2','-list','^(Test|Fuzz)',`./${owner}`],{env:{...env,GOMAXPROCS:'2'}})
        .split('\n').filter(name=>/^(?:Test|Fuzz)\w+$/.test(name)).sort()
      const wanted=selection.all.filter(row=>row.package===owner&&row.active).map(row=>row.name).sort()
      if(JSON.stringify(listed)!==JSON.stringify(wanted)) throw new Error(`Native Go inventory mismatch: ${owner}`)
      const result=execute('go',['test','-p','2','-count=1','-timeout=25m','-json','-run',exactPattern(rows.map(row=>row.name)),`./${owner}`],`${stem}.jsonl`,{env:{...env,GOMAXPROCS:'2'}})
      code ||= result.code
      outcomes.push(...goOutcomes(result.output))
      diagnostics('go',owner,result,goFailures(result.output),env)
    } else if(rows[0].kind==='node') {
      const result=execute(process.execPath,['--test','--test-concurrency=1',`--test-reporter=${resolve(root,'scripts/test-tiers/node-reporter.mjs')}`,
        '--test-name-pattern',exactPattern(rows.map(row=>row.name)),owner],`${stem}.jsonl`,{cwd:web,env})
      code ||= result.code
      diagnostics('node',owner,result,nodeFailures(result.output,owner,result.stderr),env)
      const consumed=new Set()
      for(const line of result.output.split('\n').filter(Boolean)) {
        const event=JSON.parse(line)
        if(!['test:pass','test:fail'].includes(event.type))continue
        const row=rows.find(row=>row.name===event.fullName&&!consumed.has(key(row)))
        if(!row) continue
        consumed.add(key(row))
        outcomes.push({key:key(row),status:event.skip?'skipped':event.type==='test:pass'?'passed':'failed',started:!event.skip})
      }
    } else if(rows[0].kind==='vitest') {
      const reportPath=`${stem}.json`
      writeFileSync(reportPath,'')
      const result=execute(process.execPath,['node_modules/vitest/vitest.mjs','run',owner,'--maxWorkers=1','--no-fileParallelism','--retry=0',
        '--testNamePattern',exactPattern(rows.map(row=>row.name)),'--reporter=json',`--outputFile=${reportPath}`],`${stem}.log`,{cwd:web,env})
      code ||= result.code
      let failures=[]
      try {
        const report=JSON.parse(readFileSync(reportPath,'utf8'))
        failures=vitestFailures(report,owner,`${result.output}\n${result.stderr}`)
        const consumed=new Set()
        for(const file of report.testResults) for(const result of file.assertionResults) {
          const nativeName=[...result.ancestorTitles,result.title].join(' > ')
          const row=rows.find(row=>row.name===nativeName&&!consumed.has(key(row)))
          if(!row) continue
          consumed.add(key(row))
          outcomes.push({key:key(row),status:['pending','skipped','todo','disabled'].includes(result.status)?'skipped':result.status==='passed'?'passed':'failed',started:result.status==='passed'||result.status==='failed'})
        }
      } catch { code ||= 1 }
      diagnostics('vitest',owner,result,failures,env)
    }
  }
  if(kind==='web'&&!unit) {
    if(env.RUNNER_ENVIRONMENT!=='github-hosted') throw new Error('Tier browser execution requires hosted CI; collection is safe locally')
    // Original OPS-257 configuration/env owns launches. Only the case list is
    // replaced. Nightly/changed-area cases retain their original launch policy.
    const policy=loadBrowserPolicy()
    for(const group of policy.groups) {
      const files=new Set(group.specs.map(spec=>spec.file))
      const rows=selection.tests.filter(row=>row.kind==='browser'&&files.has(row.file))
      if(!rows.length) continue
      const stem=resolve(evidence,`${job}-${group.id}`),listPath=`${stem}.txt`,reportPath=`${stem}.json`
      saveJSON(reportPath,{})
      const descriptions=browserList(rows,selection.all)
      writeFileSync(listPath,descriptions.join('\n')+'\n')
      const groupEnv=Object.fromEntries(Object.entries(group.env).map(([k,v])=>[k,v.replaceAll('${RUNNER_TEMP}',env.RUNNER_TEMP??evidence)]))
      const args=['--config',group.config,...group.flags,...(group.project?['--project',group.project]:[]),'--test-list',listPath,'--workers=1','--retries=0','--forbid-only']
      const collected=JSON.parse(command(process.execPath,['node_modules/@playwright/test/cli.js','test',...args,'--list','--reporter=json'],{cwd:web,env:{...env,...groupEnv}}))
      const actual=collected.suites.flatMap(function walk(suite) { return [...(suite.specs??[]).flatMap(spec=>(spec.tests??[]).map(test=>`${spec.id}:${test.projectName??''}`)),...(suite.suites??[]).flatMap(walk)] }).sort()
      if(JSON.stringify(actual)!==JSON.stringify(rows.map(row=>`${row.id}:${row.project}`).sort())) throw new Error(`Browser selection mismatch: ${group.id}`)
      const result=await runPlaywright([...args,
        '--reporter=line,json','--output',`test-results/test-tiers/${job}/${group.id}`],{cwd:web,env:{...env,...groupEnv,PW_RETRIES:'0',PLAYWRIGHT_JSON_OUTPUT_FILE:reportPath}})
      code ||= result.code
      try { outcomes.push(...browserOutcomes(JSON.parse(readFileSync(reportPath,'utf8')),rows)) } catch(error) { console.error(error.message);code ||= 1 }
      if([129,130,143].includes(result.code)) break
    }
  }
  const report=reportCases(selection.tests,outcomes,(performance.now()-begin)/1000,job)
  report.runId=env.GITHUB_RUN_ID
  report.attempt=env.GITHUB_RUN_ATTEMPT
  report.sha=env.GITHUB_SHA
  report.full=selection.full
  report.reason=selection.reason
  report.scope=selection.scope??'changed-area'
  report.deferredBrowserCases=selection.deferredBrowserCases??0
  report.exitCode=code
  if(Object.values(report.classes).some(c=>c.notRun||c.failed)) report.exitCode ||= 1
  saveJSON(resolve(evidence,`${job}-measurement.json`),report)
  console.log(JSON.stringify(report))
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

export async function main(args) {
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
    measuredWeights(load(kind),all) // malformed timing weights fail the manifest check, not a later plan
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
  const selection=plan(kind,options)
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
