// SPDX-License-Identifier: AGPL-3.0-only
// Offline, manifest-level view of CI's affected selection; no native collection.
import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { plan, load } from './cli.mjs'
import { root } from './collect.mjs'
import { pathsBetween, sourceTree } from './diff.mjs'
import { nightlyBrowserMap } from './browser-impact.mjs'

const usage='Usage: node scripts/test-tiers/affected.mjs [--base origin/main] [--kind web|go|all] [--json]'

export function parseArgs(args) {
  const options={base:'origin/main',kind:'all',json:false},seen=new Set()
  for(let i=0;i<args.length;i++) {
    const flag=args[i]
    if(!['--base','--kind','--json'].includes(flag)||seen.has(flag))throw new Error(usage)
    seen.add(flag)
    if(flag==='--json') { options.json=true;continue }
    const value=args[++i]
    if(!value||value.startsWith('-')||value.length>4096||/[\x00-\x1f\x7f]/.test(value))throw new Error(usage)
    options[flag.slice(2)]=value
  }
  if(!['web','go','all'].includes(options.kind))throw new Error(usage)
  return options
}

// Only local Git reads are allowed, including inside the existing coverage
// loader. Its unavailable-evidence fallback keeps browser source edits FULL.
export function offlineExec(bin,args,options={}) {
  if(bin!=='git'||!['merge-base','diff','rev-parse','show'].includes(args[0]))throw new Error('affected planner permits only local Git reads')
  return execFileSync(bin,args,{cwd:root,encoding:'utf8',timeout:30_000,maxBuffer:2*1024*1024,...options,
    stdio:'pipe',env:{...process.env,GIT_NO_LAZY_FETCH:'1',GIT_OPTIONAL_LOCKS:'0'}})
}

export function diffFromBase(revision,{exec=offlineExec}={}) {
  try {
    const base=exec('git',['merge-base','--',revision,'HEAD'],{timeout:30_000,maxBuffer:1024}).trim()
    return {base,paths:pathsBetween(base,{exec})}
  } catch { return {paths:undefined} }
}

export const offlineCoverage=(checkout,base,catalogue)=>nightlyBrowserMap(checkout,base,catalogue,{exec:offlineExec})
const unique=values=>[...new Set(values)].sort()

export function affectedSelection(paths,{base,kind='all'}={}) {
  const imports=sourceTree(root).imports
  const selections=[]
  const select=(target,options={})=>{
    const selection=plan(target,{env:{},event:'pull_request',paths,base,affectedLane:'on',
      // Ignore ambient Actions decisions: this command computes its own plan.
      inventory:{tests:load(target).tests,imports},...options},{browserCoverage:offlineCoverage,exec:offlineExec})
    selections.push(selection)
    return selection
  }
  const files={}
  if(kind!=='go') {
    files.web={browser:unique(select('web').tests.map(row=>row.file)),
      unit:unique(select('web',{unit:true}).tests.map(row=>row.file))}
  }
  if(kind!=='web') {
    files.go=unique([...select('go').tests,...select('go',{timing:true}).tests].map(row=>row.package))
  }
  const full=selections.find(selection=>selection.full)
  const selection=full??selections[0]
  const {decision}=selection
  const reason=full&&decision.mode!=='full'?selection.reason:decision.reason
  const result={mode:full?'full':decision.mode,layout:selection.layout,reason}
  return full?result:{...result,...files}
}

export function formatSelection(result,{json=false}={}) {
  if(json)return JSON.stringify(result,null,2)
  if(result.mode==='full')return `FULL: ${result.reason}`
  const lines=[`mode: ${result.mode}`,`layout: ${result.layout}`,`reason: ${result.reason}`]
  for(const [label,files] of [['web browser specs',result.web?.browser],['web unit tests',result.web?.unit],['go packages',result.go]]) {
    if(files)lines.push(`${label}:`,...(files.length?files.map(file=>`  ${file}`):['  (none)']))
  }
  return lines.join('\n')
}

export function main(args,{diff=diffFromBase,log=console.log}={}) {
  const options=parseArgs(args)
  const {base,paths}=diff(options.base)
  log(formatSelection(affectedSelection(paths,{base,kind:options.kind}),options))
  return 0
}

if(process.argv[1]&&resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try { process.exitCode=main(process.argv.slice(2)) }
  catch(error) { console.error(error.message);process.exitCode=1 }
}
