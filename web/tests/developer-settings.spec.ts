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
  const data=fixtures();data.preferences.theme={choice:theme};data.preferences['developer-ui']={show_flow_controls:true}
  await mockWork(page,data);await page.goto('/p/PHAROS/tickets')
  await expect(page.getByRole('tab',{name:'Tickets',exact:true})).toBeVisible()
  const guard=await controlStability(page,{sections:page.getByRole('tablist',{name:'Project sections'}),tickets:page.getByRole('tab',{name:'Tickets',exact:true})})
  await guard.check(()=>page.getByRole('tab',{name:'Knowledge',exact:true}).click())
  await guard.check(()=>page.getByRole('tab',{name:'Tickets',exact:true}).click());guard.done()
  await page.screenshot({path:info.outputPath(`project-${width}-${theme}.png`)})
  await page.goto('/settings/developer')
  const control=page.getByRole('switch',{name:'Show reserved versions',exact:true})
  await expect(control).toBeVisible();await expect(page.getByRole('switch')).toHaveCount(1)
  const settings=await controlStability(page,{reserved:control})
  await settings.check(()=>control.check());await settings.check(()=>control.uncheck());settings.done()
  await page.screenshot({path:info.outputPath(`developer-${width}-${theme}.png`)})
 })
}
