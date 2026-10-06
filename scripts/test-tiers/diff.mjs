// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync, existsSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { command, root } from './collect.mjs'
import { uncertain, reverseDependants, webGraph } from './core.mjs'

export function changedPaths(event, env=process.env, { fetchBase=false, exec=command }={}) {
  if (!['pull_request','merge_group'].includes(event)) return undefined
  try {
    const payload=JSON.parse(readFileSync(env.GITHUB_EVENT_PATH,'utf8'))
    const base=payload.pull_request?.base?.sha ?? payload.merge_group?.base_sha
    if (!/^[a-f0-9]{40}$/.test(base??'')) return undefined
    // The small planner checkout needs only HEAD and the exact event base, not
    // the complete repository history. Other jobs retain their own checkout.
    if (fetchBase) exec('git',['fetch','--no-tags','--depth=1','origin',base],{timeout:30_000})
    return exec('git',['diff','--name-only','-z','--no-renames',base,'HEAD'],{timeout:30_000}).split('\0').filter(Boolean)
  } catch { return undefined }
}

export function schedulingMode(event,paths,exists=path=>existsSync(resolve(root,path)),{mgReuse}={}) {
  if(event==='merge_group'&&mgReuse==='on')return 'full'
  if(!['pull_request','merge_group'].includes(event)||!Array.isArray(paths)) return 'full'
  for(const path of paths) {
    if(/^(?:docs\/|README(?:\.|$)|LICENSE(?:\.|$)|CHANGELOG(?:\.|$))/.test(path))continue
    if(uncertain(path)||!exists(path)||!/^(?:(?:internal|cmd)\/|web\/(?:src|tests|e2e)\/)/.test(path))return 'full'
    if(path.startsWith('web/')&&!/\.(?:ts|js|mjs|vue|json|css)$/.test(path))return 'full'
  }
  const webChanges=paths.filter(path=>path.startsWith('web/')).map(path=>path.slice(4))
  if(webChanges.length) {
    const graph=webGraph(resolve(root,'web'))
    const impacted=reverseDependants(new Set(webChanges),graph)
    if(webChanges.some(file=>file.startsWith('src/'))&&![...impacted].some(file=>/^tests\/.*\.(?:spec|test)\.ts$/.test(file)))return 'full'
    // Large mapped fan-outs use the old full layout too. The tier selector
    // still records the exact essential/changed union within those runners.
    const manifest=JSON.parse(readFileSync(resolve(root,'scripts/ci/web-test-tiers.json'),'utf8'))
    if(manifest.tests.filter(row=>row.kind==='browser'&&impacted.has(row.file)).length>300)return 'full'
  }
  return 'essential'
}

export function main(env=process.env) {
  const paths=changedPaths(env.GITHUB_EVENT_NAME,env,{fetchBase:true})
  const mode=schedulingMode(env.GITHUB_EVENT_NAME,paths,undefined,{mgReuse:env.CI_MG_REUSE})
  if(!env.GITHUB_OUTPUT)throw new Error('Missing Actions output path')
  writeFileSync(env.GITHUB_OUTPUT,`mode=${mode}\n`,{flag:'a'})
  console.log(`Test tier scheduling: ${mode}; changed paths ${paths?.length??'unavailable'}`)
  return 0
}
if(process.argv[1]&&resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try { process.exitCode=main() } catch(error) { console.error(error.message);process.exitCode=1 }
}
