// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { execFileSync } from 'node:child_process'
import { plan, load } from './cli.mjs'
import { root } from './collect.mjs'
import { pathsBetween, sourceTree } from './diff.mjs'
import { affectedSelection, diffFromBase, formatSelection, main, offlineExec, parseArgs } from './affected.mjs'

const replay=JSON.parse(readFileSync(new URL('./affected-replay.json',import.meta.url)))
const imports=sourceTree(root).imports
const sorted=values=>[...new Set(values)].sort()

// Risk: a worker's file list can miss tests chosen by CI, including essential
// siblings, reverse dependencies, or the separate Go timing lane.
function ciSelection(paths) {
  const options={env:{},event:'pull_request',paths,affectedLane:'on'}
  const select=(kind,flags={})=>plan(kind,{...options,...flags,inventory:{tests:load(kind).tests,imports}})
  const browser=select('web'),unit=select('web',{unit:true}),go=select('go'),timing=select('go',{timing:true})
  const full=[browser,unit,go,timing].find(selection=>selection.full)
  if(full)return {mode:'full',layout:full.layout,reason:full.decision.mode==='full'?full.decision.reason:full.reason}
  return {mode:browser.decision.mode,layout:browser.layout,reason:browser.decision.reason,
    web:{browser:sorted(browser.tests.map(row=>row.file)),unit:sorted(unit.tests.map(row=>row.file))},
    go:sorted([...go.tests,...timing.tests].map(row=>row.package))}
}

for(const number of [235,283,291]) {
  test(`AEON-1247 printed affected selection equals CI plan for recorded PR ${number}`,()=>{
    const {paths}=replay.prs.find(row=>row.number===number)
    const expected=ciSelection(paths),output=[]
    assert.equal(main(['--json'],{diff:()=>({paths}),log:line=>output.push(line)}),0)
    assert.deepEqual(JSON.parse(output.join('\n')),expected)
    const text=formatSelection(expected)
    if(expected.mode==='full') {
      assert.equal(text,`FULL: ${expected.reason}`)
      return
    }
    for(const files of [expected.web.browser,expected.web.unit,expected.go]) {
      assert.ok(files.length>0,'essential owners remain present')
      for(const file of files)assert.ok(text.split('\n').includes(`  ${file}`))
    }
    if(number===235)assert.ok(expected.web.browser.includes('tests/approval-feedback.spec.ts'))
    if(number===283)assert.ok(expected.go.includes('internal/importer'))
  })
}

test('AEON-1247 FULL fallback prints the planner reason without any test lists',()=>{
  const paths=['go.mod'],output=[]
  const ci=plan('web',{env:{},event:'pull_request',paths,affectedLane:'on',inventory:{tests:load('web').tests}})
  assert.equal(ci.full,true)
  assert.equal(main([],{diff:()=>({paths}),log:line=>output.push(line)}),0)
  assert.deepEqual(output,[`FULL: ${ci.decision.reason}`])
  assert.deepEqual(affectedSelection(paths),{mode:'full',layout:'full',reason:ci.decision.reason})
})

test('AEON-1247 docs-only output retains CI essentials and static layout',()=>{
  const paths=['docs/RELEASE.md'],expected=ciSelection(paths)
  assert.equal(expected.mode,'essential')
  assert.equal(expected.layout,'static')
  assert.deepEqual(affectedSelection(paths),expected)
  const web=affectedSelection(paths,{kind:'web'}),go=affectedSelection(paths,{kind:'go'})
  assert.deepEqual(web.web,expected.web)
  assert.equal(Object.hasOwn(web,'go'),false)
  assert.deepEqual(go.go,expected.go)
  assert.equal(Object.hasOwn(go,'web'),false)
})

test('AEON-1247 reads committed HEAD against the merge base without writes or network',t=>{
  const directory=mkdtempSync(resolve(tmpdir(),'aeon-1247-'))
  t.after(()=>rmSync(directory,{recursive:true,force:true}))
  const exec=(bin,args,options)=>offlineExec(bin,args,{...options,cwd:directory})
  const git=(...args)=>execFileSync('git',args,{cwd:directory,encoding:'utf8',timeout:30_000})
  git('init','-q')
  git('config','user.email','fixture@example.invalid');git('config','user.name','Fixture')
  const commit=message=>{git('add','.');git('-c','core.hooksPath=/dev/null','-c','commit.gpgsign=false','commit','-qm',message)}
  writeFileSync(resolve(directory,'README.md'),'base\n');commit('base')
  const base=git('rev-parse','HEAD').trim()
  git('checkout','-qb','worker')
  mkdirSync(resolve(directory,'docs'))
  writeFileSync(resolve(directory,'docs/worker.md'),'worker\n');commit('worker')
  const head=git('rev-parse','HEAD').trim()
  git('checkout','-qb','base',base)
  mkdirSync(resolve(directory,'docs'),{recursive:true})
  writeFileSync(resolve(directory,'docs/base.md'),'base branch\n');commit('base advance')
  git('checkout','-q','worker')
  writeFileSync(resolve(directory,'README.md'),'uncommitted\n')
  const before=git('status','--porcelain')
  assert.deepEqual(diffFromBase('base',{exec}),{base,paths:['docs/worker.md']})
  assert.equal(git('rev-parse','HEAD').trim(),head)
  assert.equal(git('status','--porcelain'),before)
  assert.deepEqual(diffFromBase('missing-base',{exec}),{paths:undefined})
  assert.throws(()=>offlineExec('gh',['api','anything']),/only local Git reads/)
  assert.throws(()=>offlineExec('git',['fetch','origin']),/only local Git reads/)
})

test('AEON-1247 bounded diffs and missing offline coverage fall back to FULL',()=>{
  for(const output of ['docs/RELEASE.md','../outside\0','docs/line\nname\0','x'.repeat(2*1024*1024+1)]) {
    assert.equal(pathsBetween('a'.repeat(40),{exec:()=>output}),undefined)
  }
  assert.deepEqual(pathsBetween('a'.repeat(40),{exec:()=>''}),[])
  const result=affectedSelection(['web/src/lib/ticketLinks.ts'])
  assert.equal(result.mode,'full')
  assert.match(result.reason,/browser-impact map unavailable or stale/)
  assert.equal(Object.hasOwn(result,'web'),false)
  assert.equal(affectedSelection(undefined).mode,'full')
})

test('AEON-1247 CLI flags are strict and ambient CI decisions do not change its selection',()=>{
  assert.deepEqual(parseArgs([]),{base:'origin/main',kind:'all',json:false})
  assert.deepEqual(parseArgs(['--kind','web','--base','HEAD','--json']),{base:'HEAD',kind:'web',json:true})
  for(const args of [['--wat'],['--base'],['--kind','node'],['--base','--json'],['--json','--json'],['--kind','web','--kind','go']])assert.throws(()=>parseArgs(args),/Usage:/)
  const prior={mode:process.env.AEON_TEST_TIER_MODE,layout:process.env.AEON_TEST_TIER_LAYOUT}
  try {
    process.env.AEON_TEST_TIER_MODE='full';process.env.AEON_TEST_TIER_LAYOUT='full'
    assert.equal(affectedSelection(['docs/RELEASE.md']).mode,'essential')
  } finally {
    for(const [name,value] of [['AEON_TEST_TIER_MODE',prior.mode],['AEON_TEST_TIER_LAYOUT',prior.layout]]) {
      if(value===undefined)delete process.env[name];else process.env[name]=value
    }
  }
})
