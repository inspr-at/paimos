// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync, mkdtempSync, mkdirSync, writeFileSync, symlinkSync, truncateSync, utimesSync, existsSync, rmSync } from 'node:fs'
import { resolve } from 'node:path'
import { tmpdir } from 'node:os'
import { spawn, spawnSync, execFileSync } from 'node:child_process'
import { once } from 'node:events'
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { validate, select, shard, key, exactPattern, webGraph, counts, measuredWeights, uncertain, affectedRisk, impactRisk, machineryPattern, manifestPromotions, migrationObjects, consumers, consumerCaseBound, isDocsLike, browserDataInput, registryImpact, webShardPlan, fullGateTests, driftCases, browserScopeNoneAllowlist, fullWebShards, fullWebShardCount, assertWebShardBudget } from './core.mjs'
import { reportCases, goOutcomes, browserOutcomes } from './report.mjs'
import { aggregate, jobMinutes, readReports } from './measure.mjs'
import { checkFull } from './check-full.mjs'
import { changedPaths, schedulingMode, schedulingDecision, trustedSchedulingDecision, main as tierPlanMain, effectiveLane, sourceTree, promotionsBetween, registryChangesBetween, sizeWebDecision } from './diff.mjs'
import { runnerDecision, runnerSelection, browserRunnerIdentity, run } from './cli.mjs'
import { createGoRunner } from './go-runner.mjs'
import { treeStamp, reuse, webStampEntries } from './collect.mjs'
import { inputBounds, boundedText, inputMetadata, readInput } from './inputs.mjs'
import { buildBrowserMap } from './browser-impact.mjs'
import { nightlyWeights, tierWeights, loadManifest as loadWebPolicy } from '../../web/scripts/ci-web-shard.mjs'
import { loadPolicy } from '../../web/scripts/ci-web-shard-source.mjs'
import './browser-impact.test.mjs'
import './impact-plan.test.mjs'
import './corpus-replay.test.mjs'
import './manifests.test.mjs'
import './listing-cache.test.mjs'
import './tiers-merge-driver.test.mjs'
import '../merge-drivers/merge-drivers.test.mjs'
import '../nightly-ticket.test.mjs'
import { requiredJobs } from '../ci-tree-reuse.mjs'

const fullWebShardJSON = `[${fullWebShards.join(',')}]`

test('nightly failure artifacts bind failed cases and incomplete runners to the exact attempt', async () => {
  const { run } = await import('./cli.mjs')
  const rows = ['failed case', 'passed sibling'].map(name => ({ kind: 'node', file: 'tests/fixture.test.ts', name, tier: 'GATED-FULL' }))
  const env = { GITHUB_EVENT_NAME: 'schedule', GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '2', GITHUB_SHA: 'a'.repeat(40) }
  const simulate = async incomplete => {
    const artifacts = new Map()
    const code = await run('web', { tests: rows, all: rows, full: true, scope: 'catalogue' }, { unit: true, job: 'web-unit', env }, {
      knownFlaky: { version: 1, entries: [] }, log: () => {}, saveJSON: (path, value) => artifacts.set(path.split('/').at(-1), value),
      execute: () => incomplete ? { code: 2, output: '', stderr: '', error: 'ETIMEDOUT' } : {
        code: 1, stderr: '', output: rows.map((row, i) => JSON.stringify({ type: i ? 'test:pass' : 'test:fail', fullName: row.name })).join('\n'),
      },
    })
    assert.notEqual(code, 0)
    assert.equal(artifacts.get('web-unit-measurement.json').exitCode, code)
    return artifacts.get('web-unit-failures.json')
  }
  assert.deepEqual(await simulate(false), { version: 1, job: 'web-unit', runId: '123', attempt: '2', sha: env.GITHUB_SHA, cases: [key(rows[0])], runnerFailure: false })
  assert.deepEqual(await simulate(true), { version: 1, job: 'web-unit', runId: '123', attempt: '2', sha: env.GITHUB_SHA, cases: [], runnerFailure: true })
})

// Risk: a later batch's cancellation used to erase completed failures, and an
// artifact with the right name could make incomplete coverage look reported.
test('AEON-1313 interrupted shards checkpoint completed failures and keep measurement coverage incomplete', {timeout:20_000}, async t => {
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-1313-evidence-'))
  const identity={GITHUB_RUN_ID:'1313',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'.repeat(40)}
  const env={...identity,RUNNER_ENVIRONMENT:'github-hosted',GITHUB_EVENT_NAME:'schedule'}
  const rows=['a','b','c','d'].map(id=>({kind:'browser',file:`tests/${id}.spec.ts`,name:`case ${id}`,
    tier:'NIGHTLY',id,project:'chromium',line:10,active:true}))
  const policy={groups:['a','b','c','d'].map(id=>({id,config:'playwright.ui.config.ts',project:'chromium',
    flags:[],env:{FIXTURE_GROUP:id},specs:[{file:`tests/${id}.spec.ts`}]}))}
  const native=(selected,status)=>({errors:[],suites:[{specs:selected.map(row=>({id:row.id,title:row.name,
    tests:[{projectName:row.project,results:[{status:status(row),duration:100}]}]}))}]})
  const selection={tests:rows,all:rows,full:true,scope:'catalogue',reason:'fixture'}
  const job={name:'nightly-web-shard (1)',status:'completed',conclusion:'cancelled',
    started_at:'2026-10-13T01:00:00Z',completed_at:'2026-10-13T01:01:00Z'}
  const aggregateOptions={runId:identity.GITHUB_RUN_ID,attempt:identity.GITHUB_RUN_ATTEMPT,sha:identity.GITHUB_SHA}
  const artifact=name=>JSON.parse(readFileSync(resolve(directory,name),'utf8'))
  let child
  t.signal.addEventListener('abort',()=>child?.kill('SIGKILL'),{once:true})
  try {
    // The IPC readiness barrier proves the failed first batch was finalized
    // before the synthetic runner is killed in its second batch. No browser,
    // elapsed-time threshold, sleep or retry participates in this proof.
    const script=`
      import { run } from ${JSON.stringify(new URL('./cli.mjs',import.meta.url).href)};
      import { writeFileSync } from 'node:fs';
      import { basename, resolve } from 'node:path';
      const rows=${JSON.stringify(rows)}, policy=${JSON.stringify(policy)}, env=${JSON.stringify(env)};
      const native=${native.toString()};
      process.on('message',()=>{});
      await run('web',${JSON.stringify(selection)},{env,job:'web-shard-1'},{
        knownFlaky:{version:1,entries:[]},loadBrowserPolicy:()=>policy,log:()=>{},
        saveJSON:(path,value)=>writeFileSync(resolve(${JSON.stringify(directory)},basename(path)),JSON.stringify(value)),
        command:(_bin,args,{env})=>JSON.stringify(native(rows.filter(row=>row.id===env.FIXTURE_GROUP),()=> 'passed')),
        runPlaywright:async(_args,{env})=>{
          if(env.FIXTURE_GROUP==='b'){process.send('second-batch-ready');await new Promise(()=>{});}
          writeFileSync(env.PLAYWRIGHT_JSON_OUTPUT_FILE,JSON.stringify(native([rows[0]],()=> 'failed')));
          return {code:1};
        }
      });`
    child=spawn(process.execPath,['--input-type=module','-e',script],{stdio:['ignore','ignore','pipe','ipc']})
    let stderr=''
    child.stderr.on('data',chunk=>{stderr+=chunk})
    const exited=once(child,'exit')
    const ready=await Promise.race([once(child,'message'),exited.then(result=>assert.fail(`Child exited before barrier: ${result}; ${stderr}`))])
    assert.equal(ready[0],'second-batch-ready')
    const checkpoint=artifact('web-shard-1-measurement.json')
    assert.equal(checkpoint.execution.status,'running')
    assert.equal(checkpoint.execution.activeBatch,'b')
    assert.equal(checkpoint.execution.completedBatches,1)
    assert.equal(checkpoint.runnerFailure,true,'unfinished checkpoints cannot claim a complete runner')
    assert.deepEqual(artifact('web-shard-1-failures.json').cases,[key(rows[0])])
    assert.equal(artifact('web-shard-1-failures.json').runnerFailure,true)
    child.kill('SIGKILL')
    assert.equal((await exited)[1],'SIGKILL')
    assert.deepEqual(artifact('web-shard-1-measurement.json'),checkpoint,'termination must leave a usable checkpoint')
    assert.deepEqual(checkpoint.notRunCases.map(row=>row.key),rows.slice(1).map(key))
    assert.ok(checkpoint.notRunCases.every(row=>row.reason==='Execution has not finished'))
    assert.equal(checkpoint.classes.NIGHTLY.failed,1)
    assert.equal(checkpoint.classes.NIGHTLY.notRun,3)
    const killed=aggregate([checkpoint],[job],aggregateOptions)
    assert.equal(killed.coverage,'incomplete')
    assert.deepEqual(killed.missingEvidence,[],'saved but incomplete evidence is not a missing artifact')
    assert.equal(killed.classes.NIGHTLY.failed,1)
    assert.equal(killed.incompleteEvidence[0].reason,'Execution has not finished')

    // Graceful interruption retains terminal siblings in the current batch,
    // while the interrupted attempt and all later selections remain notRun.
    const currentRows=[...rows.slice(0,2),{...rows[2],file:rows[1].file},rows[3]]
    const artifacts=new Map(),calls=[]
    const code=await run('web',{...selection,tests:currentRows,all:currentRows},{env,job:'web-shard-1'},{
      knownFlaky:{version:1,entries:[]},loadBrowserPolicy:()=>policy,log:()=>{},
      saveJSON:(path,value)=>artifacts.set(path.split('/').at(-1),structuredClone(value)),
      command:(_bin,_args,{env})=>JSON.stringify(native(currentRows.filter(row=>row.file===`tests/${env.FIXTURE_GROUP}.spec.ts`),()=> 'passed')),
      runPlaywright:async(_args,{env})=>{
        const id=env.FIXTURE_GROUP
        calls.push(id)
        const checkpoint=artifacts.get('web-shard-1-measurement.json')
        if(id==='b') assert.equal(checkpoint.classes.NIGHTLY.failed,1)
        writeFileSync(env.PLAYWRIGHT_JSON_OUTPUT_FILE,JSON.stringify(native(currentRows.filter(row=>row.file===`tests/${id}.spec.ts`),
          row=>id==='a'?'failed':row.id==='b'?'passed':'interrupted')))
        return id==='a'?{code:1}:{code:143,metrics:{signal:'SIGTERM'}}
      },
    })
    assert.equal(code,143)
    assert.deepEqual(calls,['a','b'],'interruption must stop later batches')
    const report=artifacts.get('web-shard-1-measurement.json')
    assert.deepEqual([report.runId,report.attempt,report.sha],['1313','2',identity.GITHUB_SHA])
    assert.equal(report.execution.status,'interrupted')
    assert.equal(report.execution.signal,'SIGTERM')
    assert.equal(report.execution.completedBatches,1,'an interrupted batch is not complete')
    assert.deepEqual(artifacts.get('web-shard-1-failures.json').cases,[key(rows[0])])
    assert.deepEqual(report.browserCases.map(row=>row.status),['failed','passed','notRun','notRun'])
    assert.ok(report.notRunCases.every(row=>row.reason==='Execution interrupted by SIGTERM'))
    assert.equal(report.webShardTiming.caseSeconds,null,'interrupted samples cannot calibrate complete execution')
    assert.equal(aggregate([report],[job],aggregateOptions).coverage,'incomplete')
    const saved=resolve(directory,'evidence'),summary=resolve(directory,'summary.txt')
    mkdirSync(saved)
    writeFileSync(resolve(saved,'web-shard-1-measurement.json'),JSON.stringify(report))
    writeFileSync(resolve(directory,'gh'),`#!${process.execPath}\nprocess.stdout.write(${JSON.stringify(JSON.stringify({total_count:1,jobs:[job]}))})\n`,{mode:0o755})
    const measured=spawnSync(process.execPath,[fileURLToPath(new URL('./measure.mjs',import.meta.url)),saved],{
      cwd:directory,encoding:'utf8',timeout:10_000,
      env:{PATH:directory,...identity,GITHUB_REPOSITORY:'example/aeon',GITHUB_STEP_SUMMARY:summary},
    })
    assert.ifError(measured.error)
    assert.equal(measured.status,1,measured.stderr)
    const output=JSON.parse(measured.stdout)
    assert.equal(output.coverage,'incomplete')
    assert.equal(output.coverageReason,'interrupted or incomplete case evidence')
    assert.ok(output.measurementCauses.includes('incomplete_case_execution'))
    assert.equal(output.classes.NIGHTLY.failed,1)
    assert.match(readFileSync(summary,'utf8'),/Incomplete case evidence: web-shard-1 \(Execution interrupted by SIGTERM\)/)
    const otherAttempt=aggregate([report],[job],{...aggregateOptions,attempt:'3'})
    assert.deepEqual(otherAttempt.missingEvidence,[])
    assert.equal(otherAttempt.coverage,'incomplete','an earlier interruption must remain incomplete')
    assert.equal(otherAttempt.classes.NIGHTLY.failed,1)

    const green=reportCases(rows,rows.map(row=>({key:key(row),status:'passed',started:true})),1,'web-shard-1')
    Object.assign(green,{runId:'1313',attempt:'2',sha:identity.GITHUB_SHA,execution:{status:'completed'}})
    assert.equal(aggregate([green],[{...job,conclusion:'success'}],aggregateOptions).coverage,'reported')
    for(const status of ['running','interrupted','incomplete']) {
      assert.equal(aggregate([{...green,execution:{status,reason:'unfinished cleanup'}}],[{...job,conclusion:'success'}],aggregateOptions).coverage,'incomplete')
    }
    assert.equal(aggregate([green],[job],aggregateOptions).coverage,'incomplete','Actions cancellation cannot become a completed run')
    const legacy={...green}
    delete legacy.execution
    assert.equal(aggregate([legacy],[job],aggregateOptions).coverage,'incomplete','legacy evidence cannot hide Actions cancellation')
    assert.throws(()=>aggregate([{...green,execution:{status:'green'}}],[],aggregateOptions),/execution state/)
    const workflow=readFileSync(new URL('../../.github/workflows/nightly-full.yml',import.meta.url),'utf8')
    const shardJob=workflow.slice(workflow.indexOf('\n  nightly-web-shard:'),workflow.indexOf('\n  # Preserve the ruleset'))
    const jobBudget=Number(/^    timeout-minutes: (\d+)$/m.exec(shardJob)[1])
    const executionBudget=Number(/Run selected UI cases without retries\n        timeout-minutes: (\d+)/.exec(shardJob)[1])
    const installBudget=Number(/Install Chromium system dependencies\n        timeout-minutes: (\d+)/.exec(shardJob)[1])
    assert.equal(executionBudget,30,'AEON-1351: shard 23 passed at 19.83 min under the old 20-minute step')
    assert.equal(jobBudget,40,'AEON-1351: the job cap stays 40 minutes and still catches a hung job')
    assert.equal(installBudget,13,'Chromium dependency install keeps its bound')
    assert.match(shardJob,/run web --all --shard/,'NIGHTLY registrations stay selected')
  } finally {
    if(child&&child.exitCode===null&&child.signalCode===null) { const exited=once(child,'exit');child.kill('SIGKILL');await exited }
    rmSync(directory,{recursive:true,force:true})
  }
})
import './slow-owners.test.mjs'
import './web-batches.test.mjs'
import './affected.test.mjs'

const g=(pkg,name,tier='NIGHTLY')=>({kind:'go',package:pkg,name,tier,active:true})
const w=(file,name,tier='NIGHTLY')=>({kind:'node',file,name,tier})
const cases=[g('internal/auth','TestCeiling','ESSENTIAL'),g('internal/auth','TestRotation'),
  g('internal/nodes','TestCRUD','ESSENTIAL'),g('internal/nodes','TestUndo'),g('cmd/aeon','TestRoutes'),
  w('tests/scope.test.ts','ceiling','ESSENTIAL'),w('tests/scope.test.ts','revoked'),w('tests/tree.test.ts','tree')]
const mapTree='a'.repeat(40)
const completeMap=(source,specs)=>({baseTree:mapTree,browserMap:{version:1,tree:mapTree,complete:true,specs,modules:{[source]:specs}}})
const fixture=()=>({version:1,tests:structuredClone(cases)})
const noFlaky={version:1,entries:[]}

// R13 release integrity: replacing go test must retain exact native inventory,
// execution outcomes and retry policy, including failures outside a test body.
test('AEON-1025 compiled Go owners retain exact execution, blocking failures and retry evidence',async t=>{
  const rows=[g('internal/nodes','TestFailed','GATED-FULL'),g('internal/nodes','TestPassed','GATED-FULL'),
    g('internal/nodes','FuzzSeed','GATED-FULL'),g('internal/auth','TestLegacy','GATED-FULL')]
  const all=[...rows,g('internal/nodes','TestUnselected'),{...g('internal/nodes','TestOtherPlatform'),active:false}]
  const selection={tests:rows,all,full:true,scope:'gated-full',reason:'fixture'}
  const simulate=async({event='merge_group',failed=[rows[0]],again=[],compileFailure=false,listMismatch=false,
    missing=false,unexpected=false,packageFailure=false,signal=false,saveFailure=false,goFlags}={})=>{
    const calls=[],lists=[],attempts=new Map(),binaries=[]
    let report,clock=0,error,code
    try {
      code=await run('go',selection,{env:{GITHUB_EVENT_NAME:event,FIXTURE:'retained',GOFLAGS:goFlags},job:'aeon1025-fixture'}, {
        knownFlaky:noFlaky,now:()=>{clock+=100;return clock},log:()=>{},
        saveJSON:(_path,value)=>{if(saveFailure) throw new Error('fixture save failure');report=value},
        command:(bin,args,options)=>{
          const compiled=bin!=='go',owner=compiled?'internal/nodes':args.at(-1).slice(2)
          lists.push({bin,args,options})
          if(compiled) {
            assert.equal(bin,binaries[0])
            assert.deepEqual(args,['-test.paniconexit0','-test.list=^(Test|Fuzz)'])
            assert.equal(options.cwd,resolve(new URL('../../internal/nodes',import.meta.url).pathname))
            assert.equal(options.env.PWD,options.cwd)
          }
          return all.filter(row=>row.package===owner&&row.active&&(!listMismatch||row!==rows[0])).map(row=>row.name).join('\n')
        },
        execute:(bin,args,path,options)=>{
          assert.equal(bin,'go')
          assert.equal(options.env.GOMAXPROCS,'2')
          assert.equal(options.env.FIXTURE,'retained')
          if(args.includes('-c')) {
            assert.ok(args.includes('-ldflags=-s -w'),'retain normal go test linker symbol policy')
            const binary=args[args.indexOf('-o')+1]
            binaries.push(binary);calls.push({compile:true,args,path,options})
            assert.ok(existsSync(resolve(binary,'..')))
            return {code:compileFailure?1:0,output:'',stderr:compileFailure?'fixture compile failure':''}
          }
          const compiled=args[0]==='tool',owner=compiled?'internal/nodes':args.at(-1).slice(2)
          if(compiled) {
            assert.deepEqual(args.slice(0,5),['tool','test2json','-t','-p','github.com/inspr-at/paimos/internal/nodes'])
            assert.equal(args[5],binaries[0])
            assert.ok(args.includes('-test.v=test2json'))
            assert.ok(args.includes('-test.paniconexit0'))
            assert.ok(args.includes('-test.count=1'))
            assert.ok(args.includes('-test.timeout=25m'))
            assert.equal(options.cwd,lists[0].options.cwd)
            assert.equal(options.env.PWD,options.cwd)
          } else {
            assert.ok(args.includes('-count=1'))
            assert.ok(args.includes('-timeout=25m'))
            assert.ok(args.includes('-json'))
          }
          const pattern=new RegExp(compiled?args.find(arg=>arg.startsWith('-test.run=')).slice('-test.run='.length):args[args.indexOf('-run')+1])
          const chosen=all.filter(row=>row.package===owner&&row.active&&pattern.test(row.name))
          calls.push({compile:false,compiled,keys:chosen.map(key),args,path,options})
          const attempt=(attempts.get(owner)??0)+1
          attempts.set(owner,attempt)
          const failures=attempt===1?failed:again,Package=`github.com/inspr-at/paimos/${owner}`,output=[]
          for(const row of chosen) {
            if(missing&&row===rows[1])continue
            output.push({Package,Test:row.name,Action:'run'})
            output.push({Package,Test:`${row.name}/nested`,Action:'run'},{Package,Test:`${row.name}/nested`,Action:'pass'})
            output.push({Package,Test:row.name,Action:failures.includes(row)?'fail':'pass'})
          }
          if(unexpected)output.push({Package,Test:'TestUnexpected',Action:'run'},{Package,Test:'TestUnexpected',Action:'pass'})
          const failedAny=chosen.some(row=>failures.includes(row))||packageFailure
          output.push({Package,Action:failedAny?'fail':'pass'})
          return {code:failedAny?1:0,output:output.map(JSON.stringify).join('\n'),stderr:'',signal:signal?'SIGTERM':undefined}
        },
      })
    } catch(caught) { error=caught }
    for(const binary of binaries)assert.equal(existsSync(resolve(binary,'..')),false,'owned binaries are removed on success and every failure')
    return {code,error,report,calls,lists,binaries}
  }
  await t.test('one profiled build serves listing, first execution and failed-case retry; other owners retain go test',async()=>{
    const {code,error,report,calls,lists,binaries}=await simulate()
    assert.equal(error,undefined)
    assert.equal(code,0)
    assert.equal(binaries.length,1)
    assert.equal(lists.length,2)
    assert.deepEqual(calls.filter(call=>!call.compile).map(call=>call.keys),[rows.slice(0,3).map(key),[key(rows[3])],[key(rows[0])]])
    assert.ok(calls.at(-1).path.endsWith('-retry.jsonl'))
    assert.equal(report.classes['GATED-FULL'].run,4)
    assert.equal(report.classes['GATED-FULL'].passed,4)
    assert.deepEqual(report.flakeLedger.map(row=>row.key),[key(rows[0])])
    assert.deepEqual(report.goPhases,[
      {owner:'internal/nodes',compiled:true,compileSeconds:0.1,listSeconds:0.1,executions:[{attempt:1,selectedTests:3,seconds:0.1},{attempt:2,selectedTests:1,seconds:0.1}]},
      {owner:'internal/auth',compiled:false,compileSeconds:0,listSeconds:0.1,executions:[{attempt:1,selectedTests:1,seconds:0.1}]},
    ])
  })
  await t.test('inventory mismatch and compile failure stop before any case execution',async()=>{
    for(const options of [{compileFailure:true},{listMismatch:true}]) {
      const {error,report,calls}=await simulate(options)
      assert.match(error.message,options.compileFailure?/Go test compilation failed:[\s\S]*fixture compile failure/:/Native Go inventory mismatch/)
      assert.equal(report,undefined)
      assert.equal(calls.filter(call=>!call.compile).length,0)
    }
  })
  await t.test('CI count flag retains compile reuse; other GOFLAGS retain the original go test runtime semantics',async()=>{
    assert.equal((await simulate({failed:[],goFlags:'-count=1'})).binaries.length,1)
    for(const goFlags of ['-short','-race','-shuffle=on -failfast']) {
      const {code,error,calls,report,binaries}=await simulate({failed:[],goFlags})
      assert.equal(error,undefined)
      assert.equal(code,0)
      assert.deepEqual(binaries,[])
      assert.ok(calls.every(call=>!call.compile&&!call.compiled&&call.options.env.GOFLAGS===goFlags))
      assert.ok(report.goPhases.every(row=>!row.compiled))
    }
  })
  await t.test('CI count flag stays off go tool test2json, which rejects GOFLAGS=-count=1',async()=>{
    const cli=readFileSync(new URL('./cli.mjs',import.meta.url),'utf8')
    assert.match(cli,/goRunner\.execute\(owner,rows,stem,'-count=1'\)/)
    const {code,error,calls}=await simulate({failed:[],goFlags:'-count=1'})
    assert.equal(error,undefined)
    assert.equal(code,0)
    const compiled=calls.filter(call=>call.compiled)
    assert.ok(compiled.length>=1)
    for(const call of compiled) {
      assert.equal(call.options.env.GOFLAGS,undefined)
      assert.ok(call.args.includes('-test.count=1'))
    }
    for(const call of calls.filter(call=>!call.compile&&!call.compiled)) {
      assert.equal(call.options.env.GOFLAGS,'-count=1')
      assert.ok(call.args.includes('-count=1'))
    }
    assert.equal(calls.find(call=>call.compile).options.env.GOFLAGS,'-count=1')
  })
  await t.test('a shard count other than -count=1 is refused before execution',()=>{
    const runner=createGoRunner({env:{},execute:()=>{throw new Error('must not run')},command:()=>{throw new Error('must not list')}})
    assert.throws(()=>runner.execute('internal/nodes',[{name:'TestA'}],'stem','-count=2'),/reject cached success/)
    runner.close()
  })
  await t.test('native comparison retains subtest identity and outcomes when fixture UUID values change',async()=>{
    const {goExecutionIdentity}=await import('./go-compile-profile.mjs')
    const fixture=(first,second,status='pass')=>['none',first,second,`${first},!${second}`].flatMap(name=>
      ['run',status].map(Action=>JSON.stringify({Package:'github.com/inspr-at/paimos/internal/nodes',
        Test:`TestListEpicMembershipSetBasedAndScoped/${name}`,Action}))).join('\n')
    const ids=['11111111-1111-4111-8111-111111111111','22222222-2222-4222-8222-222222222222',
      '33333333-3333-4333-8333-333333333333','44444444-4444-4444-8444-444444444444']
    const original=goExecutionIdentity(fixture(ids[0],ids[1]))
    assert.deepEqual(original,goExecutionIdentity(fixture(ids[2],ids[3])))
    assert.notDeepEqual(original,goExecutionIdentity(fixture(ids[2],ids[2])),'distinct fixture IDs must remain distinct')
    assert.notDeepEqual(original,goExecutionIdentity(fixture(ids[2],ids[3],'skip')),'subtest status must remain exact')
    assert.notDeepEqual(original,goExecutionIdentity(fixture(ids[2],ids[3]).replaceAll(',!',',')),'filter semantics must remain exact')
  })
  await t.test('PR failures, repeat failures, missing or unexpected cases, package failure and interrupted execution stay red',async()=>{
    for(const options of [{event:'pull_request'},{again:[rows[0]]},{failed:rows},{missing:true},
      {unexpected:true},{failed:[],packageFailure:true},{signal:true}]) {
      const {code,error,report}=await simulate(options)
      assert.equal(error,undefined)
      assert.notEqual(code,0,JSON.stringify(options))
      assert.deepEqual(report.flakeLedger,[],JSON.stringify(options))
    }
    const {error}=await simulate({failed:[],saveFailure:true})
    assert.match(error.message,/fixture save failure/)
  })
})

const poolBrowserEnv=(event='pull_request',eventClass='mbp2606-pr')=>({
  GITHUB_ACTIONS:'true',GITHUB_EVENT_NAME:event,RUNNER_ENVIRONMENT:'self-hosted',
  runner_class:'mbp2606',RUNNER_NAME:'mbp2606-fixture',RUNNER_OS:'Linux',RUNNER_ARCH:'ARM64',
  runs_on:JSON.stringify(['self-hosted','Linux','ARM64','mbp2606',eventClass]),
})

// R13 release integrity: arbitrary self-hosted/local runners must never produce
// browser gate evidence, and the approved pool must keep exact case accounting.
test('AEON-1015 browser guard accepts hosted and event-matched pool runners and refuses other identities before execution',async t=>{
  for(const env of [{RUNNER_ENVIRONMENT:'github-hosted'},
    {RUNNER_ENVIRONMENT:'github-hosted',runner_class:'hosted',runs_on:'["ubuntu-latest"]'}]) {
    assert.equal(browserRunnerIdentity(env).class,'hosted','hosted fallback requires no pool labels')
  }
  const base=poolBrowserEnv(),labels=JSON.parse(base.runs_on)
  for(const [event,eventClass] of [['pull_request','mbp2606-pr'],['merge_group','mbp2606-mq'],
    ['push','mbp2606-push'],['workflow_dispatch','mbp2606-dispatch']]) {
    assert.equal(browserRunnerIdentity(poolBrowserEnv(event,eventClass)).class,'mbp2606',event)
  }
  assert.deepEqual(browserRunnerIdentity({...base,runs_on:JSON.stringify([...labels].reverse())}).labels,labels,'label order is immaterial')
  const refused=[
    ['local',{}],['class alone',{runner_class:'mbp2606'}],
    ['unknown runner environment',{...base,RUNNER_ENVIRONMENT:'unknown'}],
    ['missing runner environment',{...base,RUNNER_ENVIRONMENT:undefined}],
    ['outside Actions',{...base,GITHUB_ACTIONS:undefined}],
    ['false Actions flag',{...base,GITHUB_ACTIONS:'false'}],
    ['missing class',{...base,runner_class:undefined}],['unknown class',{...base,runner_class:'other'}],
    ['hosted class on self-hosted runner',{...base,runner_class:'hosted'}],
    ['wrong OS',{...base,RUNNER_OS:'macOS'}],['missing OS',{...base,RUNNER_OS:undefined}],
    ['wrong architecture',{...base,RUNNER_ARCH:'X64'}],['missing architecture',{...base,RUNNER_ARCH:undefined}],
    ['unsupported event',{...base,GITHUB_EVENT_NAME:'schedule'}],['missing event',{...base,GITHUB_EVENT_NAME:undefined}],
    ['missing labels',{...base,runs_on:undefined}],['malformed JSON',{...base,runs_on:'['}],
    ['labels are null',{...base,runs_on:'null'}],['labels are an object',{...base,runs_on:'{}'}],
    ['labels are a string',{...base,runs_on:'"mbp2606"'}],['labels are not serialized',{...base,runs_on:labels}],
    ['empty labels',{...base,runs_on:'[]'}],['oversized label input',{...base,runs_on:' '.repeat(1025)}],
    ['wrong pool label',{...base,runs_on:JSON.stringify(labels.map(label=>label==='mbp2606'?'other':label))}],
    ['wrong event label',{...base,runs_on:JSON.stringify([...labels.slice(0,-1),'mbp2606-mq'])}],
    ['duplicate label',{...base,runs_on:JSON.stringify([...labels.slice(0,-1),'mbp2606'])}],
    ['extra event label',{...base,runs_on:JSON.stringify([...labels,'mbp2606-mq'])}],
    ['unknown extra label',{...base,runs_on:JSON.stringify([...labels,'other'])}],
    ...labels.map(label=>[`missing ${label} label`,{...base,runs_on:JSON.stringify(labels.filter(value=>value!==label))}]),
  ]
  const row={kind:'browser',file:'tests/pool.spec.ts',name:'guarded case',tier:'GATED-FULL',active:true}
  for(const [name,env] of refused) await t.test(name,async()=>{
    const reached=[]
    const touch=stage=>()=>{reached.push(stage);throw new Error(`Unexpected ${stage}`)}
    await assert.rejects(run('web',{tests:[row],all:[row]},{env,job:'aeon1015-refused'}, {
      loadBrowserPolicy:touch('policy'),command:touch('collection'),runPlaywright:touch('browser'),
      execute:touch('unit'),saveJSON:touch('measurement'),knownFlaky:noFlaky,
    }),/Tier browser execution requires GitHub-hosted CI or the approved mbp2606 runner identity/)
    assert.deepEqual(reached,[],'refusal precedes all execution and successful evidence')
  })
})

test('AEON-1015 browser measurements persist actual runner identity and preserve supervised execution',async t=>{
  const row={kind:'browser',file:'tests/pool.spec.ts',name:'selected case',tier:'GATED-FULL',
    id:'pool-case',project:'chromium',line:10,active:true}
  const native={errors:[],suites:[{specs:[{id:row.id,title:row.name,
    tests:[{projectName:row.project,results:[{status:'passed'}]}]}]}]}
  const policy={groups:[{id:'pool',config:'playwright.ui.config.ts',flags:[],env:{},specs:[{file:row.file}]}]}
  const environments=[
    {RUNNER_ENVIRONMENT:'github-hosted',runner_class:'hosted',runs_on:'["ubuntu-latest"]',RUNNER_NAME:'hosted-fixture',RUNNER_OS:'Linux',RUNNER_ARCH:'X64'},
    ...[['pull_request','mbp2606-pr'],['merge_group','mbp2606-mq'],['push','mbp2606-push'],
      ['workflow_dispatch','mbp2606-dispatch']].map(([event,eventClass])=>poolBrowserEnv(event,eventClass)),
  ]
  for(const identity of environments) await t.test(identity.GITHUB_EVENT_NAME??'hosted fallback',async()=>{
    const job=`aeon1015-${identity.GITHUB_EVENT_NAME??'hosted'}`
    const directory=resolve(fileURLToPath(new URL('../../tmp/test-tiers/',import.meta.url)))
    mkdirSync(directory,{recursive:true})
    const summary=resolve(directory,`${job}-summary.txt`)
    writeFileSync(summary,'')
    const env={...identity,GITHUB_RUN_ID:'1015',GITHUB_RUN_ATTEMPT:'1',GITHUB_SHA:'a'.repeat(40),GITHUB_STEP_SUMMARY:summary}
    const calls=[],logs=[]
    const code=await run('web',{tests:[row],all:[row],full:true,scope:'gated-full',reason:'fixture'},{env,job},{
      knownFlaky:noFlaky,loadBrowserPolicy:()=>policy,log:line=>logs.push(line),
      command:(_bin,args,{env:received})=>{
        calls.push('collect')
        assert.ok(args.includes('--list'))
        assert.equal(received.runs_on,env.runs_on)
        return JSON.stringify(native)
      },
      runPlaywright:async(args,{env:received})=>{
        calls.push('browser')
        assert.equal(received.runner_class,env.runner_class)
        assert.equal(received.PW_RETRIES,'0')
        assert.ok(args.includes('--workers=1'))
        assert.ok(args.includes('--retries=0'))
        assert.ok(args.includes('--forbid-only'))
        assert.equal(readFileSync(args[args.indexOf('--test-list')+1],'utf8'),`[chromium] › pool.spec.ts\n`)
        writeFileSync(received.PLAYWRIGHT_JSON_OUTPUT_FILE,JSON.stringify(native))
        return {code:0}
      },
    })
    assert.equal(code,0)
    assert.deepEqual(calls,['collect','browser'])
    const report=JSON.parse(readFileSync(resolve(directory,`${job}-measurement.json`),'utf8'))
    assert.equal(report.runner_class,identity.RUNNER_ENVIRONMENT==='github-hosted'?'hosted':'mbp2606')
    assert.deepEqual(report.runner,{class:report.runner_class,environment:identity.RUNNER_ENVIRONMENT,
      name:identity.RUNNER_NAME,os:identity.RUNNER_OS,arch:identity.RUNNER_ARCH,
      labels:identity.RUNNER_ENVIRONMENT==='github-hosted'?[]:JSON.parse(identity.runs_on)})
    assert.deepEqual([report.runId,report.attempt,report.sha],['1015','1',env.GITHUB_SHA])
    assert.deepEqual([report.classes['GATED-FULL'].selected,report.classes['GATED-FULL'].run,report.classes['GATED-FULL'].passed],[1,1,1])
    assert.deepEqual(report.flakeLedger,[])
    assert.deepEqual(JSON.parse(logs.at(-1)).runner,report.runner)
    const summaryReport=JSON.parse(readFileSync(summary,'utf8').match(/```json\n([\s\S]*?)\n```/)[1])
    assert.deepEqual(summaryReport.runner,report.runner)
    assert.deepEqual(aggregate([report],[]).reports[0].runner,report.runner,'aggregate retains identity for runner-class analysis')
  })
})

test('AEON-707 work safety regressions remain classified and selected by every full gate',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const protectedCases=[
    ['internal/parentbenefits','TestRetryIsPersonOnlyAndRechecksPermission'],
    ['internal/parentbenefits','TestRetryDeadlineBoundsBothBodyDecodes'],
    ['internal/parentbenefits','TestLeaseRecoveryAndTenantIsolation'],
    ['internal/parentbenefits','TestSummaryRejectsInvalidOrUnboundedModelOutput'],
    ['internal/nodes','TestWorkAggregateLimitsAreExplicitHTTPFailures'],
    ['internal/nodes','TestWorkScopeRootAndExpansionBudgets'],
    ['internal/nodes','TestWorkLifecycleBackgroundCompletionAndRevocation'],
    ['internal/releases','TestWorkParentPlacementRechecksRevokedGrantUnderFence'],
  ].map(([pkg,name])=>({kind:'go',package:pkg,name}))
  const rows=validate(manifest,manifest.tests)
  for(const row of protectedCases) {
    const declared=rows.find(candidate=>key(candidate)===key(row))
    assert.ok(declared,`Missing work safety classification: ${key(row)}`)
    assert.ok(['ESSENTIAL','GATED-FULL'].includes(declared.tier),`Work safety case deferred to NIGHTLY: ${key(row)}`)
  }
  const modes=[
    {event:'push'},
    {event:'workflow_dispatch'},
    {event:'pull_request',paths:['README.md'],forceFull:true},
    {event:'merge_group',paths:['README.md'],forceFull:true},
    {event:'pull_request',paths:['scripts/test-tiers/core.mjs']},
    {event:'merge_group',paths:['api/openapi.yaml']},
    {event:'pull_request'},
    {event:'merge_group'},
  ]
  for(const mode of modes) {
    const selection=select(rows,mode)
    assert.equal(selection.scope,'gated-full')
    const selected=new Set(selection.tests.map(key))
    for(const row of protectedCases) assert.ok(selected.has(key(row)),`${JSON.stringify(mode)} omitted ${key(row)}`)
  }
})

