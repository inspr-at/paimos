// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'
import { goFailures, nodeFailures, vitestFailures, printFailures, outputTail } from '../../scripts/test-tiers/failures.mjs'
import { goOutcomes, reportCases } from '../../scripts/test-tiers/report.mjs'
import reporter from '../../scripts/test-tiers/node-reporter.mjs'

function childEnvironment(summary: string) {
  const env={...process.env,GITHUB_STEP_SUMMARY:summary}
  // A fresh runner subprocess must not inherit Node's recursive-test sentinel.
  delete env.NODE_TEST_CONTEXT
  return env
}

// Ordinary CI diagnostics regressions: NIGHTLY, also selected when this area changes.
test('native Go enumeration reports package failure output and retains a failing exit', () => {
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-go-list-failure-'))
  const manifest=fileURLToPath(new URL('../../scripts/ci/go-test-tiers.json',import.meta.url))
  const cli=fileURLToPath(new URL('../../scripts/test-tiers/cli.mjs',import.meta.url))
  writeFileSync(resolve(directory,'go'),`#!${process.execPath}
const fs=require('node:fs');
if(process.argv.includes('run')) {
  const tests=JSON.parse(fs.readFileSync(${JSON.stringify(manifest)},'utf8')).tests.map(row=>({...row,active:true}));
  console.log(JSON.stringify({tests,imports:{}}));
} else {
  console.log(JSON.stringify({Action:'build-output',ImportPath:'github.com/inspr-at/paimos/internal/proof',Output:'enumeration fixture failure\\n'}));
  console.log(JSON.stringify({Action:'build-fail',ImportPath:'github.com/inspr-at/paimos/internal/proof'}));
  process.exitCode=6;
}
`,{mode:0o700})
  const env=childEnvironment(resolve(directory,'summary.txt'))
  env.PATH=`${directory}:${env.PATH}`
  const result=spawnSync(process.execPath,[cli,'check','go'],{env,encoding:'utf8',timeout:30_000})
  assert.equal(result.error,undefined)
  assert.equal(result.status,1,result.stderr)
  assert.match(result.stdout,/FAIL go: "internal\/proof" — "\(package\)"/)
  assert.match(result.stdout,/enumeration fixture failure/)
  assert.match(result.stderr,/failed \(6\)/)
})

test('failed Go jsonl names the package and test with the last 40 lines in logs and summary', () => {
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-tier-failure-'))
  const path=resolve(directory,'go.jsonl'),summary=resolve(directory,'summary.txt')
  const Package='github.com/inspr-at/paimos/internal/proof',Test='TestProof'
  const events=[{Action:'run',Package,Test},
    {Action:'output',Package,Test:'TestOther',Output:'unrelated output\n'},
    ...Array.from({length:50},(_,i)=>({Action:'output',Package,Test,Output:`proof line ${i}\n`})),
    {Action:'fail',Package,Test},{Action:'fail',Package}]
  writeFileSync(path,events.map(event=>JSON.stringify(event)).join('\n')+'\n')
  writeFileSync(summary,'previous summary\n')
  const jsonl=readFileSync(path,'utf8'),logs: string[]=[]
  const failures=goFailures(jsonl)
  assert.deepEqual(failures,[{kind:'go',owner:'internal/proof',name:Test,
    output:Array.from({length:40},(_,i)=>`proof line ${i+10}`).join('\n')}])
  printFailures(failures,{summary,log:(text: string)=>logs.push(text)})
  const markdown=readFileSync(summary,'utf8')
  for(const text of [logs.join('\n'),markdown]) {
    assert.match(text,/FAIL go: "internal\/proof" — "TestProof"/)
    assert.match(text,/proof line 10\n/)
    assert.match(text,/proof line 49/)
    assert.doesNotMatch(text,/proof line 9\n|unrelated output/)
  }
  assert.ok(markdown.startsWith('previous summary\n'))
  const outcomes=goOutcomes(jsonl)
  assert.deepEqual(outcomes,[{key:'internal/proof:TestProof',status:'failed',started:true}])
  const row={kind:'go',package:'internal/proof',name:Test,tier:'NIGHTLY',active:true}
  assert.equal(reportCases([row],outcomes,1,'fixture').classes.NIGHTLY.failed,1)
})

