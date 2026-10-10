// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, me, mockWork, watchErrors } from './work-fixtures'
import { agentData, mockAgents } from './agents-fixtures'
import { capacityWorld, NOW, TZ } from './capacity-fixtures'
import { mockEffectivePermissions } from './authz-fixtures'
import { expectStableControls } from './helpers/stable'
import { defaultHostPolicy } from '../src/lib/hostCapacity'

test.use({ timezoneId: TZ })
async function setup(page: Page, manage = true, longText = false, expired = longText) {
  await page.clock.setSystemTime(NOW)
  await mockWork(page,fixtures(),{admin:true})
  const world = capacityWorld(), data = agentData({ me: me.id, now: NOW, projects: {}, tickets: {}, nodes: {} })
  data.accounts = world.accounts as unknown as typeof data.accounts
  const c = world.computers[0]!
  if (longText) { c.computer_name = 'build-7 · Arbeitscomputer mit einem ausführlichen deutschen Namen'; data.accounts[0]!.label = 'Arbeitskonto für gemeinsame Entwicklungsaufgaben mit einem ausführlichen deutschen Namen'; c.enrollments[0]!.label = data.accounts[0]!.label }
  c.revision = 4
  c.enrollments.forEach(e => Object.assign(e,{can_verify:true,verification_state:'completed'}))
  Object.assign(c,{harness_statuses:{codex:'ready',claude:'ready',grok:'ready',cursor:'ready'},verification_capabilities:{...c.verification_capabilities,claude:{supported:true,policy:'read_only',reason:''}}})
  if (expired) {
    const e=c.enrollments.find(e=>e.harness==='claude')!
    Object.assign(e,{verification_state:'expired',verification_expired_ready:false,verified_at:new Date(NOW-86400000).toISOString(),verification_expires_at:new Date(NOW-3600000).toISOString(),last_used_at:new Date(NOW-7200000).toISOString()})
  }
  if (longText) { const removed=world.computers[1]!;Object.assign(removed,{computer_state:'revoked',local_cleanup:'pending',accounting_state:'unconfirmed'});removed.enrollments.forEach(e=>Object.assign(e,{state:'revoked'})) }
  Object.assign(c,{host_capacity:{policy:defaultHostPolicy(),signals:{load:32.4,cores:18,memory_pressure:'normal',power:'plugged_in',thermal:'normal'},running:8,queued:4,reported_at:new Date(NOW).toISOString(),reason:'',load_limit:0,history:[{at:new Date(NOW-60000).toISOString(),load:20},{at:new Date(NOW).toISOString(),load:32.4}]}})
  await mockAgents(page,data,{capacity:world})
  await page.route('**/api/me/permissions*',r => {
    const answer = mockEffectivePermissions('admin')
    answer.workspace.permissions = [...answer.workspace.permissions.filter(p => manage || !['account.manage','settings.manage'].includes(p)), 'account.read', ...(manage ? ['account.manage','settings.manage'] : [])]
    return r.fulfill({json:answer})
  })
  const writes: unknown[] = []
  let fail = false
  await page.route('**/api/agent-pairing/computers/*/capacity',r => {
    const body = r.request().postDataJSON(); writes.push(body)
    if (fail) return r.fulfill({status:409,json:{error:'changed'}})
    Object.assign((c as unknown as {host_capacity:{policy:unknown}}).host_capacity,{policy:body.policy,reason:body.policy.mode==='smart'?'host_load':'',load_limit:body.policy.mode==='smart'?30:0}); c.revision!++
    return r.fulfill({json:(c as unknown as {host_capacity:unknown}).host_capacity})
  })
  let thresholds = {early_percent:10,urgent_percent:3}
  await page.route('**/api/settings/quota-warnings',r => {
    if (r.request().method() === 'PUT') { const b = r.request().postDataJSON(); writes.push(b); thresholds = {early_percent:b.early_percent,urgent_percent:b.urgent_percent} }
    return r.fulfill({json:thresholds})
  })
  await page.goto('/settings/accounts')
  await expect(page.locator('[data-computer]').first()).toBeVisible()
  return {world,writes,setFail:(value:boolean)=>{fail=value}}
}
async function computer(page: Page) {
  await page.locator('[data-computer]').first().click()
  const pane = page.getByRole('dialog',{name:/^build-7/})
  await expect(pane).toBeVisible()
  await pane.getByRole('button',{name:/Capacity and load/}).click()
  return pane
}
for (const width of [390,1024,1440]) for (const theme of ['light','dark'] as const) {
  test(`host settings keep controls stable through all modes at ${width} ${theme}`,async({page}) => {
    await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:theme})
    const errors = watchErrors(page), s = await setup(page,true,true)
    if (process.env.AEON686_SHOTS) {
      mkdirSync(process.env.AEON686_SHOTS,{recursive:true})
      await page.locator('main').evaluate(el=>{el.scrollTop=0})
      await page.screenshot({path:`${process.env.AEON686_SHOTS}/overview-${width}-${theme}.png`,fullPage:true})
    }
    const pane = await computer(page)
    await expect(page.locator('.settings-frame')).toHaveClass(new RegExp(`mode-${width >= 1200 ? 'dock' : width > 720 ? 'side' : 'sheet'}`))
    await expect(pane.locator('.pane-title')).toContainText('Connected')
    await expect(page.locator('.accounts-computers')).not.toContainText('[object Object]')
    const group = pane.getByRole('radiogroup',{name:'Load setting'}), apply = pane.getByRole('button',{name:/^Apply/})
    await expect(group.getByRole('radio',{name:'Off',exact:true})).toHaveAttribute('aria-checked','true')
    await expectStableControls({controls:{frame:pane,group,apply,maximum:pane.getByRole('spinbutton',{name:'Maximum agents'}),close:pane.getByRole('button',{name:'Close details'}),smart:group.getByRole('radio',{name:'Smart'}),fixed:group.getByRole('radio',{name:'Fixed limit'}),off:group.getByRole('radio',{name:'Off',exact:true})},scrollAreas:{body:pane.locator('.pane-body')},interactions:[
      {name:'smart',run:async()=>{await group.getByRole('radio',{name:'Smart'}).click();await expect(pane.getByRole('switch',{name:/Be gentler/})).not.toBeChecked()}},
      {name:'fixed',run:async()=>{await group.getByRole('radio',{name:'Fixed limit'}).click();await expect(pane.getByRole('spinbutton',{name:'Maximum load'})).toBeVisible()}},
      {name:'off',run:async()=>{await group.getByRole('radio',{name:'Off',exact:true}).click()}},
    ]})
    await group.getByRole('radio',{name:'Smart'}).click()
    await pane.getByRole('spinbutton',{name:'Maximum agents'}).fill('12')
    await apply.click()
    await expect.poll(()=>s.writes.length).toBe(1)
    expect(s.writes[0]).toMatchObject({expected_revision:4,policy:{mode:'smart',maximum_agents:12,consider_activity:false}})
    await expect(pane.getByRole('status')).toHaveText('All changes saved')
    await expect(pane.getByRole('button',{name:/Capacity and load/})).toContainText('Smart')
    await expect(apply).toBeDisabled()
    if (process.env.AEON686_SHOTS) {
      const shots=process.env.AEON686_SHOTS;mkdirSync(shots,{recursive:true})
      await page.screenshot({path:`${shots}/computer-${width}-${theme}.png`,fullPage:true})
      await pane.getByRole('button',{name:'Close details'}).click()
      await page.screenshot({path:`${shots}/lists-${width}-${theme}.png`,fullPage:true})
      await page.locator('[data-account]').first().click()
      const accountPane=page.getByRole('dialog',{name:'Codex'});await expect(accountPane).toBeVisible()
      await page.screenshot({path:`${shots}/account-${width}-${theme}.png`,fullPage:true})
      await accountPane.getByRole('button',{name:'Close details'}).click()
      await page.locator('.quota-line').getByRole('button',{name:'Change'}).click()
      await expect(page.getByRole('dialog',{name:'Low-quota warnings'})).toBeVisible()
      await page.screenshot({path:`${shots}/quota-warnings-${width}-${theme}.png`,fullPage:true})
      await page.getByRole('dialog',{name:'Low-quota warnings'}).getByRole('button',{name:'Cancel'}).click()
      await page.locator('[data-computer]').last().click()
      await expect(page.getByRole('dialog',{name:'studio'})).toBeVisible()
      await expect(page.getByRole('dialog',{name:'studio'}).getByRole('button',{name:'Finish cleanup…'})).toBeDisabled()
      await page.screenshot({path:`${shots}/cleanup-${width}-${theme}.png`,fullPage:true})
    }
    expect(errors).toEqual([])
  })
}
test('failed capacity apply stays unsaved and reports the actual failure',async({page}) => {
  const s = await setup(page);const pane = await computer(page);s.setFail(true)
  await pane.getByRole('radio',{name:'Fixed limit'}).click();await pane.getByRole('button',{name:/^Apply/}).click()
  await expect(pane.getByRole('status')).toHaveText('Computer changed. Reopen its settings before applying.')
  await expect(pane.getByRole('button',{name:/^Apply/})).toBeEnabled()
})
test('members see account and computer details without mutation controls',async({page}) => {
  await setup(page,false);await expect(page.locator('.quota-line')).toContainText('early 10 %, urgent 3 %')
  await expect(page.locator('.quota-line').getByRole('button',{name:'Change'})).toHaveCount(0)
  const pane = await computer(page)
  await expect(pane.getByRole('radio')).toHaveCount(0);await expect(pane.getByRole('button',{name:/^Apply/})).toHaveCount(0)
  await expect(pane.getByRole('button',{name:/More/})).toHaveCount(0)
  await expect(pane.locator('.read-policy')).toContainText('Off')
})
test('quota warning popover submits with platform modifier and Undo restores its snapshot',async({page}) => {
  const s = await setup(page);await page.locator('.quota-line').getByRole('button',{name:'Change'}).click()
  const form = page.getByRole('dialog',{name:'Low-quota warnings'})
  await form.getByRole('spinbutton',{name:'Early notice (%)'}).fill('20');await form.getByRole('spinbutton',{name:'Urgent notice (%)'}).fill('5')
  const mac = await page.evaluate(()=>/Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent))
  await form.getByRole('spinbutton',{name:'Urgent notice (%)'}).press(mac?'Meta+Enter':'Control+Enter')
  await expect(page.locator('.quota-line')).toContainText('early 20 %, urgent 5 %')
  expect(s.writes[0]).toMatchObject({expected_early_percent:10,expected_urgent_percent:3})
  await page.getByRole('button',{name:'Undo',exact:true}).click()
  await expect(page.locator('.quota-line')).toContainText('early 10 %, urgent 3 %')
})

