// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync, existsSync, writeFileSync, mkdtempSync, realpathSync, statSync, readdirSync, lstatSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { tmpdir } from 'node:os'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { command, root } from './collect.mjs'
import { planningWebCases, browserCaseLimit } from './planning-web.mjs'
import { boundedText, inputMetadata, readInput, inputBounds } from './inputs.mjs'
import { reverseDependants, webGraph, unknownWebImport, impactRisk, validImpactPaths, manifestPromotions, tierManifestPattern, key, implicitTier, validateImplicitTier } from './core.mjs'

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

// The PR base commit from the Actions payload; undefined outside PR events.
export function eventBase(env=process.env) {
  try {
    if(env.GITHUB_EVENT_NAME!=='pull_request'||statSync(env.GITHUB_EVENT_PATH).size>2*1024*1024)return undefined
    const base=JSON.parse(readFileSync(env.GITHUB_EVENT_PATH,'utf8')).pull_request?.base?.sha
    return /^[a-f0-9]{40}$/.test(base??'')?base:undefined
  } catch { return undefined }
}

// Go sources and web tests of one checkout, read once per process. Bounded
// walk: the consumer rules only need internal/, cmd/, scripts/ and web/tests.
const trees=new Map()
export function sourceTree(checkout=root, { bounds=inputBounds, read=readInput }={}) {
  const cached=bounds===inputBounds&&read===readInput
  if(cached&&trees.has(checkout))return trees.get(checkout)
  const go=new Map(),webTests=new Map(), files=[]
  let entries=0,totalBytes=0
  const visit=(directory,into,accept,optional=false)=>{
    const absolute=resolve(checkout,directory)
    let stat
    try { stat=lstatSync(absolute) } catch(error) { if(optional&&error.code==='ENOENT')return;throw error }
    if(!stat.isDirectory()||stat.isSymbolicLink())throw new Error('non-directory or symlink in consumer scan')
    for(const entry of readdirSync(absolute,{withFileTypes:true})) {
      if(++entries>=bounds.entries)throw new Error('consumer entry count bound reached')
      const file=`${directory}/${entry.name}`
      if(entry.isSymbolicLink())throw new Error('symlink in consumer scan')
      if(entry.isDirectory()) { if(!['node_modules','testdata','dist'].includes(entry.name)) visit(file,into,accept) }
      else if(accept(file)) {
        const metadata=inputMetadata(checkout,file,bounds.fileBytes)
        if(files.length+1>=bounds.files)throw new Error('consumer file count bound reached')
        totalBytes+=metadata.size
        if(totalBytes>=bounds.totalBytes)throw new Error('consumer total byte bound reached')
        files.push({file,into,metadata})
      }
    }
  }
  let tree
  try {
    if(!statSync(realpathSync(checkout)).isDirectory())throw new Error('missing consumer checkout')
    // Preflight the complete file set and all sizes before the first read.
    for(const directory of ['internal','cmd','scripts']) visit(directory,go,file=>file.endsWith('.go'),true)
    visit('web/tests',webTests,file=>/\.(?:ts|tsx|mts|mjs|js)$/.test(file),true)
    for(const {file,into,metadata} of files)into.set(file,read(metadata,bounds.fileBytes))
    tree={complete:true,go,webTests:new Map([...webTests].map(([file,text])=>[file.slice(4),text]))}
  } catch(error) {
    // Discard every partial result; no consumer rule may use a truncated tree.
    tree={complete:false,reason:error.message,go:new Map(),webTests:new Map()}
  }
  if(cached)trees.set(checkout,tree)
  return tree
}

export const manifestFiles={go:'scripts/ci/go-test-tiers.json',web:'scripts/ci/web-test-tiers.json'}
export function manifestsAt(read) {
  const manifests={}
  for(const [kind,file] of Object.entries(manifestFiles)) {
    const text=read(file)
    if(typeof text==='string') manifests[kind]=JSON.parse(text)
  }
  return manifests
}
// Base manifests come from git (the exact event base is fetched by the
// planner/runners); head manifests from the checkout. Missing data fails closed.
export function promotionsBetween(base,{cwd=root,exec=command,baseDirectory}={}) {
  try {
    const before=manifestsAt(file=>{
      if(baseDirectory!==undefined)return existsSync(resolve(baseDirectory,file))?readFileSync(resolve(baseDirectory,file),'utf8'):undefined
      if(!/^[a-f0-9]{40}$/.test(base??''))return undefined
      try { return exec('git',['show',`${base}:${file}`],{cwd,timeout:30_000}) } catch { return undefined }
    })
    const after=manifestsAt(file=>existsSync(resolve(cwd,file))?readFileSync(resolve(cwd,file),'utf8'):undefined)
    for(const kind of Object.keys(manifestFiles)) if(after[kind]&&!before[kind])return undefined
    return manifestPromotions(before,after)
  } catch { return undefined }
}

