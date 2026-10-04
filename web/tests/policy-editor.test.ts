// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { createPolicyEditor, PolicyFailure, policyError, policyJSON } from '../src/lib/policyEditor.ts'
import { preferenceRequest, compensatePreferences, preferenceScalars, writePreferences, writeLadder, validateLadder, ladderDraft, compensateLadder, type PreferenceSnapshot, type PreferenceMutation, type EditableLadder } from '../src/lib/policyModels.ts'
function barrier<T>() { let resolve!: (value:T)=>void; const promise=new Promise<T>(yes=>{resolve=yes}); return {promise,resolve} }
type Snapshot={person:string;revision:number;value:string}
function fixture() {
  let context='tenant/person/role', server:Snapshot={person:'first',revision:3,value:'before'}, held:ReturnType<typeof barrier<Snapshot>>|null=null, refusal:Error|null=null, readFailure=false
  const started=barrier<void>()
  const writes:{before:Snapshot;desired:string;undo:boolean}[]=[]
  const editor=createPolicyEditor<Snapshot,string>(()=>context,{
    read:async()=>{if(readFailure)throw new Error('read failed'); return structuredClone(server)},
    write:async(before,desired,undo)=>{writes.push({before,desired,undo});started.resolve();if(held)return held.promise;if(refusal)throw refusal;server={...server,value:desired.toUpperCase(),revision:server.revision+1};return structuredClone(server)},
    identity:s=>s.person,compensate:s=>s.value,
  })
  return {editor,writes,started:started.promise,setContext:(v:string)=>{context=v},setServer:(v:Snapshot)=>{server=v},hold:()=>held=barrier<Snapshot>(),refuse:(v:Error)=>{refusal=v},failRead:()=>{readFailure=true}}
}
test('serializes writes, provisional display, actual confirmation and confirmed-revision Undo',async()=>{
 const f=fixture();await f.editor.load();f.editor.edit('desired');const held=f.hold(),save=f.editor.submit();await f.editor.submit()
 assert.equal(f.writes.length,1);assert.equal(f.editor.busy.value,true);assert.equal(f.editor.message.value,'Saving…');assert.equal(f.editor.snapshot.value?.value,'before');assert.equal(f.editor.draft.value,'desired');assert.equal(f.editor.undo.value,null)
 f.setServer({person:'first',revision:5,value:'newer elsewhere'});held.resolve({person:'first',revision:4,value:'ACTUAL'});await save
 assert.equal(f.editor.undo.value?.confirmed.value,'ACTUAL');assert.equal(f.editor.snapshot.value?.revision,5);await f.editor.submit(true)
 assert.equal(f.writes[1]!.before.revision,4);assert.equal(f.writes[1]!.desired,'before');assert.equal(f.writes[1]!.undo,true)
})
for(const context of ['tenant/other-person/role','other-tenant/person/role','tenant/person/other-role','tenant/person/project/other-kind',''])test(`drops late save after context ${context||'signout'}`,async()=>{
 const f=fixture();await f.editor.load();f.editor.edit('old');const held=f.hold(),save=f.editor.submit();await f.started;f.setContext(context);f.editor.reset();held.resolve({person:'first',revision:4,value:'committed'});await save
 assert.equal(f.editor.snapshot.value,null);assert.equal(f.editor.draft.value,null);assert.equal(f.editor.undo.value,null);assert.equal(f.editor.message.value,'');await f.editor.submit(true);assert.equal(f.writes.length,1)
})
for(const [status,code] of [[403,'permission_denied'],[409,'stale_revision'],[422,'locked_above']] as const)test(`${status}/${code} retains draft without auto-retry or success`,async()=>{
 const f=fixture();await f.editor.load();f.editor.edit('desired');f.refuse(new PolicyFailure(status,code,'specific source error'));await f.editor.submit()
 assert.equal(f.editor.phase.value,'refused');assert.equal(f.editor.draft.value,'desired');assert.equal(f.editor.snapshot.value?.value,'before');assert.equal(f.editor.undo.value,null);assert.equal(f.writes.length,1);assert.doesNotMatch(f.editor.message.value,/^Saved/)
})
test('equal-revision person conflict discards old draft and Undo',async()=>{
 const f=fixture();await f.editor.load();f.editor.edit('desired');f.setServer({person:'second',revision:3,value:'second'});f.refuse(new PolicyFailure(409,'preference_person_changed','changed'));await f.editor.submit();await f.editor.load()
 assert.equal(f.editor.draft.value,null);assert.equal(f.editor.undo.value,null);assert.equal(f.editor.snapshot.value?.person,'second');assert.equal(f.writes.length,1);assert.match(f.editor.message.value,/discarded/)
})
test('unknown outcome and failed reconciliation block new writes without success/Undo',async()=>{
 const f=fixture();await f.editor.load();f.editor.edit('desired');f.refuse(new Error('lost response'));f.failRead();await f.editor.submit()
 assert.equal(f.editor.phase.value,'unknown');assert.equal(f.editor.needsReload.value,true);assert.equal(f.editor.undo.value,null);assert.match(f.editor.message.value,/Could not confirm/);await f.editor.submit();assert.equal(f.writes.length,1)
})
test('wrong-person confirmation cannot report success',async()=>{
 const f=fixture();await f.editor.load();f.editor.edit('desired');const held=f.hold(),save=f.editor.submit();held.resolve({person:'wrong',revision:4,value:'desired'});await save;assert.equal(f.editor.phase.value,'unknown');assert.equal(f.editor.undo.value,null)
})
test('older read cannot replace newer snapshot/draft',async()=>{
 const held=barrier<Snapshot>();let reads=0
 const editor=createPolicyEditor<Snapshot,string>(()=>'owner',{read:async()=>++reads===1?held.promise:{person:'p',revision:2,value:'current'},write:async()=>({person:'p',revision:3,value:'saved'}),identity:s=>s.person,compensate:s=>s.value})
 const first=editor.load();await editor.load();editor.edit('new');held.resolve({person:'p',revision:1,value:'old'});await first;assert.equal(editor.snapshot.value?.revision,2);assert.equal(editor.draft.value,'new')
})
const auto={mode:'auto'} as const
function prefs(level:PreferenceSnapshot['level']='person'):PreferenceSnapshot {
 const value={revision:7,residency:'eu' as const,residency_locked:true,prefs_locked:false,rows:[{kind_id:'kind',normal:{mode:'pinned' as const,profile_id:'first'},complex:{mode:'latest' as const,family:'openai',line:'sol',effort:'high'},locked:true},{kind_id:'sibling',normal:auto,complex:auto,locked:false}]}
 return {level,project:level==='project'?'captured-project':'',document:{person_id:'canonical-person',kinds:[],levels:{default:value,person:value,project:level==='project'?value:null},views:{default:null,person:null,project:null},can:{edit_default:true,edit_person:true,edit_project:true},residency_lock_mode:'warn'}}
}
test('closed builders preserve sibling fields, capture paths/person and never replace a whole level',()=>{
 for(const name of ['default','person','project'] as const){
  const before=prefs(name),scalar:PreferenceMutation={unit:'scalars',value:{...preferenceScalars(before.document.levels[name]!),residency:'local'}},req=preferenceRequest(before,scalar),body=JSON.parse(req.init.body)
  assert.equal(req.init.method,'PUT');assert.equal(req.init.headers['If-Prefs-Person'],'canonical-person');assert.deepEqual(body,{revision:7,residency:'local',residency_locked:true,prefs_locked:false});assert.equal(Object.hasOwn(body,'rows'),false)
  const undone=compensatePreferences(before,scalar);assert.equal(undone.unit,'scalars');assert.equal(Object.hasOwn(JSON.parse(preferenceRequest(before,undone).init.body),'rows'),false)
  const row={...before.document.levels[name]!.rows[0]!,normal:auto},rowReq=preferenceRequest(before,{unit:'row',kind_id:'kind',value:row}),rowBody=JSON.parse(rowReq.init.body)
  assert.deepEqual(rowBody.complex,before.document.levels[name]!.rows[0]!.complex);assert.equal(rowBody.locked,true);assert.match(rowReq.path,/\/rows\/kind/)
  const reset:PreferenceMutation={unit:'row',kind_id:'kind',value:null};assert.equal(preferenceRequest(before,reset).init.method,'DELETE');assert.match(preferenceRequest(before,reset).path,/\/rows\/kind/);assert.deepEqual(compensatePreferences(before,reset),{unit:'row',kind_id:'kind',value:before.document.levels[name]!.rows[0]})
  assert.deepEqual(compensatePreferences(before,{unit:'row',kind_id:'new-kind',value:{kind_id:'new-kind',normal:auto,complex:auto,locked:false}}),{unit:'row',kind_id:'new-kind',value:null})
 }
 assert.throws(()=>preferenceRequest(prefs(),{unit:'scalars',value:{rows:[],residency:null,residency_locked:false,prefs_locked:false}} as unknown as PreferenceMutation),/scalar/)
 assert.throws(()=>preferenceRequest(prefs(),{unit:'delete-level'} as unknown as PreferenceMutation),/captured/)
 assert.throws(()=>preferenceRequest(prefs(),{unit:'row',kind_id:'kind',value:{kind_id:'other',normal:auto,complex:auto,locked:false}}),/captured/)
})
test('absent-scope scalar compensation uses null/false/false level PUT',()=>{
 const before=prefs();before.document.levels.person={revision:0,residency:null,residency_locked:false,prefs_locked:false,rows:[]}
 const req=preferenceRequest(before,compensatePreferences(before,{unit:'scalars',value:{residency:'eu',residency_locked:false,prefs_locked:false}}));assert.equal(req.init.method,'PUT');assert.deepEqual(JSON.parse(req.init.body),{revision:0,residency:null,residency_locked:false,prefs_locked:false})
})
for(const name of ['default','person','project'] as const)test(`row Reset and new-row Undo send query revisions at ${name} scope`,()=>{
 const before=prefs(name)
 if(name==='project')before.project='project /?&='
 const kind='kind /?&=',created:PreferenceMutation={unit:'row',kind_id:kind,value:{kind_id:kind,normal:auto,complex:auto,locked:false}}
 for(const mutation of [{unit:'row',kind_id:kind,value:null} as PreferenceMutation,compensatePreferences(before,created)]){
  const req=preferenceRequest(before,mutation),url=new URL(req.path,'https://fixture.invalid')
  assert.equal(req.init.method,'DELETE')
  assert.equal(url.pathname,`/model-preferences/levels/${name}/rows/${encodeURIComponent(kind)}`)
  assert.deepEqual(url.searchParams.getAll('revision'),['7'])
  assert.equal(req.init.body,undefined)
  assert.equal(url.searchParams.get('project_id'),name==='project'?before.project:null)
  assert.equal(req.init.headers['If-Prefs-Person'],'canonical-person')
 }
})
test('retired-kind compensation has the specific honest refusal',()=>assert.equal(policyError(new PolicyFailure(422,'unknown_kind','unknown_kind'),true),'This work kind is no longer available; the change could not be restored.'))
test('HTTP success alone cannot confirm wrong identity/revision',async()=>{
 const original=globalThis.fetch
 try{for(const result of [{person_id:'wrong',revision:8,level:{...prefs().document.levels.person,revision:8},running_outside:[]},{person_id:'canonical-person',revision:8,level:{...prefs().document.levels.person,revision:7},running_outside:[]}]){
 globalThis.fetch=async()=>new Response(JSON.stringify(result),{status:200});await assert.rejects(writePreferences(prefs(),{unit:'scalars',value:preferenceScalars(prefs().document.levels.person!)},false,new AbortController().signal),/confirmation/)
 }}finally{globalThis.fetch=original}
})
test('ladder role CAS, expiry-clear Undo and actual normalized token',async()=>{
 const token='"'+'a'.repeat(64)+'"',next='"'+'b'.repeat(64)+'"',route={role:'build' as const,priority:1,profile_id:'profile',state:'unavailable',reason:'hold',valid_until:'2020-01-01T00:00:00Z'}
 const before={role:'build',setup:true,truncated:false,can_edit:true,edit_token:token,routes:[route],steps:[{...route,profile:{id:'profile'}}],dispatch_family_order:[],review_floors:[]} as EditableLadder
 const original=globalThis.fetch,requests:{path:string;init:RequestInit}[]=[]
 try{globalThis.fetch=async(path,init)=>{requests.push({path:String(path),init:init!});return new Response(JSON.stringify([{...route,state:'available',reason:'',valid_until:null}]),{headers:{ETag:next}})}
 const result=await writeLadder(before,[route],true,new AbortController().signal)
 assert.equal(requests[0]!.path,'/api/models/routes?role=build&expiry_policy=clear');assert.equal((requests[0]!.init.headers as Record<string,string>)['If-Match'],token);assert.equal(result.edit_token,next);assert.equal(result.routes[0]!.state,'available')
 await assert.rejects(writeLadder({...before,truncated:true},[route],false,new AbortController().signal),/complete/);assert.throws(()=>validateLadder({...before,edit_token:'short'},'build'),/Invalid/)
 }finally{globalThis.fetch=original}
})
test('level writes remain serialized across two row editors and retain each captured revision',async()=>{
 const gate=barrier<Snapshot>(),started=barrier<void>(),writes:number[]=[]
 const wire={read:async()=>({person:'p',revision:2,value:'before'}),write:async(before:Snapshot,_desired:string)=>{writes.push(before.revision);started.resolve();if(writes.length===1)return gate.promise;return {person:'p',revision:4,value:'second'}},writeKey:()=> 'shared-level',identity:(s:Snapshot)=>s.person,compensate:(s:Snapshot)=>s.value}
 const first=createPolicyEditor<Snapshot,string>(()=>'row-one',wire),second=createPolicyEditor<Snapshot,string>(()=>'row-two',wire);await first.load();await second.load();first.edit('one');second.edit('two');const a=first.submit();await started.promise;const b=second.submit();assert.deepEqual(writes,[2]);gate.resolve({person:'p',revision:3,value:'first'});await a;await b;assert.deepEqual(writes,[2,2])
})