test('AEON-707 work migration test files have explicit tiers without changing runtime drift policy',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const files=[
    'internal/parentbenefits/module_test.go',
    'internal/parentbenefits/worker_test.go',
    'internal/nodes/work_aggregates_test.go',
    'internal/nodes/work_lifecycle_test.go',
    'internal/releases/work_parents_test.go',
  ]
  const discovered=files.flatMap(file=>{
    const source=readFileSync(new URL(`../../${file}`,import.meta.url),'utf8')
    const names=[...source.matchAll(/^func (Test\w+)\(t \*testing\.T\)/gm)].map(match=>match[1])
    assert.ok(names.length,`No test declarations found in ${file}`)
    return names.map(name=>({kind:'go',package:file.slice(0,file.lastIndexOf('/')),name}))
  })
  const identities=new Set(discovered.map(key))
  const owned={...manifest,tests:manifest.tests.filter(row=>identities.has(key(row)))}
  assert.deepEqual(owned.tests.map(key).sort(),[...identities].sort(),'Work migration tests need explicit classifications')
  const rows=validate(owned,discovered,undefined,{strict:true})
  assert.equal(rows.length,discovered.length)
  assert.ok(rows.every(row=>['ESSENTIAL','GATED-FULL'].includes(row.tier)))
  // Unrelated future registrations still reconcile permissively to NIGHTLY.
  const added={kind:'go',package:'internal/nodes',name:'TestFutureUnclassified',active:true}
  const warnings=[]
  assert.equal(validate(owned,[...discovered,added],undefined,{warn:warning=>warnings.push(warning)}).at(-1).tier,'NIGHTLY')
  assert.deepEqual(warnings,[`::warning::Unclassified test (defaults to NIGHTLY; classify these): ${key(added)}`])
})

const replay=JSON.parse(readFileSync(new URL('./affected-replay.json',import.meta.url)))
const legacy=await import(`data:text/javascript;base64,${Buffer.from(replay.legacySelector).toString('base64')}`)
const replayRows=['go','web'].flatMap(kind=>JSON.parse(readFileSync(new URL(`../ci/${kind}-test-tiers.json`,import.meta.url))).tests)
const replayGraph=webGraph(fileURLToPath(new URL('../../web',import.meta.url)))
const repoRoot=fileURLToPath(new URL('../../',import.meta.url))
const repoTree=sourceTree(repoRoot)
const noPromotions={keys:new Set(),kinds:new Set(),count:0}
const unchangedRegistries=registryChangesBetween(undefined,['scripts/ci/go-test-tiers.json','scripts/ci/web-test-tiers.json','web/ci-web-shards.json'],{cwd:repoRoot,baseDirectory:repoRoot})
const readRepo=path=>{ try { return readFileSync(resolve(repoRoot,path),'utf8') } catch { return undefined } }
const realPaths=[...new Set(replay.prs.flatMap(pr=>pr.paths))]
// Affected-lane selection against the real catalogue, tree and graph.
const on=(paths,extra={})=>select(replayRows,{event:'pull_request',paths,affectedLane:'on',webImports:replayGraph,imports:replay.goImports,tree:repoTree,promotions:noPromotions,registryChanges:unchangedRegistries,readFile:readRepo,...extra})
const decide=(paths,extra={})=>schedulingDecision('pull_request',paths,path=>readRepo(path)!==undefined,{affectedLane:'on',graph:replayGraph,tree:repoTree,promotions:noPromotions,registryChanges:unchangedRegistries,...extra})
const goPackages=selection=>new Set(selection.tests.filter(row=>row.kind==='go'&&row.tier!=='ESSENTIAL').map(row=>row.package))
const webFiles=selection=>new Set(selection.tests.filter(row=>row.kind!=='go'&&row.tier!=='ESSENTIAL').map(row=>row.file))
const essentialOnly=selection=>selection.tests.every(row=>row.tier==='ESSENTIAL')

test('affected kill switch off matches the frozen selector for all 80 real PR lists except instruction and AEON-1019 safety fallbacks',()=>{
  assert.equal(replay.prs.length,80)
  assert.equal(replay.base,'b6f74faa11d506efd3d21387ca4e2f5a9c687dcf')
  assert.equal(replay.legacySelectorSHA256,'737a4bb8a805c23e572ad83f2922e65d8c0d34a932afca2e13fd9fa51e26d710')
  assert.equal(new Set(replay.prs.map(pr=>pr.number)).size,80)
  assert.equal(createHash('sha256').update(replay.legacySelector).digest('hex'),replay.legacySelectorSHA256)
  const synthetic=[['scripts/ci/go-test-tiers.json'],['api/openapi.yaml'],['internal/db/migrations/1124_desk_notification_claims.sql'],['scripts/audit/common.py'],
    ['version.json'],['internal/auth/testdata/key_scope_ceiling.json'],['web/tests/settings-shared-harness.html'],['scripts/check-migrations.test.mjs'],['AGENTS.md'],['internal/db/migrations/README.md']]
  for(const {number,paths} of [...replay.prs,...synthetic.map(paths=>({number:paths[0],paths}))]) {
    const options={event:'pull_request',paths,imports:replay.goImports,webImports:replayGraph}
    const expected=legacy.select(replayRows,options)
    for(const affectedLane of [undefined,'','off','ON','true',' on','on ']) {
      const selected=select(replayRows,{...options,affectedLane,tree:repoTree,promotions:noPromotions,readFile:readRepo})
      const message=`PR ${number}, switch ${affectedLane}`
      // AEON-766's instruction safety rule also applies with affected narrowing off.
      if(paths.some(path=>/(?:^|\/)(?:AGENTS|CLAUDE)\.md$/i.test(path)) || selected.full && (!expected.full || /^browser CSS, asset or data/.test(selected.reason))) {
        assert.equal(selected.full,true,message)
        assert.deepEqual(selected.tests,fullGateTests(replayRows,paths),message)
        assert.match(selected.reason,/test-pinned instruction|Go reverse dependencies|browser|web dependency|mapped test|full event|uncertain/,message)
      } else if(selected.full && expected.full) {
        assert.equal(selected.scope,'gated-full',message)
        assert.equal(selected.reason,expected.reason,message)
        assert.deepEqual(selected.tests,fullGateTests(replayRows,paths),message)
      } else assert.deepEqual(selected,expected,message)
    }
    assert.deepEqual(schedulingDecision('pull_request',paths,()=>true,{graph:replayGraph}),schedulingDecision('pull_request',paths,()=>true,{affectedLane:'off',graph:replayGraph}),`PR ${number}`)
  }
})

test('merge groups always use the exact full gate with either switch state, including every real diff',()=>{
  for(const affectedLane of [undefined,'off','on'])for(const paths of [[],['README.md'],['internal/auth/key.go'],...replay.prs.map(pr=>pr.paths)]) {
    const selected=select(replayRows,{event:'merge_group',paths,affectedLane,webImports:replayGraph,tree:repoTree,promotions:noPromotions,readFile:readRepo})
    assert.equal(selected.full,true)
    assert.equal(selected.scope,'gated-full')
    assert.deepEqual(selected.tests,fullGateTests(replayRows,paths))
    const decision=schedulingDecision('merge_group',paths,()=>true,{affectedLane,graph:replayGraph,tree:repoTree,promotions:noPromotions})
    assert.equal(decision.mode,'full')
    assert.equal(decision.layout,'full')
  }
})

test('CI machinery from the real PRs and the pattern matrix always forces full, alone or beside a narrowable path',()=>{
  const real=realPaths.filter(path=>machineryPattern.test(path))
  const matrix=['.github/workflows/ci.yml','.github/workflows/nightly-full.yml','.github/actions/helpers/foo.ts','scripts/ci-static.mjs','scripts/ci-pr-plan.mjs','scripts/ci-tree-reuse.mjs',
    'scripts/ci-flake-guard.mjs','scripts/ci-quarantine.json','scripts/ci-runner-guard/main.go','scripts/ci-go-shards/shard.go','scripts/ci/static-checks.json','scripts/test-tiers/core.mjs',
    'scripts/test-tiers/diff.mjs','scripts/test-tier-go/main.go','scripts/releaseworkflow/workflow_test.go','go.mod','go.sum','go.work','go.work.sum','web/package.json','web/package-lock.json',
    'web/vite.config.ts','web/playwright.ui.config.ts','web/playwright.policy.ts','web/tsconfig.json','web/scripts/ci-web-shard.mjs','web/scripts/run.mjs']
  for(const path of ['.github/workflows/ci.yml','scripts/ci/go-test-tiers.json','web/ci-web-shards.json','scripts/audit/slices.json']) assert.ok(real.includes(path)||!machineryPattern.test(path),path)
  assert.ok(real.length>=10,'real machinery paths must be represented')
  for(const path of new Set([...real,...matrix])) {
    assert.ok(machineryPattern.test(path)||/generated|codegen|\.gen\.|\.pb\./.test(path),path)
    for(const paths of [[path],[path,'scripts/ci/web-test-tiers.json'],['api/openapi.yaml',path],['README.md',path]]) {
      const selected=on(paths)
      assert.equal(selected.full,true,paths.join(' '))
      assert.match(selected.reason,/^(?:CI machinery stays full|unnarrowed generated\/configuration risk): /,paths.join(' '))
      const decision=decide(paths)
      assert.equal(decision.mode,'full',paths.join(' '))
      assert.equal(decision.layout,'full')
      assert.equal(decision.reason,selected.reason)
    }
  }
})

test('every path the rules leave full forces full in the selector and the planner, including the risk-pattern matrix',()=>{
  // R3 helpers are excluded: the injected importer below legitimately maps them.
  const real=realPaths.filter(path=>affectedRisk(path,replayGraph).full&&!/^R3 /.test(affectedRisk(path,replayGraph).reason))
  const matrix=['go.mod','go.sum','go.work','go.work.sum','package.json','package-lock.json',
    '.github/workflows/ci.yml','.github/actions/helpers/foo.ts','internal/db/runner.go','internal/db/queries.sql','internal/dbtest/db.go',
    'scripts/test-tiers/core.mjs','scripts/test-tiers/diff.mjs','scripts/ci-pr-plan.mjs','scripts/check-migrations.mjs','scripts/migration-compat.sh','scripts/migration-compat-probe.py',
    'scripts/ci-weights.mjs','scripts/check.mjs','web/scripts/run.mjs','web/vite.config.ts','web/playwright.ui.config.ts',
    'web/tsconfig.json','web/package-other.json','web/src/generated/api.ts','web/tests/helpers/generated.ts',
    'web/tests/helpers/api.generated.ts','web/tests/helpers/client.gen.ts','internal/auth/client.pb.go','api/codegen.yaml','.dockerignore','Dockerfile','justfile','web/index.html']
  for(const path of ['internal/db/db.go','scripts/test-harness-sigterm-race.py','scripts/aeon-683-validation.json']) assert.ok(real.includes(path),`${path} must stay full on real data`)
  for(const path of new Set([...real,...matrix])) {
    const graph={...replayGraph,[path.slice(4)]:[],'tests/importer.spec.ts':[path.slice(4)]}
    assert.equal(on([path],{webImports:graph}).full,true,path)
    assert.equal(decide([path],{graph}).mode,'full',path)
  }
})

test('R1 semantically unchanged registries take the static layout; promotions must execute and decide the shard layout',()=>{
  const manifests=realPaths.filter(path=>affectedRisk(path).rule==='R1')
  for(const path of ['scripts/ci/go-test-tiers.json','scripts/ci/web-test-tiers.json','web/ci-web-shards.json','web/scripts/ci-web-shard.test.mjs'])
    assert.ok(manifests.includes(path),`${path} must appear in the real PR lists`)
  const planningRegression=replayRows.find(row=>row.kind==='node'&&row.file==='tests/test-tier-failures.test.ts'&&row.name==='native planning regression reuses one collected snapshot for unchanged-tree CLI checks')
  assert.equal(planningRegression?.tier,'NIGHTLY')
  for(const path of [...manifests,'web/scripts/aeon-681-ci.test.mjs']) {
    const selected=on([path])
    assert.equal(selected.full,false,path)
    const nightly=selected.tests.filter(row=>row.tier!=='ESSENTIAL')
    if(path==='web/scripts/aeon-681-ci.test.mjs') assert.deepEqual(nightly.map(key),[key(planningRegression)])
    else assert.deepEqual(nightly,[],`${path}: registration-only manifest diffs run essentials only`)
    assert.match(selected.reason,/R1: (?:manifest\/static|semantic registry) checks: /)
    const decision=decide([path])
    assert.deepEqual([decision.mode,decision.layout],['essential','static'],path)
    assert.ok(selected.reason.endsWith(decision.reason),path)
  }
  for (const path of ['scripts/migration-policy-exceptions.json','scripts/audit/slices.json','scripts/ci/go-shards.txt','scripts/ci/known-flaky.json','scripts/ci/test-tier-selection-baseline.json']) {
    assert.equal(on([path]).full,true,path)
    assert.equal(decide([path]).mode,'full',path)
    assert.match(on([path]).reason,/unclassified registry or execution policy stays full/)
  }
  // The static registry itself is CI machinery, never a manifest.
  assert.equal(on(['scripts/ci/static-checks.json']).full,true)
  // Promotions: absent or lower base tier -> promoted; NIGHTLY and demotions are not.
  const head={go:{tests:[g('internal/auth','TestNew','GATED-FULL'),g('internal/auth','TestUp','ESSENTIAL'),g('internal/auth','TestNightly','NIGHTLY'),g('internal/auth','TestDown','GATED-FULL')]},
    web:{tests:[w('tests/a.unit.test.ts','unit','GATED-FULL'),{...w('tests/b.spec.ts','browser','ESSENTIAL'),kind:'browser'}]}}
  const base={go:{tests:[g('internal/auth','TestUp','GATED-FULL'),g('internal/auth','TestDown','ESSENTIAL')]},web:{tests:[w('tests/a.unit.test.ts','unit','GATED-FULL')]}}
  const promoted=manifestPromotions(base,head)
  assert.deepEqual([...promoted.keys].sort(),['browser:tests/b.spec.ts:browser','internal/auth:TestNew','internal/auth:TestUp'])
  assert.deepEqual([...promoted.kinds].sort(),['browser','go'])
  assert.throws(()=>manifestPromotions({},{go:{tests:[g('internal/auth','TestBad','LATER')]}}),/Invalid tier/)
  const rows=[g('internal/auth','TestCeiling','ESSENTIAL'),g('internal/auth','TestNew','GATED-FULL'),g('internal/other','TestOther','GATED-FULL'),
    w('tests/a.unit.test.ts','unit','GATED-FULL'),{...w('tests/b.spec.ts','browser','GATED-FULL'),kind:'browser'},{...w('tests/c.unit.test.ts','vitest','GATED-FULL'),kind:'vitest'}]
  const run=(promotions,paths=['scripts/ci/web-test-tiers.json'])=>{
    const registryChanges=promotions&&new Map(paths.map(path=>[path,{full:false,promoted:rows.filter(row=>promotions.keys.has(key(row))),reason:'tier promotions execute'}]))
    const selected=select(rows,{event:'pull_request',paths,affectedLane:'on',webImports:{},tree:{go:new Map(),webTests:new Map()},promotions,registryChanges})
    const risk=impactRisk(paths,{event:'pull_request',affectedLane:'on',tree:{go:new Map(),webTests:new Map()},promotions,registryChanges,tests:rows})
    return {selected,risk}
  }
  const pick=keys=>({keys:new Set(keys),kinds:new Set(keys.map(k=>rows.find(row=>key(row)===k).kind)),count:keys.length})
  let result=run(noPromotions)
  assert.deepEqual(result.selected.tests.map(key),['internal/auth:TestCeiling']);assert.equal(result.risk.layout,'static')
  result=run(pick(['vitest:tests/c.unit.test.ts:vitest','node:tests/a.unit.test.ts:unit']))
  assert.deepEqual(result.selected.tests.map(key),['internal/auth:TestCeiling','node:tests/a.unit.test.ts:unit','vitest:tests/c.unit.test.ts:vitest'])
  assert.equal(result.risk.layout,'static','unit promotions run in web-unit, which the static layout keeps')
  assert.match(result.selected.reason,/R1: 2 promoted node\/vitest cases execute/)
  result=run(pick(['browser:tests/b.spec.ts:browser']))
  assert.ok(result.selected.tests.some(row=>key(row)==='browser:tests/b.spec.ts:browser'));assert.equal(result.risk.layout,'full','browser promotions need web shards')
  result=run(pick(['internal/auth:TestNew']),['scripts/ci/go-test-tiers.json'])
  assert.ok(result.selected.tests.some(row=>key(row)==='internal/auth:TestNew'));assert.equal(result.risk.layout,'full','Go promotions need Go shards')
  assert.ok(!result.selected.tests.some(row=>row.package==='internal/other'),'unpromoted packages stay out')
  result=run(undefined)
  assert.equal(result.selected.full,true);assert.match(result.selected.reason,/R1 full: semantic registry comparison unavailable/)
  assert.equal(run(undefined,['web/ci-web-shards.json']).selected.full,true,'browser execution policy also needs the base comparison')
  // git-backed promotions: a base without the manifest or an invalid base fails closed.
  assert.equal(promotionsBetween(undefined,{cwd:repoRoot}),undefined)
  assert.equal(promotionsBetween('--bad',{cwd:repoRoot}),undefined)
  const same=promotionsBetween(undefined,{cwd:repoRoot,baseDirectory:repoRoot})
  assert.equal(same.count,0,'identical manifests promote nothing')
})

// Risk: a timing-shaped field can hide an exclusion, changed launch authority,
// or split policy. Only numeric measurements at known schema locations narrow.
test('AEON-1334 browser registry timing edits preserve every execution policy field',()=>{
  const file='web/ci-web-shards.json'
  const source=JSON.parse(readRepo(file))
  assert.equal(registryImpact(file,source,structuredClone(source)).full,false,'identical source policy is not a behaviour change')
  const retarget=structuredClone(source);retarget.specSource='elsewhere'
  assert.equal(registryImpact(file,source,retarget).full,true,'changing the spec source fails wide')
  for(const change of [
    value=>value.groups[0].flags.push('--retries=2'),
    value=>value.groups[0].env.EXTRA='seconds',
    value=>value.groups[0].config='playwright.perf.config.ts',
    value=>value.groups[0].project='chromium',
    value=>value.groups[0].hostedOnly=!value.groups[0].hostedOnly,
    value=>value.groups[0].gate=false,
    value=>value.groups[0].id+='-changed',
    value=>value.groups[0].specs.push({file:'tests/unmapped.spec.ts',weightSeconds:1}),
    value=>value.exclusions.push({file:'tests/unmapped.spec.ts',ticket:'AEON-1334',reason:'changed exclusion'}),
  ]) {
    const unsafe=structuredClone(source);change(unsafe)
    assert.equal(registryImpact(file,source,unsafe).full,true,change.toString())
  }
  const before=loadPolicy(resolve(repoRoot,file))
  const after=structuredClone(before)
  const timed=Object.keys(after.nightlyTiming.files)[0]
  after.groups[0].specs[0].weightSeconds+=1
  after.groups[0].specs[0].tierTiming.seconds+=1
  after.nightlyTiming.files[timed].seconds+=1
  assert.equal(registryImpact(file,before,after).full,false,'assembled measurements are not policy')
  for(const change of [
    value=>value.groups[0].specs.pop(),
    value=>value.groups[0].specs[0].weightSource='other-source',
    value=>value.groups[0].specs[0].tierTiming.flags=['--grep=omitted'],
    value=>{value.nightlyTiming.files[timed].seconds=-1},
    value=>value.groups[0].specs[0].weightSeconds=0,
    value=>value.groups[0].specs[0].tierTiming.seconds=0,
    value=>value.groups[0].specs[0].tierTiming.caseSeconds={median:Infinity},
  ]) {
    const unsafe=structuredClone(after);change(unsafe)
    assert.equal(registryImpact(file,before,unsafe).full,true,change.toString())
  }
})

