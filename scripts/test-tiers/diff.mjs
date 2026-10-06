// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync, existsSync, writeFileSync, mkdtempSync, realpathSync, statSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { command, root } from './collect.mjs'
import { reverseDependants, webGraph, impactRisk, validImpactPaths } from './core.mjs'

export function changedPaths(event, env=process.env, { fetchBase=false, exec=command }={}) {
  if (!['pull_request','merge_group'].includes(event)) return undefined
  try {
    if(statSync(env.GITHUB_EVENT_PATH).size>2*1024*1024)return undefined
    const payload=JSON.parse(readFileSync(env.GITHUB_EVENT_PATH,'utf8'))
    const base=payload.pull_request?.base?.sha ?? payload.merge_group?.base_sha
    if (!/^[a-f0-9]{40}$/.test(base??'')) return undefined
    // The small planner checkout needs only HEAD and the exact event base, not
    // the complete repository history. Other jobs retain their own checkout.
    if (fetchBase) exec('git',['fetch','--no-tags','--depth=1','origin',base],{timeout:30_000})
    const output=exec('git',['diff','--name-only','-z','--no-renames',base,'HEAD'],{timeout:30_000})
    if(Buffer.byteLength(output)>2*1024*1024||(output&&!output.endsWith('\0')))return undefined
    const paths=output.split('\0').filter(Boolean)
    return validImpactPaths(paths)?paths:undefined
  } catch { return undefined }
}

export function schedulingDecision(event,paths,exists=path=>existsSync(resolve(root,path)),{affectedLane,graph}={}) {
  const full=reason=>({mode:'full',reason})
  if(event!=='pull_request'||!Array.isArray(paths)) return full('full event or missing diff')
  if(!validImpactPaths(paths))return full('invalid or oversized impact diff')
  const webChanges=paths.filter(path=>path.startsWith('web/')).map(path=>path.slice(4))
  if(webChanges.length&&affectedLane==='on') graph??=webGraph(resolve(root,'web'))
  const risk=impactRisk(paths,{event,affectedLane,webImports:graph})
  if(risk.full)return full(risk.reasons.join('; ')||'full event or uncertain impact')
  for(const path of paths) {
    if(/^(?:docs\/|README(?:\.|$)|LICENSE(?:\.|$)|CHANGELOG(?:\.|$))/.test(path))continue
    if(!exists(path)||!/^(?:(?:internal|cmd)\/|web\/(?:src|tests|e2e)\/)/.test(path))return full(`unmapped or deleted input: ${path}`)
    if(path.startsWith('web/')&&!/\.(?:ts|js|mjs|vue|json|css)$/.test(path))return full(`unsupported web input: ${path}`)
  }
  if(webChanges.length) {
    graph??=webGraph(resolve(root,'web'))
    const impacted=reverseDependants(new Set(webChanges),graph)
    if(affectedLane==='on'&&webChanges.some(file=>!Object.hasOwn(graph,file)))return full('missing trusted web dependency metadata')
    if(webChanges.some(file=>file.startsWith('src/'))&&![...impacted].some(file=>/^tests\/.*\.(?:spec|test)\.ts$/.test(file)))return full('web module has no mapped test importer')
    // Large mapped fan-outs use the old full layout too. The tier selector
    // still records the exact essential/changed union within those runners.
    const manifest=JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json'),'utf8'))
    if(manifest.tests.filter(row=>row.kind==='browser'&&impacted.has(row.file)).length>300)return full('browser fan-out exceeds 300 cases')
  }
  return {mode:'essential',reason:risk.reasons.join('; ')||'essential plus changed area and reverse dependencies'}
}

export function schedulingMode(event,paths,exists,options) {
  return schedulingDecision(event,paths,exists,options).mode
}

// Snapshot the trusted planner AND graph inputs. An incomplete/old base copy
// fails closed; never import candidate classifier code to enable narrowing.
export function trustedSchedulingDecision(base, paths, env=process.env, {cwd=root}={}) {
  if(!/^[a-f0-9]{40}$/.test(base??''))throw new Error('Invalid trusted planner base')
  if(!validImpactPaths(paths))throw new Error('Invalid trusted paths')
  const directory=realpathSync(mkdtempSync(resolve(tmpdir(),'aeon-trusted-tier-')))
  const archive=execFileSync('git',['archive',base,'scripts/test-tiers','scripts/ci/web-test-tiers.json','web/src','web/tests','web/e2e'],
    {cwd,timeout:30_000,maxBuffer:64*1024*1024})
  execFileSync('tar',['-x','-C',directory],{input:archive,timeout:30_000,maxBuffer:2*1024*1024})
  const output=execFileSync(process.execPath,[resolve(directory,'scripts/test-tiers/diff.mjs'),'--trusted-paths'],
    {cwd:directory,encoding:'utf8',timeout:30_000,maxBuffer:2*1024*1024,
      env:{...env,GITHUB_OUTPUT:'',GITHUB_STEP_SUMMARY:'',AEON_TIER_PATHS:JSON.stringify(paths),AEON_TIER_CHECKOUT:cwd}})
  const decision=JSON.parse(output)
  if(!['full','essential'].includes(decision.mode)||typeof decision.reason!=='string')throw new Error('Invalid trusted planner decision')
  return decision
}

export function main(env=process.env) {
  const paths=env.GITHUB_EVENT_NAME==='pull_request'?changedPaths(env.GITHUB_EVENT_NAME,env,{fetchBase:true}):undefined
  let decision
  try {
    if(env.GITHUB_EVENT_NAME==='pull_request'&&env.CI_AFFECTED_LANE==='on'&&Array.isArray(paths)) {
      const payload=JSON.parse(readFileSync(env.GITHUB_EVENT_PATH,'utf8'))
      decision=trustedSchedulingDecision(payload.pull_request?.base?.sha,paths,env)
    } else decision=schedulingDecision(env.GITHUB_EVENT_NAME,paths)
  } catch { decision={mode:'full',reason:'trusted affected classification unavailable'} }
  const {mode,reason}=decision
  if(!env.GITHUB_OUTPUT)throw new Error('Missing Actions output path')
  writeFileSync(env.GITHUB_OUTPUT,`mode=${mode}\n`,{flag:'a'})
  console.log(`Test tier scheduling: ${mode}; ${reason}; changed paths ${paths?.length??'unavailable'}`)
  if(env.GITHUB_STEP_SUMMARY)writeFileSync(env.GITHUB_STEP_SUMMARY,`Test tier scheduling: ${mode}; ${reason}\n`,{flag:'a'})
  return 0
}
if(process.argv[1]&&resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try {
    if(process.argv[2]==='--trusted-paths') {
      const paths=JSON.parse(process.env.AEON_TIER_PATHS)
      if(!validImpactPaths(paths))throw new Error('Invalid trusted paths')
      console.log(JSON.stringify(schedulingDecision(process.env.GITHUB_EVENT_NAME,paths,
        path=>existsSync(resolve(process.env.AEON_TIER_CHECKOUT,path)),{affectedLane:process.env.CI_AFFECTED_LANE})))
    } else process.exitCode=main()
  } catch(error) { console.error(error.message);process.exitCode=1 }
}
