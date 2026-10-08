// SPDX-License-Identifier: AGPL-3.0-only
import { createHash } from 'node:crypto'
import type { Page } from '@playwright/test'
import { mockPolicies, policyLadder } from './policies-fixtures'
import type { EditableLadder, ModelRoute, PreferenceDocument, PreferenceLevelName } from '../src/lib/policyModels'
import type { PolicyRole } from '../src/lib/policies'
function barrier() { let release!:()=>void;const promise=new Promise<void>(resolve=>{release=resolve});return {promise,release} }
const token=(routes:ModelRoute[],mode?:string)=>'"'+createHash('sha256').update(JSON.stringify({routes,mode})).digest('hex')+'"'
export function preferenceDocument():PreferenceDocument {
 const auto={mode:'auto'} as const,profiles=policyLadder('build').steps.map(row=>row.profile)
 profiles[0]!.display_name='Sehr lange Modellbezeichnung für nachvollziehbare normale und komplexe Entwicklungsaufgaben'
 const kinds=[{id:'kind-build',slug:'backend',label:'Backend und nachvollziehbare technische Entwicklungsarbeit',hint:'Eine ausführliche deutsche Beschreibung der Aufgabe.'},{id:'kind-review',slug:'review',label:'Review',hint:'Minimum checks still apply.'},{id:'kind-other',slug:'other',label:'Other',hint:''},{id:'kind-security',slug:'security',label:'Security review',hint:''}]
 const row={kind_id:'kind-build',normal:{mode:'pinned' as const,profile_id:profiles[0]!.id},complex:{mode:'pinned' as const,profile_id:profiles[1]!.id},locked:false}
 const level=()=>({revision:1,residency:null,residency_locked:false,prefs_locked:false,rows:[]})
 const view=()=>({choices:profiles.map(profile=>({profile,line:profile.model,model_version:'1',retired:false,review_ladder:true,review_reason:'',residency_routes:2})),choices_truncated:false,resolution_truncated:false,changes:0,residency:{value:'any',set_by:'default',loosened_lock:false,qualifying_routes:4},rows:kinds.map(kind=>({kind_id:kind.id,set_by:'default',locked_by:'',changed_here:false,reset_to:'default',warnings:[],normal:{selector:auto,label:'Automatic',today_version:''},complex:{selector:auto,label:'Automatic',today_version:''}}))})
 const result={person_id:'first-canonical-person',kinds,levels:{default:level(),person:{...level(),rows:[row]},project:level()},views:{default:view(),person:view(),project:view()},can:{edit_default:true,edit_person:true,edit_project:true},residency_lock_mode:'warn'} as PreferenceDocument
 return refreshViews(result)
}
function refreshViews(doc:PreferenceDocument) {
 for(const name of ['default','person','project'] as const){const value=doc.levels[name],view=doc.views[name];if(!value||!view)continue
  view.changes=value.rows.length+(value.residency?1:0)+Number(value.residency_locked)+Number(value.prefs_locked)
  view.residency.value=value.residency??'any';view.residency.qualifying_routes=value.residency==='local'?0:4
  view.rows=doc.kinds.map(kind=>{const row=value.rows.find(r=>r.kind_id===kind.id),prior=view.rows.find(r=>r.kind_id===kind.id);const cell=(bucket:'normal'|'complex')=>({selector:row?.[bucket]??{mode:'auto' as const},label:row?.[bucket].mode==='pinned'?'Chosen profile':'Automatic',today_version:''})
   return {kind_id:kind.id,set_by:row?name:'default',locked_by:prior?.locked_by??'',changed_here:!!row,reset_to:name==='project'?'person':'default',warnings:[],normal:cell('normal'),complex:cell('complex')}
  })
 }
 return doc
}
export async function mockPolicyEditors(page:Page,theme:'light'|'dark'='light') {
 const base=await mockPolicies(page,theme)
 base.data.grants.push('models.manage','model_prefs.manage','nodes.read')
 const catalog=policyLadder('build',3).steps.map(s=>s.profile)
 catalog[0]!.display_name='Sehr lange Modellbezeichnung für nachvollziehbare normale und komplexe Reviewaufgaben'
 const state={document:preferenceDocument(),ladders:new Map<PolicyRole,EditableLadder>(),writes:[] as {path:string;method:string;body:Record<string,unknown>|ModelRoute[]|null;headers:Record<string,string>}[],refusal:null as null|{status:number;code:string},failReads:false,malformed:false,unknown:false}
 let next:{started:ReturnType<typeof barrier>;until:ReturnType<typeof barrier>;settled:ReturnType<typeof barrier>}|null=null
 let nextRead:{started:ReturnType<typeof barrier>;until:ReturnType<typeof barrier>}|null=null
 let nextCatalog:{started:ReturnType<typeof barrier>;until:ReturnType<typeof barrier>}|null=null
 await page.route('**/api/models',async route=>{
  const held=nextCatalog;nextCatalog=null;held?.started.release();if(held)await held.until.promise
  return route.fulfill({json:catalog})
 })
 await page.route(/\/api\/models\/routes(\?|$)/,async route=>{
  const req=route.request(),url=new URL(req.url()),role=url.searchParams.get('role') as PolicyRole
  if(!state.ladders.has(role)){const ladder=policyLadder(role) as EditableLadder;ladder.edit_token=token(ladder.routes);state.ladders.set(role,ladder)}
  const ladder=state.ladders.get(role)!
  if(req.method()==='GET')return route.fulfill({json:ladder})
  const desired=req.postDataJSON() as ModelRoute[];state.writes.push({path:url.pathname+url.search,method:req.method(),body:desired,headers:req.headers()})
  const held=next;next=null;held?.started.release();if(held)await held.until.promise
  try {
  if(state.refusal)return await route.fulfill({status:state.refusal.status,json:{code:state.refusal.code,error:state.refusal.code}})
  if(req.headers()['if-match']!==ladder.edit_token)return await route.fulfill({status:409,json:{code:'stale_revision',error:'stale_revision'}})
  if(new Set(desired.map(row=>row.priority)).size!==desired.length)return await route.fulfill({status:400,json:{error:'duplicate priority for role'}})
  const actual=desired.map(row=>url.searchParams.get('expiry_policy')==='clear'&&row.valid_until&&Date.parse(row.valid_until)<=Date.now()?{...row,state:'available',reason:'',valid_until:null}:row).sort((a,b)=>a.priority-b.priority||a.profile_id.localeCompare(b.profile_id))
  ladder.routes=actual;if(role==='review-gate'){ladder.order_mode=(url.searchParams.get('order_mode')??ladder.order_mode) as 'legacy'|'saved';ladder.managed_fallback_order=actual.map(row=>row.profile_id)}ladder.edit_token=token(actual,ladder.order_mode);ladder.steps=actual.map(row=>({...row,profile:catalog.find(item=>item.id===row.profile_id)!}))
  return await route.fulfill({json:actual,headers:{ETag:ladder.edit_token!,...(role==='review-gate'?{'Model-Order-Mode':ladder.order_mode!}:{})}})
  } finally {held?.settled.release()}
 })
 await page.route(/\/api\/model-preferences(\/|\?|$)/,async route=>{
  const req=route.request(),url=new URL(req.url())
  if(req.method()==='GET'){
   const held=nextRead;nextRead=null;held?.started.release();if(held)await held.until.promise
   if(state.failReads)return route.fulfill({status:503,json:{error:'read refused'}})
   return route.fulfill({json:refreshViews(structuredClone(state.document))})
  }
  const body=req.postDataJSON();state.writes.push({path:url.pathname+url.search,method:req.method(),body,headers:req.headers()})
  const held=next;next=null;held?.started.release();if(held)await held.until.promise
  if(state.refusal)return route.fulfill({status:state.refusal.status,json:{code:state.refusal.code,error:state.refusal.code}})
  if(req.headers()['if-prefs-person']!==state.document.person_id)return route.fulfill({status:409,json:{code:'preference_person_changed',error:'preference_person_changed'}})
  const name=url.pathname.split('/')[4] as PreferenceLevelName,value=state.document.levels[name]!,id=url.pathname.split('/')[6]
  // DELETE follows requestedRevision in preferences_http.go: the body is ignored.
  const revisions=url.searchParams.getAll('revision'),revision=req.method()==='DELETE'?revisions.length===1&&revisions[0]!.length<=19&&/^[+]?[0-9]+$/.test(revisions[0]!)?Number(revisions[0]):null:body?.revision
  if(!Number.isSafeInteger(revision)||revision<0)return route.fulfill({status:400,json:{code:'revision_required',error:'revision_required'}})
  if(revision!==value.revision)return route.fulfill({status:409,json:{code:'stale_revision',error:'stale_revision'}})
  if(id&&!state.document.kinds.some(kind=>kind.id===id))return route.fulfill({status:422,json:{code:'unknown_kind',error:'unknown_kind'}})
  if(id){value.rows=value.rows.filter(row=>row.kind_id!==id);if(req.method()==='PUT')value.rows.push({kind_id:id,normal:body.normal,complex:body.complex,locked:body.locked})}
  else{if(req.method()!=='PUT'||'rows' in body)throw new Error('Unsafe whole-level preference mutation');Object.assign(value,{residency:body.residency,residency_locked:body.residency_locked,prefs_locked:body.prefs_locked})}
  value.revision++;refreshViews(state.document)
  if(state.unknown)return route.abort('connectionreset')
  return route.fulfill({json:state.malformed?{person_id:'wrong-person'}:{person_id:state.document.person_id,revision:value.revision,level:value,running_outside:[],residency:state.document.views[name]!.residency}})
 })
 return {state,base,holdCatalog(){const held={started:barrier(),until:barrier()};nextCatalog=held;return {started:held.started.promise,release:held.until.release}},holdRead(){const held={started:barrier(),until:barrier()};nextRead=held;return {started:held.started.promise,release:held.until.release}},holdNext(){const held={started:barrier(),until:barrier(),settled:barrier()};next=held;return {started:held.started.promise,settled:held.settled.promise,release:held.until.release}}}
}