test('AEON-1334 tier registry timing and promotions retain identity and run the promoted cases',()=>{
  const file='scripts/ci/web-test-tiers.json'
  const before={version:1,tests:[{...w('tests/a.spec.ts','browser','NIGHTLY'),kind:'browser',config:'playwright.ui.config.ts',project:''},
    w('tests/a.test.ts','unit','NIGHTLY')],timingWeights:{owners:{'tests/a.spec.ts':{seconds:10,selectedTests:1}},runId:'1',sha:'a'.repeat(40)}}
  const after=structuredClone(before)
  after.tests.reverse();after.tests.forEach(row=>row.tier='GATED-FULL')
  after.timingWeights.owners['tests/a.spec.ts'].seconds=20
  after.timingWeights.runId='2';after.timingWeights.sha='b'.repeat(40)
  const change=registryImpact(file,before,after)
  assert.equal(change.full,false)
  assert.deepEqual(change.promoted.map(key).sort(),after.tests.map(key).sort())
  const registryChanges=new Map([[file,change]])
  const rows=[...after.tests,g('internal/core','TestEssential','ESSENTIAL'),g('internal/other','TestOther','GATED-FULL')]
  const options={event:'pull_request',paths:[file],affectedLane:'on',registryChanges,tree:{go:new Map(),webTests:new Map()}}
  const selected=select(rows,options)
  assert.equal(selected.full,false)
  assert.deepEqual(selected.tests.map(key).sort(),[...after.tests.map(key),'internal/core:TestEssential'].sort())
  assert.equal(impactRisk([file],{...options,tests:rows}).layout,'full','promoted browser cases need browser shards')
  for(const mutate of [
    value=>value.tests[0].config='playwright.perf.config.ts',
    value=>value.tests[0].project='changed',
    value=>value.tests[0].flags=['--grep=omitted'],
    value=>value.policy='new policy',
    value=>value.postGateCases=['node:tests/a.test.ts:unit'],
    value=>value.timingWeights.owners['tests/a.spec.ts'].env={OMIT:'yes'},
    value=>value.timingWeights.owners['tests/a.spec.ts'].seconds=NaN,
  ]) {
    const unsafe=structuredClone(after);mutate(unsafe)
    const classified=registryImpact(file,before,unsafe)
    assert.equal(classified.full,true,mutate.toString())
    const decision=schedulingDecision('pull_request',[file],()=>true,{...options,registryChanges:new Map([[file,classified]]),tests:rows})
    assert.equal(decision.mode,'full',mutate.toString())
  }
})

test('AEON-1334 demotions require explicit approval; invalid and changed catalogues never narrow',()=>{
  const file='scripts/ci/go-test-tiers.json'
  const before={version:1,tests:[g('internal/core','TestCore','ESSENTIAL'),g('internal/a','TestA','NIGHTLY')]}
  const demoted=structuredClone(before);demoted.tests[0].tier='GATED-FULL'
  const result=registryImpact(file,before,demoted)
  assert.equal(result.full,true);assert.equal(result.requiresApproval,true)
  assert.match(result.reason,/explicit person approval/)
  for(const after of [undefined,{version:2,tests:before.tests},{...before,tests:[]},
    {...before,tests:[...before.tests,before.tests[0]]},
    {...before,tests:[before.tests[0],g('internal/new','TestNew','GATED-FULL')]},
    {...before,tests:[...before.tests,g('internal/new','TestNew','NIGHTLY')]},
    {...before,tests:[{...before.tests[0],tier:'SOMETIMES'},before.tests[1]]}]) {
    assert.equal(registryImpact(file,before,after).full,true)
  }
  assert.equal(registryImpact('scripts/ci/known-flaky.json',before,before).full,true)
  const split={version:1,tests:before.tests,timingWeights:{owners:{'internal/core':{seconds:10,selectedTests:1,split:{count:2,overheadSeconds:1,caseSeconds:{TestCore:2}}}}}}
  const timing=structuredClone(split);timing.timingWeights.owners['internal/core'].split.overheadSeconds=2
  assert.equal(registryImpact(file,split,timing).full,false)
  timing.timingWeights.owners['internal/core'].split.count=3
  assert.equal(registryImpact(file,split,timing).full,false,'recorded split counts 2 and 3 are measurements')
  timing.timingWeights.owners['internal/core'].split.count=4
  assert.equal(registryImpact(file,split,timing).full,true,'an unrecorded split count is not a measurement')
  timing.timingWeights.owners['internal/core'].split.count=3
  timing.timingWeights.owners['internal/core'].split.policy='hidden'
  assert.equal(registryImpact(file,split,timing).full,true,'unknown split fields stay policy')
})

test('AEON-1334 registry reads bind the base and fail wide on malformed, missing, symlinked and oversized files',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-1334-registries-'))
  const file='scripts/ci/go-test-tiers.json',base='a'.repeat(40)
  const before={version:1,tests:[g('internal/a','TestA','NIGHTLY')]},after=structuredClone(before)
  after.tests[0].tier='GATED-FULL'
  const path=resolve(directory,file)
  mkdirSync(resolve(path,'..'),{recursive:true})
  const read=(candidate,baseText=JSON.stringify(before))=>{
    writeFileSync(path,candidate)
    return registryChangesBetween(base,[file],{cwd:directory,exec:(bin,args,options)=>{
      assert.equal(bin,'git');assert.deepEqual(args,['show',`${base}:${file}`]);assert.equal(options.timeout,15_000)
      return baseText
    }}).get(file)
  }
  try {
    assert.equal(read(JSON.stringify(after)).full,false)
    assert.equal(read('{bad json').full,true)
    assert.equal(read(JSON.stringify(after),'{bad base').full,true)
    assert.equal(read(' '.repeat(4*1024*1024)).full,true)
    assert.equal(read(JSON.stringify(after),' '.repeat(4*1024*1024)).full,true)
    assert.equal(registryChangesBetween(undefined,[file],{cwd:directory}).get(file).full,true)
    const other='web/ci-web-shards.json'
    assert.equal(registryChangesBetween(base,[other],{cwd:directory,exec:()=>JSON.stringify(before)}).get(other).full,true)
    // The symlink's target contains a valid, unchanged registry. Rejecting it
    // must come from the bounded reader, rather than malformed policy data.
    const target=resolve(directory,'fixture.json');writeFileSync(target,JSON.stringify(before))
    rmSync(path);symlinkSync(target,path)
    const linked=registryChangesBetween(base,[file],{cwd:directory,exec:()=>JSON.stringify(before)}).get(file)
    assert.equal(linked.full,true);assert.equal(linked.reason,'registry comparison unavailable')
  } finally { rmSync(directory,{recursive:true,force:true}) }
})

