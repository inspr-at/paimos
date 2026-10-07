// SPDX-License-Identifier: AGPL-3.0-only
// AEON-853: real component wiring, honest writes and stable controls in EN/DE.
import { expect, test, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { mockSettings, settingsData } from './settings-fixtures'
import { policyLadder } from './policies-fixtures'
import { controlStability } from './control-stability'
import type { DeliveryItem, DeliveryPage, DeliveryState } from '../src/lib/delivery'
import type { ReviewPolicy, ReviewPolicySettings } from '../src/lib/reviewPolicy'

const at = '2026-10-07T05:00:00Z', now = '2026-10-07T06:00:00Z'
function row(state:DeliveryState, index:number):DeliveryItem {
 return { id:`delivery-${index}`,project_id:'p-pharos',ticket_node_id:'n-1',repository:'example/paimos',pull_request:357+index,branch:'work/aeon-853-delivery-status-and-cross-family-review-settings-with-a-long-german-name',head_sha:'a'.repeat(40),state,state_since:at,owner:state==='held'?'person':state==='pushed'?'ci':state==='in_queue'?'queue':state==='queue_failed'?'builder':state==='merged'?'':'coordinator',deadline_at:state==='held'||state==='built'||state==='merged'?null:index===2?'2026-10-07T05:45:00Z':'2026-10-07T06:30:00Z',held_reason:state==='held'?'Die Freigabe der Lieferdokumentation wird vor dem nächsten Queue-Lauf durch die zuständige Person geprüft.':null,held_from_state:state==='held'?'ci_green':null,required_checks_passed:3,required_checks_total:5,link_source:'title_key',updated_at:at }
}
async function setup(page:Page, theme:'light'|'dark'='light', lang:'en'|'de'='en', manage=true) {
 const work=fixtures();work.preferences.theme={choice:theme};await mockWork(page,work,{admin:true})
 const data=settingsData();data.profile.locale=lang==='de'?'de-AT':'en-GB';await mockSettings(page,data)
 await page.clock.install({ time: new Date(now) })
 await page.route('**/api/me/permissions*',route => {
  const answer=mockEffectivePermissions('admin',new URL(route.request().url()).searchParams.get('project_id')??undefined)
  const grants=['delivery.read','reviewpolicy.read',...(manage?['delivery.manage','reviewpolicy.manage']:[])]
  answer.workspace.permissions.push(...grants);answer.project?.permissions.push(...grants)
  return route.fulfill({json:answer})
 })
 await page.route('**/api/models/routes*',route => route.fulfill({json:policyLadder('review-gate')}))
 await page.route('**/api/settings/work-vocabulary',route => route.fulfill({json:{revision:0,leaf:{name:'',icon:''},levels:[],lead:{singular:'Dirigent',plural:'Dirigenten'}}}))
 const delivery:DeliveryPage={available:true,next_cursor:null,items:['held','queue_failed','pushed','built','reviewed','ci_green','in_queue','merged'].map((state,i) => row(state as DeliveryState,i))}
 let failRead=false, failWrite=false, policyRevision=1
 const writes:{path:string;body:unknown;revision:string|undefined}[]=[]
 const workspace:ReviewPolicy={mode:'other_family',allowed_families:[]}, overrides=new Map<string,ReviewPolicy>()
 const answer=(id:string):ReviewPolicySettings => ({project_id:id||null,policy:id?overrides.get(id)??null:{...workspace},effective:id?overrides.get(id)??{...workspace}:{...workspace},workspace:{...workspace},source:id&&overrides.has(id)?'project':'tenant',updated_by:!id||overrides.has(id)?'editor':null,updated_at:!id||overrides.has(id)?`2026-10-07T05:00:${String(policyRevision).padStart(2,'0')}Z`:null,valid_families:['openai','anthropic','xai','cursor','google','local']})
 await page.route('**/api/nodes/*/delivery*',route => failRead?route.fulfill({status:503,json:{error:'Delivery read failed'}}):route.fulfill({json:delivery}))
 await page.route('**/api/delivery/*/hold',route => {
  writes.push({path:new URL(route.request().url()).pathname,body:null,revision:route.request().headers()['if-unmodified-since']})
  if(failWrite)return route.fulfill({status:412,json:{error:'Delivery changed; reload before lifting the hold'}})
  const item=delivery.items.find(i => route.request().url().includes(i.id))!;Object.assign(item,{state:'ci_green',held_from_state:null,held_reason:null,owner:'coordinator',state_since:now,updated_at:now,deadline_at:'2026-10-07T06:20:00Z'})
  return route.fulfill({json:item})
 })
 await page.route(/\/api\/(settings\/review-policy|projects\/[^/]+\/review-policy)$/,route => {
  const path=new URL(route.request().url()).pathname,id=/projects\/([^/]+)/.exec(path)?.[1]??'',method=route.request().method()
  if(method!=='GET') {
   writes.push({path,body:method==='DELETE'?null:route.request().postDataJSON(),revision:route.request().headers()['if-unmodified-since']})
   if(failWrite)return route.fulfill({status:403,json:{error:'Review policy permission was revoked'}})
   policyRevision++
   if(method==='DELETE')overrides.delete(id)
   else if(id)overrides.set(id,route.request().postDataJSON())
   else Object.assign(workspace,route.request().postDataJSON())
  }
  return route.fulfill({json:answer(id)})
 })
 return {delivery,writes,overrides,setFailRead:(value:boolean) => {failRead=value},setFailWrite:(value:boolean) => {failWrite=value}}
}
const ticket=(page:Page) => page.getByRole('region',{name:/^(Delivery|Lieferstatus)$/})
const card=(page:Page) => page.locator('#cross-family-review')
for(const width of [390,1024,1440])for(const theme of ['light','dark'] as const)for(const lang of ['en','de'] as const) {
 test(`delivery and review card keep controls still at ${width} ${theme} ${lang}`,async ({page},info) => {
  await page.setViewportSize({width,height:width===390?844:1000});await setup(page,theme,lang)
  await page.goto('/p/PHAROS/PHAROS-11')
  const block=ticket(page);await expect(block.locator('.delivery-row')).toHaveCount(8)
  await expect(block).toContainText(lang==='de'?'3 von 5 bestanden':'3 of 5 passed')
  await expect(block).toContainText('Dirigent')
  await expect(block).toContainText(lang==='de'?'Lieferfrist um 15 Min. überschritten':'Past its deadline by 15 min')
  const held=block.locator('[data-delivery-id="delivery-0"]'),lift=held.getByRole('button',{name:lang==='de'?'Halt aufheben':'Lift hold'})
  const deliveryGuard=await controlStability(page,{header:block.locator('.head'),reload:block.getByRole('button',{name:/neu laden|Reload delivery/}),lift,row:held.locator('.top'),pr:held.getByRole('link').first()})
  await deliveryGuard.check(() => held.locator('.where').focus())
  await held.locator('.where').evaluate(el => (el as HTMLElement).blur())
  await page.mouse.move(0, 0)
  await block.locator('.head').scrollIntoViewIfNeeded()
  await page.screenshot({path:info.outputPath(`aeon-853/delivery-${width}-${theme}-${lang}.png`)})
  for (const state of ['held', 'queue_failed', 'pushed', 'built', 'reviewed', 'ci_green', 'in_queue', 'merged']) {
    const index = ['held', 'queue_failed', 'pushed', 'built', 'reviewed', 'ci_green', 'in_queue', 'merged'].indexOf(state)
    const sample = block.locator(`[data-delivery-id="delivery-${index}"]`)
    await sample.scrollIntoViewIfNeeded()
    await sample.screenshot({path:info.outputPath(`aeon-853/row-${state}-${width}-${theme}-${lang}.png`)})
  }
  await deliveryGuard.check(async () => { await lift.click(); await expect(held.locator('.state')).toHaveText(lang==='de'?'CI grün':'CI green') });deliveryGuard.done()
  await page.goto('/settings/policies#cross-family-review')
  const policy=card(page);await expect(policy.getByRole('radio').first()).toBeEnabled()
  const save=policy.getByRole('button',{name:/Save rule|Regel speichern/}),discard=policy.getByRole('button',{name:/Discard|Verwerfen/})
  const controls={save,discard,scope:policy.getByLabel(/Applies to|Gilt für/),modes:policy.locator('.modes'),off:policy.locator('.option').nth(0),other:policy.locator('.option').nth(1),allow:policy.locator('.option').nth(2),families:policy.locator('.families')}
  const guard=await controlStability(page,controls)
  for(const radio of await policy.getByRole('radio').all())await guard.check(() => radio.check())
  await expect(save).toBeDisabled()
  await guard.check(() => policy.getByRole('checkbox').nth(1).check())
  await expect(save).toBeEnabled()
  await guard.check(async () => {await save.click();await expect(policy.getByRole('status')).toContainText(lang==='de'?'Gespeichert.':'Saved.')})
  guard.done()
  await policy.screenshot({path:info.outputPath(`aeon-853/review-policy-${width}-${theme}-${lang}.png`)})
 })
}
test('delivery loading, empty, unavailable and error replace one slot; readers have no management actions',async ({page}) => {
 const state=await setup(page,'light','en',false);state.delivery.items=[]
 await page.goto('/p/PHAROS/PHAROS-11');const block=ticket(page)
 await expect(block).toContainText('No pull request yet')
 const height=await block.locator('.slot').evaluate(el => el.getBoundingClientRect().height)
 const guard=await controlStability(page,{header:block.locator('.head'),reload:block.getByRole('button',{name:'Reload delivery status'})})
 state.delivery.available=false
 await guard.check(async () => {await block.getByRole('button',{name:'Reload delivery status'}).click();await expect(block).toContainText('Delivery status is not available')})
 expect(await block.locator('.slot').evaluate(el => el.getBoundingClientRect().height)).toBe(height)
 state.setFailRead(true)
 await guard.check(async () => {await block.getByRole('button',{name:'Reload delivery status'}).click();await expect(block).toContainText('Delivery status could not be loaded')})
 expect(await block.locator('.slot').evaluate(el => el.getBoundingClientRect().height)).toBe(height)
 guard.done();state.setFailRead(false);state.delivery.available=true;state.delivery.items=[row('held',0)]
 await block.getByRole('button',{name:'Reload delivery status'}).click();await expect(block).toContainText('Only people with Manage delivery status can lift a hold.')
 await expect(block.getByRole('button',{name:'Lift hold'})).toHaveCount(0)
 await page.goto('/settings/policies#cross-family-review');await expect(card(page).getByRole('radio').first()).toBeDisabled();await expect(card(page)).toContainText('Manage review policy');await expect(card(page).getByRole('button',{name:/Save rule/})).toHaveCount(0)
})
test('failed writes stay honest; project reset offers scope-bound Undo and uses captured revisions',async ({page}) => {
 const state=await setup(page);await page.goto('/p/PHAROS/PHAROS-11');const block=ticket(page)
 state.setFailWrite(true);await block.getByRole('button',{name:'Lift hold'}).click();await expect(block.getByRole('alert')).toContainText('Delivery changed');await expect(block.locator('[data-delivery-id="delivery-0"]')).toContainText('On hold')
 expect(state.writes[0]?.revision).toBe(at)
 await page.goto('/settings/policies#cross-family-review');const policy=card(page)
 await policy.getByLabel('Applies to').selectOption('p-pharos');await expect(policy.getByRole('radio',{name:/Use the workspace default/})).toBeChecked()
 await policy.getByRole('radio',{name:/^Off/}).check();await policy.getByRole('button',{name:/Save rule/}).click();await expect(policy.getByRole('status')).toContainText('permission was revoked');await expect(policy.getByRole('radio',{name:/^Off/})).toBeChecked()
 state.setFailWrite(false);await policy.getByRole('button',{name:/Save rule/}).click();await expect(policy.getByRole('status')).toContainText('Saved.')
 await policy.getByRole('button',{name:'Reset to workspace default'}).click();await expect(policy.getByRole('radio',{name:/Use the workspace default/})).toBeChecked()
 await page.getByRole('button',{name:'Undo',exact:true}).click();await expect(policy.getByRole('radio',{name:/^Off/})).toBeChecked()
 expect(state.overrides.get('p-pharos')?.mode).toBe('off')
 await policy.getByRole('button',{name:'Reset to workspace default'}).click();await policy.getByLabel('Applies to').selectOption('p-aeon')
 const writes=state.writes.length;await page.getByRole('button',{name:'Undo',exact:true}).click();expect(state.writes).toHaveLength(writes)
})
