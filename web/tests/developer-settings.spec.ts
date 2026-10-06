// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { controlStability } from './control-stability'

test('retired Flow bookmarks open tickets and saved opt-ins cannot revive it', async ({ page }) => {
 const data=fixtures();data.preferences['developer-ui']={show_flow_controls:true}
 await mockWork(page,data)
 const calls:string[]=[];page.on('request',request=>{if (/\/(journey|stage-handoffs)/.test(new URL(request.url()).pathname)&&new URL(request.url()).pathname.startsWith('/api/'))calls.push(request.url())})
 for(const path of ['/p/PHAROS/journey?stage=plan&walk=PHAROS-11','/p/PHAROS?view=journey&stage=build','/p/PHAROS/PHAROS-11?section=journey','/projects/p-pharos/journey/plan']){
  await page.goto(path)
  await expect(page.getByRole('tab',{name:'Tickets',exact:true})).toHaveAttribute('aria-selected','true')
  await expect(page.locator('.journey-view,.journey-chip,.flow-slot')).toHaveCount(0)
  await expect(page.getByRole('tab',{name:'Journey',exact:true})).toHaveCount(0)
 }
 expect(calls).toEqual([])
})
for(const width of [390,1024,1440]) for(const theme of ['light','dark'] as const) {
 test(`retirement keeps project and settings controls stable ${width} ${theme}`,async({page},info)=>{
  await page.setViewportSize({width,height:900});await page.emulateMedia({colorScheme:theme})
  const data=fixtures();data.preferences.theme={choice:theme};data.preferences['developer-ui']={show_flow_controls:true};data.preferences['list:display']={headerGraph:false}
  data.projects[0]!.title='Pharos · Betriebsübersicht'
  data.projects[0]!.description='Überprüfung der mandantenübergreifenden Berechtigungsverwaltung und außergewöhnlich langer Projektbeschreibungen mit nachvollziehbaren Änderungen für sämtliche verantwortlichen Personen und Agenten.'
  await mockWork(page,data)
  await page.route('**/api/queue?*',route=>route.fulfill({json:{items:[],manual_order:false,capacity:{queued_hours:0,parallel_runs:0,work_hours:0,warning:false}}}))
  await page.goto('/p/PHAROS/tickets')
  await expect(page.getByRole('tab',{name:'Tickets',exact:true})).toBeVisible()
  await expect(page.locator('#row-n-1')).toBeVisible()
  await page.evaluate(()=>document.fonts.ready)
  const open=page.getByRole('button',{name:/^Filter open tickets:/})
  const guard=await controlStability(page,{sections:page.getByRole('tablist',{name:'Project sections'}),tickets:page.getByRole('tab',{name:'Tickets',exact:true}),counts:page.getByRole('group',{name:'Filter tickets by status'}),open})
  await guard.check(async()=>{await open.click();await expect(open).toHaveAttribute('aria-pressed','true')})
  await guard.check(async()=>{await open.click();await expect(open).toHaveAttribute('aria-pressed','false')});guard.done()
  await page.screenshot({path:info.outputPath(`project-${width}-${theme}.png`)})
  await page.goto('/settings/developer')
  const control=page.getByRole('switch',{name:'Show reserved versions',exact:true})
  await expect(control).toBeVisible();await expect(page.getByRole('switch')).toHaveCount(1)
  const settings=await controlStability(page,{reserved:control})
  await settings.check(async()=>{await control.check();await expect.poll(()=>data.preferences['developer-ui']?.show_reserved_versions).toBe(true);await expect(control).toBeEnabled()})
  await settings.check(async()=>{await control.uncheck();await expect.poll(()=>data.preferences['developer-ui']?.show_reserved_versions).toBe(false);await expect(control).toBeEnabled()});settings.done()
  await expect(page.getByRole('status').filter({hasText:'Saving your preference'})).toHaveCount(0)
  await page.screenshot({path:info.outputPath(`developer-${width}-${theme}.png`)})
 })
}