test('AEON-1334 rollback and merge queue never consume semantic exemptions; mixed policy edits run full',()=>{
  const paths=['scripts/ci/web-test-tiers.json']
  const options={registryChanges:unchangedRegistries,tree:repoTree,tests:replayRows}
  for(const affectedLane of [undefined,'off','ON','true',' on','on ']) {
    assert.equal(schedulingDecision('pull_request',paths,()=>true,{...options,affectedLane}).mode,'full')
    assert.equal(select(replayRows,{...options,event:'pull_request',paths,affectedLane}).full,true)
  }
  for(const event of ['merge_group','push','schedule','workflow_dispatch']) {
    const decision=schedulingDecision(event,paths,()=>true,{...options,affectedLane:'on'})
    assert.equal(decision.mode,'full');assert.equal(decision.layout,'full')
    assert.equal(select(replayRows,{...options,event,paths,affectedLane:'on'}).full,true)
  }
  assert.equal(decide([...paths,'scripts/ci/known-flaky.json']).mode,'full')
  assert.equal(on([...paths,'scripts/ci/known-flaky.json']).full,true)
  const env={GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'}
  for (const scope of ['none','affected']) {
    assert.throws(()=>checkFull({...reportCases(cases,[],1,'fixture'),full:true,exitCode:0,runId:'123',attempt:'2',sha:'a',scope},env),/Full execution scope is not proven/)
  }
})

test('AEON-1389 shard records stay R1 and a new spec does not go full because of its record',()=>{
  const record='web/scripts/ci-web-shard-specs/tests/needs-attention.spec.ts.json'
  assert.equal(machineryPattern.test(record),false,record)
  assert.equal(affectedRisk(record).rule,'R1')
  const weight=on([record])
  assert.equal(weight.full,false)
  assert.ok(essentialOnly(weight),'a weight record runs essentials only')
  assert.match(weight.reason,/R1: manifest\/static checks: /)
  assert.deepEqual([decide([record]).mode,decide([record]).layout],['essential','static'])
  const spec='web/tests/needs-attention.spec.ts'
  const changed=on([spec])
  assert.equal(changed.full,false,'an existing spec stays reduced')
  const withRecord=on([spec,record])
  assert.equal(withRecord.full,false,'its shard record must not widen the spec')
  assert.deepEqual(withRecord.tests,changed.tests)
  assert.equal(decide([spec,record]).mode,'essential')
  const fresh='tests/aeon-1389-fresh.spec.ts'
  const freshRecord=`web/scripts/ci-web-shard-specs/${fresh}.json`
  const graph={...replayGraph,[fresh]:[]}
  const rows=[...replayRows,{kind:'browser',file:fresh,name:'fresh',tier:'GATED-FULL',config:'playwright.ui.config.ts',project:''}]
  const options={event:'pull_request',affectedLane:'on',webImports:graph,imports:replay.goImports,tree:repoTree,promotions:noPromotions,readFile:readRepo}
  const specOnly=select(rows,{...options,paths:[`web/${fresh}`]})
  const both=select(rows,{...options,paths:[`web/${fresh}`,freshRecord]})
  assert.equal(specOnly.full,false)
  assert.equal(both.full,false,'a new spec record is not CI machinery')
  assert.deepEqual(both.tests,specOnly.tests)
  assert.ok(both.tests.some(row=>row.file===fresh))
  const planned=schedulingDecision('pull_request',[`web/${fresh}`,freshRecord],()=>true,{affectedLane:'on',graph,tree:repoTree,promotions:noPromotions,tests:rows})
  assert.equal(planned.mode,'essential')
  assert.doesNotMatch(planned.reason,/CI machinery/)
  assert.equal(on(['web/scripts/ci-web-shard-source.mjs']).full,true,'the assembler stays machinery')
  assert.equal(on(['web/scripts/ci-web-shard-specs/notes.json']).full,true,'a non-record under the source directory stays machinery')
  assert.equal(machineryPattern.test('web/scripts/ci-web-shard.mjs'),true)
  assert.equal(machineryPattern.test('web/scripts/ci-web-shard.test.mjs'),false)
})

test('R2 contract diffs run the Go packages that read the contract and keep web at essentials',()=>{
  assert.equal(on(['api/openapi.yaml']).full,true,'real consumer fan-out keeps the full gate')
  const selected=on(['api/openapi.yaml'],{imports:{}})
  assert.equal(selected.full,false)
  const packages=goPackages(selected)
  for(const pkg of ['internal/chat','internal/modelregistry','internal/reportercontract','internal/harness']) assert.ok(packages.has(pkg),`${pkg} reads api/openapi.yaml`)
  assert.equal(webFiles(selected).size,0,'handwritten web wire types: no generated importers to select')
  assert.match(selected.reason,/R2: contract consumers: api\/openapi\.yaml; consumer packages: /)
  const decision=decide(['api/openapi.yaml'],{imports:{}})
  assert.deepEqual([decision.mode,decision.layout],['essential','full'])
  assert.ok(selected.reason.endsWith(decision.reason))
  assert.equal(on(['api/openapi-messaging.yaml'],{imports:{}}).full,false)
  // The OpenAPI lint package, codegen configuration or generated sources stay full.
  assert.match(on(['api/openapi.yaml','internal/reportercontract/pins.json']).reason,/^R2 full: OpenAPI lint package changed/)
  assert.match(on(['api/codegen.yaml']).reason,/generated\/configuration/)
  // No reader at all is not a narrowing: the tree must name a consumer.
  const empty={go:new Map([['internal/x/x.go','package x']]),webTests:new Map()}
  assert.match(on(['api/openapi.yaml'],{tree:empty}).reason,/^R2 full: no Go consumer found/)
  assert.match(on(['api/openapi.yaml'],{tree:undefined}).reason,/^R2 full: no Go consumer found/)
})

test('R2 OpenAPI fragments retain the live bundle reader set and their owners or stay full',()=>{
  const readers=consumers(repoTree,{substrings:['api/openapi.yaml']}).goPackages
  assert.ok(readers.has('scripts'),'the reporter pin tool is a bundle reader')
  const options={event:'pull_request',affectedLane:'on',tree:repoTree,readFile:readRepo}
  const baseline=impactRisk(['api/openapi.yaml'],options)
  assert.equal(baseline.full,false)
  assert.deepEqual(baseline.goSeeds,readers)
  for(const path of ['api/openapi.base.yaml','internal/harness/openapi.yaml',
    ...readdirSync(resolve(repoRoot,'api/areas')).map(file=>`api/areas/${file}`)]) {
    const risk=impactRisk([path],options)
    const expected=new Set(readers)
    if(path.startsWith('internal/'))expected.add('internal/harness')
    const text=readRepo(path)
    const owners=[...text.matchAll(/^# aeon:owner (\S+)$/gm)].map(match=>match[1])
    if(path.startsWith('api/areas/')&&!owners.length&&/^  \//m.test(text)) {
      assert.equal(risk.full,true,path)
      assert.match(risk.reasons[0],/fragment owner unavailable/)
    } else {
      for(const owner of owners)expected.add(owner)
      assert.equal(risk.full,false,path)
      assert.deepEqual(risk.goSeeds,expected,path)
    }
    assert.equal(impactRisk([path,'internal/reportercontract/pins.json'],options).full,true,path)
  }
  assert.deepEqual(goPackages(on(['internal/harness/openapi.yaml'])),goPackages(on(['api/openapi.yaml'])))
  assert.deepEqual(goPackages(on(['api/openapi.base.yaml'])),goPackages(on(['api/openapi.yaml'])))
  assert.equal(impactRisk(['api/areas/missing.yaml'],options).full,true)
  assert.equal(impactRisk(['api/openapi.base.yaml'],{...options,tree:{go:new Map(),webTests:new Map()}}).full,true)
})

test('R4 migrations map recognised schema objects to referencing packages; unreadable, unparseable or wide diffs stay full',()=>{
  assert.deepEqual([...migrationObjects(`-- comment CREATE TABLE ignored
    CREATE TABLE desk_claims (id uuid, note text DEFAULT 'INSERT INTO fake');
    ALTER TABLE ONLY public.project_leads ADD COLUMN dispatch_key_id uuid;
    CREATE UNIQUE INDEX desk_claims_idx ON desk_claims (id);
    INSERT INTO settings (k) VALUES ('new');
    UPDATE nodes SET kind = 'x' WHERE kind = 'y';
    DELETE FROM old_settings WHERE k = 'old';`)].sort(),
    ['desk_claims','nodes','old_settings','project_leads','settings'])
  assert.equal(migrationObjects('DO $$ BEGIN PERFORM 1; END $$;'),undefined)
  assert.deepEqual([...consumers({go:new Map([['internal/a/a.go','SELECT * FROM desk_claims'],['internal/b/b.go','desk_claims_extra']]),webTests:new Map()},{words:['desk_claims']}).goPackages],['internal/a'])
  const narrow='internal/db/migrations/1124_desk_notification_claims.sql'
  const simple='CREATE TABLE desk_notification_claims (id uuid, note text);'
  assert.equal(on([narrow],{readFile:()=>simple}).full,true,'real migration fan-out keeps the full gate')
  const selected=on([narrow],{readFile:()=>simple,imports:{}})
  assert.equal(selected.full,false)
  assert.ok(goPackages(selected).has('internal/db'),'the migration runner package always runs')
  const readers=consumers(repoTree,{words:[...migrationObjects(simple)]}).goPackages
  assert.ok(readers.size>=1)
  for(const pkg of readers) assert.ok(goPackages(selected).has(pkg),pkg)
  assert.match(selected.reason,/R4: migration schema consumers: .*; consumer packages: /)
  const unrelated=replayRows.find(row=>row.kind==='go'&&row.tier==='GATED-FULL'&&row.package!=='internal/db'&&!readers.has(row.package))
  assert.ok(unrelated,'fixture must include an unrelated gated package')
  assert.ok(!selected.tests.includes(unrelated),'narrow SQL selection must omit unrelated gated tests')
  assert.equal(webFiles(selected).size,0,'browser specs are API-mocked; the real server runs in e2e')
  assert.equal(decide([narrow]).mode,'full','the real migration contains unsupported policy and constraint expressions')
  const decision=decide([narrow],{readFile:()=>simple,imports:{}})
  assert.deepEqual([decision.mode,decision.layout],['essential','full'])
  assert.ok(selected.reason.endsWith(decision.reason))
  const wide='internal/db/migrations/1206_themes.sql'
  assert.match(on([wide]).reason,/^R4 full: unsupported or incomplete SQL/)
  assert.equal(decide([wide]).mode,'full')
  assert.match(on(['internal/db/migrations/9999_missing.sql']).reason,/^R4 full: migration unreadable/)
  assert.match(decide(['internal/db/migrations/9999_missing.sql']).reason,/^R4 full: migration unreadable/)
  assert.match(on([narrow],{readFile:()=>'-- nothing'}).reason,/^R4 full: no schema object recognised/)
  assert.match(on([narrow],{readFile:()=>simple,tree:{go:new Map(),webTests:new Map()}}).reason,/^R4 full: no Go package references desk_notification_claims/)
  // Other database internals stay full; the migrations README belongs to internal/db.
  assert.match(on(['internal/db/db.go']).reason,/^unnarrowed risk: internal\/db\/db\.go/)
  const readme=on(['internal/db/migrations/README.md'],{imports:{}})
  assert.equal(readme.full,false);assert.ok(goPackages(readme).has('internal/db'))
  assert.equal(decide(['internal/db/migrations/README.md']).layout,'full')
})

test('R4 never narrows partially understood SQL or omits quoted, qualified or string-adjacent targets',()=>{
  const path='internal/db/migrations/9999_fixture.sql'
  const rows=[g('internal/core','TestCore','ESSENTIAL'),g('internal/billing','TestBilling','GATED-FULL'),
    g('internal/db','TestMigrations','GATED-FULL'),g('internal/unrelated','TestUnrelated','GATED-FULL')]
  const tree={go:new Map([['internal/billing/billing.go','SELECT * FROM billing_records']]),webTests:new Map()}
  const selection=sql=>select(rows,{event:'pull_request',affectedLane:'on',paths:[path],tree,readFile:()=>sql})
  const safe="CREATE TABLE harmless (id uuid, note text DEFAULT 'a--b /* literal */');"
  for(const sql of [
    `${safe} DO $$ BEGIN UPDATE billing_records SET value=1; END $$;`,
    `${safe} WITH changed AS (SELECT 1) INSERT INTO billing_records (value) VALUES (1);`,
    `${safe} WITH changed AS (SELECT 1) UPDATE billing_records SET value=1;`,
    `${safe} WITH changed AS (SELECT 1) DELETE FROM billing_records;`,
    `${safe} TRUNCATE harmless, billing_records;`,
    `${safe} CREATE FUNCTION f() RETURNS void AS $body_42$ DELETE FROM billing_records; $body_42$ LANGUAGE sql;`,
    `${safe} SELECT update_billing_records();`,
    `${safe} INSERT INTO harmless (id) SELECT id FROM billing_records;`,
    `${safe} UPDATE harmless SET id=(DELETE FROM billing_records RETURNING id);`,
    `${safe} CREATE TABLE other (id uuid REFERENCES billing_records(id));`,
    `${safe} CREATE INDEX i ON harmless (write_billing_records());`,
    `${safe} UPDATE harmless SET note=E'billing_records';`,
    `${safe} INSERT INTO harmless (note) VALUES ($x_1$billing_records$x_1$);`,
    `${safe} ALTER TABLE harmless ADD COLUMN id uuid; billing_records`,
    `${safe} /* billing_records`, `${safe} 'billing_records`, `${safe} "billing_records`,
    `${safe} $tag_1$billing_records`, `${safe}; DELETE FROM billing_records;`,
  ]) {
    assert.equal(migrationObjects(sql),undefined,sql)
    const selected=selection(sql)
    assert.equal(selected.full,true,sql)
    assert.deepEqual(selected.tests,rows,sql)
    assert.match(selected.reason,/R4 full: unsupported or incomplete SQL/)
    assert.equal(schedulingDecision('pull_request',[path],()=>true,{affectedLane:'on',tree,readFile:()=>sql}).mode,'full',sql)
  }
  for(const target of ['billing_records','public.billing_records','"billing_records"','"public"."billing_records"']) {
    const sql=`${safe} UPDATE ${target} SET value=1 WHERE id=2;`
    assert.deepEqual([...migrationObjects(sql)],['harmless','billing_records'])
    const selected=selection(sql)
    assert.equal(selected.full,false,target)
    assert.deepEqual(selected.tests,rows.slice(0,3),target)
    assert.ok(selected.tests.includes(rows[1]),target)
    assert.ok(!selected.tests.includes(rows[3]),target)
  }
  assert.deepEqual([...migrationObjects("CREATE TABLE harmless (note text DEFAULT '--'); DELETE FROM billing_records;")],['harmless','billing_records'])
  assert.deepEqual([...migrationObjects('/* outer /* DELETE FROM fake */ end */ DELETE FROM "public"."billing_records"; -- tail')],['billing_records'])
  assert.equal(migrationObjects('DELETE FROM other.billing_records;'),undefined)
  assert.equal(migrationObjects('DELETE FROM "Public"."billing_records";'),undefined)
  assert.equal(migrationObjects('CREATE TABLE harmless (note text DEFAULT \'broken\\\'string\'); DELETE FROM billing_records;'),undefined)
  assert.equal(migrationObjects('x'.repeat(inputBounds.fileBytes)),undefined)
})

test('R4 seeded comment, string and dollar-body perturbations never hide an unparsed affected object',()=>{
  let seed=257
  const random=()=>{seed=(Math.imul(seed,1664525)+1013904223)>>>0;return seed}
  const gaps=[' ','\n','/* billing_records */','-- billing_records\n','/* nested /* body */ end */']
  const valid="CREATE TABLE harmless (id uuid, note text DEFAULT 'a--b');"
  for(let n=0;n<160;n++) {
    const gap=()=>gaps[random()%gaps.length]
    const body=[
      ['DO',`$tag_${n}$ BEGIN UPDATE billing_records SET note='-- /* */'; END $tag_${n}$`],
      ['WITH','q','AS','(','SELECT',"'-- billing_records'",')','DELETE','FROM','billing_records'],
      ['TRUNCATE','harmless',',','billing_records'],
      ['CREATE','FUNCTION','f','(',')','RETURNS','void','AS',`$$ DELETE FROM billing_records; $$`,'LANGUAGE','sql'],
    ][random()%4].join(gap())
    const sql=`${gap()}${valid}${gap()}${body};${gap()}`
    assert.equal(migrationObjects(sql),undefined,`seed case ${n}: ${sql}`)
  }
})

test('consumer scans preflight file, aggregate, per-file and traversal bounds before reading',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-consumer-bounds-'))
  const file=(path,text)=>{mkdirSync(resolve(directory,path,'..'),{recursive:true});writeFileSync(resolve(directory,path),text)}
  file('internal/a/a.go','api/contract.json')
  file('cmd/b/b.go','billing_records')
  file('web/tests/reader.test.ts','version.json')
  let reads=0
  const read=(...args)=>{reads++;return readInput(...args)}
  for(const [bounds,reason] of [
    [{...inputBounds,files:3},/file count/],
    [{...inputBounds,totalBytes:10},/total byte/],
    [{...inputBounds,fileBytes:5},/per-file byte/],
    [{...inputBounds,entries:2},/entry count/],
  ]) {
    reads=0
    const tree=sourceTree(directory,{bounds,read})
    assert.equal(tree.complete,false)
    assert.match(tree.reason,reason)
    assert.equal(reads,0,'all bounds must be checked before any file read')
    assert.equal(tree.go.size+tree.webTests.size,0)
    for(const path of ['api/contract.json','version.json','internal/db/migrations/9999_test.sql','internal/a/a.go']) {
      const options={event:'pull_request',affectedLane:'on',paths:[path],tree,readFile:()=>'DELETE FROM billing_records;'}
      assert.equal(select(cases,options).full,true,path)
      assert.equal(schedulingDecision('pull_request',[path],()=>true,{affectedLane:'on',tree}).mode,'full',path)
    }
  }
  const complete=sourceTree(directory,{read})
  assert.equal(complete.complete,true)
  assert.deepEqual([...complete.go.keys()].sort(),['cmd/b/b.go','internal/a/a.go'])
  assert.equal(complete.webTests.get('tests/reader.test.ts'),'version.json')
  const failed=sourceTree(directory,{read:()=>{throw new Error('fixture read error')}})
  assert.equal(failed.complete,false);assert.match(failed.reason,/fixture read error/)
  assert.equal(failed.go.size,0)
  assert.equal(sourceTree(resolve(directory,'missing')).complete,false)
  // Sparse files test real default bounds without allocating/reading their bytes.
  file('internal/a/huge.go','')
  truncateSync(resolve(directory,'internal/a/huge.go'),inputBounds.fileBytes)
  reads=0
  assert.equal(sourceTree(directory,{read}).complete,false)
  assert.equal(reads,0)
  assert.equal(boundedText(directory,'internal/a/huge.go'),undefined)
})

test('consumer scans reject binary, malformed UTF-8 and escaping symlinks, discarding partial reads',()=>{
  for(const bytes of [Buffer.from([0,1,2]),Buffer.from([1,2,3]),Buffer.from([0xff,0xfe])]) {
    const directory=mkdtempSync(resolve(tmpdir(),'aeon-consumer-binary-'))
    mkdirSync(resolve(directory,'internal/a'),{recursive:true})
    writeFileSync(resolve(directory,'internal/a/binary.go'),bytes)
    assert.equal(sourceTree(directory).complete,false)
    assert.equal(boundedText(directory,'internal/a/binary.go'),undefined)
  }
  for(const directoryLink of [false,true]) {
    const directory=mkdtempSync(resolve(tmpdir(),'aeon-consumer-link-'))
    const outside=mkdtempSync(resolve(tmpdir(),'aeon-consumer-outside-'))
    mkdirSync(resolve(directory,'internal/a'),{recursive:true})
    writeFileSync(resolve(outside,'escape.go'),'api/contract.json')
    symlinkSync(directoryLink?outside:resolve(outside,'escape.go'),resolve(directory,'internal/a',directoryLink?'escape':'escape.go'))
    const tree=sourceTree(directory)
    assert.equal(tree.complete,false);assert.match(tree.reason,/symlink/)
    assert.equal(tree.go.size,0)
    assert.equal(boundedText(directory,directoryLink?'internal/a/escape/escape.go':'internal/a/escape.go'),undefined)
  }
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-consumer-growth-'))
  const path=resolve(directory,'migration.sql')
  writeFileSync(path,'DELETE FROM billing_records;')
  const metadata=inputMetadata(directory,'migration.sql')
  writeFileSync(path,'DELETE FROM billing_records; SELECT unknown();')
  assert.throws(()=>readInput(metadata),/changed during scan/)
})

test('R5 audit tooling and R9 always-on migration tests take the static layout; their executables stay full',()=>{
  for(const path of ['scripts/audit/common.py','scripts/audit/test_audit.py','scripts/audit/run.cjs','scripts/audit/rubric.md','scripts/check-migrations.test.mjs','scripts/migration_compat_probe_test.py']) {
    const selected=on([path])
    assert.equal(selected.full,false,path);assert.ok(essentialOnly(selected),path)
    assert.match(selected.reason,/; R[59]: /,path)
    assert.deepEqual([decide([path]).mode,decide([path]).layout],['essential','static'],path)
  }
  for(const path of ['scripts/check-migrations.mjs','scripts/migration-compat.sh','scripts/migration-compat-probe.py']) assert.match(on([path]).reason,/^unnarrowed risk: /,path)
  assert.ok(realPaths.includes('scripts/audit/test_audit.py')&&realPaths.includes('scripts/check-migrations.test.mjs'))
})

test('test-pinned instruction files require the full gate under every affected switch state (AEON-766)',()=>{
  const rollout=JSON.parse(readFileSync(new URL('../rules-bootstrap/rollout.json',import.meta.url),'utf8'))
  const pinned=rollout.targets.flatMap(({target,candidate})=>[target,candidate])
  const switches=[undefined,'','off','ON','true',' on','on ','on']
  const gated=fullGateTests(replayRows)
  for(const path of [...pinned,'docs/AGENTS.md','docs/nested/CLAUDE.md','web/AGENTS.md','internal/auth/CLAUDE.md','cmd/aeon/AGENTS.md',
    'internal/rulesimport/testdata/pack/AGENTS.md','scripts/audit/AGENTS.md','agents.md','docs/claude.MD','internal/auth/cLaUdE.mD']) {
    assert.equal(isDocsLike(path),false,path)
    for(const paths of [[path],['README.md',path]]) for(const affectedLane of switches) {
      const message=`${paths.join()}, switch ${affectedLane}`
      const selected=on(paths,{affectedLane})
      assert.equal(selected.full,true,message)
      assert.deepEqual(selected.tests,gated,message)
      // Model existing instruction files, so deletion fallback cannot hide a missing guard.
      const decision=schedulingDecision('pull_request',paths,()=>true,{affectedLane,graph:replayGraph,tree:repoTree,promotions:noPromotions})
      assert.deepEqual([decision.mode,decision.layout],['full','full'],message)
    }
  }
  for(const path of ['README.md','docs/x.md','docs/guide.txt','CHANGELOG.md','LICENSE']) for(const affectedLane of switches) {
    const message=`${path}, switch ${affectedLane}`
    const selected=on([path],{affectedLane})
    assert.equal(selected.full,false,message)
    assert.ok(essentialOnly(selected),message)
    const decision=schedulingDecision('pull_request',[path],()=>true,{affectedLane,graph:replayGraph,tree:repoTree,promotions:noPromotions})
    assert.equal(decision.mode,'essential',message)
    assert.equal(decision.layout,affectedLane==='on'?'static':'full',message)
  }
  for(const path of ['README.md','docs/x.md','web/README.md','CHANGELOG.md','LICENSE'])
    assert.equal(isDocsLike(path),true,path)
})

test('R6 release data, R7 test data and R8 harness pages select their readers; docs-like files are static',()=>{
  assert.equal(on(['version.json','internal/releasehistory/data/product-notes.json']).full,true,'real release consumer fan-out keeps the full gate')
  const release=on(['version.json','internal/releasehistory/data/product-notes.json'],{imports:{}})
  assert.equal(release.full,false)
  for(const pkg of ['internal/releasehistory','internal/releases']) assert.ok(goPackages(release).has(pkg),pkg)
  assert.ok(webFiles(release).has('tests/calver3.test.ts'))
  assert.equal(decide(['version.json']).layout,'full')
  assert.match(on(['version.json'],{tree:{go:new Map(),webTests:new Map()}}).reason,/^R6 full: no Go consumer found/)
  const data=on(['internal/auth/testdata/key_scope_ceiling.json'],{imports:{}})
  assert.equal(data.full,false)
  assert.ok(goPackages(data).has('internal/auth'),'owning package');assert.ok(webFiles(data).has('tests/access.test.ts'),'web reader by name')
  const lonely=on(['internal/auth/testdata/key_scope_ceiling.json'],{tree:{go:new Map(),webTests:new Map()},imports:{}})
  assert.equal(lonely.full,false);assert.ok(goPackages(lonely).has('internal/auth'),'test data without other readers still runs its owner')
  const page=on(['web/tests/settings-shared-harness.html'])
  assert.equal(page.full,true)
  assert.deepEqual(page.tests,fullGateTests(replayRows))
  assert.equal(decide(['web/tests/settings-shared-harness.html']).mode,'full')
  for(const paths of [['docs/RELEASE.md'],['web/README.md'],['scripts/audit/rubric.md'],['README.md','LICENSE','CHANGELOG.md']]) {
    const selected=on(paths)
    assert.equal(selected.full,false,paths.join());assert.ok(essentialOnly(selected))
    assert.deepEqual([decide(paths).mode,decide(paths).layout],['essential','static'],paths.join())
  }
  assert.equal(decide([]).layout,'full','an empty diff keeps the shard layout')
  assert.match(on(['internal/auth/testdata/notes.md'],{imports:{}}).reason,/R7: test data owner/,'markdown inside testdata is test data, not docs')
  assert.equal(decide(['internal/auth/testdata/notes.md']).layout,'full')
})

test('AEON-1334 fixture markdown widens and the browser scope allowlist stays empty',()=>{
  for(const path of ['web/tests/fixtures/example.md','web/tests/example.md','web/e2e/notes.md','internal/auth/testdata/notes.md','scripts/notes.md'])
    assert.equal(isDocsLike(path),false,path)
  assert.equal(browserScopeNoneAllowlist.length,0)
  const fixture=on(['web/tests/fixtures/example.md'])
  assert.equal(fixture.full,true)
  assert.match(fixture.reason,/browser CSS, asset or data input/)
  assert.deepEqual(fixture.tests,fullGateTests(replayRows))
})

test('AEON-1334 shared principal, auth and schema changes run the appearance migration fixtures',()=>{
  const names=['TestAppearanceMigrationPreservesInheritedExplicitChoices','TestAppearanceMigrationRetainsLegacyAliasAfterExplicitCanonicalUnlink','TestAppearanceMigrationKeepsCanonicalAppearanceWhileLinkedAndAliasesAfterUnlink']
  const rows=[g('internal/auth','TestAuth','ESSENTIAL'),...names.map(name=>g('internal/themes',name,'NIGHTLY')),g('internal/themes','TestOtherTheme','NIGHTLY')]
  const tree={go:new Map([['internal/auth/auth.go','package auth'],['internal/themes/themes.go','package themes']]),webTests:new Map()}
  for(const path of ['internal/auth/store.go','api/areas/messaging-assistants.yaml','internal/db/migrations/1355_authenticated_review_runs.sql','internal/fieldschema/schema.go']) {
    const selected=select(rows,{event:'pull_request',paths:[path],affectedLane:'on',tree,imports:{},readFile:()=>'package auth\n'})
    for(const name of names) assert.ok(selected.tests.some(row=>row.name===name),`${path} omitted ${name}`)
    assert.ok(!selected.tests.some(row=>row.name==='TestOtherTheme'),path)
  }
  const full=select(rows,{event:'pull_request',paths:['.github/workflows/ci.yml']})
  for(const name of names) assert.ok(full.tests.some(row=>row.name===name),name)
})

test('AEON-1334 the full gate runs the recorded drift cases, not their files, and fits the web job timeout',()=>{
  // Whole drift files added 1,523 NIGHTLY browser cases to every full PR and
  // merge group; eight of twelve web shards hit the 20-minute job cap (#695).
  const full=fullGateTests(replayRows)
  const selected=new Set(full.map(key))
  const recorded=(row,names)=>names.some(name=>row.name===name||row.name.endsWith(` › ${name}`)||name.endsWith(` › ${row.name}`))
  let siblings=0
  for(const [owner,names] of driftCases) {
    const rows=replayRows.filter(row=>row.kind!=='go'&&`${row.kind}:${row.file}`===owner)
    for(const row of rows.filter(row=>recorded(row,names))) assert.ok(selected.has(key(row)),key(row))
    // Every recorded title resolves here, so the file's other nightly cases stay deferred.
    if(!names.every(name=>rows.some(row=>recorded(row,[name])))) continue
    for(const row of rows.filter(row=>row.tier==='NIGHTLY'&&!recorded(row,names))) { siblings++; assert.ok(!selected.has(key(row)),key(row)) }
  }
  assert.ok(siblings>500,`unrecorded nightly siblings ${siblings}`)
  const policy=loadWebPolicy()
  const browser=rows=>rows.filter(row=>row.kind==='browser')
  const established=webShardPlan(browser(replayRows.filter(row=>row.tier==='ESSENTIAL'||row.tier==='GATED-FULL')),policy,{mode:'full'})
  const plan=webShardPlan(browser(full),policy,{mode:'full'})
  assert.deepEqual(plan.shards,fullWebShards)
  assertWebShardBudget(plan)
  assert.ok(plan.projectedMaxSeconds<=plan.budgetSeconds,`busiest full shard ${Math.round(plan.projectedMaxSeconds)}s`)
  assert.ok(plan.projectedMaxSeconds<plan.timeoutSeconds,`busiest full shard ${Math.round(plan.projectedMaxSeconds)}s`)
  // Drift obligations may spend at most the planner's timeout headroom.
  assert.ok(plan.projectedMaxSeconds-established.projectedMaxSeconds<=plan.timeoutSeconds*plan.headroom,
    `drift adds ${Math.round(plan.projectedMaxSeconds-established.projectedMaxSeconds)}s to the busiest shard`)
})

test('AEON-1451 the full web layout and the essential gate stay inside the shard budget',()=>{
  const policy=loadWebPolicy()
  const browser=rows=>rows.filter(row=>row.kind==='browser')
  const full=webShardPlan(browser(fullGateTests(replayRows)),policy,{mode:'full'})
  assert.equal(full.timeoutSeconds,1200)
  assert.equal(full.budgetSeconds,900)
  assert.equal(full.shards.length,fullWebShardCount)
  assert.deepEqual(full.shards,fullWebShards)
  assertWebShardBudget(full)
  for(const bin of full.projections) assert.ok(bin.seconds<=full.budgetSeconds,`shard ${bin.shard} predicts ${bin.seconds}s`)
  const previous=webShardPlan(browser(fullGateTests(replayRows)),policy,{mode:'full',maximum:12})
  assert.equal(previous.fitsBudget,false)
  assert.throws(()=>assertWebShardBudget(previous),/over the 900s budget/)
  const essential=webShardPlan(browser(replayRows.filter(row=>row.tier==='ESSENTIAL'||row.tier==='GATED-FULL')),policy)
  assertWebShardBudget(essential)
  assert.ok(essential.shards.length>=4&&essential.shards.length<=fullWebShardCount)
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  const planner=ci.slice(ci.indexOf('\n  tier-plan:'),ci.indexOf('\n  runner-route:'))
  assert.equal(planner.split(fullWebShardJSON).length-1,3,'merge-queue and non-PR planners publish the fitting shard list')
  const shardJob=ci.slice(ci.indexOf('\n  web-shard:'),ci.indexOf('\n  web:'))
  const spaced=`[${fullWebShards.join(', ')}]`
  assert.match(shardJob,/timeout-minutes: 20\n/)
  assert.match(shardJob,/PW_RETRIES: '0'/)
  assert.equal(shardJob.includes(`github.event_name == 'pull_request' && needs.tier-plan.outputs.web_shards || '${spaced}'`),true)
  assert.doesNotMatch(shardJob,/continue-on-error|max-attempts/)
  assert.deepEqual(requiredJobs.filter(name=>name.startsWith('web-shard (')),fullWebShards.map(shard=>`web-shard (${shard})`))
})

test('AEON-1453 the merged full web catalogue needs seventeen shards to stay inside the budget',()=>{
  // Merge-queue head 688f05b6a packed the full gate into sixteen shards and
  // left shard 14 at 900.2s. Whole files are larger than the leftover gaps, so
  // the same catalogue needs one more shard. Sixteen must stay over budget.
  // AEON-1388's four essential group-figures cases leave shard 3 at 902.0s.
  const policy=loadWebPolicy()
  const gate=fullGateTests(replayRows).filter(row=>row.kind==='browser')
  const sixteen=webShardPlan(gate,policy,{mode:'full',maximum:16})
  assert.equal(sixteen.shards.length,16)
  assert.equal(sixteen.fitsBudget,false)
  assert.throws(()=>assertWebShardBudget(sixteen),/Web shard 3 predicts 902\.0s, over the 900s budget/)
  const full=webShardPlan(gate,policy,{mode:'full'})
  assert.equal(fullWebShardCount,17)
  assert.deepEqual(full.shards,fullWebShards)
  assertWebShardBudget(full)
  for(const bin of full.projections) assert.ok(bin.seconds<=full.budgetSeconds,`shard ${bin.shard} predicts ${bin.seconds}s`)
  assert.equal(full.projections.reduce((sum,bin)=>sum+bin.cases,0),gate.length)
})

test('AEON-1334 a recorded drift title resolves across describe prefixes and a renamed one widens to its file',()=>{
  const [owner,names]=[...driftCases].find(([owner])=>owner.startsWith('browser:'))
  const file=owner.slice('browser:'.length)
  const row=(name,tier='NIGHTLY')=>({kind:'browser',file,name,tier,project:'',line:1})
  const exact=row(names[0]),described=row(`outer › ${names[0]}`),sibling=row('an unrecorded sibling'),essential=row('an essential neighbour','ESSENTIAL')
  const rest=names.slice(1).map(name=>row(name))
  assert.deepEqual(fullGateTests([exact,sibling,essential,...rest]).map(key).sort(),[exact,essential,...rest].map(key).sort())
  assert.deepEqual(fullGateTests([described,sibling,...rest]).map(key).sort(),[described,...rest].map(key).sort())
  const renamed=row(`${names[0]} (renamed)`)
  assert.deepEqual(fullGateTests([renamed,sibling,...rest]).map(key).sort(),[renamed,sibling,...rest].map(key).sort(),'an unresolved recorded title runs its whole file')
  assert.deepEqual(fullGateTests([{...sibling,file:'tests/elsewhere.spec.ts'}]),[],'a nightly case outside the drift files stays deferred')
})

test('AEON-1431 a planner diff runs the nightly planning regression and a workflow diff still defers cases outside the drift files',()=>{
  const name='native planning regression reuses one collected snapshot for unchanged-tree CLI checks'
  const row=replayRows.find(item=>item.kind==='node'&&item.file==='tests/test-tier-failures.test.ts'&&item.name===name)
  assert.equal(row?.tier,'NIGHTLY')
  const siblings=replayRows.filter(item=>item.file===row.file&&key(item)!==key(row))
  assert.ok(siblings.length>0)
  for(const path of ['scripts/test-tiers/core.mjs','scripts/test-tiers/drift-obligations.json','web/tests/test-tier-failures.test.ts','web/scripts/aeon-681-ci.test.mjs']) {
    const selected=on([path])
    assert.ok(selected.tests.some(item=>key(item)===key(row)),path)
    if(path.startsWith('scripts/test-tiers/')) assert.ok(siblings.every(item=>!selected.tests.some(picked=>key(picked)===key(item))),path)
  }
  const workflow=on(['.github/workflows/ci.yml'])
  assert.equal(workflow.full,true)
  assert.equal(workflow.scope,'gated-full')
  assert.ok(!workflow.tests.some(item=>key(item)===key(row)))
  const drift=new Set(JSON.parse(readFileSync(new URL('./drift-obligations.json',import.meta.url),'utf8')).browserFiles)
  const deferred=replayRows.filter(item=>item.kind==='browser'&&item.tier==='NIGHTLY'&&!drift.has(item.file))
  assert.ok(deferred.length>0)
  const gated=new Set(workflow.tests.map(key))
  assert.ok(deferred.every(item=>!gated.has(key(item))))
  assert.ok(workflow.tests.some(item=>item.kind==='browser'&&item.tier==='NIGHTLY'&&drift.has(item.file)))
})

test('layout is static only when every path is docs, manifest, audit or always-on test material',()=>{
  for(const [paths,layout] of [[['scripts/ci/web-test-tiers.json','README.md'],'static'],[['scripts/audit/slices.json','scripts/check-migrations.test.mjs'],'full'],
    [['scripts/ci/web-test-tiers.json','web/src/lib/rowStore.ts'],'full'],[['scripts/audit/common.py','internal/auth/key.go'],'full'],[['AGENTS.md','api/openapi.yaml'],'full']]) {
    const decision=decide(paths)
    assert.equal(decision.mode==='full'?'full':decision.layout,layout,paths.join())
  }
})

test('R3 real helpers retain essentials and select every mapped importing case, including transitive units',()=>{
  const helpers=[...new Set(replay.prs.flatMap(pr=>pr.paths).filter(path=>path.startsWith('web/tests/')&&uncertain(path)))]
  let narrowed=0
  for(const path of helpers) {
    const rule=affectedRisk(path,replayGraph)
    const selected=on([path])
    if(rule.full || browserDataInput(path)) {
      assert.equal(selected.full,true,path)
      assert.deepEqual(selected.tests,fullGateTests(replayRows))
      continue
    }
    narrowed++
    assert.equal(selected.full,false,path)
    assert.match(selected.reason,/R3: helper reverse dependants/)
    const impacted=legacy.reverseDependants(new Set([path.slice(4)]),replayGraph)
    assert.deepEqual(selected.tests,replayRows.filter(row=>row.tier==='ESSENTIAL'||row.kind!=='go'&&impacted.has(row.file)))
  }
  assert.ok(narrowed>0,'The real data must exercise narrowing')
  const graph={'tests/helpers/shared.ts':[],'tests/helpers/reexport.ts':['tests/helpers/shared.ts'],
    'tests/feature.unit.test.ts':['tests/helpers/reexport.ts'],'tests/feature.spec.ts':['tests/helpers/reexport.ts']}
  const rows=[...cases,w('tests/feature.unit.test.ts','unit'),{...w('tests/feature.spec.ts','browser'),kind:'browser'}]
  const result=select(rows,{event:'pull_request',paths:['web/tests/helpers/shared.ts'],affectedLane:'on',webImports:graph})
  assert.deepEqual(result.tests,rows.filter(row=>row.tier==='ESSENTIAL'||row.name==='unit'||row.name==='browser'))
})

test('R3 fails closed on unknown, deleted, unimported and over-15-spec helpers',()=>{
  const path='web/tests/helpers/shared.ts',file=path.slice(4)
  for(const graph of [{},{[file]:[]}]) {
    assert.equal(select(cases,{event:'pull_request',paths:[path],affectedLane:'on',webImports:graph}).full,true)
    assert.equal(schedulingMode('pull_request',[path],()=>true,{affectedLane:'on',graph}),'full')
  }
  for(const n of [15,16]) {
    const rows=Array.from({length:n},(_,i)=>({...w(`tests/spec-${i}.spec.ts`,`${i}`),kind:'browser'}))
    const graph={[file]:[],...Object.fromEntries(rows.map(row=>[row.file,[file]]))}
    const result=select(rows,{event:'pull_request',paths:[path],affectedLane:'on',webImports:graph})
    assert.equal(result.full,n>15)
    assert.equal(schedulingMode('pull_request',[path],()=>true,{affectedLane:'on',graph}),n>15?'full':'essential')
    assert.equal(schedulingMode('pull_request',[path],()=>false,{affectedLane:'on',graph}),'full')
    if(n>15)assert.match(result.reason,/exceed 15 specs \(16\)/)
  }
})

test('trusted tier planner uses base classifier and base graph despite candidate rewrites',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-trusted-plan-test-'))
  const home=mkdtempSync(resolve(tmpdir(),'aeon-trusted-plan-home-'))
  const env={PATH:process.env.PATH,HOME:home,XDG_CONFIG_HOME:home,GIT_CONFIG_NOSYSTEM:'1',GIT_CONFIG_GLOBAL:'/dev/null',
    GIT_AUTHOR_NAME:'Fixture',GIT_AUTHOR_EMAIL:'fixture@example.invalid',GIT_COMMITTER_NAME:'Fixture',GIT_COMMITTER_EMAIL:'fixture@example.invalid',
    GITHUB_EVENT_NAME:'pull_request',CI_AFFECTED_LANE:'on'}
  const git=args=>execFileSync('git',args,{cwd:directory,env,encoding:'utf8'})
  const file=(path,source)=>{mkdirSync(resolve(directory,path,'..'),{recursive:true});writeFileSync(resolve(directory,path),source)}
  git(['init','-q'])
  for(const name of ['core.mjs','diff.mjs','collect.mjs','listing-cache.mjs','inputs.mjs','browser-impact.mjs','migration.mjs','manifests.mjs','failures.mjs'])file(`scripts/test-tiers/${name}`,readFileSync(new URL(name,import.meta.url),'utf8'))
  for(const name of ['ci-pr-plan.mjs','playwright-safe.mjs','playwright-global-setup.mjs','playwright-lock.mjs','playwright-processes.mjs'])file(`scripts/${name}`,readFileSync(new URL(`../${name}`,import.meta.url),'utf8'))
  file('web/scripts/ci-web-shard.mjs',readFileSync(new URL('../../web/scripts/ci-web-shard.mjs',import.meta.url),'utf8'))
  file('web/scripts/ci-web-shard-source.mjs',readFileSync(new URL('../../web/scripts/ci-web-shard-source.mjs',import.meta.url),'utf8'))
  file('scripts/test-tiers/drift-obligations.json',readFileSync(new URL('./drift-obligations.json',import.meta.url)))
  const tierFixture={version:1,tests:[{kind:'vitest',file:'tests/promoted.unit.test.ts',name:'n',tier:'NIGHTLY'},
    {kind:'browser',file:'tests/promoted.spec.ts',name:'n',tier:'NIGHTLY',config:'playwright.ui.config.ts',project:''}]}
  file('scripts/ci/web-test-tiers.json',JSON.stringify(tierFixture))
  file('scripts/ci/go-test-tiers.json',JSON.stringify({version:1,tests:[g('internal/fixture','TestFixture','NIGHTLY')]}))
  file('web/ci-web-shards.json',JSON.stringify({version:1,exclusions:[],groups:[{id:'ui',config:'playwright.ui.config.ts',project:null,flags:[],env:{},hostedOnly:true,specs:[{file:'tests/promoted.spec.ts',weightSeconds:1}]}]}))
  file('web/src/unused.ts','export {}')
  file('web/e2e/unused.ts','export {}')
  file('web/tests/helpers/shared.ts','export const value=1')
  file('web/tests/importer.spec.ts','import "./helpers/shared"')
  git(['add','.']);git(['commit','-qm','trusted fixture'])
  const base=git(['rev-parse','HEAD']).trim()
  // The candidate classifier lies, and candidate graph gains 16 importers.
  file('scripts/test-tiers/core.mjs','throw new Error("candidate classifier executed")')
  for(let i=0;i<16;i++)file(`web/tests/extra-${i}.spec.ts`,'import "./helpers/shared"')
  const run=paths=>trustedSchedulingDecision(base,paths,env,{cwd:directory})
  // Promotions compare the base snapshot's manifest with the candidate's.
  file('scripts/ci/web-test-tiers.json',JSON.stringify({...tierFixture,tests:tierFixture.tests.map(row=>({...row,tier:row.kind==='vitest'?'GATED-FULL':row.tier}))}))
  let manifest=run(['scripts/ci/web-test-tiers.json'])
  assert.deepEqual([manifest.mode,manifest.layout],['essential','static'])
  assert.match(manifest.reason,/R1: 1 promoted vitest cases execute/)
  file('scripts/ci/web-test-tiers.json',JSON.stringify({...tierFixture,tests:tierFixture.tests.map(row=>({...row,tier:row.kind==='browser'?'ESSENTIAL':row.tier}))}))
  manifest=run(['scripts/ci/web-test-tiers.json'])
  assert.deepEqual([manifest.mode,manifest.layout],['essential','full'])
  file('scripts/ci/web-test-tiers.json',JSON.stringify(tierFixture))
  assert.deepEqual(run(['scripts/ci/web-test-tiers.json','README.md']),{mode:'essential',layout:'static',reason:'R1: semantic registry checks: scripts/ci/web-test-tiers.json; R1: timing-only or semantically unchanged registry: scripts/ci/web-test-tiers.json'})
  assert.equal(run(['web/tests/helpers/shared.ts']).mode,'essential')
  assert.equal(run(['web/tests/helpers/shared.ts']).layout,'full')
  assert.match(run(['web/tests/helpers/shared.ts']).reason,/R3:.*1 specs/)
  assert.equal(run(['scripts/test-tiers/core.mjs','web/tests/helpers/shared.ts']).mode,'full')
  assert.equal(run(['web/tests/extra-0.spec.ts','web/tests/helpers/shared.ts']).mode,'full','new candidate graph nodes must not narrow themselves')
  file('web/tests/new-feature.unit.test.ts','export {}')
  const paths=['web/tests/new-feature.unit.test.ts']
  const trusted=run(paths)
  const candidateGraph=webGraph(resolve(directory,'web'))
  const candidate=schedulingDecision('pull_request',paths,()=>true,{affectedLane:'on',graph:candidateGraph,checkout:directory})
  assert.equal(trusted.mode,'full','the base graph has no new file')
  assert.equal(candidate.mode,'essential','the candidate graph can map its new file')
  const rows=[w('tests/core.test.ts','core','ESSENTIAL'),{kind:'vitest',file:'tests/new-feature.unit.test.ts',name:'new feature',tier:'GATED-FULL'}]
  const options={event:'pull_request',paths,affectedLane:'on',webImports:candidateGraph,tree:sourceTree(directory)}
  const selected=runnerSelection(rows,options,candidate,trusted)
  assert.equal(selected.full,true)
  assert.equal(selected.scope,'gated-full')
  assert.deepEqual(selected.tests,rows)
  assert.equal(checkFull({...reportCases(rows,rows.map(row=>({key:key(row),status:'passed',started:true})),1,'web-unit-1'),
    full:selected.full,scope:selected.scope,exitCode:0,runId:'1',attempt:'1',sha:'a'},
    {GITHUB_RUN_ID:'1',GITHUB_RUN_ATTEMPT:'1',GITHUB_SHA:'a'}),true)
  // A newly added browser spec is raw spec-only, but absent from the base
  // graph. The effective lane must reach tier inventory/full checks, rather
  // than bypassing them through the old exact-spec branch.
  const browserPath='web/tests/new-feature.spec.ts'
  file(browserPath,'export {}')
  const browserTrusted=run([browserPath])
  const browserGraph=webGraph(resolve(directory,'web'))
  const browserCandidate=schedulingDecision('pull_request',[browserPath],()=>true,{affectedLane:'on',graph:browserGraph,checkout:directory})
  assert.equal(browserTrusted.mode,'full')
  assert.equal(browserCandidate.mode,'essential')
  assert.equal(effectiveLane('spec-only',{...browserTrusted,event:'pull_request',affectedLane:'on'}),'full')
  assert.equal(effectiveLane('spec-only',{...browserTrusted,event:'pull_request',affectedLane:'off'}),'spec-only')
  const browserRows=[rows[0],{kind:'browser',file:browserPath.slice(4),name:'new feature',tier:'GATED-FULL'}]
  const browserSelected=runnerSelection(browserRows,{...options,paths:[browserPath],webImports:browserGraph},browserCandidate,browserTrusted)
  assert.equal(browserSelected.full,true)
  assert.deepEqual(browserSelected.tests,browserRows)
  const deleted=['web/tests/deleted.unit.test.ts']
  const deletedPlan=run(deleted)
  assert.equal(deletedPlan.mode,'full')
  assert.equal(runnerSelection(rows,{...options,paths:deleted},candidate,deletedPlan).full,true)
  const staticPaths=['scripts/audit/check.py']
  const staticPlan=run(staticPaths)
  const staticCandidate=schedulingDecision('pull_request',staticPaths,()=>true,{affectedLane:'on',checkout:directory})
  const staticSelection=runnerSelection(rows,{...options,paths:staticPaths},staticCandidate,staticPlan)
  assert.equal(staticSelection.full,false)
  assert.equal(staticSelection.layout,'static')
  assert.deepEqual(staticSelection.tests,[rows[0]])
  // A missing/old classifier is represented by the planner's explicit full
  // fallback. The runner must retain it even with candidate graph metadata.
  let fallback
  try { fallback=trustedSchedulingDecision('0'.repeat(40),paths,env,{cwd:directory}) }
  catch { fallback={mode:'full',layout:'full',reason:'trusted affected classification unavailable'} }
  assert.equal(fallback.reason,'trusted affected classification unavailable')
  assert.equal(runnerSelection(rows,options,candidate,fallback).full,true)
  assert.throws(()=>run(['x'.repeat(4097)]),/Invalid trusted paths/)
  assert.throws(()=>trustedSchedulingDecision('--bad',[],env,{cwd:directory}),/Invalid trusted planner base/)
})

test('runner modes honour planner input and candidate disagreement only widens',()=>{
  const candidate={mode:'essential',layout:'full',reason:'candidate mapping'}
  const staticCandidate={...candidate,layout:'static'}
  assert.equal(runnerDecision(candidate,{mode:'full'}).mode,'full')
  for(const mode of ['essential','spec-only'])assert.deepEqual(runnerDecision(candidate,{mode}),candidate)
  assert.deepEqual(runnerDecision(staticCandidate,{mode:'static'}),staticCandidate)
  assert.deepEqual(runnerDecision(staticCandidate,{mode:'essential',layout:'static'}),staticCandidate)
  assert.equal(runnerDecision(candidate,{mode:'static'}).mode,'full')
  assert.equal(runnerDecision(staticCandidate,{mode:'essential',layout:'full'}).mode,'full')
  for(const mode of ['essential','static','spec-only'])assert.equal(runnerDecision({mode:'full',layout:'full',reason:'candidate uncertainty'},{mode}).mode,'full')
  for(const mode of ['',null,'FULL','bogus'])assert.equal(runnerDecision(candidate,{mode}).mode,'full')
  assert.equal(runnerDecision(candidate,{mode:'essential',layout:'bogus'}).mode,'full')
  assert.deepEqual(runnerDecision(candidate),candidate)
})

test('merge-group planner writes full without needing payload, checkout history or a network fetch',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-merge-full-'))
  for(const affectedLane of ['','off','on']) {
    const output=resolve(directory,`output-${affectedLane}`)
    assert.equal(tierPlanMain({GITHUB_EVENT_NAME:'merge_group',CI_AFFECTED_LANE:affectedLane,GITHUB_OUTPUT:output}),0)
    assert.equal(readFileSync(output,'utf8'),`mode=full\nlayout=full\nlane=full\nweb_shards=${fullWebShardJSON}\n`)
    for(const raw of ['full','spec-only','docs-only']) {
      writeFileSync(output,'')
      tierPlanMain({GITHUB_EVENT_NAME:'merge_group',CI_PLAN_LANE:raw,CI_AFFECTED_LANE:affectedLane,GITHUB_OUTPUT:output})
      assert.equal(readFileSync(output,'utf8'),`mode=full\nlayout=full\nlane=${affectedLane==='on'?'full':raw}\nweb_shards=${fullWebShardJSON}\n`)
    }
  }
})