test('Go subtest diagnostics retain ancestry and package-only failures keep their output', () => {
  const events=[
    {Action:'output',Package:'p',Test:'TestParent/child',Output:'child assertion\n'},
    {Action:'fail',Package:'p',Test:'TestParent/child'},
    {Action:'fail',Package:'p',Test:'TestParent'},
    {Action:'fail',Package:'p'},
    {Action:'output',Package:'build',Output:'compile error\n'},
    {Action:'fail',Package:'build'},
    {Action:'pass',Package:'pass',Test:'TestPass'},
  ]
  assert.deepEqual(goFailures(events.map(event=>JSON.stringify(event)).join('\n')),[
    {kind:'go',owner:'p',name:'TestParent/child',output:'child assertion'},
    {kind:'go',owner:'p',name:'TestParent',output:'child assertion'},
    {kind:'go',owner:'build',name:'(package)',output:'compile error'},
  ])
})

test('Node reporter preserves assertion details and console output without changing terminal events', async () => {
  const error=Object.assign(new Error('expected scoped value'),{actual:'wrong',expected:'right'})
  async function* events() {
    yield {type:'test:enqueue',data:{testId:1,name:'scope',nesting:0}}
    yield {type:'test:stdout',data:{file:'scope.ts',message:'console evidence\n'}}
    yield {type:'test:start',data:{testId:1,name:'scope',nesting:0}}
    yield {type:'test:fail',data:{testId:1,name:'scope',nesting:0,details:{error}}}
  }
  let jsonl=''
  for await(const line of reporter(events()))jsonl+=line
  const failures=nodeFailures(jsonl,'tests/scope.test.ts')
  assert.equal(failures.length,1)
  assert.equal(failures[0].name,'scope')
  assert.equal(failures[0].owner,'tests/scope.test.ts')
  assert.match(failures[0].output,/console evidence/)
  assert.match(failures[0].output,/expected scoped value/)
  assert.match(failures[0].output,/actual: 'wrong'/)
  assert.deepEqual(jsonl.trim().split('\n').map(line=>JSON.parse(line).type),['test:stdout','test:start','test:fail'])
})

test('Node tier runner logs a real failure once and retains failure accounting and exit code', () => {
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-node-tier-failure-'))
  const file=resolve(directory,'fixture.test.mjs'),summary=resolve(directory,'summary.txt'),calls=resolve(directory,'calls.txt')
  writeFileSync(file,`import test from 'node:test'; import assert from 'node:assert/strict'; import {appendFileSync} from 'node:fs';
test('failure fixture',()=>{appendFileSync(${JSON.stringify(calls)},'once\\n'); console.log('captured fixture output'); assert.equal('actual fixture value','expected fixture value')})`)
  const row={kind:'node',file,name:'failure fixture',tier:'NIGHTLY'}
  const cli=new URL('../../scripts/test-tiers/cli.mjs',import.meta.url).href
  const script=`import {run} from ${JSON.stringify(cli)}; process.exitCode=await run('web',${JSON.stringify({tests:[row],all:[row]})},{unit:true,job:${JSON.stringify(directory.split('/').at(-1))}})`
  const result=spawnSync(process.execPath,['--input-type=module','-e',script],{
    env:childEnvironment(summary),encoding:'utf8',timeout:30_000,
  })
  assert.equal(result.error,undefined)
  assert.equal(result.status,1,result.stderr)
  assert.equal(readFileSync(calls,'utf8'),'once\n')
  for(const text of [result.stdout,readFileSync(summary,'utf8')]) {
    assert.match(text,/FAIL node:/)
    assert.ok(text.includes(JSON.stringify(file)))
    assert.match(text,/failure fixture/)
    assert.match(text,/captured fixture output/)
    assert.match(text,/actual fixture value/)
    assert.match(text,/expected fixture value/)
  }
  const measurement=JSON.parse(result.stdout.trim().split('\n').at(-1)!)
  assert.equal(measurement.exitCode,1)
  assert.equal(measurement.classes.NIGHTLY.failed,1)
  assert.equal(measurement.classes.NIGHTLY.run,1)
})

