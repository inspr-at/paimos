// SPDX-License-Identifier: AGPL-3.0-only
// Risks: late reads/writes land on another ticket/person; failed writes look saved;
// policy drafts accept no eligible family or lose the captured revision.
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import * as Vue from 'vue'
import { readFileSync } from 'node:fs'
import { compileScript, parse } from '@vue/compiler-sfc'
import ts from 'typescript'
const http = vi.hoisted(() => ({ handle: async (_path: string, _init?: RequestInit) => new Response('{}') }))
vi.mock('../src/lib/api.ts', () => ({ api:(path:string, init?:RequestInit) => http.handle(path,init), APIError:class extends Error { constructor(readonly status:number,message:string) { super(message) } }, getProjects:async () => ({ items:[] }) }))
import * as delivery from '../src/lib/delivery'
import * as policy from '../src/lib/reviewPolicy'
import * as identity from '../src/lib/identityScope'
import * as reviews from '../src/lib/reviews'
import * as api from '../src/lib/api'
const session = Vue.reactive({ identity:{ tenant:{ id:'tenant' }, principal:{ id:'person', kind:'person' } } })
const profile = Vue.reactive({ profile:{ locale:'en' } })
const access = Vue.reactive({ manage:true, read:true })
const scopes: Vue.EffectScope[] = [], unmounts:(() => void)[] = [], actions:(() => void)[] = []
const settings = (): policy.ReviewPolicySettings => ({ project_id:null, policy:{ mode:'other_family', allowed_families:[] }, effective:{ mode:'other_family', allowed_families:[] }, workspace:{ mode:'other_family', allowed_families:[] }, valid_families:['openai','anthropic'], source:'tenant', updated_by:'editor', updated_at:'2026-10-07T00:00:00Z' })
const item = (): delivery.DeliveryItem => ({ id:'held', project_id:'project',ticket_node_id:'ticket',repository:'example/repo',pull_request:7,branch:'work/long-branch',head_sha:'b'.repeat(40),state:'held',state_since:'2026-10-07T00:00:00Z',owner:'person',deadline_at:null,held_reason:'Release freeze',held_from_state:'pushed',required_checks_passed:3,required_checks_total:5,link_source:'title_key',updated_at:'2026-10-07T00:00:00Z' })
const json = (body:unknown, status=200) => new Response(JSON.stringify(body), { status, headers:{'Content-Type':'application/json'} })
const deferred = () => { let release!:(value:Response) => void; const promise = new Promise<Response>(resolve => { release=resolve }); return {promise,release} }
async function settle() { for(let i=0;i<15;i++) await Promise.resolve(); await Vue.nextTick() }
function setup<T>(file:string,props:object):T {
 const modules:Record<string,unknown> = { vue:{...Vue,onBeforeUnmount:(fn:() => void) => unmounts.push(fn)}, '../../lib/api':api, '../../lib/authz':{can:(key:string) => key.endsWith('.read') ? access.read : access.manage,onAccessChange:() => () => {}}, '../../lib/delivery':delivery,'../../lib/reviewPolicy':policy,'../../lib/reviews':reviews,'../../lib/identityScope':identity,'../../lib/toast':{toast:(_text:string,options:{action:{run:() => void}}) => actions.push(options.action.run)},'../../stores/session':{useSession:() => session},'../../stores/profile':{useProfile:() => profile}, '../AppIcon.vue':{}, '../KeyCap.vue':{}, './DeliveryRow.vue':{}, './DeliveryQueue.vue':{}, './SettingsCard.vue':{} }
 const {descriptor}=parse(readFileSync(new URL(`../src/components/${file}`,import.meta.url),'utf8'))
 const {content}=compileScript(descriptor,{id:file})
 const exports:{default?:{setup:(props:object,context:object) => T}}={}
 new Function('require','exports',ts.transpileModule(content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText)((name:string) => {if(!(name in modules))throw new Error(name);return modules[name]},exports)
 const scope=Vue.effectScope();scopes.push(scope)
 return scope.run(() => exports.default!.setup(props,{expose:() => {}}))!
}
beforeEach(() => { vi.useFakeTimers();vi.stubGlobal('navigator',{platform:'Mac'});session.identity.principal.id='person';access.manage=true;access.read=true;http.handle=async () => json(settings()) })
afterEach(() => {for(const stop of unmounts.splice(0))stop();for(const scope of scopes.splice(0))scope.stop();actions.splice(0);vi.useRealTimers();vi.unstubAllGlobals()})
type Ticket = { result:Vue.Ref<delivery.DeliveryPage|null>; lift:(item:delivery.DeliveryItem) => Promise<unknown>; load:() => Promise<unknown>; mutationError:Vue.Ref<string>; error:Vue.Ref<string>; lifted:Vue.Ref<string[]>; showSlot:Vue.ComputedRef<boolean> }
type Card = { base:Vue.Ref<policy.ReviewPolicySettings|null>; selected:Vue.Ref<string>; mode:Vue.Ref<string>; families:Vue.Ref<string[]>; save:() => Promise<unknown>; write:(policy:policy.ReviewPolicy|null,reset:boolean) => Promise<unknown>; message:Vue.Ref<string>; dirty:Vue.ComputedRef<boolean>; feedback:Vue.ComputedRef<string> }
it('the paused step and configured check progress are shown without inventing a completed step', () => {
 const held=item();expect(delivery.stepIndex(held.state,held.held_from_state)).toBe(2);expect(delivery.stepIndex('held',null)).toBe(-1)
 expect(delivery.nextAction({...held,state:'pushed'},'de')).toContain('3 von 5 bestanden')
 expect(delivery.ownerLabel('coordinator','Dirigent','de')).toBe('Dirigent')
 expect(delivery.sortDelivery([held,{...held,id:'placeholder',pull_request:null},{...held,id:'merged',state:'merged'}],Date.now()).map(i => i.id)).toEqual(['held','merged'])
})
it('a pending ticket read and lift result are discarded after ticket or person changes', async () => {
 const props=Vue.reactive({nodeId:'ticket',projectId:'project'}), pending=deferred()
 http.handle=async path => path.includes('/nodes/ticket/') ? pending.promise : json({items:[],next_cursor:null,available:true})
 const ticket=setup<Ticket>('work/TicketDelivery.vue',props)
 props.nodeId='other';await settle();pending.release(json({items:[item()],next_cursor:null,available:true}));await settle()
 expect(ticket.result.value?.items).toEqual([])
 http.handle=async () => json({items:[item()],next_cursor:null,available:true});props.nodeId='ticket';await settle()
 expect(ticket.result.value?.items[0]?.id).toBe('held')
 const writing=deferred();http.handle=async () => writing.promise
 const lift=ticket.lift(item());session.identity.principal.id='new-person';await settle()
 writing.release(json({...item(),state:'ci_green'}));await lift
 expect(ticket.result.value).toBeNull()
})
it('a failed refresh keeps the previous rows out of the empty slot', async () => {
 http.handle=async () => json({items:[item()],next_cursor:null,available:true})
 const ticket=setup<Ticket>('work/TicketDelivery.vue',{nodeId:'ticket',projectId:'project'});await settle()
 expect(ticket.showSlot.value).toBe(false);expect(ticket.result.value?.items).toHaveLength(1)
 http.handle=async () => json({error:'Delivery read failed'},503)
 await ticket.load();await settle()
 expect(ticket.error.value).toBe('Delivery read failed')
 expect(ticket.result.value?.items[0]?.state).toBe('held')
 expect(ticket.showSlot.value).toBe(false)
})
it('a later hold with a newer revision can be lifted again', async () => {
 const held=item()
 http.handle=async (_path,init) => init?.method==='DELETE' ? json({...held,state:'ci_green',held_reason:null,held_from_state:null,updated_at:'2026-10-07T01:00:00Z'}) : json({items:[held],next_cursor:null,available:true})
 const ticket=setup<Ticket>('work/TicketDelivery.vue',{nodeId:'ticket',projectId:'project'});await settle();await ticket.lift(held)
 expect(ticket.lifted.value).toContain('held')
 http.handle=async () => json({items:[{...held,updated_at:'2026-10-07T01:00:00Z'}],next_cursor:null,available:true})
 await ticket.load();await settle();expect(ticket.lifted.value).toContain('held')
 http.handle=async () => json({items:[{...held,updated_at:'2026-10-07T02:00:00Z'}],next_cursor:null,available:true})
 await ticket.load();await settle()
 expect(ticket.result.value?.items[0]?.state).toBe('held')
 expect(ticket.lifted.value).not.toContain('held')
})
it('a failed hold lift preserves the held row and reports its real error', async () => {
 http.handle=async (_path,init) => init?.method === 'DELETE' ? json({error:'Delivery changed; reload'},412) : json({items:[item()],next_cursor:null,available:true})
 const ticket=setup<Ticket>('work/TicketDelivery.vue',{nodeId:'ticket',projectId:'project'});await settle();await ticket.lift(item())
 expect(ticket.result.value?.items[0]?.state).toBe('held');expect(ticket.mutationError.value).toBe('Delivery changed; reload')
})
it('family eligibility comes from the server; empty allowlists never save and failed writes keep the draft', async () => {
 const writes:RequestInit[]=[]
 http.handle=async (_path,init) => {if(init?.method==='PUT'){writes.push(init);return json({error:'Permission was revoked'},403)}return json(settings())}
 const card=setup<Card>('settings/CrossFamilyReviewCard.vue',{});await settle()
 expect(card.base.value?.valid_families).toEqual(['openai','anthropic']);card.mode.value='allowlist';await card.save();expect(writes).toHaveLength(0)
 card.families.value=['anthropic'];await card.save();expect(writes).toHaveLength(1)
 expect(writes[0]?.headers).toMatchObject({'If-Unmodified-Since':'2026-10-07T00:00:00Z'})
 expect(card.dirty.value).toBe(true);expect(card.message.value).toBe('Permission was revoked');expect(card.base.value?.effective.mode).toBe('other_family')
})
it('a denied or failed review-policy read selects no saved mode', async () => {
 access.read=false
 const denied=setup<Card>('settings/CrossFamilyReviewCard.vue',{});await settle()
 expect(denied.base.value).toBeNull();expect(denied.mode.value).toBe('')
 expect(denied.message.value).toBe('The review rule is unavailable.')
 expect(denied.feedback.value).toBe('The review rule is unavailable.')
 for (const stop of unmounts.splice(0)) stop();for (const scope of scopes.splice(0)) scope.stop()
 access.read=true
 http.handle=async () => json({error:'The review rule could not be loaded.'},403)
 const failed=setup<Card>('settings/CrossFamilyReviewCard.vue',{});await settle()
 expect(failed.base.value).toBeNull();expect(failed.mode.value).toBe('')
 expect(failed.message.value).toBe('The review rule could not be loaded.')
 expect(failed.feedback.value).not.toContain('Saved rules apply')
})
it('a policy save cannot report success after the person changes', async () => {
 const card=setup<Card>('settings/CrossFamilyReviewCard.vue',{});await settle();card.mode.value='off'
 const pending=deferred();http.handle=async () => pending.promise
 const saving=card.save();session.identity.principal.id='different';await settle()
 pending.release(json({...settings(),effective:{mode:'off',allowed_families:[]}}));await saving
 expect(card.message.value).not.toContain('Saved.');expect(card.base.value?.effective.mode).not.toBe('off')
})

it('delivery queue reads stay with the ticket and person and expose refresh failures', async () => {
 const props=Vue.reactive({nodeId:'ticket',projectId:'project',de:false}), pending=deferred()
 const first={items:[{id:'round',key:'AEON-888',kind:'fix',round_number:1,state:'queued',hold_reason:null,reason:'slug_hold',estimate_minutes:60}],settings:{mode:'shadow',freeze:true},next_cursor:null}
 http.handle=async path => path.includes('ticket=ticket') ? pending.promise : json({...first,items:[]})
 const queue=setup<{result:Vue.Ref<typeof first|null>;error:Vue.Ref<string>;load:() => Promise<unknown>}>('work/DeliveryQueue.vue',props)
 props.nodeId='other';await settle();pending.release(json(first));await settle()
 expect(queue.result.value?.items).toEqual([])
 http.handle=async () => json(first);props.nodeId='ticket';await settle()
 expect(queue.result.value?.items[0]?.id).toBe('round')
 http.handle=async () => json({error:'Unavailable'},503);await queue.load()
 expect(queue.error.value).toContain('could not be loaded');expect(queue.result.value?.items[0]?.id).toBe('round')
 const late=deferred();http.handle=async () => late.promise;const loading=queue.load()
 session.identity.principal.id='new-person';await settle();late.release(json(first));await loading
 expect(queue.result.value).toBeNull()
})