test('impact bounds reject malformed and oversized paths before building a graph or snapshot',()=>{
  for(const paths of [[null],['../web/tests/helper.ts'],['web//tests/file.ts'],['/absolute'],['web/tests/a\nb.ts'],
    ['web/tests/'+ 'x'.repeat(4097)],Array.from({length:10001},()=> 'README.md')]) {
    const options={event:'pull_request',paths,affectedLane:'on'}
    assert.equal(select(cases,options).reason,'invalid or oversized impact diff')
    assert.deepEqual(schedulingDecision('pull_request',paths),{mode:'full',layout:'full',reason:'invalid or oversized impact diff'})
    assert.throws(()=>trustedSchedulingDecision('a'.repeat(40),paths),/Invalid trusted paths/)
  }
})

test('offline replay CLI prints every real PR with selected counts, effective lane and reasons',()=>{
  const result=spawnSync(process.execPath,[fileURLToPath(new URL('./replay.mjs',import.meta.url)),'--json'],{encoding:'utf8',timeout:30_000})
  assert.equal(result.status,0,result.stderr)
  const report=JSON.parse(result.stdout)
  assert.equal(report.replay.length,80)
  assert.deepEqual(report.correctness,{recordedFailureCases:1,misses:0})
  assert.ok(report.historicalFailures.every(row=>row.selected))
  // Measured on the current tree (consumers, migrations); deleted files replay as full.
  // The merge keeps PR 200 and PR 245 over the 1000-case Go consumer bound (openapi fan-out).
  // AEON-879 retires the dialog files: historical PR 208 now safely widens
  // because those deleted paths have no dependency metadata in this tree.
  assert.deepEqual(report.transitions,{'full->full':66,'essential->full':10,'essential->essential':4})
  assert.equal(report.summary.newNarrowed,4)
  assert.equal(report.summary.oldEssential,14)
  assert.ok(report.summary.fullReasons['CI machinery']>=15)
  for(const row of report.replay) {
    assert.ok(row.oldCases>0&&row.newCases>0)
    assert.ok(row.reason.length>0)
    assert.ok(['full','spec-only','docs-only'].includes(row.lane))
    assert.ok(['full','essential','static'].includes(row.new))
    assert.equal(row.newCases,Object.values(row.newKinds).reduce((sum,n)=>sum+n,0))
    assert.ok(row.new==='full'||row.newCases<=row.oldCases,`PR ${row.number} narrowed selection must not grow`)
    assert.equal(row.newJobs,row.lane==='spec-only'?12:row.new==='static'?16:row.new==='essential'?24:42)
  }
})

test('AEON-697 Go attention regressions are classified and permission and revision guards remain essential',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const discovered=[]
  for(const file of ['internal/db/attention_events_migration_test.go',
    'internal/releases/node_undo_integration_test.go','internal/statusautopilot/attention_test.go']) {
    const source=readFileSync(new URL(`../../${file}`,import.meta.url),'utf8')
    const names=[...source.matchAll(/^func (Test\w+)\(t \*testing\.T\)/gm)].map(match=>match[1])
    assert.ok(names.length>0,`${file} must contain regression cases`)
    for(const name of names)discovered.push({kind:'go',package:file.slice(0,file.lastIndexOf('/')),name})
  }
  const ids=new Set(discovered.map(key)),warnings=[]
  const stored={...manifest,tests:manifest.tests.filter(row=>ids.has(key(row)))}
  assert.deepEqual(stored.tests.map(key).sort(),discovered.map(key).sort(),'Every Go attention regression needs an explicit tier entry')
  const rows=validate(stored,discovered,undefined,
    {strict:true,warn:warning=>warnings.push(warning)})
  assert.deepEqual(warnings,[])
  assert.ok(rows.every(row=>row.tier==='ESSENTIAL'||row.tier==='GATED-FULL'))
  for(const event of ['pull_request','merge_group']) {
    const core=select(rows,{event,paths:['README.md']}).tests
    for(const name of ['TestAttentionChecksRevocationUnderFinalWriteFence','TestAdditionalAttentionFencePreservesAuthenticationGuard',
      'TestAttentionResolutionVisibilityPreservesHiddenReferences','TestAttentionUndoRequiresPermissionAndActorOwnership',
      'TestAttentionReleaseUndoRejectsExternalProjectAndReleaseChanges','TestAttentionBulkReturnsPartialResultsAndGuardsUndo']) {
      assert.ok(core.some(row=>row.name===name),`${event} must retain ${name}`)
    }
    assert.deepEqual(select(rows,{event,forceFull:true}).tests.map(key).sort(),rows.map(key).sort())
  }
})

test('AEON-648 pending writes and identity fences stay protected without promoting ordinary display tests',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  for(const [file,names] of [
    ['tests/parent-benefit-generation.unit.test.ts',[
      'retry captures its generation/revision and discards a result after navigation',
      'tenant changes fence an old status read even for the same node id',
      'an old provider save cannot restore another person’s opt-in or drafts',
    ]],
    ['tests/work-vocabulary-card.unit.test.ts',['serializes a deferred Reload with Save and leaves the saved revision visible']],
  ]) for(const name of names) {
    const row=manifest.tests.find(row=>row.file===file&&row.name===name)
    assert.equal(row?.tier,'ESSENTIAL',`${file}: ${name}`)
    for(const event of ['pull_request','merge_group'])
      assert.deepEqual(select([row],{event,paths:['README.md']}).tests,[row])
  }
  const display=manifest.tests.find(row=>row.file==='tests/work-benefit-visibility.unit.test.ts'&&row.name==='work offers its benefit reading section in the workspace')
  assert.equal(display?.tier,'NIGHTLY')
})

test('runtime reconciliation defaults new cases to NIGHTLY and drops stale cases with named warnings',()=>{
  const stale=cases[0],added={kind:'go',package:'internal/auth',name:'TestNew',active:false}
  const discovered=[...cases.slice(1),added],warnings=[],manifest=fixture(),before=structuredClone(manifest)
  const rows=validate(manifest,discovered,noFlaky,{warn:message=>warnings.push(message)})
  assert.deepEqual(rows.map(key),discovered.map(key))
  assert.deepEqual(rows.at(-1),{...added,tier:'NIGHTLY'})
  assert.equal(rows.find(row=>row.name==='TestCRUD').tier,'ESSENTIAL')
  assert.deepEqual(warnings,[`::warning::Unclassified test (defaults to NIGHTLY; classify these): ${key(added)}`,
    `::warning::Stale manifest entry (dropped): ${key(stale)}`])
  for(const event of ['pull_request']) {
    assert.ok(select(rows,{event,paths:['internal/auth/key.go']}).tests.some(row=>key(row)===key(added)))
    assert.ok(!select(rows,{event,paths:['README.md']}).tests.some(row=>key(row)===key(added)))
  }
  assert.deepEqual(select(rows,{event:'schedule',paths:[]}).tests,rows)
  assert.deepEqual(manifest,before, 'Reconciliation must leave the stored manifest unchanged')
})

test('runtime reconciliation escapes warning titles and retains browser configuration validation',()=>{
  const row={kind:'browser',file:'tests/new.spec.ts',name:'new\ncase%',config:'playwright.ui.config.ts',project:'',line:12,id:'native'}
  const warnings=[]
  const rows=validate(fixture(),[...cases,row],noFlaky,{warn:message=>warnings.push(message)})
  assert.deepEqual(rows.at(-1),{...row,tier:'NIGHTLY',active:true})
  assert.deepEqual(warnings,[`::warning::Unclassified test (defaults to NIGHTLY; classify these): ${key(row).replaceAll('%','%25').replaceAll('\n','%0A')}`])
  for(const strict of [false,true]) assert.throws(()=>validate({version:1,tests:[{...row,config:'old.config.ts',tier:'ESSENTIAL'}]},[row],noFlaky,{strict}),/Changed browser configuration/)
})

test('reconciliation never hides malformed, duplicate or known-flaky ESSENTIAL classifications',()=>{
  for(const strict of [false,true]) {
    assert.throws(()=>validate({...fixture(),tests:[{...cases[0],tier:'UNKNOWN'}]},[],noFlaky,{strict}),/Invalid classification/)
    assert.throws(()=>validate({...fixture(),tests:[cases[0],cases[0]]},[],noFlaky,{strict}),/Duplicate manifest entry/)
    assert.throws(()=>validate(fixture(),[cases[0],cases[0]],noFlaky,{strict}),/Duplicate collected identity/)
    assert.throws(()=>validate(fixture(),[],{version:1,entries:[{key:key(cases[0]),owner:'AEON-675'}]},{strict}),/Known-flaky case cannot be ESSENTIAL/)
  }
})

test('strict checks reject unknown and stale registrations; tagging never removes a nightly test',()=>{
  assert.throws(()=>validate(fixture(),[...cases,g('internal/auth','TestNew')],noFlaky,{strict:true}),/Unclassified test.*TestNew/)
  assert.throws(()=>validate(fixture(),cases.slice(1),noFlaky,{strict:true}),/Stale manifest entry.*TestCeiling/)
  const manifest=fixture();manifest.tests[1].tags=['delete-candidate']
  const rows=validate(manifest,cases)
  const full=select(rows,{event:'schedule',paths:[]})
  assert.equal(full.tests.length,cases.length)
  assert.ok(full.tests.find(row=>row.name==='TestRotation').tags.includes('delete-candidate'))
  manifest.tests[0].tags=['delete-candidate']
  assert.throws(()=>validate(manifest,cases),/Invalid classification/)
})

test('known-flaky cases cannot enter ESSENTIAL, but remain in changed-area and nightly execution',()=>{
  for (const row of [g('internal/importer','TestSourceRequestCapAndDelay'),
    {kind:'browser',file:'tests/clip-tip.spec.ts',name:'touch disclosure',config:'playwright.ui.config.ts',project:'',tier:'NIGHTLY'}]) {
    const manifest={version:1,tests:[{...row,tier:'ESSENTIAL'}]}
    const known={version:1,entries:[{key:key(row),owner:row.kind==='go'?'AEON-675':'AEON-676'}]}
    assert.throws(()=>validate(manifest,[row],known),error=>
      error.message===`Known-flaky case cannot be ESSENTIAL: ${key(row)} (${known.entries[0].owner})`)
    manifest.tests[0].tier='NIGHTLY'
    const retained=validate(manifest,[row],known)
    assert.deepEqual(select(retained,{event:'pull_request',paths:['README.md']}).tests,[])
    assert.deepEqual(select(retained,{event:'schedule',paths:[]}).tests,retained)
    const paths=row.kind==='go'?['internal/importer/source.go']:[`web/${row.file}`]
    assert.deepEqual(select(retained,{event:'pull_request',paths,webImports:{[row.file]:[]}}).tests,retained)
    assert.deepEqual(select(retained,{event:'merge_group',paths,webImports:{[row.file]:[]}}).tests,[])
  }
})

test('known-flaky registry fails closed on missing ownership and duplicate case keys',()=>{
  for(const entry of [{key:key(cases[0])},{key:'',owner:'AEON-675'},{key:key(cases[0]),owner:'675'}])
    assert.throws(()=>validate(fixture(),cases,{version:1,entries:[entry]}),/case key and owner ticket/)
  const entry={key:key(cases[1]),owner:'AEON-675'}
  assert.throws(()=>validate(fixture(),cases,{version:1,entries:[entry,entry]}),/Duplicate known-flaky entry/)
  assert.throws(()=>validate(fixture(),cases,{version:2,entries:[]}),/registry version 1/)
})

test('known-flaky committed cases retain exact owners and cannot be promoted by the CLI default validator',()=>{
  const known=JSON.parse(readFileSync(new URL('../ci/known-flaky.json',import.meta.url)))
  const manifests=['go','web'].map(kind=>JSON.parse(readFileSync(new URL(`../ci/${kind}-test-tiers.json`,import.meta.url))))
  assert.equal(known.entries.length,9)
  assert.deepEqual(Object.fromEntries(['AEON-675','AEON-676','AEON-683'].map(owner=>
    [owner,known.entries.filter(entry=>entry.owner===owner).length])),{'AEON-675':1,'AEON-676':1,'AEON-683':7})
  for(const entry of known.entries) {
    const manifest=manifests.find(manifest=>manifest.tests.some(row=>key(row)===entry.key))
    assert.ok(manifest,`Stale known-flaky case: ${entry.key}`)
    const row=manifest.tests.find(row=>key(row)===entry.key)
    assert.equal(row.tier,'GATED-FULL',entry.key)
    assert.ok(select(manifest.tests,{event:'schedule',paths:[]}).tests.some(row=>key(row)===entry.key))
    const promoted=structuredClone(manifest)
    promoted.tests.find(row=>key(row)===entry.key).tier='ESSENTIAL'
    assert.throws(()=>validate(promoted,manifest.tests),error=>
      error.message===`Known-flaky case cannot be ESSENTIAL: ${entry.key} (${entry.owner})`)
  }
})

test('PR takes essential union changed package and reverse dependencies; docs retain core',()=>{
  const imports={'cmd/aeon':['internal/auth'],'internal/auth':[]}
  for(const event of ['pull_request']) {
    const docs=select(cases,{event,paths:['README.md','docs/RELEASE.md'],imports})
    assert.equal(docs.full,false)
    assert.deepEqual(docs.tests.map(row=>row.name),['TestCeiling','TestCRUD','ceiling'])
    const picked=select(cases,{event,paths:['docs/RELEASE.md','internal/auth/key.go'],imports})
    assert.equal(picked.full,false)
    assert.deepEqual(picked.tests.map(row=>row.name),['TestCeiling','TestRotation','TestCRUD','TestRoutes','ceiling'])
  }
})

test('Go reverse-dependency expansion over the cap runs the entire gate',()=>{
  const many=[...cases,...Array.from({length:301},(_,i)=>g('internal/dependant',`TestExtra${i}`,'GATED-FULL'))]
  const selected=select(many,{event:'pull_request',paths:['internal/auth/key.go'],imports:{'internal/dependant':['internal/auth']}})
  assert.equal(selected.full,true)
  assert.deepEqual(selected.tests,many.filter(row=>['ESSENTIAL','GATED-FULL'].includes(row.tier)))
  assert.ok(selected.tests.some(row=>row.name==='TestCeiling'))
  assert.ok(selected.tests.some(row=>row.name==='TestCRUD'))
  assert.match(selected.reason,/Go reverse dependencies exceed 300/)
})

test('uncertain and missing metadata select full on every premerge event',()=>{
  for(const path of ['go.mod','go.sum','api/openapi.yaml','.github/workflows/ci.yml',
    'internal/db/migrations/9999.sql','internal/auth/testdata/token.json','web/tests/access-fixtures.ts',
    'web/vite.config.ts','web/package-lock.json','scripts/test-tiers/core.mjs','unexpected.input']) {
    for(const event of ['pull_request','merge_group']) assert.equal(select(cases,{event,paths:[path]}).full,true,path)
  }
  assert.equal(select(cases,{event:'merge_group'}).full,true)
  assert.equal(select(cases,{event:'pull_request',paths:['web/src/deleted.ts'],webImports:{}}).full,true)
})

test('fan-out chooses full shard counts for uncertain and deleted paths, small counts for mapped changes',()=>{
  for(const event of ['pull_request']) {
    assert.equal(schedulingMode(event,['README.md']), 'essential')
    assert.equal(schedulingMode(event,['internal/auth/flow.go'],()=>true,{tests:cases,imports:{'cmd/aeon':['internal/auth']}}),'essential')
    assert.equal(schedulingMode(event,['.github/workflows/ci.yml']), 'full')
    assert.equal(schedulingMode(event,['internal/removed/file.go'],()=>false),'full')
    assert.equal(schedulingMode(event,undefined),'full')
  }
  assert.equal(schedulingMode('schedule',['README.md']),'full')
  assert.equal(schedulingMode('merge_group',['README.md']),'full')
  assert.equal(schedulingMode('merge_group',['internal/auth/flow.go'],()=>true),'full')
})

test('uncertain infrastructure changes preserve the classified gate plus promoted essentials',()=>{
  const browser=(file,name,tier='NIGHTLY')=>({kind:'browser',file,name,tier})
  const rows=[...cases.map(row=>({...row,tier:row.tier==='NIGHTLY'?'GATED-FULL':row.tier})),browser('tests/gated.spec.ts','guard','GATED-FULL'),browser('tests/optional.spec.ts','core','ESSENTIAL'),
    browser('tests/optional.spec.ts','gallery')]
  for(const event of ['pull_request','merge_group','push']) {
    const picked=select(rows,{event,paths:['.github/workflows/ci.yml','web/package.json','web/playwright.ui.config.ts']})
    assert.equal(picked.full,true)
    assert.deepEqual(picked.tests,rows.slice(0,-1))
    assert.equal(picked.scope,'gated-full')
    assert.equal(picked.deferredBrowserCases,1)
  }
  for(const options of [{event:'schedule'},{event:'workflow_dispatch',forceAll:true},{event:'pull_request',forceAll:true}]) {
    const picked=select(rows,{...options,paths:['.github/workflows/ci.yml']})
    assert.deepEqual(picked.tests,rows)
    assert.equal(picked.scope,'catalogue')
    assert.equal(picked.deferredBrowserCases,0)
  }
})

test('optional changed browser cases run in changed-area selection; uncertain impact widens only to the gate',()=>{
  const rows=[{kind:'browser',file:'tests/optional.spec.ts',name:'race',tier:'NIGHTLY'},
    {kind:'browser',file:'tests/other.spec.ts',name:'other',tier:'GATED-FULL'}]
  const webImports={'src/store.ts':[],'tests/optional.spec.ts':['src/store.ts'],'tests/other.spec.ts':[]}
  for(const path of ['web/tests/optional.spec.ts','web/src/store.ts']) {
    const picked=select(rows,{event:'pull_request',paths:[path],webImports,...completeMap('src/store.ts',['tests/optional.spec.ts'])})
    assert.deepEqual(picked.tests,[rows[0]])
    assert.equal(picked.full,false)
  }
  for(const path of ['web/src/deleted.ts','web/src/Unmapped.vue']) {
    const picked=select(rows,{event:'pull_request',paths:[path],webImports:{...webImports,'src/Unmapped.vue':[]}})
    assert.deepEqual(picked.tests,[rows[1]],path)
    assert.equal(picked.deferredBrowserCases,1,path)
  }
})

test('lean planner fetches only the exact event base before diffing and widens on fetch failure',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-tier-planner-'))
  const eventPath=resolve(directory,'event.json'),base='a'.repeat(40)
  for(const event of ['pull_request','merge_group']) {
    writeFileSync(eventPath,JSON.stringify(event==='pull_request'?{pull_request:{base:{sha:base}}}:{merge_group:{base_sha:base}}))
    const calls=[]
    const exec=(bin,args,options)=>{calls.push({bin,args,options});return args[0]==='diff'?'README.md\0internal/auth/key.go\0':''}
    assert.deepEqual(changedPaths(event,{GITHUB_EVENT_PATH:eventPath},{fetchBase:true,exec}),['README.md','internal/auth/key.go'])
    assert.deepEqual(calls,[
      {bin:'git',args:['fetch','--no-tags','--depth=1','origin',base],options:{timeout:30_000}},
      {bin:'git',args:['diff','--name-only','-z','--no-renames',base,'HEAD'],options:{timeout:30_000}}])
    const failed=changedPaths(event,{GITHUB_EVENT_PATH:eventPath},{fetchBase:true,exec:()=>{throw new Error('fetch refused')}})
    assert.equal(failed,undefined)
    assert.equal(schedulingMode(event,failed),'full')
  }
  writeFileSync(eventPath,JSON.stringify({pull_request:{base:{sha:'--invalid'}}}))
  const invalidCalls=[]
  assert.equal(changedPaths('pull_request',{GITHUB_EVENT_PATH:eventPath},{fetchBase:true,exec:(...args)=>{invalidCalls.push(args);return ''}}),undefined)
  assert.deepEqual(invalidCalls,[], 'Invalid base must never be fetched')
})

test('lean tier-plan skips setup, deep checkout and non-premerge checkout work',()=>{
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  const planner=ci.slice(ci.indexOf('\n  tier-plan:'),ci.indexOf('\n  runner-route:'))
  assert.match(planner,/timeout-minutes: 2/)
  assert.match(planner,/fetch-depth: 1\n/)
  assert.doesNotMatch(planner,/setup-node|setup-go|npm ci|fetch-depth: 0/)
  assert.match(planner,/if: contains\(fromJSON\('\["pull_request","merge_group"\]'\), github.event_name\)/)
  assert.match(planner,/pull_request\) node scripts\/test-tiers\/diff.mjs/)
  assert.match(planner,new RegExp(`merge_group\\) printf 'mode=full\\\\nlayout=full\\\\nlane=full\\\\nweb_shards=\\[${fullWebShards.join(',')}]\\\\n' >> "\\$GITHUB_OUTPUT" ;;`))
  assert.match(planner,new RegExp(`printf 'mode=full\\\\nlayout=full\\\\nlane=%s\\\\nweb_shards=\\[${fullWebShards.join(',')}]\\\\n'`))
  assert.match(planner,/CI_PLAN_LANE: \$\{\{ needs.ci-plan.outputs.lane \}\}/)
  assert.match(planner,/^      layout: \$\{\{ steps.tiers.outputs.layout \}\}$/m)
})

test('workflow merge-group planner writes only full outputs without executing candidate code; PR output is unchanged',()=>{
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  const planner=ci.slice(ci.indexOf('\n  tier-plan:'),ci.indexOf('\n  runner-route:'))
  // The scheduling block ends at the next step. Shadow publication is a separate
  // step and must not be executed as part of this planner script.
  const run=planner.split('        run: |\n')[1].split('\n      - ')[0].replace(/^          /gm,'')
  assert.doesNotMatch(run,/impact-plan| - name:/)
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-workflow-tier-plan-'))
  mkdirSync(resolve(directory,'scripts/test-tiers'),{recursive:true})
  writeFileSync(resolve(directory,'scripts/test-tiers/diff.mjs'),`
    import {appendFileSync} from 'node:fs';
    appendFileSync(process.env.GITHUB_OUTPUT,'mode=essential\\nlayout=static\\nlane=full\\n');
    console.log('candidate planner executed');
    process.exit(Number(process.env.CANDIDATE_STATUS));
  `)
  for(const event of ['merge_group','pull_request']) for(const flag of ['on','off',''])
    for(const lane of ['full','spec-only','docs-only']) for(const code of ['0','7']) {
      const output=resolve(directory,`${event}-${flag}-${lane}-${code}`)
      writeFileSync(output,'')
      const result=spawnSync('/bin/bash',['-e','-c',run],{cwd:directory,encoding:'utf8',timeout:10_000,
        env:{PATH:process.env.PATH,GITHUB_EVENT_NAME:event,GITHUB_OUTPUT:output,CI_AFFECTED_LANE:flag,CI_PLAN_LANE:lane,CANDIDATE_STATUS:code}})
      assert.equal(result.status,event==='merge_group'?0:Number(code),result.stderr)
      assert.equal(readFileSync(output,'utf8'),event==='merge_group'?`mode=full\nlayout=full\nlane=full\nweb_shards=${fullWebShardJSON}\n`:'mode=essential\nlayout=static\nlane=full\n')
      assert.equal(result.stdout,event==='merge_group'?'':'candidate planner executed\n')
    }
})

test('static layout accounting expects skipped Go shards, timing and browser shards but still requires unit evidence',()=>{
  const finished={status:'completed',conclusion:'success',started_at:'2026-10-06T01:00:00Z',completed_at:'2026-10-06T01:02:00Z'}
  const skipped={status:'completed',conclusion:'skipped',started_at:'2026-10-06T01:00:00Z',completed_at:'0001-01-01T00:00:00Z',steps:[]}
  const jobs=[{...finished,name:'tier-plan'},{...finished,name:'web-setup'},{...finished,name:'web-unit (1)'},{...skipped,name:'go-test (1)'},{...skipped,name:'go-timing'},{...skipped,name:'web-shard (1)'},{...skipped,name:'e2e-run'}]
  const unit={version:1,job:'web-unit-1',classes:{ESSENTIAL:{selected:1,run:1,passed:1,skipped:0,failed:0,notRun:0,platformInactive:0}}}
  const report=aggregate([unit],jobs,{lane:'full',layout:'static'})
  assert.equal(report.coverage,'reported');assert.deepEqual(report.missingEvidence,[]);assert.deepEqual(report.skippedJobs,[])
  const withoutUnits=aggregate([],jobs,{lane:'full',layout:'static'})
  assert.equal(withoutUnits.coverage,'incomplete');assert.deepEqual(withoutUnits.missingEvidence,['web-unit-1'])
  const fullLayout=aggregate([unit],jobs,{lane:'full',layout:'full'})
  assert.equal(fullLayout.coverage,'incomplete');assert.deepEqual(fullLayout.skippedJobs,['go-test (1)','go-timing','web-shard (1)'])
  assert.throws(()=>aggregate([unit],jobs,{lane:'full',layout:'wide'}),/Invalid tier layout/)
})

test('changed modules select dependent specs/units; equal file basenames do not broaden impact',()=>{
  const graph={'src/ceiling.ts':[],'tests/scope.test.ts':['src/ceiling.ts'],'tests/tree.test.ts':[]}
  const selected=select(cases,{event:'pull_request',paths:['web/src/ceiling.ts'],webImports:graph})
  assert.deepEqual(selected.tests.filter(row=>row.kind==='node').map(row=>row.name),['ceiling','revoked'])
  assert.deepEqual(select(cases,{event:'pull_request',paths:['web/tests/tree.test.ts'],webImports:graph}).tests.filter(row=>row.kind==='node').map(row=>row.name),['ceiling','tree'])
})

