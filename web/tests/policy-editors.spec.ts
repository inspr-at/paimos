// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Locator, type Page } from '@playwright/test'
import { expectStableControls } from './helpers/stable'
import { mockPolicyEditors } from './policy-editor-fixtures'
import { policyLadder } from './policies-fixtures'
import type { EditableLadder } from '../src/lib/policyModels'
const actionNames=['edit','save','cancel','undo','reload']
const actions=(frame:Locator)=>Object.fromEntries(actionNames.map(name=>[name,frame.getByTestId(`policy-${name}`)]))
const status=(frame:Locator)=>frame.getByTestId('policy-editor-status')
async function shot(page:Page,label:string,width:number,theme:string){await page.locator('.editor-frame').scrollIntoViewIfNeeded();await page.screenshot({path:test.info().outputPath(`${label}-${width}-${theme}.png`),fullPage:true})}
for(const priorities of [[10,20],[1,3]])test(`adding to sparse priorities ${priorities.join(',')} saves canonical order and preserves Undo`,async({page})=>{
 const mock=await mockPolicyEditors(page),original=policyLadder('review-gate') as EditableLadder
 original.routes.forEach((row,index)=>{row.priority=priorities[index]!;original.steps[index]!.priority=row.priority})
 const prior=structuredClone(original.routes);mock.state.ladders.set('review-gate',original)
 await page.goto('/settings/policies');const frame=page.locator('.editor-frame');await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','false');await frame.getByTestId('policy-edit').click()
 const add=page.getByLabel('Add ladder model');await expect(add.locator('option[value="profile-2"]')).toHaveCount(1)
 await expectStableControls({controls:{...actions(frame),status:status(frame),add,step:page.getByLabel('Selected ladder step'),position:page.getByLabel('Ladder position')},scrollAreas:{body:frame.getByTestId('policy-editor-body')},interactions:[
  {name:'append a step to sparse priorities',run:async()=>{await add.selectOption('profile-2');await expect(page.getByLabel('Selected ladder step')).toHaveValue('2')}},
  {name:'save canonical order',run:async()=>{await frame.getByTestId('policy-save').click();await expect(status(frame)).toContainText('Saved.');await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','false')}},
 ]})
 expect(mock.state.writes[0]!.body).toEqual([...prior,{role:'review-gate',profile_id:'profile-2',priority:3,state:'available',reason:'',valid_until:null}].map((row,index)=>({...row,priority:index+1})))
 expect(mock.state.ladders.get('review-gate')!.routes.map(row=>row.profile_id)).toEqual(['profile-0','profile-1','profile-2'])
 await frame.getByTestId('policy-undo').click();await expect(status(frame)).toContainText('Order restored.');expect(mock.state.ladders.get('review-gate')!.routes).toEqual(prior)
})
for(const width of [390,1024,1440])for(const theme of ['light','dark'] as const){
 test(`ladder geometry and conditional Undo ${width} ${theme}`,async({page})=>{
  await page.setViewportSize({width,height:width===390?844:1000});const mock=await mockPolicyEditors(page,theme)
  const original=policyLadder('review-gate') as EditableLadder;original.routes[1]!.valid_until='2020-01-01T00:00:00Z';original.steps[1]!.valid_until='2020-01-01T00:00:00Z';mock.state.ladders.set('review-gate',original)
  const capturedToken=original.edit_token
  await page.goto('/settings/policies');const panel=page.locator('.policies'),frame=panel.locator('.editor-frame')
  await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','false')
  const slots=panel.getByTestId('ladder-slot'),common={tabs:panel.locator('.policy-tabs'),roles:panel.getByTestId('policies-role-group'),invokingRow:slots.first()}
  await expectStableControls({controls:width===390?common:{...common,...actions(frame),status:status(frame)},scrollAreas:{panel},interactions:[{name:'open editor',run:async()=>{await frame.getByTestId('policy-edit').click();await expect(page.getByLabel('Ladder position')).toBeVisible()}}]})
  const held=mock.holdNext()
  await expectStableControls({controls:{...actions(frame),status:status(frame),step:page.getByLabel('Selected ladder step'),position:page.getByLabel('Ladder position'),availability:page.getByLabel('Ladder availability'),...(width===390?{phoneFrame:frame}:common)},scrollAreas:{body:frame.getByTestId('policy-editor-body')},interactions:[
   {name:'reorder in fixed slots',run:async()=>{await page.getByLabel('Ladder position').fill('2');await page.getByLabel('Ladder position').press('Tab');await expect(slots.first()).toContainText('Fallback model')}},
   {name:'availability reason and expiry',run:async()=>{await page.getByLabel('Ladder availability').selectOption('conserved');await page.getByLabel('Hold reason').fill('Eine ausführliche Begründung für eine vorübergehend eingeschränkte Verfügbarkeit.');await page.getByLabel('Hold expiry').fill('2099-10-04T12:00:00Z')}},
   {name:'saving has no provisional success',run:async()=>{await frame.getByTestId('policy-save').click();await held.started;await expect(status(frame)).toContainText('Saving');await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','true')}},
   {name:'confirmed result',run:async()=>{held.release();await expect(status(frame)).toContainText('Saved.');await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','false')}},
   {name:'conditional expiry-normalized Undo',run:async()=>{await frame.getByTestId('policy-undo').click();await expect(status(frame)).toContainText('Order restored; expired holds remain available.')}},
  ]})
  expect(mock.state.writes[0]!.headers['if-match']).toBe(capturedToken)
  expect(mock.state.writes[1]!.path).toBe('/api/models/routes?role=review-gate&expiry_policy=clear&order_mode=legacy')
  expect(mock.state.ladders.get('review-gate')!.routes[1]!.state).toBe('available')
  await shot(page,'ladder',width,theme)
  await frame.getByTestId('policy-cancel').click()
 })
}
for(const platform of ['MacIntel','Linux x86_64'])for(const width of [390,1440])for(const editor of ['ladder'] as const)test(`keyboard Save retains action focus through Undo and Escape ${editor} ${platform} ${width}`,async({page})=>{
 await page.setViewportSize({width,height:width===390?844:1000})
 await page.addInitScript(value=>Object.defineProperty(navigator,'platform',{get:()=>value}),platform)
 const mock=await mockPolicyEditors(page)
 await page.goto('/settings/policies')
 const frame=page.locator('.editor-frame')
 const edit=frame.getByTestId('policy-edit'),save=frame.getByTestId('policy-save')
 const catalog=editor==='ladder'?mock.holdCatalog():null
 await expect(edit).toHaveAttribute('aria-disabled','false');await edit.click()
 const field=page.getByLabel(editor==='ladder'?'Ladder position':'normal preference mode')
 const modifier=platform==='MacIntel'?'Meta':'Control'
 await expect(save.locator('.keys')).toHaveAttribute('aria-label',platform==='MacIntel'?'Command+Enter':'Ctrl+Enter')
 const before=structuredClone(editor==='ladder'?mock.state.ladders.get('review-gate')!.routes:mock.state.document.levels.person!.rows)
 if(editor==='ladder'){
  try{
   await catalog!.started
   // Platform emulation changes app shortcuts, not the host's native select-all.
   await field.fill('')
   await expect(field).toHaveValue('')
   await field.fill('2')
   // The pending catalog causes a render before blur. It must not replace
   // the position the user just typed with the old selected index.
   catalog!.release()
   await expect(page.getByLabel('Add ladder model').locator('option[value="profile-2"]')).toHaveCount(1)
   await expect(field).toHaveValue('2')
   await field.press('Tab')
   await expect(page.getByLabel('Selected ladder step')).toHaveValue('1')
  }finally{catalog!.release()}
 }else await field.selectOption('auto')
 await field.focus()
 const held=mock.holdNext()
 try{
  await expectStableControls({controls:{...actions(frame),status:status(frame),field,...(width===390?{phoneFrame:frame}:{})},scrollAreas:{body:frame.getByTestId('policy-editor-body')},interactions:[
   {name:'keyboard Save transfers focus before disabling the field',run:async()=>{await field.press(`${modifier}+Enter`);await held.started;await expect(status(frame)).toContainText('Saving…');await expect(field).toBeDisabled();await expect(save).toBeFocused();expect(mock.state.writes).toHaveLength(1)}},
   {name:'confirmed Save keeps action focus',run:async()=>{held.release();await expect(status(frame)).toContainText('Saved.');await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','false');await expect(save).toBeFocused();const after=editor==='ladder'?mock.state.ladders.get('review-gate')!.routes:mock.state.document.levels.person!.rows;expect(after).not.toEqual(before)}},
   {name:'keyboard Undo restores the captured setting',run:async()=>{await page.keyboard.press('u');await expect(status(frame)).toContainText(editor==='ladder'?'Order restored.':'Work-kind setting restored.');await expect(save).toBeFocused();await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','true');expect(mock.state.writes).toHaveLength(2);expect(editor==='ladder'?mock.state.ladders.get('review-gate')!.routes:mock.state.document.levels.person!.rows).toEqual(before)}},
  ]})
  await page.keyboard.press('Escape');await expect(edit).toBeFocused();await expect(frame.getByTestId('policy-cancel')).toHaveAttribute('aria-disabled','true');await expect(field).toBeDisabled();expect(mock.state.writes).toHaveLength(2)
  if(width===390)await expect(frame).not.toHaveAttribute('aria-modal','true')
 }finally{held.release()}
})
test('truncated ladder and missing workspace model visibility have no functioning editors',async({page})=>{
 const mock=await mockPolicyEditors(page),partial=policyLadder('review-gate',51) as EditableLadder;mock.state.ladders.set('review-gate',partial);await page.goto('/settings/policies');const frame=page.locator('.editor-frame');await expect(status(frame)).toContainText('complete order is needed');await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','true');expect(mock.state.writes).toHaveLength(0)
 mock.base.data.grants=['settings.read','model_prefs.manage'];await page.reload();await page.locator('#policy-tab-elsewhere').click();await expect(page.locator('.policy-row').filter({hasText:'Model preferences'}).getByRole('link',{name:'Open Models'})).toHaveAttribute('href','/settings/models?layer=rules');await expect(page.locator('.preferences-editor')).toHaveCount(0)
})

for(const width of [390,1024,1440])for(const theme of ['light','dark'] as const)test(`managed order activation and mode Undo ${width} ${theme}`,async({page})=>{
 await page.setViewportSize({width,height:width===390?844:1000})
 const mock=await mockPolicyEditors(page,theme)
 const initial=policyLadder('review-gate') as EditableLadder
 initial.steps[0]!.profile.display_name='Sehr lange Modellbezeichnung für nachvollziehbare normale und komplexe Reviewaufgaben'
 initial.managed_fallback_order=[...initial.routes].reverse().map(row=>row.profile_id)
 mock.state.ladders.set('review-gate',initial)
 const prior=structuredClone(initial.routes)
 await page.goto('/settings/policies')
 const frame=page.locator('.editor-frame'),panel=page.locator('.policies')
 await expect(frame).toContainText('built-in family and tier order')
 await frame.getByTestId('policy-edit').click()
 await expect(page.getByLabel('Selected ladder step')).toHaveValue('0')
 await expect(page.getByLabel('Selected ladder step').locator('option').first()).toContainText('Fallback model')
 await expectStableControls({controls:{...actions(frame),status:status(frame),position:page.getByLabel('Ladder position'),step:page.getByLabel('Selected ladder step'),...(width===390?{phoneFrame:frame}:{roles:panel.getByTestId('policies-role-group')})},scrollAreas:{body:frame.getByTestId('policy-editor-body')},interactions:[
  {name:'explicit managed order activation',run:async()=>{await frame.getByTestId('policy-save').click();await expect(status(frame)).toContainText('Saved.');await expect(frame).toContainText('saved order');await shot(page,'managed-order-saved',width,theme)}},
  {name:'restore rows and legacy mode',run:async()=>{await frame.getByTestId('policy-undo').click();await expect(status(frame)).toContainText('Order restored.');await expect(frame).toContainText('built-in family and tier order')}},
 ]})
 expect(mock.state.writes[0]!.path).toContain('order_mode=saved')
 expect(mock.state.writes[1]!.path).toContain('order_mode=legacy')
 expect(mock.state.ladders.get('review-gate')!.routes).toEqual(prior)
 expect(mock.state.ladders.get('review-gate')!.order_mode).toBe('legacy')
 await frame.getByTestId('policy-cancel').click()
 await shot(page,'managed-order',width,theme)
})

test('managed mode-only change refuses captured Undo without overwriting the new mode',async({page})=>{
 const mock=await mockPolicyEditors(page)
 await page.goto('/settings/policies')
 const frame=page.locator('.editor-frame')
 await frame.getByTestId('policy-edit').click()
 await frame.getByTestId('policy-save').click()
 await expect(status(frame)).toContainText('Saved.')
 const current=mock.state.ladders.get('review-gate')!
 current.order_mode='legacy';current.edit_token='"'+'c'.repeat(64)+'"'
 await expectStableControls({controls:{...actions(frame),status:status(frame),step:page.getByLabel('Selected ladder step'),position:page.getByLabel('Ladder position')},scrollAreas:{body:frame.getByTestId('policy-editor-body')},interactions:[{name:'mode-only conflict prevents Undo',run:async()=>{await frame.getByTestId('policy-undo').click();await expect(status(frame)).toContainText('A newer change prevents Undo.')}}]})
 expect(mock.state.ladders.get('review-gate')!.order_mode).toBe('legacy')
 expect(mock.state.writes).toHaveLength(2)
 await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','true')
 await expect(status(frame)).not.toContainText('Order restored.')
})

async function focusRefresh(page: Page) {
 const answer=page.waitForResponse(response=>response.url().includes('/api/me/permissions')&&response.request().method()==='GET')
 await page.evaluate(()=>window.dispatchEvent(new Event('focus')))
 await answer
 // Let the permission replacement and Vue's watchers settle; no timed sleep.
 await page.evaluate(()=>new Promise<void>(resolve=>requestAnimationFrame(()=>requestAnimationFrame(()=>resolve()))))
}

for(const width of [390,1024,1440])for(const theme of ['light','dark'] as const)test(`focus refresh preserves ladder draft, pending save, Undo and controls ${width} ${theme}`,async({page})=>{
 await page.setViewportSize({width,height:width===390?844:1000})
 const mock=await mockPolicyEditors(page,theme);await page.goto('/settings/policies')
 const frame=page.locator('.editor-frame'),position=page.getByLabel('Ladder position'),step=page.getByLabel('Selected ladder step')
 await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','false')
 const prior=structuredClone(mock.state.ladders.get('review-gate')!.routes),capturedToken=mock.state.ladders.get('review-gate')!.edit_token
 await frame.getByTestId('policy-edit').click();await expect(page.getByLabel('Add ladder model').locator('option[value="profile-2"]')).toHaveCount(1);await position.fill('2');await position.dispatchEvent('change');await expect(step).toHaveValue('1')
 const held=mock.holdNext()
 try {
  await expectStableControls({controls:{...actions(frame),status:status(frame),position,step,...(width===390?{phoneFrame:frame}:{roles:page.getByTestId('policies-role-group')})},scrollAreas:{body:frame.getByTestId('policy-editor-body')},interactions:[
   {name:'refresh with a captured draft',run:async()=>{await focusRefresh(page);await expect(position).toBeEnabled();await expect(position).toHaveValue('2');await expect(step).toHaveValue('1');await expect(frame.getByTestId('policy-save')).toHaveAttribute('aria-disabled','false')}},
   {name:'refresh while save awaits confirmation',run:async()=>{await frame.getByTestId('policy-save').click();await held.started;await focusRefresh(page);await expect(status(frame)).toContainText('Saving…');await expect(frame.getByTestId('policy-save')).toHaveAttribute('aria-disabled','true');await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','true')}},
   {name:'confirm the original save',run:async()=>{held.release();await expect(status(frame)).toContainText('Saved.');await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','false')}},
   {name:'refresh with confirmed Undo',run:async()=>{await focusRefresh(page);await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','false');await shot(page,'focus-ladder',width,theme)}},
   {name:'restore the captured order',run:async()=>{await frame.getByTestId('policy-undo').click();await expect(status(frame)).toContainText('Order restored.')}},
  ]})
 } finally {held.release()}
 expect(mock.state.writes).toHaveLength(2);expect(mock.state.writes[0]!.headers['if-match']).toBe(capturedToken)
 expect(mock.state.ladders.get('review-gate')!.routes).toEqual(prior)
})

for(const permission of ['models.manage','models.read'])for(const stage of ['draft','undo','saving'] as const)test(`permission loss during focus refresh invalidates ladder ${permission} ${stage}`,async({page})=>{
 const mock=await mockPolicyEditors(page);await page.goto('/settings/policies');const frame=page.locator('.editor-frame')
 await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','false');await frame.getByTestId('policy-edit').click()
 const held=stage==='saving'?mock.holdNext():null
 if(stage!=='draft')await frame.getByTestId('policy-save').click()
 if(held)await held.started
 if(stage==='undo')await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','false')
 mock.base.data.grants=mock.base.data.grants.filter(grant=>grant!==permission)
 try {
  await focusRefresh(page)
  if(permission==='models.read') { await expect(frame).toHaveCount(0);await expect(page.getByRole('tabpanel')).toContainText("You can't see this") }
  else {
   await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','true');await expect(frame.getByTestId('policy-save')).toHaveAttribute('aria-disabled','true');await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','true')
   await expect(page.getByLabel('Ladder position')).toBeDisabled();await frame.getByTestId('policy-undo').dispatchEvent('click')
  }
 } finally {held?.release()}
 if(held)await held.settled
 expect(mock.state.writes).toHaveLength(stage==='draft'?0:1)
 await expect(page.locator('.policies')).not.toContainText('Saved.')
})