test('Go tier runner consumes one failing jsonl execution and preserves the original exit code', () => {
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-go-tier-failure-'))
  const summary=resolve(directory,'summary.txt'),calls=resolve(directory,'calls.txt')
  const Package='github.com/inspr-at/paimos/internal/proof',Test='TestProof'
  const events=[{Action:'run',Package,Test},{Action:'output',Package,Test,Output:'Go fixture assertion\n'},
    {Action:'fail',Package,Test},{Action:'fail',Package}]
  const jsonl=events.map(event=>JSON.stringify(event)).join('\n')+'\n'
  writeFileSync(resolve(directory,'fixture.jsonl'),jsonl)
  writeFileSync(resolve(directory,'go'),`#!${process.execPath}
const fs=require('node:fs');
if(process.argv.includes('-list')){console.log('TestProof');process.exit(0)}
fs.appendFileSync(${JSON.stringify(calls)},'once\\n');
process.stdout.write(fs.readFileSync(${JSON.stringify(resolve(directory,'fixture.jsonl'))},'utf8'));
process.exitCode=7;
`,{mode:0o700})
  const row={kind:'go',package:'internal/proof',name:Test,tier:'NIGHTLY',active:true}
  const cli=new URL('../../scripts/test-tiers/cli.mjs',import.meta.url).href
  const script=`import {run} from ${JSON.stringify(cli)}; process.exitCode=await run('go',${JSON.stringify({tests:[row],all:[row]})},{job:${JSON.stringify(directory.split('/').at(-1))}})`
  const env=childEnvironment(summary)
  env.PATH=`${directory}:${env.PATH}`
  const result=spawnSync(process.execPath,['--input-type=module','-e',script],{env,encoding:'utf8',timeout:30_000})
  assert.equal(result.error,undefined)
  assert.equal(result.status,7,result.stderr)
  assert.equal(readFileSync(calls,'utf8'),'once\n')
  for(const text of [result.stdout,readFileSync(summary,'utf8')]) {
    assert.match(text,/FAIL go: "internal\/proof" — "TestProof"/)
    assert.match(text,/Go fixture assertion/)
  }
  const measurement=JSON.parse(result.stdout.trim().split('\n').at(-1)!)
  assert.equal(measurement.exitCode,7)
  assert.equal(measurement.classes.NIGHTLY.failed,1)
  assert.equal(measurement.classes.NIGHTLY.run,1)
})

test('Vitest diagnostics retain assertion details and captured stdout stderr within bounds in logs and summary', () => {
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-vitest-output-'))
  const cases=[
    {details:'assertion detail',captured:'captured stdout\ncaptured stderr'},
    {details:'assertion detail',captured:[...Array.from({length:50},(_,i)=>`stdout line ${i}`),'captured stderr'].join('\n')},
    {details:[...Array.from({length:50},(_,i)=>`assertion line ${i}`),'assertion detail'].join('\n'),captured:'captured stdout\ncaptured stderr'},
    {details:`${'a'.repeat(20_000)}\nassertion detail`,captured:`${'c'.repeat(20_000)}\ncaptured stderr`},
  ]
  for(const [index,{details,captured}] of cases.entries()) {
    const report={testResults:[{assertionResults:[
      {ancestorTitles:['scope'],title:'rejects',status:'failed',failureMessages:[details]},
    ]}]}
    const failures=vitestFailures(report,'tests/scope.unit.test.ts',captured)
    assert.equal(failures.length,1)
    assert.ok(failures[0].output.split('\n').length<=40)
    assert.ok(failures[0].output.length<=16_384)
    const summary=resolve(directory,`summary-${index}.txt`),logs: string[]=[]
    printFailures(failures,{summary,log:(text: string)=>logs.push(text)})
    for(const text of [logs.join('\n'),readFileSync(summary,'utf8')]) {
      assert.match(text,/FAIL vitest: "tests\/scope.unit.test.ts" — "scope > rejects"/)
      assert.match(text,/assertion detail/)
      assert.match(text,/captured stderr/)
      if(index===0||index===2)assert.match(text,/captured stdout/)
      if(index===1) {
        assert.match(text,/stdout line 49/)
        assert.doesNotMatch(text,/stdout line 0\n/)
      }
    }
  }
  assert.deepEqual(vitestFailures({testResults:[{assertionResults:[
    {ancestorTitles:[],title:'rejects',status:'failed',failureMessages:[]},
  ]}]},'tests/scope.unit.test.ts','captured stdout\ncaptured stderr'),[
    {kind:'vitest',owner:'tests/scope.unit.test.ts',name:'rejects',output:'captured stdout\ncaptured stderr'},
  ])
})