test('response byte bounds precede JSON parsing',async()=>{
 await assert.rejects(policyJSON(new Response('"'+'x'.repeat(32)+'"'),8),/exceeds the editor limit/)
 assert.deepEqual(await policyJSON(new Response('{"ok":true}'),64),{ok:true})
})
for(const status of [401,403])test(`confirmed save followed by denied preview ${status} clears private snapshot and Undo`,async()=>{
 let reads=0,writes=0
 const editor=createPolicyEditor<Snapshot,string>(()=>'owner',{
  read:async()=>{if(++reads>1)throw new PolicyFailure(status,'permission_denied','source refused');return {person:'p',revision:2,value:'before'}},
  write:async()=>{writes++;return {person:'p',revision:3,value:'saved'}},identity:s=>s.person,compensate:s=>s.value,
 })
 await editor.load();editor.edit('desired');await editor.submit()
 assert.equal(editor.snapshot.value,null);assert.equal(editor.undo.value,null);assert.equal(editor.draft.value,null);assert.equal(editor.needsReload.value,true);assert.equal(editor.phase.value,'saved');assert.match(editor.message.value,/Saved\. Current preview could not be refreshed/)
 await editor.submit(true);assert.equal(writes,1)
})

test('managed order drafts preserve holds and Undo restores rows plus captured mode',async()=>{
 const routes=[{role:'review-gate' as const,priority:10,profile_id:'strong',state:'available',reason:'',valid_until:null},{role:'review-gate' as const,priority:20,profile_id:'frontier',state:'conserved',reason:'old hold',valid_until:'2020-01-01T00:00:00Z'}]
 const token='"'+'a'.repeat(64)+'"',next='"'+'b'.repeat(64)+'"'
 const before={role:'review-gate',setup:true,truncated:false,can_edit:true,edit_token:token,routes,steps:routes.map(row=>({...row,profile:{id:row.profile_id,family:'anthropic',tier:row.profile_id}})),order_mode:'legacy',managed_fallback_order:['frontier','strong'],dispatch_family_order:['anthropic'],review_floors:[]} as EditableLadder
 const draft=ladderDraft(before),prior=compensateLadder(before)
 assert.equal(draft.order_mode,'saved');assert.deepEqual(draft.routes,routes.toReversed().map((row,index)=>({...row,priority:index+1})));assert.deepEqual(prior,{routes,order_mode:'legacy'})
 const original=globalThis.fetch,requests:{path:string;init:RequestInit}[]=[]
 try{globalThis.fetch=async(path,init)=>{requests.push({path:String(path),init:init!});const body=JSON.parse(String(init!.body));return new Response(JSON.stringify(body),{headers:{ETag:next,'Model-Order-Mode':String(path).includes('order_mode=legacy')?'legacy':'saved'}})}
 const saved=await writeLadder(before,draft,false,new AbortController().signal)
 assert.equal(saved.order_mode,'saved');assert.match(requests[0]!.path,/order_mode=saved/)
 const restored=await writeLadder(saved,prior,true,new AbortController().signal)
 assert.equal(restored.order_mode,'legacy');assert.match(requests[1]!.path,/expiry_policy=clear&order_mode=legacy/);assert.deepEqual(restored.routes,routes);assert.equal((requests[1]!.init.headers as Record<string,string>)['If-Match'],next)
 globalThis.fetch=async()=>new Response(JSON.stringify(draft.routes),{headers:{ETag:next,'Model-Order-Mode':'legacy'}})
 await assert.rejects(writeLadder(before,draft,false,new AbortController().signal),/ordering mode confirmation/)
 assert.throws(()=>ladderDraft({...before,managed_fallback_order:['strong','strong']}),/managed order snapshot/)
 }finally{globalThis.fetch=original}
})