test('a mapped Go change does not widen unrelated web tests; unmapped rendered components widen web',()=>{
  const goOnly=cases.filter(row=>row.kind==='go'),webOnly=cases.filter(row=>row.kind!=='go')
  assert.equal(select(webOnly,{event:'pull_request',paths:['internal/auth/flow.go']}).full,false)
  assert.deepEqual(select(webOnly,{event:'pull_request',paths:['internal/auth/flow.go']}).tests.map(row=>row.name),['ceiling'])
  assert.equal(select(goOnly,{event:'pull_request',paths:['web/src/ceiling.ts']}).full,false)
  assert.equal(select(webOnly,{event:'pull_request',paths:['web/src/Unmapped.vue'],webImports:{'src/Unmapped.vue':[]}}).full,true)
})

test('web dependency graph follows Vue, TS, export and index imports transitively',()=>{
  const root=mkdtempSync(resolve(tmpdir(),'aeon-tier-graph-'))
  for(const directory of ['src','src/lib','tests','e2e'])mkdirSync(resolve(root,directory),{recursive:true})
  writeFileSync(resolve(root,'src/Screen.vue'),'<template/><script setup>import { grant } from "./lib"</script>')
  writeFileSync(resolve(root,'src/lib/index.ts'),'export { grant } from "./grant"')
  writeFileSync(resolve(root,'src/lib/grant.ts'),'export const grant=true')
  writeFileSync(resolve(root,'tests/grant.spec.ts'),'import "../src/Screen.vue"')
  const graph=webGraph(root)
  assert.deepEqual(graph['src/Screen.vue'],['src/lib/index.ts'])
  const row={kind:'browser',file:'tests/grant.spec.ts',name:'grant',tier:'NIGHTLY'}
  const picked=select([row],{event:'pull_request',paths:['web/src/lib/grant.ts'],webImports:graph,...completeMap('src/lib/grant.ts',['tests/grant.spec.ts'])})
  assert.equal(picked.full,false)
  assert.deepEqual(picked.tests,[row])
})

test('two tier shards contain every selected identity exactly once, including equal printed titles',()=>{
  const duplicate=[w('tests/param.test.ts','undefined'),w('tests/param.test.ts','undefined')].map((row,i)=>({...row,occurrence:i+1}))
  const rows=[...cases,...duplicate],all=[...shard(rows,1,2),...shard(rows,2,2)]
  assert.deepEqual(all.map(key).sort(),rows.map(key).sort())
  assert.equal(new Set(all.map(key)).size,rows.length)
  const pattern=new RegExp(exactPattern(['test [1]','ends.$']))
  assert.ok(pattern.test('test [1]'));assert.ok(!pattern.test('test 1'));assert.ok(!pattern.test('other ends.$'))
})

for(const [kind,maxShards,options] of [['go',7,{}],['browser',12,{}],['node',4,{}],['node',4,{firstShardLast:true}]]) {
  test(`${kind}${options.firstShardLast?' (first shard last)':''} zero-weight owners fill shards before reusing tied bins for counts 1..${maxShards}`,()=>{
    for(let count=1;count<=maxShards;count++) {
      for(const ownerCount of new Set([0,1,Math.max(0,count-1),count,count+1,2*count+1])) {
        for(const distribution of ['mixed','all-zero','equal-positive']) {
          const owners=Array.from({length:ownerCount},(_,i)=>kind==='go'
            ? `internal/owner-${String(i).padStart(2,'0')}`
            : `tests/owner-${String(i).padStart(2,'0')}.${kind==='browser'?'spec':'test'}.ts`)
          // Different registration counts ensure the tie-break counts owners,
          // rather than splitting owners or comparing their number of tests.
          const rows=owners.flatMap((owner,i)=>Array.from({length:1+i%3},(_,j)=>kind==='go'
            ? g(owner,`TestCase${j}`) : {...w(owner,`case ${j}`),kind}))
          const weights=Object.fromEntries(owners.map((owner,i)=>[owner,
            distribution==='all-zero'?0:distribution==='mixed'?(i===0?1:0):1]))
          const before={rows:structuredClone(rows),weights:{...weights}}
          const bins=Array.from({length:count},(_,i)=>shard(rows,i+1,count,weights,options))
          const ownerOf=row=>kind==='go'?row.package:row.file
          const ownerBins=bins.map(bin=>[...new Set(bin.map(ownerOf))])
          const context=`${kind}${options.firstShardLast?' (first shard last)':''}: ${count} shards, ${ownerCount} owners, ${distribution}`
          assert.deepEqual(bins.flat().map(key).sort(),rows.map(key).sort(),context)
          assert.equal(new Set(bins.flat().map(key)).size,rows.length,context)
          assert.deepEqual(ownerBins.flat().sort(),owners,`${context}: owners stay whole`)
          assert.equal(ownerBins.filter(bin=>bin.length).length,Math.min(count,ownerCount),`${context}: empty shards only when owners are fewer`)
          for(let i=0;i<count;i++) {
            assert.deepEqual(shard([...rows].reverse(),i+1,count,weights,options).map(key).sort(),bins[i].map(key).sort(),`${context}: input order cannot change assignment`)
          }
          if(distribution==='all-zero'&&!options.firstShardLast) {
            // Equal weights and owner counts resolve by path then shard index.
            assert.deepEqual(ownerBins,Array.from({length:count},(_,i)=>owners.filter((_,j)=>j%count===i)),context)
          }
          assert.deepEqual({rows,weights},before,`${context}: inputs remain unchanged`)
        }
      }
    }
  })
}

test('fewer owners than shards retain successful empty-shard reports in Go, unit and browser callers',async()=>{
  const {run}=await import('./cli.mjs')
  const env={RUNNER_ENVIRONMENT:'github-hosted',GITHUB_RUN_ID:'empty-shard',GITHUB_RUN_ATTEMPT:'1',GITHUB_SHA:'a'.repeat(40)}
  for(const [kind,unit,count,row] of [['go',false,7,cases[0]],['web',true,4,cases[5]],
    ['web',false,12,{kind:'browser',file:'tests/only.spec.ts',name:'only',tier:'ESSENTIAL'}]]) {
    const owner=row.kind==='go'?row.package:row.file
    const tests=shard([row],count,count,{[owner]:0})
    assert.deepEqual(tests,[])
    const job=`ops257r3-empty-${kind}-${unit?'unit':'cases'}`
    assert.equal(await run(kind,{tests,all:[row],full:true,scope:'gated-full',reason:'fewer owners than shards'},
      {unit,job,env}),0)
    const report=JSON.parse(readFileSync(new URL(`../../tmp/test-tiers/${job}-measurement.json`,import.meta.url)))
    assert.equal(checkFull(report,env),true)
    assert.ok(Object.values(report.classes).every(tier=>tier.selected===0&&tier.run===0&&tier.passed===0&&tier.notRun===0&&tier.failed===0))
  }
})

test('reports distinguish missing, skipped and failed outcomes; nested Go subtests cannot inflate totals',()=>{
  const text=[{Package:'github.com/inspr-at/paimos/internal/auth',Test:'TestCeiling',Action:'run'},
    {Package:'github.com/inspr-at/paimos/internal/auth',Test:'TestCeiling/sub',Action:'pass'},
    {Package:'github.com/inspr-at/paimos/internal/auth',Test:'TestCeiling',Action:'pass'}].map(JSON.stringify).join('\n')
  const report=reportCases(cases.slice(0,3),goOutcomes(text),60,'go-test-1')
  assert.equal(report.classes.ESSENTIAL.passed,1)
  assert.equal(report.classes.ESSENTIAL.notRun,1)
  assert.equal(report.classes.NIGHTLY.notRun,1)
  assert.equal(report.executionMinutes,1);assert.equal(report.runnerMinutes,null)
  const inactive=reportCases([{...cases[0],active:false}],[],0,'darwin-only')
  assert.equal(inactive.classes.ESSENTIAL.platformInactive,1)
  assert.equal(inactive.classes.ESSENTIAL.passed,0)
  const browser={kind:'browser',file:'tests/a.spec.ts',name:'action',tier:'ESSENTIAL',id:'id',project:''}
  assert.throws(()=>browserOutcomes({suites:[{specs:[{id:'id',tests:[{results:[{status:'failed'},{status:'passed'}]}]}]}]},[browser]),/Automatic retries forbidden/)
})

test('job accounting includes setup in minutes and reports missing artifacts rather than zero-case success',()=>{
  const job={name:'go-test (1)',status:'completed',conclusion:'failure',started_at:'2026-10-04T01:00:00Z',completed_at:'2026-10-04T01:02:00Z'}
  assert.equal(jobMinutes(job),2)
  assert.equal(jobMinutes({...job,status:'in_progress'}),null)
  const measurement=aggregate([], [job])
  assert.equal(measurement.measured.goRunnerMinutes,2)
  assert.equal(measurement.coverage,'incomplete')
  assert.deepEqual(measurement.missingEvidence,['go-test-1'])
})

test('skipped jobs have no runner duration and cannot hide missing test evidence',()=>{
  const skipped={name:'tree-reuse',status:'completed',conclusion:'skipped',
    started_at:'2026-10-05T12:38:25Z',completed_at:'2026-10-05T12:38:24Z'}
  for(const timestamps of [{},{started_at:skipped.completed_at},{started_at:null,completed_at:null}]) {
    assert.equal(jobMinutes({...skipped,...timestamps}),null)
  }
  const measurement=aggregate([], [skipped,{...skipped,name:'web-unit (1)'}])
  assert.ok(measurement.jobs.every(job=>job.runnerMinutes===null))
  assert.equal(measurement.measured.webRunnerMinutes,0)
  assert.equal(measurement.coverage,'incomplete')
  assert.deepEqual(measurement.missingEvidence,['web-unit-1'])
  for(const conclusion of ['success','failure','cancelled']) {
    assert.throws(()=>jobMinutes({...skipped,conclusion}),/Invalid job timestamps: tree-reuse/)
  }
})

test('Actions skipped jobs with zero completed_at never invalidate PR, queue, push or nightly accounting',()=>{
  // Exact shape returned for tree-reuse on the failing PR/merge-group runs.
  const skipped={name:'tree-reuse',status:'completed',conclusion:'skipped',
    started_at:'2026-10-05T10:00:00Z',completed_at:'0001-01-01T00:00:00Z',steps:[]}
  const lanes={
    pull_request:{names:['tree-reuse','cache-prime','go-test (1)','go-timing','web-setup','web-shard (1)'],options:{lane:'docs-only'},missing:[]},
    merge_group:{names:['tree-reuse','cache-prime'],options:{lane:'full'},missing:[]},
    push:{names:['go-test (1)','go-timing','web-setup','web-shard (1)','release-check-run','e2e-run'],options:{lane:'full',reusedFrom:'123'},missing:[]},
    workflow_dispatch:{names:['tree-reuse','cache-prime'],options:{lane:'full'},missing:[]},
    schedule:{names:['nightly-go-test (1)','nightly-go-timing','nightly-web-setup','nightly-web-shard (1)'],options:{},missing:['go-test-1','go-timing','web-unit','web-shard-1']},
  }
  for(const [event,{names,options,missing}] of Object.entries(lanes)) for(const timestamps of [
    {},{started_at:'0001-01-01T00:00:00Z'},
    {started_at:null,completed_at:null},{started_at:0,completed_at:0},
  ]) {
    const jobs=names.map(name=>({...skipped,name,...timestamps}))
    const report=aggregate([],jobs,options)
    assert.ok(report.jobs.every(job=>job.conclusion==='skipped'&&job.runnerMinutes===null),event)
    assert.deepEqual(report.missingEvidence,missing,event)
    assert.equal(report.coverage,missing.length?'incomplete':options.reusedFrom?'reused':'reported',event)
    assert.equal(report.measured.goRunnerMinutes,0,event)
    assert.equal(report.measured.webRunnerMinutes,0,event)
    assert.deepEqual(report.classes,{},'Skipped jobs must not fabricate case passes')
  }
  assert.equal(jobMinutes({...skipped,status:'queued',conclusion:null}),null)
  assert.equal(jobMinutes({...skipped,conclusion:'cancelled',runner_id:0}),null)
  assert.throws(()=>jobMinutes({...skipped,conclusion:'cancelled',runner_id:1}),/Invalid job timestamps/)
  assert.throws(()=>jobMinutes({...skipped,conclusion:'cancelled',runner_id:0,steps:[{name:'Checkout',conclusion:'success'}]}),/Invalid job timestamps/)
})

// Classification: fixed go-static/nightly-go-static CI checks, outside the
// dynamically collected Go/web tier and browser-shard inventories.
test('AEON-732 full and nightly skipped tier fixtures remain incomplete after upstream failures',()=>{
  const finished={status:'completed',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'}
  const skipped={...finished,conclusion:'skipped',completed_at:'0001-01-01T00:00:00Z',steps:[]}
  for(const lane of [undefined,'full']) for(const prerequisite of ['tier-plan','web-setup']) {
    const jobs=[{...finished,name:prerequisite,conclusion:'failure'},
      ...['go-test (1)','go-timing','web-unit (1)','web-shard (1)'].map(name=>({...skipped,name}))]
    const report=aggregate([],jobs,{lane})
    assert.equal(report.coverage,'incomplete')
    assert.equal(report.coverageReason,'skipped after upstream failure')
    assert.deepEqual(report.missingEvidence,['go-test-1','go-timing','web-unit-1','web-shard-1'])
    assert.deepEqual(report.upstreamFailures,[{name:prerequisite,status:'completed',conclusion:'failure'}])
    assert.deepEqual(report.skippedJobs,jobs.slice(1).map(job=>job.name))
    assert.ok(report.jobs.slice(1).every(job=>job.runnerMinutes===null))
    assert.equal(report.measured.goRunnerMinutes,0)
    assert.equal(report.measured.webRunnerMinutes,prerequisite==='web-setup'?2:0)
    assert.deepEqual(report.classes,{})
    // Required skips remain incomplete even without prerequisite metadata.
    const noPrerequisite=aggregate([],jobs.slice(1),{lane})
    assert.equal(noPrerequisite.coverage,'incomplete')
    assert.equal(noPrerequisite.coverageReason,'required tier jobs skipped')
  }
  const nightly=aggregate([], [{...finished,name:'nightly-web-setup',conclusion:'failure'},
    {...skipped,name:'nightly-web-shard (1)'}])
  assert.equal(nightly.coverage,'incomplete')
  assert.equal(nightly.coverageReason,'skipped after upstream failure')
  assert.deepEqual(nightly.missingEvidence,['web-unit','web-shard-1'])
  assert.deepEqual(nightly.upstreamFailures,[{name:'nightly-web-setup',status:'completed',conclusion:'failure'}])
  for(const name of ['tier-plan','web-setup','nightly-web-setup']) for(const conclusion of ['failure','cancelled','skipped']) {
    const job={...finished,name,conclusion,...(conclusion==='skipped'?{completed_at:'0001-01-01T00:00:00Z'}:{})}
    // Even a retained unit artifact cannot conceal a later setup/build failure.
    const report=aggregate([reportCases([],[],0,'web-unit')],[job])
    const skippedUnit=name==='nightly-web-setup'&&conclusion==='skipped'
    assert.deepEqual(report.missingEvidence,skippedUnit?['web-unit']:[])
    assert.equal(report.coverage,'incomplete')
    assert.equal(report.coverageReason,skippedUnit?'skipped after upstream failure':'upstream jobs did not succeed')
    assert.deepEqual(report.upstreamFailures,[{name,status:'completed',conclusion}])
  }
  const jobs=['go-test (1)','go-timing','web-unit (1)','web-shard (1)'].map(name=>({...skipped,name}))
  const exact=aggregate([],jobs,{lane:'spec-only'})
  assert.equal(exact.coverage,'untiered')
  assert.deepEqual(exact.missingEvidence,[])
  assert.deepEqual(exact.classes,{})
  // Actions may skip a matrix before it expands, leaving the family name.
  const unexpanded=aggregate([],jobs.map(job=>({...job,name:job.name.replace(/ \(\d+\)$/,'')})))
  assert.equal(unexpanded.coverage,'incomplete')
  assert.deepEqual(unexpanded.missingEvidence,['go-test','go-timing','web-unit','web-shard'])
  const unexpected=aggregate([], [{...finished,name:'go-test (1)',conclusion:'success'}],{lane:'docs-only'})
  assert.equal(unexpected.coverage,'incomplete','Executed docs-only jobs still require evidence')
  const successful=aggregate([reportCases([],[],0,'go-test-1')],[{...finished,name:'go-test (1)',conclusion:'success'}],{lane:'full'})
  assert.equal(successful.coverage,'reported')
  assert.deepEqual(successful.missingEvidence,[])
  const artifactForSkipped=aggregate([reportCases([],[],0,'go-test-1')],[jobs[0]],{lane:'full'})
  assert.equal(artifactForSkipped.coverage,'incomplete')
  assert.deepEqual(artifactForSkipped.missingEvidence,['go-test-1'])
})

test('AEON-732 measurement CLI fixtures expose upstream skips in JSON, summary and exit status',()=>{
  const root=mkdtempSync(resolve(tmpdir(),'aeon-tier-measurement-'))
  const finished={status:'completed',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'}
  const skipped={...finished,conclusion:'skipped',completed_at:'0001-01-01T00:00:00Z',steps:[]}
  const jobs=[{...finished,name:'tier-plan',conclusion:'failure'},
    ...['go-test (1)','go-timing','web-setup','web-unit (1)','web-shard (1)'].map(name=>({...skipped,name}))]
  const evidence=resolve(root,'evidence'),summary=resolve(root,'summary.txt')
  mkdirSync(evidence)
  for(const fixture of [
    {lane:'full',status:1,coverage:'incomplete',reason:'skipped after upstream failure'},
    {lane:'docs-only',planned:true,status:0,coverage:'reported'},
    {lane:'full',planned:true,reuse:'merge_group',status:0,coverage:'reused'},
    {lane:'full',planned:true,reuse:'pull_request',status:0,coverage:'reused'},
    {lane:'spec-only',planned:true,status:0,coverage:'untiered'},
    {lane:'spec-only',status:1,coverage:'incomplete',reason:'upstream jobs did not succeed'},
  ]) {
    const fixtureEvidence=mkdtempSync(resolve(root,'case-evidence-'))
    if(fixture.reuse==='pull_request')writeFileSync(resolve(fixtureEvidence,'go-timing-measurement.json'),JSON.stringify({version:1,job:'go-timing',classes:{},runId:'123',attempt:'2',sha:'a'.repeat(40)}))
    const fixtureJobs=jobs.map(job=>fixture.reuse==='pull_request'&&job.name==='go-timing'?{...job,...finished,conclusion:'success'}:fixture.planned&&(job.name==='tier-plan'||fixture.lane==='spec-only'&&job.name.startsWith('web-'))
      ? {...job,...finished,conclusion:'success'}:job)
    writeFileSync(resolve(root,'gh'),`#!${process.execPath}\nif (process.argv[2] !== 'api' || process.argv[3] !== 'repos/example/aeon/actions/runs/123/jobs?filter=latest&per_page=100&page=1') process.exit(2)\nprocess.stdout.write(${JSON.stringify(JSON.stringify({total_count:fixtureJobs.length,jobs:fixtureJobs}))})\n`,{mode:0o755})
    writeFileSync(summary,'')
    const result=spawnSync(process.execPath,[fileURLToPath(new URL('./measure.mjs',import.meta.url)),fixtureEvidence],{
      cwd:root,encoding:'utf8',timeout:10_000,
      env:{PATH:root,GITHUB_REPOSITORY:'example/aeon',GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'.repeat(40),
        GITHUB_STEP_SUMMARY:summary,CI_LANE:fixture.lane,REUSE:fixture.reuse??'',SOURCE_RUN:fixture.reuse?'456':''},
    })
    assert.ifError(result.error)
    assert.equal(result.status,fixture.status,result.stderr)
    assert.equal(result.stderr,'')
    const report=JSON.parse(readFileSync(resolve(root,'tmp/test-tier-run-measurement.json'),'utf8'))
    const output=JSON.parse(result.stdout)
    for(const evidence of [report,output]) {
      assert.equal(evidence.coverage,fixture.coverage)
      assert.equal(evidence.coverageReason,fixture.reason)
      assert.deepEqual(evidence.classes,{})
    }
    assert.ok(readFileSync(summary,'utf8').includes(`Test tiers: ${fixture.coverage}${fixture.reason?` — ${fixture.reason}`:''};`))
    if(fixture.lane==='full'&&!fixture.reuse) {
      assert.deepEqual(report.missingEvidence,['go-test-1','go-timing','web-unit-1','web-shard-1'])
      assert.ok(readFileSync(summary,'utf8').includes('tier-plan (failure), web-setup (skipped)'))
      assert.equal(report.measured.sharedPlanningRunnerMinutes,2)
      assert.equal(report.measured.goRunnerMinutes,0)
      assert.equal(report.measured.webRunnerMinutes,0)
    } else assert.deepEqual(report.missingEvidence,[])
  }
})

test('completed jobs that ran still reject absent, zero, invalid and reversed timestamps',()=>{
  const job={name:'go-test (1)',status:'completed',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'}
  for(const conclusion of ['success','failure','cancelled']) for(const timestamps of [
    {started_at:null},{completed_at:null},{started_at:undefined},{completed_at:undefined},
    {started_at:''},{completed_at:''},{started_at:0},{completed_at:0},
    {started_at:'0001-01-01T00:00:00Z'},{completed_at:'0001-01-01T00:00:00Z'},
    {started_at:'invalid'},{completed_at:'2026-10-05T09:59:59Z'},
  ]) assert.throws(()=>aggregate([], [{...job,conclusion,...timestamps}]),/Invalid job timestamps: go-test \(1\)/)
})

test('four unit shards partition the existing ledger by file with every identity selected once',()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  for(const event of ['pull_request','merge_group','push','workflow_dispatch','schedule']) {
    const selected=select(manifest.tests,{event,paths:['.github/workflows/ci.yml']}).tests.filter(row=>row.kind!=='browser')
    const bins=Array.from({length:4},(_,i)=>shard(selected,i+1,4))
    assert.deepEqual(bins.flat().map(key).sort(),selected.map(key).sort(),event)
    const owners=new Map()
    bins.forEach((rows,i)=>rows.forEach(row=>{
      if(owners.has(row.file))assert.equal(owners.get(row.file),i,'A file must not run twice')
      owners.set(row.file,i)
    }))
    const sizes=bins.map(rows=>rows.length).sort((a,b)=>a-b)
    // Nightly retains a large parameterized file; it is intentionally kept
    // whole and does not participate in the four full-gate unit jobs.
    if(event!=='schedule')assert.ok(sizes[3]<=1.3*(sizes[1]+sizes[2])/2,`Unit registration balance: ${sizes}`)
  }
})

test('serial tier weights rebalance the full browser shards without changing full or changed-area coverage',()=>{
  const manifest=loadWebPolicy()
  const rows=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url))).tests
  const graph=Object.fromEntries([...new Set(rows.map(row=>row.file))].map(file=>[file,[]]))
  for(const options of [
    {event:'pull_request',paths:['.github/workflows/ci.yml']},
    {event:'merge_group',paths:['web/tests/clip-tip.spec.ts'],webImports:graph},
    {event:'push'}, {event:'workflow_dispatch'}, {event:'schedule'},
  ]) {
    const selected=select(rows,options).tests.filter(row=>row.kind==='browser')
    const weights=tierWeights(manifest,selected)
    const count=['schedule','workflow_dispatch'].includes(options.event)?24:fullWebShardCount
    const bins=Array.from({length:count},(_,i)=>shard(selected,i+1,count,weights))
    assert.deepEqual(bins.flat().map(key).sort(),selected.map(key).sort(),options.event)
    if(options.paths?.[0]==='.github/workflows/ci.yml'||options.event==='push') {
      const loads=bins.map(bin=>[...new Set(bin.map(row=>row.file))].reduce((n,file)=>n+weights[file],0)).sort((a,b)=>a-b)
      const middle=Math.floor(loads.length/2), median=loads.length%2?loads[middle]:(loads[middle-1]+loads[middle])/2
      assert.ok(loads.at(-1)<=1.3*median,`Serial scheduling estimates: ${loads}`)
      assert.ok(loads.at(-1)<=1.25*median,`Hosted scheduling estimates: ${loads}`)
    }
  }
})

test('AEON-1325 nightly shards retain every browser case and require all 24 measurements',()=>{
  const workflow=readFileSync(new URL('../../.github/workflows/nightly-full.yml',import.meta.url),'utf8')
  const job=workflow.slice(workflow.indexOf('\n  nightly-web-shard:'),workflow.indexOf('\n  # Preserve the ruleset'))
  const indices=JSON.parse(/shard: (\[[^\n]+\])/.exec(job)[1])
  assert.deepEqual(indices,Array.from({length:24},(_,i)=>i+1))
  assert.match(job,/run web --all --shard \$\{\{ matrix.shard \}\}\/\$\{\{ strategy.job-total \}\}/)
  const rows=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url))).tests
  const selected=select(rows,{event:'schedule'}).tests.filter(row=>row.kind==='browser')
  const policy=loadWebPolicy()
  const declared=new Set(policy.groups.flatMap(group=>group.specs.map(spec=>spec.file)))
  for(const file of Object.keys(policy.nightlyTiming.files)) assert.ok(declared.has(file),`stale nightly timing: ${file}`)
  for(const file of new Set(selected.map(row=>row.file))) assert.ok(policy.nightlyTiming.files[file],`missing nightly timing: ${file}`)
  const weights=nightlyWeights(policy,selected)
  const bins=indices.map(index=>shard(selected,index,indices.length,weights))
  assert.deepEqual(bins.flat().map(key).sort(),selected.map(key).sort())
  assert.equal(new Set(bins.flat().map(key)).size,selected.length)
  const owners=new Map()
  bins.forEach((bin,i)=>bin.forEach(row=>{
    if(owners.has(row.file))assert.equal(owners.get(row.file),i,'A spec must stay on one shard')
    owners.set(row.file,i)
  }))
  const loads=bins.map(bin=>[...new Set(bin.map(row=>row.file))].reduce((n,file)=>n+weights[file],0))
  const peak=Math.max(...loads)
  assert.ok(peak<=16*60,`Nightly estimates need four minutes under the 20-minute cap: ${peak}`)
  const jobs=indices.map(index=>({name:`nightly-web-shard (${index})`,status:'completed',conclusion:'success',
    started_at:'2026-10-13T01:00:00Z',completed_at:'2026-10-13T01:10:00Z'}))
  const reports=bins.map((bin,i)=>reportCases(bin,bin.map(row=>({key:key(row),status:'passed',started:true})),600,`web-shard-${i+1}`))
  const complete=aggregate(reports,jobs)
  assert.equal(complete.coverage,'reported')
  assert.equal(complete.measured.webRunnerMinutes,240)
  assert.equal(Object.values(complete.classes).reduce((sum,tier)=>sum+tier.passed,0),selected.length)
  const missing=aggregate(reports.slice(0,-1),jobs)
  assert.equal(missing.coverage,'incomplete')
  assert.deepEqual(missing.missingEvidence,['web-shard-24'])
})

test('hosted unit and Go weights preserve files/packages and every selected identity',()=>{
  for(const kind of ['web','go']) {
    const manifest=JSON.parse(readFileSync(new URL(`../ci/${kind}-test-tiers.json`,import.meta.url)))
    for(const event of ['pull_request','merge_group','push','schedule']) {
      const rows=select(manifest.tests,{event,paths:['.github/workflows/ci.yml']}).tests.filter(row=>kind==='web'?row.kind!=='browser':row.lane!=='timing')
      const weights=measuredWeights(manifest,rows),count=kind==='web'?4:7
      const bins=Array.from({length:count},(_,i)=>shard(rows,i+1,count,weights,{firstShardLast:kind==='web'}))
      assert.deepEqual(bins.flat().map(key).sort(),rows.map(key).sort())
      const owners=new Map()
      bins.forEach((bin,i)=>bin.forEach(row=>{
        const owner=kind==='go'?row.package:row.file
        if(owners.has(owner))assert.equal(owners.get(owner),i,'An owner cannot run twice')
        owners.set(owner,i)
      }))
    }
  }
})

