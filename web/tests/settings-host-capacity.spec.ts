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
async function setup(page: Page, manage = true) {
  await page.clock.setSystemTime(NOW)
  await mockWork(page,fixtures(),{admin:true})
  const world = capacityWorld(), data = agentData({ me: me.id, now: NOW, projects: {}, tickets: {}, nodes: {} })
  data.accounts = world.accounts as unknown as typeof data.accounts
  const c = world.computers[0]!
  c.revision = 4
  c.enrollments.forEach(e => Object.assign(e,{can_verify:true,verification_state:'completed'}))
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
    Object.assign((c as unknown as {host_capacity:{policy:unknown}}).host_capacity,{policy:body.policy}); c.revision!++
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
  const pane = page.getByRole('dialog',{name:'mbp2607'})
  await expect(pane).toBeVisible()
  await pane.getByRole('button',{name:/Capacity and load/}).click()
  return pane
}
for (const width of [390,1024,1440]) for (const theme of ['light','dark'] as const) {
  test(`host settings keep controls stable through all modes at ${width} ${theme}`,async({page}) => {
    await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:theme})
    const errors = watchErrors(page), s = await setup(page)
    const pane = await computer(page)
    const group = pane.getByRole('radiogroup',{name:'Load setting'}), apply = pane.getByRole('button',{name:/^Apply/})
    await expect(group.getByRole('radio',{name:'Off',exact:true})).toHaveAttribute('aria-checked','true')
    await expectStableControls({controls:{group,apply,smart:group.getByRole('radio',{name:'Smart'}),fixed:group.getByRole('radio',{name:'Fixed limit'}),off:group.getByRole('radio',{name:'Off',exact:true})},scrollAreas:{body:pane.locator('.pane-body')},interactions:[
      {name:'smart',run:async()=>{await group.getByRole('radio',{name:'Smart'}).click();await expect(pane.getByRole('switch',{name:/Be gentler/})).not.toBeChecked()}},
      {name:'fixed',run:async()=>{await group.getByRole('radio',{name:'Fixed limit'}).click();await expect(pane.getByRole('spinbutton',{name:'Maximum load'})).toBeVisible()}},
      {name:'off',run:async()=>{await group.getByRole('radio',{name:'Off',exact:true}).click()}},
    ]})
    await group.getByRole('radio',{name:'Smart'}).click()
    await pane.getByRole('spinbutton',{name:'Maximum agents'}).fill('12')
    await apply.click()
    await expect.poll(()=>s.writes.length).toBe(1)
    expect(s.writes[0]).toMatchObject({expected_revision:4,policy:{mode:'smart',maximum_agents:12,consider_activity:false}})
    if (process.env.AEON686_SHOTS) { mkdirSync(process.env.AEON686_SHOTS,{recursive:true});await page.screenshot({path:`${process.env.AEON686_SHOTS}/computer-${width}-${theme}.png`,fullPage:true}) }
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
