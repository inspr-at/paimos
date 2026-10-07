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
 const modules:Record<string,unknown> = { vue:{...Vue,onBeforeUnmount:(fn:() => void) => unmounts.push(fn)}, '../../lib/api':api, '../../lib/authz':{can:(key:string) => key.endsWith('.read') ? access.read : access.manage,onAccessChange:() => () => {}}, '../../lib/delivery':delivery,'../../lib/reviewPolicy':policy,'../../lib/reviews':reviews,'../../lib/identityScope':identity,'../../lib/toast':{toast:(_text:string,options:{action:{run:() => void}}) => actions.push(options.action.run)},'../../stores/session':{useSession:() => session},'../../stores/profile':{useProfile:() => profile}, '../AppIcon.vue':{}, '../KeyCap.vue':{}, './DeliveryRow.vue':{}, './SettingsCard.vue':{} }
 const {descriptor}=parse(readFileSync(new URL(`../src/components/${file}`,import.meta.url),'utf8'))
 const {content}=compileScript(descriptor,{id:file})
 const exports:{default?:{setup:(props:object,context:object) => T}}={}
 new Function('require','exports',ts.transpileModule(content,{compilerOptions:{module:ts.ModuleKind.CommonJS,target:ts.ScriptTarget.ES2022}}).outputText)((name:string) => {if(!(name in modules))throw new Error(name);return modules[name]},exports)
 const scope=Vue.effectScope();scopes.push(scope)
 return scope.run(() => exports.default!.setup(props,{expose:() => {}}))!
}
beforeEach(() => { vi.useFakeTimers();vi.stubGlobal('navigator',{platform:'Mac'});session.identity.principal.id='person';access.manage=true;access.read=true;http.handle=async () => json(settings()) })
afterEach(() => {for(const stop of unmounts.splice(0))stop();for(const scope of scopes.splice(0))scope.stop();actions.splice(0);vi.useRealTimers();vi.unstubAllGlobals()})
type Ticket = { result:Vue.Ref<delivery.DeliveryPage|null>; lift:(item:delivery.DeliveryItem) => Promise<unknown>; mutationError:Vue.Ref<string> }
type Card = { base:Vue.Ref<policy.ReviewPolicySettings|null>; selected:Vue.Ref<string>; mode:Vue.Ref<string>; families:Vue.Ref<string[]>; save:() => Promise<unknown>; write:(policy:policy.ReviewPolicy|null,reset:boolean) => Promise<unknown>; message:Vue.Ref<string>; dirty:Vue.ComputedRef<boolean> }
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
it('a policy save cannot report success after the person changes', async () => {
 const card=setup<Card>('settings/CrossFamilyReviewCard.vue',{});await settle();card.mode.value='off'
 const pending=deferred();http.handle=async () => pending.promise
 const saving=card.save();session.identity.principal.id='different';await settle()
 pending.release(json({...settings(),effective:{mode:'off',allowed_families:[]}}));await saving
 expect(card.message.value).not.toContain('Saved.');expect(card.base.value?.effective.mode).not.toBe('off')
})
