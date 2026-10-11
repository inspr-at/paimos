// SPDX-License-Identifier: AGPL-3.0-only
import test from 'node:test'
import assert from 'node:assert/strict'
import { createPolicyEditor, PolicyFailure, policyError, policyJSON } from '../src/lib/policyEditor.ts'
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
test('retired-kind compensation has the specific honest refusal',()=>assert.equal(policyError(new PolicyFailure(422,'unknown_kind','unknown_kind'),true),'This work kind is no longer available; the change could not be restored.'))
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

test('local picker invalidation keeps its document while dropping drafts, Undo and late writes',async()=>{
 const f=fixture();await f.editor.load();const before=f.editor.snapshot.value
 f.editor.edit('old choice');const held=f.hold(),saving=f.editor.submit();await f.started
 try {
  f.setContext('tenant/person/other-kind');f.editor.reset({retainSnapshot:true})
  assert.equal(f.editor.snapshot.value,before);assert.equal(f.editor.draft.value,null);assert.equal(f.editor.undo.value,null);assert.equal(f.editor.busy.value,false)
 } finally { held.resolve({person:'first',revision:4,value:'late saved choice'});await saving }
 assert.equal(f.editor.snapshot.value,before);assert.equal(f.editor.undo.value,null);assert.equal(f.editor.message.value,'')
 f.editor.reset();assert.equal(f.editor.snapshot.value,null)
})

test('same-identity preview refresh keeps the captured draft baseline and confirmed Undo revision',async()=>{
 const f=fixture();await f.editor.load();f.editor.edit('desired');f.setServer({person:'first',revision:9,value:'elsewhere'})
 await f.editor.load();assert.equal(f.editor.draft.value,'desired');assert.equal(f.editor.snapshot.value?.revision,9)
 await f.editor.submit();assert.equal(f.writes[0]!.before.revision,3)
 const confirmed=f.editor.undo.value!.confirmed;f.setServer({person:'first',revision:12,value:'newer elsewhere'})
 await f.editor.load();assert.equal(f.editor.undo.value!.confirmed,confirmed);await f.editor.submit(true)
 assert.equal(f.writes[1]!.before.revision,confirmed.revision)
})