// The trusted lightweight planner has no Go compiler or web dependencies.
// Count every Test/Fuzz identifier in its bounded test-source snapshot. This
// includes references/comments/inactive files: an upper bound also handles
// comments between declaration tokens and Unicode Go identifiers safely.
export function planningGoCases(manifest, tree) {
  if (!manifest) return []
  validateImplicitTier(manifest)
  const declared = new Map(manifest.tests.map(row => [key(row), row]))
  const rows = new Map()
  for (const [file, text] of tree?.go ?? []) if (file.endsWith('_test.go')) {
    for (const match of text.matchAll(/(?:Test|Fuzz)[\p{L}\p{N}_]*/gu)) {
      const row = { kind: 'go', package: file.slice(0, file.lastIndexOf('/')), name: match[0], tier: implicitTier }
      rows.set(key(row), { ...row, ...declared.get(key(row)) })
    }
  }
  return [...rows.values()]
}

export function schedulingDecision(event,paths,exists,{affectedLane,graph,checkout=root,base,tree,webTree,promotions,readFile,tests}={}) {
  exists??=path=>existsSync(resolve(checkout,path))
  const full=reason=>({mode:'full',layout:'full',reason})
  if(event!=='pull_request'||!Array.isArray(paths)) return full('full event or missing diff')
  if(!validImpactPaths(paths))return full('invalid or oversized impact diff')
  const webChanges=paths.filter(path=>path.startsWith('web/')).map(path=>path.slice(4))
  const on=affectedLane==='on'
  if(webChanges.length&&on) graph??=webGraph(resolve(root,'web'))
  if(on) {
    tree??=sourceTree(checkout)
    if(promotions===undefined&&paths.some(path=>tierManifestPattern.test(path))) promotions=promotionsBetween(base,{cwd:checkout})
  }
  const manifests=on?manifestsAt(file=>existsSync(resolve(checkout,file))?readFileSync(resolve(checkout,file),'utf8'):undefined):{}
  if (on && !tests?.some(row=>row.kind==='go')) {
    if (!manifests.go) return full('Go tier policy unavailable for fan-out')
    // A web runner's native inventory cannot prove Go consumer counts.
    tests = [...planningGoCases(manifests.go, tree), ...(tests ?? [])]
  }
  tests ??= []
  const risk=impactRisk(paths,{event,affectedLane,webImports:graph,tree,promotions,tests,
    readFile:readFile??(path=>exists(path)?boundedText(checkout,path):undefined)})
  if(risk.full)return full(risk.reasons.join('; ')||'full event or uncertain impact')
  for(const path of paths) {
    if(risk.handled.has(path))continue
    if(/^(?:docs\/|README(?:\.|$)|LICENSE(?:\.|$)|CHANGELOG(?:\.|$))/.test(path))continue
    if(!exists(path)||!/^(?:(?:internal|cmd)\/|web\/(?:src|tests|e2e)\/)/.test(path))return full(`unmapped or deleted input: ${path}`)
    if(path.startsWith('web/')&&!/\.(?:ts|tsx|mts|js|mjs|vue|json|css)$/.test(path))return full(`unsupported web input: ${path}`)
  }
  const webSeeds=[...risk.webSeeds]
  if(webChanges.length||webSeeds.length) {
    graph??=webGraph(resolve(root,'web'))
    const impacted=reverseDependants(new Set([...webChanges.filter(file=>!risk.handled.has(`web/${file}`)),...webSeeds]),graph)
    if(on&&webChanges.some(file=>!risk.handled.has(`web/${file}`)&&!Object.hasOwn(graph,file)))return full('missing trusted web dependency metadata')
    if(webChanges.some(file=>file.startsWith('src/'))&&![...impacted].some(file=>/^tests\/.*\.(?:spec|test)\.ts$/.test(file)))return full('web module has no mapped test importer')
    // Large mapped fan-outs use the old full layout too. The tier selector
    // still records the exact essential/changed union within those runners.
    webTree ??= tree ?? sourceTree(checkout)
    // A registrar can be loaded by an import the graph cannot see. Keep
    // Playwright references even without a spec edge; standalone unit files
    // belong to Node/Vitest, not the browser case count.
    const specs = [...impacted].filter(file => file.startsWith('tests/') &&
      (file.endsWith('.spec.ts') ||
        !file.endsWith('.test.ts') && (!webTree.complete || webTree.webTests.get(file) === undefined || webTree.webTests.get(file).includes('@playwright/test')) ||
        [...reverseDependants(new Set([file]), graph)].some(importer => /^tests\/.*\.spec\.ts$/.test(importer))))
    if (specs.length) {
      const unknown = reverseDependants(new Set([unknownWebImport]), graph)
      if (specs.some(file => unknown.has(file))) return full('browser dependency proof unavailable (unresolved or dynamic import)')
      const browser = tests.filter(row => row.kind === 'browser')
      // A native runner inventory is complete. The lightweight planner reads
      // bounded source bytes from its own (trusted, for L4) tree snapshot.
      const bound = browser.length ? browser.filter(row => impacted.has(row.file)).length
        : specs.reduce((sum, file) => {
          const source = webTree.complete ? webTree.webTests.get(file) : undefined
          const estimate = planningWebCases(source)
          return sum + (!file.endsWith('.spec.ts') && estimate > 0 ? browserCaseLimit + 1 : estimate)
        }, 0)
      if (bound > browserCaseLimit) return full('browser fan-out exceeds 300 cases (or source bound unavailable)')
    }
  }
  return {mode:'essential',layout:risk.layout,reason:risk.reasons.join('; ')||'essential plus changed area and reverse dependencies'}
}

