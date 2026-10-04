// SPDX-License-Identifier: AGPL-3.0-only
import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { expectStableControls } from './helpers/stable'
import { mockPolicyEditors } from './policy-editor-fixtures'
import { policyLadder } from './policies-fixtures'
import type { EditableLadder } from '../src/lib/policyModels'
const actionNames=['edit','save','cancel','undo','reload']
const actions=(frame:Locator)=>Object.fromEntries(actionNames.map(name=>[name,frame.getByTestId(`policy-${name}`)]))
const status=(frame:Locator)=>frame.getByTestId('policy-editor-status')
async function preferences(page:Page){await page.getByRole('group',{name:'Model policy view'}).getByRole('button',{name:'Model preferences',exact:true}).click();await expect(page.getByLabel('Preference work kind')).toBeVisible();await page.getByLabel('Preference work kind').selectOption('kind-build');const frame=page.locator('.editor-frame');await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','false');return frame}
async function shot(page:Page,label:string,width:number,theme:string){const directory=join(process.cwd(),'test-results','aeon-633g');await mkdir(directory,{recursive:true});await page.locator('.editor-frame').scrollIntoViewIfNeeded();await page.screenshot({path:join(directory,`${label}-${width}-${theme}.png`),fullPage:true})}
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
 test(`preference rows/scalars and geometry ${width} ${theme}`,async({page})=>{
  await page.setViewportSize({width,height:width===390?844:1000});const mock=await mockPolicyEditors(page,theme);await page.goto('/settings/policies');const frame=await preferences(page)
  const held=mock.holdNext()
  const prior=structuredClone(mock.state.document.levels.person!.rows[0]!),panel=page.locator('.policies'),row=panel.getByTestId('preference-kind-row').first()
  await expectStableControls({controls:{tabs:panel.locator('.policy-tabs'),navigation:panel.getByTestId('preference-navigation'),invokingRow:row,...(width===390?{}:actions(frame))},scrollAreas:{panel},interactions:[{name:'open row editor',run:async()=>{await frame.getByTestId('policy-edit').click();await expect(page.getByLabel('normal preference mode')).toBeVisible()}}]})
  await expectStableControls({controls:{...actions(frame),status:status(frame),normal:page.getByLabel('normal preference mode'),complex:page.getByLabel('complex preference mode'),normally:page.getByLabel('normal model profile'),complexModel:page.getByLabel('complex model profile'),reset:frame.getByTestId('preference-row-reset'),...(width===390?{phoneFrame:frame}:{invokingRow:row})},scrollAreas:{body:frame.getByTestId('policy-editor-body')},interactions:[
   {name:'one bucket keeps sibling',run:async()=>{await page.getByLabel('normal preference mode').selectOption('auto');await expect(page.getByLabel('complex preference mode')).toHaveValue('pinned')}},
   {name:'row lock',run:async()=>{await page.getByText('Lock this work kind for narrower levels',{exact:true}).click();await expect(frame.locator('input[type=checkbox]')).toBeChecked();await shot(page,'preference-row',width,theme)}},
   {name:'row Saving state',run:async()=>{await frame.getByTestId('policy-save').click();await held.started;await expect(status(frame)).toContainText('Saving…')}},
   {name:'confirmed row state',run:async()=>{held.release();await expect(status(frame)).toContainText('Saved.');expect(mock.state.document.levels.person!.rows[0]!.complex).toEqual(prior.complex)}},
   {name:'compensated row state',run:async()=>{await frame.getByTestId('policy-undo').click();await expect(status(frame)).toContainText('Work-kind setting restored.');expect(mock.state.document.levels.person!.rows[0]).toEqual(prior)}},
  ]})
  await frame.getByTestId('policy-cancel').click();await frame.getByTestId('preference-row-reset').click();await frame.getByTestId('policy-save').click();await expect(status(frame)).toContainText('Saved.');expect(mock.state.document.levels.person!.rows).toEqual([])
  await frame.getByTestId('policy-undo').click();await expect(status(frame)).toContainText('Work-kind setting restored.');expect(mock.state.document.levels.person!.rows[0]).toEqual(prior)
  await frame.getByTestId('policy-cancel').click();await page.getByRole('button',{name:'Providers & locks',exact:true}).click();await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','false');await frame.getByTestId('policy-edit').click()
  await expectStableControls({controls:{...actions(frame),status:status(frame),provider:page.getByLabel('Provider requirement',{exact:true}),...(width===390?{phoneFrame:frame}:{navigation:panel.getByTestId('preference-navigation')})},scrollAreas:{body:frame.getByTestId('policy-editor-body')},interactions:[{name:'provider selection',run:async()=>{await page.getByLabel('Provider requirement',{exact:true}).selectOption('local')}},{name:'provider lock selection',run:async()=>{await page.getByText('Lock provider requirement',{exact:true}).click()}}]})
  await shot(page,'preference-providers',width,theme)
  await frame.getByTestId('policy-save').click();await expect(status(frame)).toContainText('Saved.');await expect(frame).toContainText('No qualifying route today; work will wait.')
  await frame.getByTestId('policy-undo').click();await expect(status(frame)).toContainText('Existing runs keep their stricter requirement.');expect(mock.state.document.levels.person!.rows[0]).toEqual(prior)
  const scalarWrites=mock.state.writes.filter(write=>!write.path.includes('/rows/'));expect(scalarWrites).toHaveLength(2);for(const write of scalarWrites){expect(write.method).toBe('PUT');expect(write.body).not.toHaveProperty('rows');expect(write.headers['if-prefs-person']).toBe('first-canonical-person')}
  await expect(panel.getByText('Reset all',{exact:true})).toHaveCount(0)
 })
}
for(const refusal of [{status:403,code:'permission_denied'},{status:409,code:'stale_revision'},{status:422,code:'locked_above'}])test(`preference refusal ${refusal.code} keeps draft without success`,async({page})=>{
 const mock=await mockPolicyEditors(page);await page.goto('/settings/policies');const frame=await preferences(page);await frame.getByTestId('policy-edit').click();await page.getByLabel('normal preference mode').selectOption('auto');mock.state.refusal=refusal;await expectStableControls({controls:{...actions(frame),status:status(frame),normal:page.getByLabel('normal preference mode'),complex:page.getByLabel('complex preference mode')},scrollAreas:{body:frame.getByTestId('policy-editor-body')},interactions:[{name:'refused save',run:async()=>{await frame.getByTestId('policy-save').click();await expect(status(frame)).toContainText(refusal.status===403?'Permission was refused':refusal.status===409?'changed elsewhere':'broader setting locks')}}]});await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','true');await expect(page.getByLabel('normal preference mode')).toHaveValue('auto');expect(mock.state.writes).toHaveLength(1);expect(mock.state.document.levels.person!.rows[0]!.normal.mode).toBe('pinned');await expect(status(frame)).not.toContainText('Saved.')
})
test('person linking with equal revisions discards old draft and Undo without auto-retry',async({page})=>{
 const mock=await mockPolicyEditors(page);await page.goto('/settings/policies');const frame=await preferences(page);await frame.getByTestId('policy-edit').click();await page.getByLabel('normal preference mode').selectOption('auto');const held=mock.holdNext();await frame.getByTestId('policy-save').click();await held.started;mock.state.document.person_id='second-canonical-person';held.release();await expect(status(frame)).toContainText('old draft and Undo were discarded');await expect(page.getByLabel('normal preference mode')).toBeDisabled();expect(mock.state.writes).toHaveLength(1);await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','true')
})
test('retired-kind Reset Undo refuses specifically without restoration',async({page})=>{
 const mock=await mockPolicyEditors(page);await page.goto('/settings/policies');const frame=await preferences(page);await frame.getByTestId('preference-row-reset').click();await frame.getByTestId('policy-save').click();await expect(status(frame)).toContainText('Saved.');mock.state.document.kinds=mock.state.document.kinds.filter(kind=>kind.id!=='kind-build');await frame.getByTestId('policy-undo').click();await expect(status(frame)).toContainText('This work kind is no longer available; the change could not be restored.');expect(mock.state.document.levels.person!.rows).toEqual([]);await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','true')
})
test('unknown response and failed reconciliation never show success or Undo',async({page})=>{
 const mock=await mockPolicyEditors(page);await page.goto('/settings/policies');const frame=await preferences(page);await frame.getByTestId('policy-edit').click();await page.getByLabel('normal preference mode').selectOption('auto');mock.state.unknown=true;mock.state.failReads=true;await frame.getByTestId('policy-save').click();await expect(status(frame)).toContainText('Could not confirm the save.');await expect(status(frame)).toContainText('Current value could not be loaded');await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','true');await expect(frame.getByTestId('policy-save')).toHaveAttribute('aria-disabled','true');expect(mock.state.writes).toHaveLength(1)
})
test('switching level during save cannot attach old Undo to the new record',async({page})=>{
 const mock=await mockPolicyEditors(page);await page.goto('/settings/policies');const frame=await preferences(page);await frame.getByTestId('policy-edit').click();const held=mock.holdNext();await frame.getByTestId('policy-save').click();await held.started;await page.getByRole('group',{name:'Preference level'}).getByRole('button',{name:'Default',exact:true}).click();held.release();await expect(frame.getByTestId('policy-undo')).toHaveAttribute('aria-disabled','true');await expect(status(frame)).not.toContainText('Saved.');expect(mock.state.writes).toHaveLength(1);expect(mock.state.writes[0]!.path).toContain('/levels/person/rows/kind-build')
})
test('keyboard submits only with platform modifier and Esc first leaves a field',async({page})=>{
 const mock=await mockPolicyEditors(page);await page.goto('/settings/policies');const frame=await preferences(page);await frame.getByTestId('policy-edit').click();const field=page.getByLabel('normal preference mode');await field.focus();await field.press('u');expect(mock.state.writes).toHaveLength(0);const modifier=await page.evaluate(()=>/Mac|iPhone|iPad/.test(navigator.platform)?'Meta':'Control');await field.press(`${modifier}+a`);expect(mock.state.writes).toHaveLength(0);await field.press('Escape');await expect(frame.getByTestId('policy-save')).toBeFocused();await frame.getByTestId('policy-save').press('Escape');await expect(field).toBeDisabled();await frame.getByTestId('policy-edit').click();await page.getByLabel('normal preference mode').press(`${modifier}+Enter`);await expect(status(frame)).toContainText('Saved.');expect(mock.state.writes).toHaveLength(1)
})
test('Project writes bind the chosen visible project and offer no project locks',async({page})=>{
 const mock=await mockPolicyEditors(page);await page.goto('/settings/policies');await preferences(page)
 await page.getByRole('group',{name:'Preference level'}).getByRole('button',{name:'Project',exact:true}).click();await page.getByLabel('Preference project').selectOption('p-pharos');await page.getByLabel('Preference work kind').selectOption('kind-build');const frame=page.locator('.editor-frame');await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','false');await frame.getByTestId('policy-edit').click();await expect(frame.locator('input[type=checkbox]')).toBeDisabled();await page.getByLabel('normal preference mode').selectOption('pinned');await frame.getByTestId('policy-save').click();await expect(status(frame)).toContainText('Saved.');expect(mock.state.writes[0]!.path).toBe('/api/model-preferences/levels/project/rows/kind-build?project_id=p-pharos');await frame.getByTestId('policy-undo').click();await expect(status(frame)).toContainText('Work-kind setting restored.');expect(mock.state.writes[1]!.method).toBe('DELETE');expect(mock.state.writes[1]!.path).toBe(mock.state.writes[0]!.path+'&revision=2');expect(mock.state.writes[1]!.body).toBeNull();expect(mock.state.writes[1]!.headers['if-prefs-person']).toBe('first-canonical-person');expect(mock.state.document.levels.project!.rows).toEqual([])
})
test('truncated ladder and missing workspace model visibility have no functioning editors',async({page})=>{
 const mock=await mockPolicyEditors(page),partial=policyLadder('review-gate',51) as EditableLadder;mock.state.ladders.set('review-gate',partial);await page.goto('/settings/policies');const frame=page.locator('.editor-frame');await expect(status(frame)).toContainText('complete order is needed');await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','true');expect(mock.state.writes).toHaveLength(0)
 mock.base.data.grants=['settings.read','model_prefs.manage'];await page.reload();await page.locator('#policy-tab-elsewhere').click();await page.getByRole('article').filter({hasText:'Model preferences'}).getByRole('button',{name:'Open here'}).click();await expect(page.locator('.policy-content')).toContainText('also need workspace model visibility');await expect(page.locator('.preferences-editor')).toHaveCount(0)
})
test('work-kind selection and lock explanation keep navigation and rows still',async({page})=>{
 const mock=await mockPolicyEditors(page);mock.state.document.views.person!.rows.find(row=>row.kind_id==='kind-review')!.locked_by='default';await page.goto('/settings/policies');await preferences(page);const panel=page.locator('.policies'),frame=panel.locator('.editor-frame')
 await expectStableControls({controls:{navigation:panel.getByTestId('preference-navigation'),...actions(frame),status:status(frame),invokingRow:panel.getByTestId('preference-kind-row').first()},scrollAreas:{panel},interactions:[{name:'broader review lock',run:async()=>{await page.getByLabel('Preference work kind').selectOption('kind-review');await expect(status(frame)).toContainText('locked by default');await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','true')}},{name:'security unavailable',run:async()=>{await page.getByLabel('Preference work kind').selectOption('kind-security');await expect(status(frame)).toContainText('Security review setup is unavailable');await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','true')}},{name:'return to editable row',run:async()=>{await page.getByLabel('Preference work kind').selectOption('kind-build');await expect(frame.getByTestId('policy-edit')).toHaveAttribute('aria-disabled','false')}}]})
})

test('native browser shortcuts pass through the editor untouched',async({page})=>{
 const mock=await mockPolicyEditors(page);await page.goto('/settings/policies');const frame=await preferences(page);await frame.getByTestId('policy-edit').click();const field=page.getByLabel('normal preference mode');await field.focus()
 await page.evaluate(()=>{const recorded:{key:string;prevented:boolean}[]=[];(window as unknown as {policyKeys:typeof recorded}).policyKeys=recorded;document.addEventListener('keydown',event=>{if((event.metaKey||event.ctrlKey)&&['s','r','d','p','a'].includes(event.key.toLowerCase())){recorded.push({key:event.key.toLowerCase(),prevented:event.defaultPrevented});event.preventDefault()}})})
 const modifier=await page.evaluate(()=>/Mac|iPhone|iPad/.test(navigator.platform)?'Meta':'Control');for(const key of ['s','r','d','p','a'])await field.press(`${modifier}+${key}`)
 expect(await page.evaluate(()=>(window as unknown as {policyKeys:unknown[]}).policyKeys)).toEqual(['s','r','d','p','a'].map(key=>({key,prevented:false})));expect(mock.state.writes).toHaveLength(0)
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