test('Needs you verifies the expired sign-in once and reports a failed request honestly',async({page}) => {
  const s=await setup(page,true,false,true), e=s.world.computers[0]!.enrollments.find(e=>e.harness==='claude')!
  const requests: unknown[]=[]
  await page.route('**/api/agent-pairing/computers/*/enrollments/*/verify',r=>{
    requests.push(r.request().postDataJSON())
    expect(r.request().url()).toContain(`/enrollments/${e.account_id}/verify`)
    return r.fulfill({status:503,json:{error:'unavailable'}})
  })
  const action=page.locator('.needs-block').getByRole('button',{name:'Verify again'})
  await expect(action).toHaveCount(1)
  await action.click()
  await expect(page.getByText('Verification could not be requested.',{exact:true})).toBeVisible()
  expect(requests).toEqual([{expected_revision:4,expected_verification_run_id:null}])
  await expect(page.getByText('Verification requested on build-7.',{exact:true})).toHaveCount(0)
  await page.locator(`[data-computer="${s.world.computers[0]!.computer_id}"]`).click()
  await expect(page.getByRole('dialog',{name:'build-7'}).getByRole('button',{name:'Verify again',exact:true})).toBeVisible()
})

test('sign-out confirmation names its scope and a failed write keeps the reviewed account open',async({page}) => {
  const s=await setup(page), requests: unknown[]=[]
  await page.route('**/api/agent-pairing/accounts/sign-out',r=>{
    requests.push(r.request().postDataJSON())
    return r.fulfill({status:409,json:{error:'sign-ins changed'}})
  })
  await page.locator('[data-account]').first().click()
  const pane=page.getByRole('dialog',{name:'Codex',exact:true})
  await pane.getByRole('button',{name:'More actions for Codex'}).click()
  await page.getByRole('menuitem',{name:/Sign out everywhere/}).click()
  const confirm=page.getByRole('dialog',{name:'Sign out of Codex everywhere?'})
  await expect(confirm).toContainText('Other accounts, the vendor subscription and vendor sessions outside PAIMOS are untouched.')
  expect(requests).toEqual([])
  const cancel=confirm.getByRole('button',{name:'Cancel'}), signout=confirm.getByRole('button',{name:'Sign out everywhere',exact:true})
  await expectStableControls({controls:{cancel,signout},scrollAreas:{confirmation:confirm},interactions:[{name:'failed signout',run:async()=>{await signout.click();await expect(confirm.getByRole('alert')).toContainText('Sign-ins changed or sign-out failed.')}}]})
  expect(requests).toEqual([{targets:[{computer_id:s.world.computers[0]!.computer_id,account_id:s.world.accounts[0]!.id,expected_revision:4}]}])
  await expect(pane).toBeVisible()
  await cancel.click();await expect(confirm).toHaveCount(0)
})