test('unit tie preference keeps the heaviest file away from shard 1 extra checks without losing cases',()=>{
  const rows=[...Array(4)].flatMap((_,i)=>[1,2].map(n=>({kind:'node',file:`tests/unit-${i}.test.ts`,name:`case ${n}`})))
  const weights=Object.fromEntries(rows.map(row=>[row.file, row.file.includes('unit-0')?44:30]))
  const bins=Array.from({length:4},(_,i)=>shard(rows,i+1,4,weights,{firstShardLast:true}))
  assert.deepEqual(bins.flat().map(key).sort(),rows.map(key).sort())
  assert.deepEqual(bins[1],rows.slice(0,2))
  assert.ok(bins[0].every(row=>row.file!=='tests/unit-0.test.ts'))
  assert.deepEqual(shard(rows,1,1,weights,{firstShardLast:true}).map(key).sort(),rows.map(key).sort())
  assert.deepEqual(shard(rows,1,4,weights),rows.slice(0,2),'Go/browser default tie policy stays unchanged')
})

test('measured weights scale the selected slice, retain zero elapsed and reject invalid data',()=>{
  const manifest={timingWeights:{owners:{'tests/scope.test.ts':{seconds:10,selectedTests:2}}}}
  assert.deepEqual(measuredWeights(manifest,[cases[5]]),{'tests/scope.test.ts':5})
  assert.deepEqual(measuredWeights(manifest,cases),{'tests/scope.test.ts':10})
  manifest.timingWeights.owners['tests/scope.test.ts'].seconds=0
  assert.deepEqual(measuredWeights(manifest,cases),{'tests/scope.test.ts':0})
  for(const timing of [{seconds:-1,selectedTests:2},{seconds:NaN,selectedTests:2},{seconds:1,selectedTests:0},{seconds:1,selectedTests:1.5}]) {
    manifest.timingWeights.owners['tests/scope.test.ts']=timing
    assert.throws(()=>measuredWeights(manifest,cases),/Invalid measured timing: tests\/scope.test.ts/)
  }
})

test('unit measurement artifacts are required for every executed shard and legacy nightly units',()=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-unit-measurements-'))
  const job=name=>({name,status:'completed',conclusion:'success',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:02:00Z'})
  const names=['web-unit-1','web-unit-2','web-unit-3','web-unit-4']
  for(const name of names)writeFileSync(resolve(directory,`${name}-measurement.json`),JSON.stringify(reportCases([],[],0,name)))
  const jobs=names.map((_,i)=>job(`web-unit (${i+1})`))
  const reports=readReports(directory)
  assert.equal(reports.length,4)
  assert.equal(aggregate(reports,jobs).coverage,'reported')
  for(const name of names)assert.deepEqual(aggregate(reports.filter(r=>r.job!==name),jobs).missingEvidence,[name])
  assert.deepEqual(aggregate([], [job('nightly-web-setup')]).missingEvidence,['web-unit'])
  assert.deepEqual(aggregate([], [job('web-setup')]).missingEvidence,[],'Main setup no longer executes units')
  const exact=aggregate([], [job('web-unit (1)'),job('web-shard (1)')],{lane:'spec-only'})
  assert.equal(exact.coverage,'untiered')
  assert.deepEqual(exact.classes,{})
  assert.deepEqual(exact.missingEvidence,[])
  assert.match(exact.caseScope,/no tier passes are claimed/)
  assert.deepEqual(aggregate([], [job('web-unit (1)'),job('web-shard (1)')],{lane:'full'}).missingEvidence,['web-unit-1','web-shard-1'])
})

test('AEON-1384 partial reruns accept same-run same-SHA evidence and select the newest attempt per case',()=>{
  const job={name:'go-test (1)',status:'completed',conclusion:'success',started_at:'2026-10-04T01:00:00Z',completed_at:'2026-10-04T01:01:00Z'}
  const selected=[{...cases[0],active:true},{...cases[0],name:'TestSibling',active:true}]
  const make=(rows,attempt,status='passed')=>({...reportCases(rows,rows.map(row=>({key:key(row),status,started:true})),1,'go-test-1'),
    runId:'123',attempt,sha:'a',execution:{status:'completed'}})
  const earlier=make(selected,'1'),fresh=make([selected[0]],'2')
  const options={runId:'123',attempt:'2',sha:'a'}
  for(const reports of [[earlier],[fresh,earlier],[earlier,fresh]]) {
    const report=aggregate(reports,[job],options)
    assert.equal(report.coverage,'reported')
    assert.equal(report.classes.ESSENTIAL.passed,2)
    assert.equal(report.classes.ESSENTIAL.selected,2)
    assert.deepEqual(report.missingEvidence,[])
    assert.deepEqual(report.excludedEvidence,[])
    assert.match(report.notes.join('\n'),/partial rerun: [12] cases from attempt 1/)
  }
  const failedEarlier=make(selected,'1','failed')
  const newest=aggregate([failedEarlier,make(selected,'2')],[job],options)
  assert.equal(newest.classes.ESSENTIAL.failed,0)
  assert.equal(newest.classes.ESSENTIAL.passed,2)
  assert.deepEqual(newest.notes,[])
  const failure=aggregate([earlier,make([selected[0]],'2','failed')],[job],options)
  assert.equal(failure.classes.ESSENTIAL.failed,1,'a newer failure must supersede an old pass')
  assert.equal(failure.classes.ESSENTIAL.passed,1)
  assert.deepEqual(failure.reports[0].caseAttempts,{[key(selected[0])]:'2',[key(selected[1])]:'1'})
  const interrupted=make([selected[0]],'2');interrupted.caseResults[0].status='notRun';interrupted.caseResults[0].started=false
  interrupted.classes=reportCases([selected[0]],[],1,'go-test-1').classes
  assert.equal(aggregate([earlier,interrupted],[job],options).coverage,'incomplete','newer notRun cannot borrow an old pass')
  for(const change of [{sha:'b'},{runId:'999'},{attempt:'3'},{attempt:'0'},{attempt:undefined},{sha:undefined},{runId:undefined}]) {
    const report=aggregate([{...earlier,...change}],[job],options)
    assert.equal(report.coverage,'incomplete',JSON.stringify(change))
    assert.deepEqual(report.classes,{})
    assert.equal(report.excludedEvidence.length,1)
    assert.equal(aggregate([fresh,{...earlier,...change}],[job],options).coverage,'incomplete','foreign artifacts remain a failure beside valid evidence')
  }
  const {caseResults,...legacy}=earlier
  const legacyReport=aggregate([legacy],[job],options)
  assert.equal(legacyReport.coverage,'reported','legacy artifacts retain whole-job accounting')
  assert.equal(legacyReport.classes.ESSENTIAL.passed,2)
  assert.throws(()=>aggregate([fresh,fresh],[job],options),/duplicate measurement/)
  assert.equal(aggregate([earlier],[{...job,conclusion:'cancelled'}],options).coverage,'incomplete')
})

test('exact-SHA reuse records provenance and current costs without fabricating fresh case passes',()=>{
  const jobs=[{name:'go-test (1)',status:'completed',started_at:'2026-10-04T01:00:00Z',completed_at:'2026-10-04T01:00:30Z'}]
  const reused=aggregate([],jobs,{reusedFrom:'123'})
  assert.equal(reused.coverage,'reused');assert.equal(reused.reusedFrom,'123')
  assert.deepEqual(reused.classes,{});assert.deepEqual(reused.missingEvidence,[])
  assert.equal(reused.measured.goRunnerMinutes,0.5)
  assert.throws(()=>aggregate([],jobs,{reusedFrom:'invalid'}),/Invalid reused source/)
  assert.throws(()=>aggregate([{}],jobs,{reusedFrom:'123'}),/must not report fresh test passes/)
})

test('committed allowlists preserve classifications, helpers and reviewed AEON-541 demotions without fixed inventory sizes',()=>{
  const go=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const web=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  for(const manifest of [go,web]) {
    const reconciled=validate(manifest,manifest.tests)
    assert.deepEqual(reconciled.map(key).sort(),manifest.tests.map(key).sort())
    for(const tier of ['ESSENTIAL','GATED-FULL']) assert.ok(reconciled.some(row=>row.tier===tier),tier)
  }
  for(const helper of ['TestFakeVendorProcess','TestNoOutboundServerHelper'])assert.equal(go.tests.find(row=>row.name===helper).tier,'ESSENTIAL')
  const known=JSON.parse(readFileSync(new URL('../ci/known-flaky.json',import.meta.url)))
  const guardFiles=new Set(['tests/no-shift.spec.ts','tests/no-shift-agents.spec.ts','tests/no-shift-dialogs.spec.ts'])
  const guards=web.tests.filter(row=>guardFiles.has(row.file)&&known.entries.some(entry=>entry.key===key(row)))
  assert.deepEqual(guards.map(key).sort(),known.entries.filter(entry=>[...guardFiles].some(file=>entry.key.startsWith(`browser:${file}:`))).map(entry=>entry.key).sort())
  assert.ok(guards.every(row=>row.tier==='GATED-FULL'))
  assert.ok(go.tests.some(row=>row.tags?.includes('delete-candidate')))
  assert.ok(go.tests.some(row=>row.lane==='timing'))
  assert.ok([...go.tests,...web.tests].filter(row=>row.tags?.includes('delete-candidate')).every(row=>row.tier==='GATED-FULL'||row.tier==='NIGHTLY'))
})

// These exact registrations were missing in the AEON-706 static report.
// Keep AEON-707's reviewed work-order promotion and main's new nightly guards.
for(const [label,pkg,tier,names] of [
  ['work-order body guards','internal/workorders','GATED-FULL',[
    'TestBufferBodyBoundsAndDecode','TestEndpointBodyNetworkDeadline','TestEndpointPositionedBodyNetworkDeadline',
  ]],
  ['builder and shared-runtime guards','scripts/releaseworkflow','NIGHTLY',[
    'TestBuilderChecksumAndPluginPathFailClosed','TestBuilderVersionAssertionFailsClosed',
    'TestRehearsedBuilderPins','TestParallelWebUnitsShareRuntimeAndRunOnce',
  ]],
]) test(`AEON-706 ${label} retain explicit classifications and intended gate execution`,()=>{
  const manifest=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const discovered=names.map(name=>g(pkg,name))
  const ids=discovered.map(key).sort(), required=new Set(ids)
  const stored=manifest.tests.filter(row=>required.has(key(row)))
  assert.deepEqual(stored.map(key).sort(),ids,'Every reported registration needs exactly one explicit classification')
  const rows=validate({...manifest,tests:stored},discovered,noFlaky,{strict:true})
  for(const row of rows) {
    assert.equal(row.tier,tier,key(row))
    assert.equal(manifest.postGateCases.includes(key(row)),tier==='NIGHTLY',`${key(row)} must retain its reviewed post-gate policy`)
  }
  for(const event of ['pull_request','merge_group','push','schedule']) {
    const selected=select(rows,{event,forceFull:true}).tests.map(key).sort()
    assert.deepEqual(selected,event==='schedule'||tier==='GATED-FULL'?ids:[],`${event}: preserve explicit gate scope`)
  }
  assert.deepEqual(select(rows,{event:'pull_request',forceAll:true}).tests.map(key).sort(),ids)
})

test('strict classification is required before tests in CI and nightly; the independent inventory audit remains',()=>{
  const nightly=readFileSync(new URL('../../.github/workflows/nightly-full.yml',import.meta.url),'utf8')
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  const job=nightly.slice(nightly.indexOf('\n  nightly-tier-classification:'),nightly.indexOf('\n  nightly-web-shard:'))
  assert.match(job,/if: github.event_name == 'schedule'/)
  assert.match(job,/cli\.mjs check go --strict/)
  assert.match(job,/cli\.mjs check web --strict/)
  assert.match(job,/Report web cases needing classification\n\s+if: always\(\)/)
  for (const [source, prefix] of [[ci, ''], [nightly, 'nightly-']]) {
    const job = name => source.match(new RegExp(`\\n  ${prefix}${name}:[\\s\\S]*?(?=\\n  [a-z][a-z0-9-]*:|$)`))?.[0] ?? ''
    const go = job('go-static'), web = job(prefix ? 'web-setup' : 'web-catalogue')
    assert.match(go,/cli\.mjs check go --strict/)
    assert.match(web,/Require every web case to be classified\n\s+run: node scripts\/test-tiers\/cli\.mjs check web --strict/)
    if (prefix) assert.ok(web.indexOf('check web --strict') < web.indexOf('Build web'))
    else {
      assert.doesNotMatch(job('web-setup'), /check web --strict/)
      assert.equal(/^    needs: (.+)$/m.exec(web)?.[1], /^    needs: (.+)$/m.exec(job('web-setup'))?.[1], 'catalogue and runtime start from the same planning dependencies')
      assert.match(job('web'), /needs: \[[^\n]*web-catalogue/)
      assert.match(job('web'), /test "\$WEB_CATALOGUE" = success/)
    }
  }
  assert.doesNotMatch(nightly,/needs:.*nightly-tier-classification/)
})

test('nightly runs every tier and fixed gate; PR/MQ aggregates and compatibility remain',()=>{
  const read=name=>readFileSync(new URL(`../../.github/workflows/${name}`,import.meta.url),'utf8')
  const nightly=read('nightly-full.yml'),ci=read('ci.yml')
  assert.match(nightly,/schedule:\n\s+- cron:/);assert.match(nightly,/workflow_dispatch:/)
  assert.doesNotMatch(nightly,/pull_request:|merge_group:|runner-route:/)
  for(const command of ['run go --all --shard','run web --all --unit','run web --all --shard'])assert.ok(nightly.includes(command))
  for(const id of ['go-static','go-timing','migration-compat','e2e','release-check'])assert.ok(nightly.includes(`  nightly-${id}:`))
  assert.match(ci,/  go:\n\s+if: always\(\)\n\s+needs: \[ci-plan, go-test, go-static, go-timing, tree-reuse, cache-prime, tier-plan\]/)
  assert.match(ci,/  web:\n\s+name: web\n\s+if: always\(\)\n\s+needs: \[ci-plan, web-catalogue, web-setup, web-unit, web-shard, tree-reuse, cache-prime, tier-plan\]/)
  assert.match(ci,/  migration-compat:\n\s+permissions:/)
  assert.doesNotMatch(ci,/ci-flake-guard\.mjs --kind/)
})

test('full-execution proof binds actual complete outcomes to this run, attempt and SHA',()=>{
  const report={...reportCases(cases,cases.map(row=>({key:key(row),status:'passed',started:true})),1,'go-test-1'),full:true,scope:'catalogue',exitCode:0,runId:'123',attempt:'2',sha:'a'}
  const env={GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'}
  assert.equal(checkFull(report,env),true)
  for(const change of [{full:false},{exitCode:1},{attempt:'1'},{sha:'b'},{classes:{...report.classes,NIGHTLY:{...report.classes.NIGHTLY,notRun:1}}}])assert.throws(()=>checkFull({...report,...change},env),/Full execution/)
})

test('browser registrations blocked before execution remain notRun rather than counted as failed runs',()=>{
  const row={kind:'browser',file:'tests/a.spec.ts',name:'action',tier:'ESSENTIAL',id:'id',project:''}
  const outcomes=browserOutcomes({suites:[{specs:[{id:'id',tests:[{results:[]}]}]}]},[row])
  const report=reportCases([row],outcomes,1,'web-shard-1')
  assert.equal(report.classes.ESSENTIAL.notRun,1)
  assert.equal(report.classes.ESSENTIAL.run,0)
  assert.equal(report.classes.ESSENTIAL.failed,0)
})

test('three-tier full is exactly ESSENTIAL plus GATED-FULL even for unknown impact and explicit full',()=>{
  const rows=[g('internal/auth','TestCore','ESSENTIAL'),g('internal/auth','TestExisting','GATED-FULL'),
    g('internal/auth','TestUnclassified'),
    {kind:'browser',file:'tests/gated.spec.ts',name:'gate',tier:'GATED-FULL'},
    {kind:'browser',file:'tests/optional.spec.ts',name:'gallery',tier:'NIGHTLY'},
    {kind:'browser',file:'tests/gated.spec.ts',name:'new case defaults to nightly',tier:'NIGHTLY'},
    {kind:'vitest',file:'tests/new.unit.test.ts',name:'new unit defaults to nightly',tier:'NIGHTLY'}]
  for(const event of ['pull_request','merge_group','push','workflow_dispatch']) {
    for(const paths of [undefined,['.github/workflows/ci.yml'],['web/src/deleted.ts'],['scripts/check.mjs','web/tests/optional.spec.ts']]) {
      for(const forceFull of [false,true]) {
        const selection=select(rows,{event,paths,forceFull})
        assert.deepEqual(selection.tests,[rows[0],rows[1],rows[3]],`${event} ${paths} explicit=${forceFull}`)
        assert.equal(selection.scope,'gated-full')
        assert.equal(selection.deferredBrowserCases,2)
      }
    }
  }
  for(const options of [{event:'schedule'},{event:'pull_request',forceAll:true},{event:'workflow_dispatch',forceAll:true}]) {
    const selection=select(rows,{...options,paths:['README.md']})
    assert.deepEqual(selection.tests,rows)
    assert.equal(selection.full,true)
    assert.equal(selection.scope,'catalogue')
    assert.equal(selection.deferredBrowserCases,0)
  }
  // The changed-area lane still exercises optional tests when their area changes.
  for(const event of ['pull_request']) assert.deepEqual(
    select(rows,{event,paths:['web/tests/optional.spec.ts'],webImports:{'tests/optional.spec.ts':[]}}).tests,[rows[0],rows[4]])
  for(const event of ['pull_request']) assert.deepEqual(
    select(rows,{event,paths:['web/tests/gated.spec.ts'],webImports:{'tests/gated.spec.ts':[]}}).tests,[rows[0],rows[3],rows[5]])
})

test('three-tier validation and reports retain GATED-FULL results and deletion candidates',()=>{
  const rows=[g('internal/auth','TestCore','ESSENTIAL'),{...g('internal/auth','TestExisting','GATED-FULL'),tags:['delete-candidate']},g('internal/auth','TestNew')]
  const manifest={version:1,tests:rows}
  assert.doesNotThrow(()=>validate(manifest,rows,noFlaky))
  assert.deepEqual(validate(manifest,rows,noFlaky),rows)
  assert.deepEqual(counts(rows),{ESSENTIAL:1,'GATED-FULL':1,NIGHTLY:1})
  const report=reportCases(rows,[{key:key(rows[0]),status:'passed',started:true},{key:key(rows[1]),status:'failed',started:true}],1,'go-test-1')
  assert.equal(report.classes['GATED-FULL'].failed,1)
  assert.equal(report.classes['GATED-FULL'].run,1)
  assert.equal(report.classes.NIGHTLY.notRun,1)
  const known={version:1,entries:[{key:key(rows[1]),owner:'AEON-675'}]}
  assert.deepEqual(validate(manifest,rows,known),rows)
  const promoted=rows.map(row=>({...row,tier:'ESSENTIAL',tags:[]}))
  assert.throws(()=>validate({version:1,tests:promoted},rows,known),/Known-flaky case cannot be ESSENTIAL/)
})

test('AEON-686 host-capacity bound and fence cases run in every PR gate',()=>{
  // Unlisted cases default to NIGHTLY, so new Go tests in these files would
  // silently leave the PR gate; derive the list from source, not by hand.
  const go=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const sources=[['internal/hostcapacity','policy_test.go'],['internal/agentpairing','host_capacity_test.go']]
  const rows=sources.flatMap(([pkg,file])=>[...readFileSync(new URL(`../../${pkg}/${file}`,import.meta.url),'utf8')
    .matchAll(/^func (Test\w+)\(t \*testing\.T\)/gm)].map(match=>`${pkg}:${match[1]}`))
  for(const required of ['internal/hostcapacity:TestHostCapacityLargestEncodingsStayBounded',
    'internal/agentpairing:TestHostCapacityBoundsHeldByWritersWithoutDatabaseChecks'])assert.ok(rows.includes(required),required)
  for(const id of rows) {
    const row=go.tests.find(row=>key(row)===id)
    assert.equal(row?.tier,'ESSENTIAL',id)
    for(const event of ['pull_request','merge_group'])
      assert.ok(select(go.tests,{event,paths:['README.md']}).tests.some(selected=>key(selected)===id),`${event}: ${id}`)
  }
})

test('three-tier manifests allow new NIGHTLY cases and never promote ungated browser cases',()=>{
  const policy=loadWebPolicy()
  const gated=new Set(policy.groups.filter(group=>group.gate!==false).flatMap(group=>group.specs.map(spec=>spec.file)))
  const go=JSON.parse(readFileSync(new URL('../ci/go-test-tiers.json',import.meta.url)))
  const web=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url)))
  // Cases first classified after the old gate retain the NIGHTLY default.
  // An explicit inventory keeps the assertion strict for every legacy case.
  const postGateCases=new Set([...(go.postGateCases??[]),...(web.postGateCases??[])])
  assert.equal(postGateCases.size,(go.postGateCases??[]).length+(web.postGateCases??[]).length)
  for(const id of postGateCases) {
    const row=[...go.tests,...web.tests].find(row=>key(row)===id)
    assert.ok(row,`Unknown post-gate case: ${id}`)
    assert.equal(row.tier,'NIGHTLY',id)
  }
  for(const row of [...go.tests,...web.tests]) {
    if(postGateCases.has(key(row)))continue
    if(row.tier==='ESSENTIAL')continue
    assert.equal(row.tier,row.kind!=='browser'||gated.has(row.file)?'GATED-FULL':'NIGHTLY',key(row))
  }
  // This established Knowledge registration caused the merge-group regression:
  // its launch group is gated, so it must retain its full-gate classification.
  assert.equal(web.tests.find(row=>row.kind==='browser'&&row.file==='tests/knowledge.spec.ts'&&
    row.name==='the Knowledge tab groups by kind, filters, searches the text and moves with keys')?.tier,'GATED-FULL')
  assert.ok(web.tests.some(row=>row.kind==='browser'&&row.tier==='NIGHTLY'))
  assert.ok(web.tests.some(row=>row.kind==='browser'&&row.tier==='GATED-FULL'))
})

test('three-tier nightly explicitly requests all cases and remains outside required CI jobs',()=>{
  const nightly=readFileSync(new URL('../../.github/workflows/nightly-full.yml',import.meta.url),'utf8')
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  for(const command of ['run go --all --shard','run go --all --timing','run web --all --unit','run web --all --shard'])assert.ok(nightly.includes(command),command)
  assert.doesNotMatch(nightly,/cli\.mjs run (?:go|web) --full/)
  assert.doesNotMatch(ci,/cli\.mjs run (?:go|web) --all|needs:.*nightly-/)
})

test('three-tier full proof rejects incomplete gated results and old two-tier evidence',()=>{
  const empty={selected:0,run:0,passed:0,skipped:0,failed:0,notRun:0,platformInactive:0}
  const report={version:1,full:true,scope:'gated-full',exitCode:0,runId:'123',attempt:'2',sha:'a',
    classes:{ESSENTIAL:{...empty},'GATED-FULL':{...empty,selected:1,run:1,passed:1},NIGHTLY:{...empty}}}
  const env={GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'}
  assert.equal(checkFull(report,env),true)
  for(const scope of [undefined,'changed-area','browser-gate'])assert.throws(()=>checkFull({...report,scope},env),/Full execution/)
  const {['GATED-FULL']:gated,...oldClasses}=report.classes
  assert.throws(()=>checkFull({...report,classes:oldClasses},env),/Full execution/)
  for(const counts of [{...gated,notRun:1},{...gated,failed:1}])assert.throws(()=>checkFull({...report,classes:{...report.classes,'GATED-FULL':counts}},env),/Full execution/)
})


test('MG reuse lookup misses retain main full scheduling and case selection',()=>{
  const exists=()=>true
  for(const paths of [[],['internal/nodes/module.go'],undefined]) {
    for(const mgReuse of [undefined,'off','ON','true','on']) {
      assert.equal(schedulingMode('merge_group',paths,exists,{mgReuse}),'full')
      const selected=runnerSelection([g('internal/nodes','TestEssential','ESSENTIAL'),g('internal/nodes','TestFull','GATED-FULL')],
        {event:'merge_group',paths},schedulingDecision('merge_group',paths,exists),{mode:'essential',layout:'static'})
      assert.equal(selected.full,true)
      assert.equal(selected.tests.length,2)
    }
  }
  assert.equal(schedulingMode('pull_request',['internal/nodes/module.go'],exists,{tests:cases,imports:{}}),'essential')
  const ci=readFileSync(new URL('../../.github/workflows/ci.yml',import.meta.url),'utf8')
  assert.match(ci,/^  CI_MG_REUSE: \$\{\{ vars.CI_MG_REUSE \}\}$/m)
  assert.match(ci,/^  CI_AFFECTED_LANE: \$\{\{ vars.CI_AFFECTED_LANE \}\}$/m)
})


test('MG measurements count only fresh timing guards beside reused PR evidence',()=>{
  const selected=[g('internal/budget','TestClock','GATED-FULL')]
  const timing={...reportCases(selected,[{key:key(selected[0]),status:'passed',started:true}],1,'go-timing'),runId:'2',attempt:'1',sha:'a'}
  const jobs=[{name:'go-timing',status:'completed',conclusion:'success',started_at:'2026-10-05T10:00:00Z',completed_at:'2026-10-05T10:01:00Z'},
    {name:'go-test (1)',status:'completed',conclusion:'skipped'}]
  const options={runId:'2',attempt:'1',sha:'a',reusedFrom:'123',freshTiming:true}
  const report=aggregate([timing],jobs,options)
  assert.equal(report.coverage,'reused')
  assert.equal(report.classes['GATED-FULL'].passed,1)
  assert.equal(report.reusedFrom,'123')
  assert.match(report.caseScope,/Only freshly executed timing guard cases/)
  assert.throws(()=>aggregate([{...timing,job:'go-test-1'}],jobs,options),/must not report fresh test passes/)
  assert.throws(()=>aggregate([timing],jobs,{...options,freshTiming:false}),/must not report fresh test passes/)
  for(const reports of [[],[{...timing,attempt:'2'}]]) {
    const missing=aggregate(reports,jobs,options)
    assert.equal(missing.coverage,'incomplete')
    assert.deepEqual(missing.missingEvidence,['go-timing'])
  }
})

test('web collection is reused within a process only while the stamped tree is unchanged', () => {
  const base = mkdtempSync(resolve(tmpdir(), 'aeon-724-stamp-'))
  mkdirSync(resolve(base, 'src/deep'), { recursive: true }); mkdirSync(resolve(base, 'tests'))
  mkdirSync(resolve(base, 'node_modules/pkg'), { recursive: true }); mkdirSync(resolve(base, 'dist'))
  writeFileSync(resolve(base, 'package.json'), '{}'); writeFileSync(resolve(base, 'src/deep/a.ts'), 'a')
  writeFileSync(resolve(base, 'tests/a.test.ts'), 'a'); writeFileSync(resolve(base, 'node_modules/pkg/index.js'), '1')
  assert.deepEqual(webStampEntries, ['.', 'src', 'tests'])
  const initial = treeStamp(base, webStampEntries)
  assert.equal(treeStamp(base, webStampEntries), initial, 'stable without changes')
  // Installed dependencies and build output never invalidate the inventory.
  writeFileSync(resolve(base, 'node_modules/pkg/index.js'), '22'); writeFileSync(resolve(base, 'dist/bundle.js'), 'x')
  assert.equal(treeStamp(base, webStampEntries), initial)
  // A same-size edit changes mtime; nested source, native tests and top-level configs all count.
  const stamps = new Set([initial])
  for (const [file, text] of [['src/deep/a.ts', 'b'], ['tests/b.test.ts', 'new'], ['playwright.ui.config.ts', 'export {}']]) {
    const clock = new Date(Date.now() + stamps.size * 2000)
    writeFileSync(resolve(base, file), text); utimesSync(resolve(base, file), clock, clock)
    const stamp = treeStamp(base, webStampEntries)
    assert.ok(!stamps.has(stamp), file); stamps.add(stamp)
  }
  // Missing entries stamp as absent rather than throwing; the memo hands out private copies.
  assert.notEqual(treeStamp(base, ['missing']), treeStamp(base, ['.']))
  const memo = {}, runs = []
  const compute = () => { runs.push(1); return { tests: [{ kind: 'vitest', name: 'a' }] } }
  const first = reuse(memo, 'one', compute)
  first.tests[0].name = 'poisoned'
  assert.deepEqual(reuse(memo, 'one', compute), { tests: [{ kind: 'vitest', name: 'a' }] })
  assert.equal(runs.length, 1, 'unchanged stamp reuses')
  assert.deepEqual(reuse(memo, 'two', compute), { tests: [{ kind: 'vitest', name: 'a' }] })
  assert.equal(runs.length, 2, 'changed stamp recomputes')
  reuse(memo, 'two', compute, { fresh: true })
  assert.equal(runs.length, 3, 'fresh recomputes')
  assert.equal(memo.stamp, 'two')
})

// AEON-642: nightly `check web --strict` rejects a manifest name Vitest no
// longer emits. The state model generates its titles from these objects, and
// comments in that fixture contain apostrophes, so the scan has to skip
// comments before it treats a quote as a string.
function themeEditorObjectBody(source, constName) {
  const start = source.indexOf(`const ${constName}`)
  if (start < 0) throw new Error(`missing ${constName}`)
  const eq = source.indexOf('= {', start)
  if (eq < 0 || eq - start > 500) throw new Error(`no object for ${constName}`)
  let depth = 0, end = -1
  for (let p = eq + 2; p < source.length; p++) {
    if (source.startsWith('//', p)) { p = source.indexOf('\n', p); if (p < 0) break; continue }
    if (source.startsWith('/*', p)) { const stop = source.indexOf('*/', p + 2); p = stop < 0 ? source.length : stop + 1; continue }
    const c = source[p]
    if (c === "'" || c === '"' || c === '`') {
      p++
      while (p < source.length && source[p] !== c) { if (source[p] === '\\') p++; p++ }
      continue
    }
    if (c === '{') depth++
    else if (c === '}') { depth--; if (depth === 0) { end = p; break } }
  }
  if (end < 0) throw new Error(`unclosed ${constName}`)
  return source.slice(eq + 3, end)
}
function themeEditorObjectKeys(body) {
  const keys = []
  let i = 0, depth = 0
  while (i < body.length) {
    while (i < body.length && /\s/.test(body[i])) i++
    if (i >= body.length) break
    if (body.startsWith('//', i)) { const n = body.indexOf('\n', i); i = n < 0 ? body.length : n + 1; continue }
    if (body.startsWith('/*', i)) { const stop = body.indexOf('*/', i + 2); i = stop < 0 ? body.length : stop + 2; continue }
    if (depth === 0) {
      const match = /^('([^']*)'|"([^"]*)"|([A-Za-z_][A-Za-z0-9_]*))\s*:/.exec(body.slice(i))
      if (match) { keys.push(match[2] ?? match[3] ?? match[4]); i += match[0].length; continue }
    }
    const c = body[i]
    if (c === "'" || c === '"' || c === '`') {
      i++
      while (i < body.length && body[i] !== c) { if (body[i] === '\\') i++; i++ }
      i++
      continue
    }
    if (c === '{') depth++
    else if (c === '}') depth--
    i++
  }
  return keys
}
function themeEditorStateModelNames(unit, editor) {
  if (!/const STATES\b[^=]*=\s*\{\s*\.\.\.IDLE,\s*\.\.\.PENDING\s*\}/.test(unit)) throw new Error('theme editor states are no longer IDLE plus PENDING')
  if (!/const OUTCOME_NAMES = \['resolved', \.\.\.ERROR_CLASSES\]/.test(unit)) throw new Error('theme editor outcome names changed')
  const begin = unit.indexOf("describe('theme editor state model'")
  const end = unit.indexOf("describe('theme editor runner'", begin)
  if (begin < 0 || end < 0) throw new Error('theme editor state model block moved')
  const region = unit.slice(begin, end)
  const cell = region.match(/it\.each\(cells\)\('(%s[^']*)'/)
  const outcomeFormats = [...region.matchAll(/it\.each\(outcomes\)\('(%s[^']*)'/g)].map(match => match[1])
  if (!cell || outcomeFormats.length !== 3) throw new Error('theme editor matrix titles changed')
  const fill = (fmt, args) => { let n = 0; return fmt.replace(/%s/g, () => args[n++]) }
  const idle = themeEditorObjectKeys(themeEditorObjectBody(unit, 'IDLE'))
  const pending = themeEditorObjectKeys(themeEditorObjectBody(unit, 'PENDING'))
  const events = themeEditorObjectKeys(themeEditorObjectBody(unit, 'EVENTS'))
  for (const [label, keys] of [['IDLE', idle], ['PENDING', pending], ['EVENTS', events]]) {
    if (new Set(keys).size !== keys.length || !keys.length) throw new Error(`duplicate or empty ${label} keys`)
  }
  const classes = [...editor.match(/export const ERROR_CLASSES = \[([^\]]+)\]/)[1].matchAll(/'([^']+)'/g)].map(match => match[1])
  if (new Set(classes).size !== classes.length || !classes.length) throw new Error('ERROR_CLASSES changed shape')
  const states = [...idle, ...pending.filter(name => !idle.includes(name))]
  const outcomes = ['resolved', ...classes]
  const prefix = 'theme editor state model > '
  const names = new Set()
  for (const state of states) for (const event of events) names.add(prefix + fill(cell[1], [state, event]))
  for (const name of pending) for (const outcome of outcomes) for (const fmt of outcomeFormats) names.add(prefix + fill(fmt, [name, outcome]))
  if (names.size !== states.length * events.length + pending.length * outcomes.length * outcomeFormats.length) throw new Error('theme editor case generation collided')
  return names
}