// One precedence rule for every workflow consumer. Only matching trusted
// exemptions can keep a narrow PR lane. Essential/static are wider layouts.
// Off returns the frozen classifier value unchanged, regardless of mode/event.
export function effectiveLane(lane,{mode,event,affectedLane}={}) {
  if(affectedLane!=='on')return lane
  if(event!=='pull_request')return 'full'
  if(lane==='spec-only'&&mode==='spec-only')return lane
  if(lane==='docs-only'&&mode==='docs')return lane
  return 'full'
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
  const archive=execFileSync('git',['archive',base,'scripts/test-tiers','scripts/ci','web/src','web/tests','web/e2e'],
    {cwd,timeout:30_000,maxBuffer:64*1024*1024})
  execFileSync('tar',['-x','-C',directory],{input:archive,timeout:30_000,maxBuffer:2*1024*1024})
  const output=execFileSync(process.execPath,[resolve(directory,'scripts/test-tiers/diff.mjs'),'--trusted-paths'],
    {cwd:directory,encoding:'utf8',timeout:30_000,maxBuffer:2*1024*1024,
      env:{...env,GITHUB_OUTPUT:'',GITHUB_STEP_SUMMARY:'',AEON_TIER_PATHS:JSON.stringify(paths),AEON_TIER_CHECKOUT:cwd}})
  const decision=JSON.parse(output)
  if(!['full','essential'].includes(decision.mode)||typeof decision.reason!=='string')throw new Error('Invalid trusted planner decision')
  // Pre-adoption base planners report no layout; that is the full layout.
  decision.layout??='full'
  if(!['full','static'].includes(decision.layout)||(decision.mode==='full'&&decision.layout!=='full'))throw new Error('Invalid trusted planner layout')
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
  } catch { decision={mode:'full',layout:'full',reason:'trusted affected classification unavailable'} }
  const {mode,reason}=decision
  const layout=mode==='full'?'full':decision.layout??'full'
  if(!env.GITHUB_OUTPUT)throw new Error('Missing Actions output path')
  const lane=effectiveLane(env.CI_PLAN_LANE??'full',{mode,event:env.GITHUB_EVENT_NAME,affectedLane:env.CI_AFFECTED_LANE})
  writeFileSync(env.GITHUB_OUTPUT,`mode=${mode}\nlayout=${layout}\nlane=${lane}\n`,{flag:'a'})
  console.log(`Test tier scheduling: ${mode} (${layout} layout, ${lane} effective lane); ${reason}; changed paths ${paths?.length??'unavailable'}`)
  if(env.GITHUB_STEP_SUMMARY)writeFileSync(env.GITHUB_STEP_SUMMARY,`Test tier scheduling: ${mode} (${layout} layout, ${lane} effective lane); ${reason}\n`,{flag:'a'})
  return 0
}
if(process.argv[1]&&resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try {
    if(process.argv[2]==='--trusted-paths') {
      const paths=JSON.parse(process.env.AEON_TIER_PATHS)
      if(!validImpactPaths(paths))throw new Error('Invalid trusted paths')
      // Base planner, base graph and base manifests come from this snapshot;
      // the candidate checkout supplies the changed files and head manifests.
      const checkout=process.env.AEON_TIER_CHECKOUT
      const promotions=paths.some(path=>tierManifestPattern.test(path))?promotionsBetween(undefined,{cwd:checkout,baseDirectory:root}):undefined
      console.log(JSON.stringify(schedulingDecision(process.env.GITHUB_EVENT_NAME,paths,
        path=>existsSync(resolve(checkout,path)),{affectedLane:process.env.CI_AFFECTED_LANE,checkout,promotions,webTree:sourceTree(root)})))
    } else process.exitCode=main()
  } catch(error) { console.error(error.message);process.exitCode=1 }
}