// AEON-686 review: pausing, approval and confirming a shared login stay in the
// account panel. A newly matched login gains the canonical identity there, the
// panel follows it, and Sign out everywhere then names every sign-in.
test('a matching login joins the shared quota from the account panel and Sign out everywhere covers it',async({page}) => {
  const errors=watchErrors(page)
  const s=await setup(page), fingerprint='ab'.repeat(32)
  const [main,studio]=[s.world.accounts.find(a=>a.label==='Main')!,s.world.accounts.find(a=>a.label==='Studio')!]
  for (const a of s.world.accounts.filter(a=>a.harness==='codex')) Object.assign(a,{quota_fingerprint:fingerprint,quota_pool_fingerprint:''})
  const pools: unknown[]=[], signouts: unknown[]=[]
  await page.route('**/api/agent-accounts/quota-pool',async r=>{
    const body=r.request().postDataJSON(); pools.push(body)
    for (const a of s.world.accounts) if (body.account_ids.includes(a.id)) a.quota_pool_fingerprint=body.confirmed?body.quota_fingerprint:''
    await r.fulfill({status:204})
  })
  await page.route('**/api/agent-pairing/accounts/sign-out',r=>{ signouts.push(r.request().postDataJSON()); return r.fulfill({json:{signed_out:2,local_cleanup:'pending'}}) })
  await page.reload()
  await page.locator(`.list-row[data-accounts~="${main.id}"]`).click()
  const pane=page.locator('section.pane'), use=pane.locator('.use-sec')
  await expect(use.locator('[data-account]')).toHaveCount(1)
  await expect(use.getByRole('switch',{name:'Agents may use it · Main'})).toBeEnabled()
  await use.getByRole('button',{name:'Details for Main'}).click()
  await use.getByRole('button',{name:'Pool with Studio · studio',exact:true}).click()
  const confirm=page.getByRole('dialog',{name:'Same login — pool them?'})
  await expect(confirm.locator('.points li')).toHaveText(['Main · build-7','Studio · studio'])
  await confirm.getByRole('button',{name:'Pool accounts'}).click()
  await expect.poll(()=>pools).toEqual([{account_ids:[main.id,studio.id],quota_fingerprint:fingerprint,confirmed:true}])
  // The same panel now shows the canonical account with both logins.
  await expect(use.locator('[data-account]')).toHaveCount(2)
  await expect(use.locator('.p-head span')).toHaveText('2 logins share this quota')
  await expect(page.locator(`.list-row[data-accounts~="${studio.id}"]`)).toHaveAttribute('data-accounts',`${main.id} ${studio.id}`)
  await pane.getByRole('button',{name:'More actions for Codex'}).click()
  await page.getByRole('menuitem',{name:/Sign out everywhere/}).click()
  await page.getByRole('dialog',{name:'Sign out of Codex everywhere?'}).getByRole('button',{name:'Sign out everywhere',exact:true}).click()
  await expect.poll(()=>signouts.length).toBe(1)
  const computerOf=(id:string)=>s.world.computers.find(c=>c.enrollments.some(e=>e.account_id===id))!
  expect(signouts[0]).toEqual({targets:[main.id,studio.id].map(id=>({computer_id:computerOf(id).computer_id,account_id:id,expected_revision:computerOf(id).revision}))})
  expect(errors).toEqual([])
})