test('AEON-642 theme editor state model registrations are exactly the cases the suite emits', () => {
  const unit = readFileSync(new URL('../../web/tests/theme-editor.unit.test.ts', import.meta.url), 'utf8')
  const editor = readFileSync(new URL('../../web/src/stores/themeEditor.ts', import.meta.url), 'utf8')
  const expected = themeEditorStateModelNames(unit, editor)
  const manifest = JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json', import.meta.url)))
  const prefix = 'theme editor state model > '
  const declared = manifest.tests.filter(row => row.kind === 'vitest' && row.file === 'tests/theme-editor.unit.test.ts' && row.name.startsWith(prefix))
  const declaredNames = new Set(declared.map(row => row.name))
  const stale = [...declaredNames].filter(name => !expected.has(name)).sort()
  const missing = [...expected].filter(name => !declaredNames.has(name)).sort()
  assert.deepEqual({ stale: stale.length, missing: missing.length, staleSample: stale.slice(0, 3), missingSample: missing.slice(0, 3) },
    { stale: 0, missing: 0, staleSample: [], missingSample: [] })
  for (const row of declared) {
    assert.equal(row.tier, 'NIGHTLY', row.name)
    assert.equal(manifest.postGateCases.includes(key(row)), true, row.name)
  }
  const ids = new Set(declared.map(key))
  const extraPostGate = manifest.postGateCases.filter(id => id.includes(prefix) && !ids.has(id))
  assert.deepEqual(extraPostGate, [])
})

// R13 release integrity: retry/quarantine must never turn a real regression,
// missing execution or runner failure into successful merge-queue evidence.
test('AEON-980 merge-group Go retry and quarantine preserve the blocking gate and exact evidence',async t=>{
  const {run}=await import('./cli.mjs')
  const rows=[g('internal/auth','TestFailed','GATED-FULL'),g('internal/auth','TestPassed','GATED-FULL'),
    g('internal/nodes','TestOther','GATED-FULL'),g('internal/nodes','TestSibling','GATED-FULL')]
  const identity={GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'.repeat(40)}
  const selection={tests:rows,all:rows,full:true,scope:'gated-full',reason:'fixture'}
  const simulate=async({event='merge_group',failed=[rows[0],rows[2]],again=[],skipped=[],known=noFlaky,
    resultCode,error,missing=[],retryError=false,tests=rows}={})=>{
    const calls=[],logs=[],attempts=new Map()
    let report
    const selected={...selection,tests,all:tests}
    const env={...identity,...(event===null?{}:{GITHUB_EVENT_NAME:event})}
    const code=await run('go',selected,{env,job:'aeon980-go-fixture'}, {
      compileOwners:[],
      knownFlaky:known,saveJSON:(_path,value)=>{report=value},log:line=>logs.push(line),
      command:(_bin,args)=>tests.filter(row=>`./${row.package}`===args.at(-1)).map(row=>row.name).join('\n'),
      execute:(_bin,args,path)=>{
        const owner=args.at(-1).slice(2),pattern=new RegExp(args[args.indexOf('-run')+1])
        const chosen=tests.filter(row=>row.package===owner&&pattern.test(row.name))
        calls.push({owner,keys:chosen.map(key),args,path})
        const attempt=(attempts.get(owner)??0)+1
        attempts.set(owner,attempt)
        const output=[]
        let failedAny=false
        for(const row of chosen) {
          if(missing.some(candidate=>key(candidate)===key(row))) continue
          const status=attempt===1
            ? failed.some(candidate=>key(candidate)===key(row))?'fail':'pass'
            : again.some(candidate=>key(candidate)===key(row))?'fail':skipped.some(candidate=>key(candidate)===key(row))?'skip':'pass'
          const Package=`github.com/inspr-at/paimos/${owner}`
          output.push({Package,Test:row.name,Action:'run'})
          if(status==='fail') {
            failedAny=true
            // Nested failures count as the one registered top-level case.
            output.push({Package,Test:`${row.name}/nested`,Action:'fail'})
          }
          output.push({Package,Test:row.name,Action:status})
        }
        output.push({Package:`github.com/inspr-at/paimos/${owner}`,Action:failedAny?'fail':'pass'})
        return {code:attempt===1?resultCode??(failedAny?1:0):retryError?2:failedAny?1:0,
          output:output.map(JSON.stringify).join('\n'),stderr:'',error:attempt===1?error:undefined}
      },
    })
    return {code,report,calls,logs,env}
  }
  await t.test('only merge_group retries only failed top-level cases once and records warnings',async()=>{
    const {code,report,calls,logs,env}=await simulate()
    assert.equal(code,0)
    assert.deepEqual(calls.map(call=>call.keys),[[key(rows[0]),key(rows[1])],[key(rows[2]),key(rows[3])],[key(rows[0])],[key(rows[2])]])
    for(const call of calls.slice(2)) {
      assert.ok(call.args.includes('-count=1'))
      assert.match(call.path,/-retry\.jsonl$/)
    }
    assert.deepEqual(report.flakeLedger,[rows[0],rows[2]].map(row=>({key:key(row),status:'flaky',runId:'123',sha:identity.GITHUB_SHA,attempt:'2',testAttempt:2})))
    assert.equal(report.classes['GATED-FULL'].passed,4)
    assert.equal(report.classes['GATED-FULL'].flaky,2)
    assert.equal(report.classes['GATED-FULL'].failed,0)
    assert.equal(logs.filter(line=>line.startsWith('::warning title=Test tier flake::')).length,2)
    assert.ok(logs.some(line=>line.startsWith('::warning')&&line.includes(key(rows[0]))))
    assert.equal(checkFull(report,env),true)
    const measured=aggregate([report],[],{runId:'123',attempt:'2',sha:identity.GITHUB_SHA})
    assert.deepEqual(measured.flakeCounts,{[key(rows[0])]:1,[key(rows[2])]:1})
    assert.equal(measured.flakeLedger[0].job,'aeon980-go-fixture')
    assert.equal(measured.classes['GATED-FULL'].flaky,2)
    const repeated=aggregate([report,{...report,job:"another-shard"}],[])
    assert.deepEqual(repeated.flakeCounts,{[key(rows[0])]:2,[key(rows[2])]:2})
    const stale=aggregate([report],[],{runId:'123',attempt:'3',sha:identity.GITHUB_SHA})
    assert.deepEqual(stale.flakeLedger,measured.flakeLedger)
    assert.deepEqual(stale.flakeCounts,measured.flakeCounts)
    assert.match(stale.notes.join('\n'),/partial rerun:/)
  })
  await t.test('PR, push, schedule, manual and absent event retain the original failure',async()=>{
    for(const event of ['pull_request','push','schedule','workflow_dispatch',null]) {
      const {code,report,calls}=await simulate({event,known:{version:1,entries:[{key:key(rows[0]),owner:'AEON-675'}]}})
      assert.equal(code,1,event)
      assert.equal(calls.length,2,event)
      assert.equal(report.classes['GATED-FULL'].failed,2,event)
      assert.equal(report.classes['GATED-FULL'].quarantined,0,event)
      assert.deepEqual(report.flakeLedger,[],event)
    }
  })
  await t.test('three failures retry; four across packages exceed the job-wide cap',async()=>{
    const capped=await simulate({failed:rows})
    assert.equal(capped.code,1)
    assert.equal(capped.calls.length,2)
    assert.deepEqual(capped.report.flakeLedger,[])
    const boundary=await simulate({failed:rows.slice(0,3)})
    assert.equal(boundary.code,0)
    assert.equal(boundary.calls.length,4)
    assert.equal(boundary.report.flakeLedger.length,3)
    const known={version:1,entries:rows.map(row=>({key:key(row),owner:'AEON-675'}))}
    const quarantine=await simulate({failed:rows,known})
    assert.equal(quarantine.code,0)
    assert.equal(quarantine.calls.length,2,'quarantine cannot shrink the cap')
    assert.equal(quarantine.report.classes['GATED-FULL'].passed,0)
    assert.equal(quarantine.report.classes['GATED-FULL'].quarantined,4)
    assert.ok(quarantine.report.flakeLedger.every(entry=>entry.testAttempt===1))
  })
  await t.test('second failure stays red; skips, missing execution and runner errors cannot prove a flake',async()=>{
    const repeated=await simulate({again:[rows[0]]})
    assert.equal(repeated.code,1)
    assert.equal(repeated.calls.length,4)
    assert.equal(repeated.report.classes['GATED-FULL'].failed,1)
    assert.deepEqual(repeated.report.flakeLedger.map(entry=>entry.key),[key(rows[2])])
    const skip=await simulate({skipped:[rows[0]]})
    assert.equal(skip.code,1)
    assert.equal(skip.report.classes['GATED-FULL'].failed,1)
    for(const options of [{resultCode:2},{error:'ETIMEDOUT'},{missing:[rows[1]]},{retryError:true}]) {
      const {code,report}=await simulate(options)
      assert.notEqual(code,0,JSON.stringify(options))
      assert.ok(!report.flakeLedger.some(entry=>entry.key===key(rows[0])),JSON.stringify(options))
    }
    const emptyFailure=await simulate({failed:[],resultCode:1})
    assert.equal(emptyFailure.code,1)
    assert.equal(emptyFailure.calls.length,2)
  })
  await t.test('quarantine runs and reports its owner; ESSENTIAL and malformed ownership are refused',async()=>{
    const known={version:1,entries:[{key:key(rows[0]),owner:'AEON-675'}]}
    const {code,report,calls,logs,env}=await simulate({failed:[rows[0]],again:[rows[0]],known})
    assert.equal(code,0)
    assert.equal(calls.length,3)
    assert.equal(report.classes['GATED-FULL'].passed,3)
    assert.equal(report.classes['GATED-FULL'].failed,0)
    assert.equal(report.classes['GATED-FULL'].quarantined,1)
    assert.deepEqual(report.flakeLedger,[{key:key(rows[0]),status:'quarantined',owner:'AEON-675',runId:'123',sha:identity.GITHUB_SHA,attempt:'2',testAttempt:2}])
    assert.ok(logs.some(line=>line.startsWith('::warning')&&line.includes(key(rows[0]))&&line.includes('AEON-675')))
    assert.equal(checkFull(report,env),true)
    assert.equal(aggregate([report],[]).classes['GATED-FULL'].quarantined,1)
    assert.notEqual((await simulate({failed:[rows[0]],known,resultCode:2})).code,0,'quarantine does not forgive runner errors')
    await assert.rejects(simulate({known:{version:1,entries:[{key:key(rows[0])}]}}),/owner ticket/)
    await assert.rejects(simulate({known,tests:[{...rows[0],tier:'ESSENTIAL'}]}),/Known-flaky case cannot be ESSENTIAL/)
  })
})

test('AEON-980 web retry uses exact browser registrations and native unit patterns without weakening supervision',async t=>{
  const {run}=await import('./cli.mjs')
  const identity={GITHUB_RUN_ID:'123',GITHUB_RUN_ATTEMPT:'2',GITHUB_SHA:'a'.repeat(40)}
  const rows=[11,12,21,22].map((line,index)=>({kind:'browser',file:`tests/${index<2?'a':'b'}.spec.ts`,
    name:index===0?'failed %\n::error::title':`case ${index}`,tier:'GATED-FULL',id:`id-${index}`,project:'chromium',line,active:true}))
  // Conflicting per-group settings deliberately exercise the separate-launch
  // retry path; AEON-1023 also covers compatible groups in web-batches.test.mjs.
  const policy={groups:['a','b'].map(id=>({id,config:'playwright.ui.config.ts',project:'chromium',flags:[],env:{FIXTURE_ROOT:'${RUNNER_TEMP}/fixture',FIXTURE_GROUP:id},specs:[{file:`tests/${id}.spec.ts`}]}))}
  const simulate=async({event='merge_group',failed=[rows[0],rows[2]],again=[],known=noFlaky,reportErrors=false,automaticRetries=false,missing=false,collectionMismatch=false,interrupted=false,retryInterrupted=false}={})=>{
    const calls=[],collections=[],attempts=new Map(),logs=[]
    let report
    // Match native --test-list descriptors including embedded title newlines;
    // never split titles or match a sibling just because it shares a spec.
    const chosen=args=>{
      const list=readFileSync(args[args.indexOf('--test-list')+1],'utf8')
      return rows.filter(row=>list.includes(`[${row.project}] › ${row.file.slice(6)}\n`)||
        list.includes(`[${row.project}] › ${row.file.slice(6)}:${row.line} › ${row.name}\n`))
    }
    const nativeReport=(selected,status='passed')=>({errors:reportErrors?[{message:'global setup failed'}]:[],suites:[{specs:selected.map(row=>({id:row.id,title:row.name,
      tests:[{projectName:row.project,results:missing?[]:automaticRetries?[{status:'failed'},{status:'passed'}]:[{status:interrupted?'interrupted':typeof status==='function'?status(row):status}]}]}))}]})
    const code=await run('web',{tests:rows,all:rows,full:true,scope:'gated-full',reason:'fixture'},
      {env:{...identity,GITHUB_EVENT_NAME:event,RUNNER_ENVIRONMENT:'github-hosted'},job:'aeon980-browser-fixture'}, {
        knownFlaky:known,loadBrowserPolicy:()=>policy,saveJSON:(_path,value)=>{report=value},log:line=>logs.push(line),
        command:(_bin,args,{env})=>{
          const selected=chosen(args)
          collections.push(selected.map(key))
          assert.equal(env.PW_RETRIES,'0')
          const report=nativeReport(selected)
          report.errors=[] // Runtime errors do not alter safe collection.
          if(collectionMismatch&&collections.length>2)report.suites[0].specs=[]
          return JSON.stringify(report)
        },
        runPlaywright:async(args,{env})=>{
          const selected=chosen(args),group=selected[0].file
          const attempt=(attempts.get(group)??0)+1
          attempts.set(group,attempt)
          const failures=attempt===1?failed:again
          calls.push({keys:selected.map(key),args,env,list:readFileSync(args[args.indexOf('--test-list')+1],'utf8')})
          assert.equal(env.PW_RETRIES,'0')
          assert.ok(args.includes('--workers=1'))
          assert.ok(args.includes('--retries=0'))
          assert.ok(args.includes('--forbid-only'))
          assert.equal(env.FIXTURE_ROOT.endsWith('/fixture'),true)
          writeFileSync(env.PLAYWRIGHT_JSON_OUTPUT_FILE,JSON.stringify(nativeReport(selected,row=>failures.some(candidate=>key(candidate)===key(row))?'failed':'passed')))
          return {code:retryInterrupted&&attempt===2?130:selected.some(row=>failures.some(candidate=>key(candidate)===key(row)))?1:0}
        },
      })
    return {code,report,calls,collections,logs}
  }
  await t.test('browser retry selects spec:line/title/project cases and escapes Actions commands in titles',async()=>{
    const {code,report,calls,collections,logs}=await simulate()
    assert.equal(code,0)
    assert.deepEqual(calls.map(call=>call.keys),[[key(rows[0]),key(rows[1])],[key(rows[2]),key(rows[3])],[key(rows[0])],[key(rows[2])]])
    assert.deepEqual(collections,calls.map(call=>call.keys),'every initial/retry selection is verified natively')
    assert.ok(calls[2].list.includes('a.spec.ts:11'))
    assert.ok(calls[2].args.at(-1).endsWith('/a-retry'),'retry artifacts cannot overwrite the first attempt')
    assert.deepEqual(report.flakeLedger.map(entry=>entry.key),[key(rows[0]),key(rows[2])])
    const warnings=logs.filter(line=>line.startsWith('::warning'))
    assert.equal(warnings.length,2)
    assert.ok(warnings[0].includes('%25%0A::error::title'))
    assert.ok(warnings.every(line=>!line.includes('\n')))
  })
  await t.test('browser retry respects other events, the cap, repeat failures and quarantine ownership',async()=>{
    for(const event of ['pull_request','push','schedule','workflow_dispatch']) {
      const result=await simulate({event})
      assert.equal(result.code,1)
      assert.equal(result.calls.length,2)
      assert.deepEqual(result.report.flakeLedger,[])
    }
    const capped=await simulate({failed:rows})
    assert.equal(capped.code,1)
    assert.equal(capped.calls.length,2)
    const repeated=await simulate({again:[rows[0]]})
    assert.equal(repeated.code,1)
    assert.equal(repeated.report.classes['GATED-FULL'].failed,1)
    const known={version:1,entries:[{key:key(rows[0]),owner:'AEON-676'}]}
    const quarantined=await simulate({failed:[rows[0]],again:[rows[0]],known})
    assert.equal(quarantined.code,0)
    assert.equal(quarantined.report.classes['GATED-FULL'].quarantined,1)
    assert.equal(quarantined.report.flakeLedger[0].owner,'AEON-676')
    assert.ok(quarantined.logs.some(line=>line.startsWith('::warning')&&line.includes('AEON-676')))
    for(const options of [{reportErrors:true,known},{automaticRetries:true},{missing:true},{interrupted:true,known}]) {
      const result=await simulate(options)
      assert.notEqual(result.code,0)
      assert.equal(result.calls.length,options.interrupted?1:2,'invalid first-pass evidence cannot trigger retries; interruption stops later batches')
      assert.ok(!result.report.flakeLedger.some(entry=>entry.status==='flaky'))
    }
    const cancelled=await simulate({retryInterrupted:true})
    assert.notEqual(cancelled.code,0)
    assert.equal(cancelled.calls.length,3,'an interrupted retry must stop later retries')
    assert.deepEqual(cancelled.report.flakeLedger,[])
    await assert.rejects(simulate({collectionMismatch:true}),/Browser selection mismatch/)
  })
  await t.test('Node and Vitest units retry only the failed native name in merge_group',async()=>{
    for(const kind of ['node','vitest']) for(const event of ['merge_group','pull_request']) {
      const file=kind==='node'?'tests/fixture.test.ts':'tests/fixture.unit.test.ts'
      const unitRows=['failed (case)','passed sibling'].map(name=>({kind,file,name,tier:'GATED-FULL',active:true}))
      const calls=[]
      let report
      const code=await run('web',{tests:unitRows,all:unitRows,full:true,scope:'gated-full'},
        {unit:true,job:`aeon980-${kind}-fixture`,env:{...identity,GITHUB_EVENT_NAME:event}}, {
          knownFlaky:noFlaky,saveJSON:(_path,value)=>{report=value},log:()=>{},
          execute:(_bin,args)=>{
            const flag=kind==='node'?'--test-name-pattern':'--testNamePattern'
            const selected=unitRows.filter(row=>new RegExp(args[args.indexOf(flag)+1]).test(row.name))
            const first=calls.length===0
            calls.push(selected.map(key))
            const results=selected.map(row=>({row,status:first&&row===unitRows[0]?'failed':'passed'}))
            if(kind==='vitest') writeFileSync(args.find(arg=>arg.startsWith('--outputFile=')).slice('--outputFile='.length),
              JSON.stringify({testResults:[{assertionResults:results.map(({row,status})=>({title:row.name,ancestorTitles:[],status,failureMessages:[]}))}]}))
            return {code:first?1:0,stderr:'',output:kind==='vitest'?'':results.map(({row,status})=>JSON.stringify({type:status==='failed'?'test:fail':'test:pass',fullName:row.name})).join('\n')}
          },
        })
      assert.equal(code,event==='merge_group'?0:1,`${kind} ${event}`)
      assert.deepEqual(calls,event==='merge_group'?[[key(unitRows[0]),key(unitRows[1])],[key(unitRows[0])]]:[[key(unitRows[0]),key(unitRows[1])]])
      assert.equal(report.flakeLedger.length,event==='merge_group'?1:0)
    }
  })
})

// Risk: a changed heavy area can overload the former fixed four shards even
// while remaining below the selector's case-count fan-out guard.
test('AEON-1389 web shard sizing keeps small essential at four and reserves timeout headroom',()=>{
  const rows=Array.from({length:8},(_,i)=>({kind:'browser',file:`tests/size-${i}.spec.ts`,name:`case-${i}`,tier:'ESSENTIAL'}))
  const policy=seconds=>({groups:[{specs:rows.map(row=>({file:row.file,weightSeconds:seconds}))}]})
  const small=webShardPlan(rows,policy(10))
  assert.deepEqual(small.shards,[1,2,3,4])
  const heavy=webShardPlan(rows,policy(600))
  assert.ok(heavy.shards.length>4)
  for(const plan of [small,heavy]) {
    assert.equal(plan.timeoutSeconds,1200)
    assert.equal(plan.budgetSeconds,900)
    assert.ok(plan.projectedMaxSeconds<=plan.budgetSeconds)
    assert.equal(plan.projections.reduce((sum,bin)=>sum+bin.cases,0),rows.length)
    assert.equal(plan.projections.reduce((sum,bin)=>sum+bin.seconds-plan.overheadSeconds,0),rows.length*(plan===small?10:600))
  }
  const impossible=webShardPlan(rows,policy(1000))
  assert.equal(impossible.fitsBudget,false,'an indivisible spec cannot be made cheap by extra bins')
  assert.equal(impossible.shards.length,fullWebShardCount)
  assert.throws(()=>webShardPlan(rows,{groups:[]}),/Missing web shard weight/)
  const decision=sizeWebDecision({mode:'essential',layout:'full'},['web/tests/size-0.spec.ts'],{
    tests:rows,policy:policy(1000),graph:Object.fromEntries(rows.map(row=>[row.file,[]]))})
  assert.equal(decision.mode,'full','an over-budget essential projection must widen to the full gate')
})

test('AEON-1389 PR 684 diff plans more web shards within budget without losing selected cases',()=>{
  const fixture=JSON.parse(readFileSync(new URL('./pr-684-diff.json',import.meta.url)))
  const tests=JSON.parse(readFileSync(new URL('../ci/web-test-tiers.json',import.meta.url))).tests
  const policy=loadWebPolicy()
  const graph=webGraph(fileURLToPath(new URL('../../web',import.meta.url)))
  const candidate=schedulingDecision('pull_request',fixture.paths,()=>true,{graph})
  assert.equal(candidate.mode,'essential','replay must exercise the formerly overloaded lane')
  const planned=sizeWebDecision(candidate,fixture.paths,{tests,policy,graph})
  assert.ok(planned.webShards.length>4)
  assert.ok(planned.webShardSizing.projectedMaxSeconds<=900)
  assert.ok(planned.webShardSizing.projectedMaxSeconds<20*60)
  const loads=planned.webShardSizing.projections.map(bin=>bin.seconds).sort((a,b)=>a-b)
  const middle=Math.floor(loads.length/2),median=loads.length%2?loads[middle]:(loads[middle-1]+loads[middle])/2
  assert.ok(loads.at(-1)/median<=1.25,'concept-v2 longest/median bound')
  const selected=select(tests,{event:'pull_request',paths:fixture.paths,webImports:graph}).tests.filter(row=>row.kind==='browser')
  const weights=tierWeights(policy,selected)
  const assigned=planned.webShards.flatMap(index=>shard(selected,index,planned.webShards.length,weights))
  assert.deepEqual(assigned.map(key).sort(),selected.map(key).sort(),'every selected registration executes once')
  const full=fullWebShards
  for(const event of ['merge_group','push','schedule']) {
    const decision=sizeWebDecision(schedulingDecision(event,fixture.paths,()=>true),fixture.paths,{event,tests,policy,graph})
    assert.equal(decision.mode,'full')
    assert.deepEqual(decision.webShards??full,full)
  }
})