test('Vitest diagnostics name failed assertions and bounded output cannot escape summary fences', () => {
  const report={testResults:[{assertionResults:[
    {ancestorTitles:['scope'],title:'rejects',status:'failed',failureMessages:['assertion details\n```\n::error::untrusted']},
    {ancestorTitles:['scope'],title:'passes',status:'passed'},
    {ancestorTitles:['scope'],title:'skips',status:'pending'},
  ]}]}
  const failures=vitestFailures(report,'tests/scope.unit.test.ts')
  assert.deepEqual(failures,[{kind:'vitest',owner:'tests/scope.unit.test.ts',name:'scope > rejects',output:'assertion details\n```\n::error::untrusted'}])
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-vitest-failure-')),summary=resolve(directory,'summary.txt'),logs: string[]=[]
  printFailures(failures,{summary,log:(text: string)=>logs.push(text)})
  assert.match(logs[0]!,/  \| ::error::untrusted/)
  const markdown=readFileSync(summary,'utf8')
  assert.match(markdown,/````text\nFAIL vitest:/)
  assert.ok(markdown.endsWith('\n````\n'))
  assert.equal(outputTail('x'.repeat(20_000)).length,16_384)
  const emptyLogs: string[]=[]
  printFailures([],{summary,log:(text: string)=>emptyLogs.push(text)})
  assert.deepEqual(emptyLogs,[])
  assert.equal(readFileSync(summary,'utf8'),markdown)
})

// Count collector invocations instead of asserting a machine-dependent runtime.
test('native planning regression reuses one collected snapshot for unchanged-tree CLI checks', () => {
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-planning-snapshot-'))
  const hook=resolve(directory,'fixture.mjs'),calls=resolve(directory,'collections.txt')
  const collector=new URL('../../scripts/test-tiers/collect.mjs',import.meta.url).href
  const manifest=JSON.parse(readFileSync(new URL('../../scripts/ci/web-test-tiers.json',import.meta.url),'utf8'))
  const policy=JSON.parse(readFileSync(new URL('../ci-web-shards.json',import.meta.url),'utf8'))
  const optional=new Set(policy.groups.filter(group=>group.gate===false).flatMap(group=>group.specs.map(spec=>spec.file)))
  const essential=manifest.tests.find(row=>row.kind==='browser'&&row.tier==='ESSENTIAL')
  const nightly=manifest.tests.find(row=>row.kind==='browser'&&row.tier==='NIGHTLY'&&optional.has(row.file))
  assert.ok(essential);assert.ok(nightly)
  const wrapper=`export * from ${JSON.stringify(`${collector}?collection-count-fixture`)};
import {appendFileSync} from 'node:fs';
export const collectWeb = () => {
  appendFileSync(${JSON.stringify(calls)},'collection\\n');
  return ${JSON.stringify({tests:[essential,nightly]})};
};`
  writeFileSync(hook,`import {registerHooks} from 'node:module';
registerHooks({load(url,context,next) {
  if(url===${JSON.stringify(collector)}) return {format:'module',source:${JSON.stringify(wrapper)},shortCircuit:true};
  return next(url,context);
}});`)
  const name='native full CI planning retains the OPS-257 gate and essential promotions without gating the optional catalogue'
  const result=spawnSync(process.execPath,['--import',hook,'--test','--test-concurrency=1',
    '--test-name-pattern',`^${name}$`,fileURLToPath(new URL('../scripts/aeon-681-ci.test.mjs',import.meta.url))],{
    env:childEnvironment(resolve(directory,'summary.txt')),encoding:'utf8',timeout:30_000,maxBuffer:8*1024*1024,
  })
  assert.equal(result.error,undefined)
  assert.equal(result.status,0,`${result.stdout}\n${result.stderr}`)
  assert.equal(readFileSync(calls,'utf8'),'collection\n','All planner assertions must reuse the one unchanged-tree native snapshot')
})
