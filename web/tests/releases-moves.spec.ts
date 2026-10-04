// SPDX-License-Identifier: AGPL-3.0-only
import { mkdirSync } from 'node:fs'
import { expect,test,type Page } from '@playwright/test'
import { fixtures,mockWork,watchErrors } from './work-fixtures'
import { expectStableControls } from './helpers/stable'
import type { PlanningItem,PlanningRelease } from '../src/lib/deliveryPlanning'
const rid=(n:number)=>`00000000-0000-4000-8000-${String(n).padStart(12,'0')}`
const a=rid(1),b=rid(2),frozen=rid(3),work=rid(101),peer=rid(102),nextPeer=rid(103)
const long='Langfristige Verbesserungen für nachvollziehbare und gemeinsame Releaseplanung'
const release=(id:string,rank:string,title:string,state:PlanningRelease['state']='planned'):PlanningRelease=>({release_id:id,project_id:'p-pharos',rank,title,display_name:title,state,visibility:'internal',revision:1,rollup:{units:2,completed:0,open_hours:3},build_summary:{budget_outlook:'unknown'}})
const item=(id:string,key:string,releaseId:string|undefined,rank:string|undefined):PlanningItem=>({item_id:id,project_id:'p-pharos',release_id:releaseId,rank,key,title:`${long} · ${key}`,kind:'ticket',state:'backlog',revision:rank?1:0,node_revision:'2026-10-03T12:00:00Z',created_at:'2026-10-03T12:00:00Z',estimated_hours:2,expedite:false,due_on:null})
async function setup(page:Page,options:{theme?:string;agent?:boolean;refuse?:boolean;rows?:number;terminal?:boolean}={}) {
 const data=fixtures();data.preferences.theme={choice:options.theme??'light'};data.preferences['header-graph']={enabled:false}
 await mockWork(page,data,{principalKind:options.agent?'agent':'person'})
 const errors=watchErrors(page),writes:{path:string;body:Record<string,unknown>}[]=[],undos:number[]=[]
 const releases=[release(a,'B','Current work'),release(b,'D',long),release(frozen,'F','Frozen work','frozen')]
 if(options.terminal)releases.push(release(rid(4),'H','Delivered work','released'))
 const items=[item(work,'PHAROS-101',a,'V'),item(peer,'PHAROS-102',b,'V'),item(nextPeer,'PHAROS-103',b,'X'),item(rid(104),'PHAROS-104',undefined,undefined)]
 if(options.rows)items.push(...Array.from({length:options.rows},(_,i)=>item(rid(200+i),`PHAROS-${200+i}`,b,`Y${String(i).padStart(3,'1')}V`)))
 let receipt=700,previous:PlanningItem|undefined
 await page.route('**/api/projects/p-pharos/**',async route=>{
  const url=new URL(route.request().url()),path=url.pathname,query=url.searchParams
  if(path.endsWith('/delivery'))return route.fulfill({json:{project_id:'p-pharos',mode:'releases',revision:1}})
  if(path.endsWith('/rank')) {
   const body=route.request().postDataJSON();writes.push({path,body});const subject=releases.find(r=>r.release_id===path.split('/').at(-2))!
   subject.rank=body.position==='top'?'A':body.before_id?'C':'E';subject.revision++
   return route.fulfill({json:{...subject,undo_event_id:++receipt}})
  }
  const offset=Number(query.get('cursor')??0),limit=Number(query.get('limit')??200)
  const count=(list:PlanningItem[])=>({matched_count:list.length,shown_count:list.length,hidden_count:0,hidden_finished:0,hidden_exit:0,incomplete:false})
  if(path.endsWith('/overview'))return route.fulfill({json:{active:releases.filter(r=>r.state!=='released'),released:{items:releases.filter(r=>r.state==='released')},backlog:{ranked:items.filter(i=>!i.release_id&&i.rank).length,tail:1},abandoned:0,counts_incomplete:false,matches:count(items)}})
  if(path.endsWith('/releases'))return route.fulfill({json:{items:releases.filter(r=>!query.get('q')||r.title.includes(query.get('q')!))}})
  if(path.endsWith('/items')||path.endsWith('/backlog')){
   const source=path.endsWith('/items')?path.split('/').at(-2):undefined
   const rows=items.filter(i=>i.release_id===source && (source ? true : query.get('part')==='tail' ? !i.rank : !!i.rank)).filter(i=>!query.get('q')||`${i.key} ${i.title}`.includes(query.get('q')!))
   return route.fulfill({json:{items:rows.slice(offset,offset+limit),count:rows.length,incomplete:false,matches:count(rows),next_cursor:rows.length>offset+limit?String(offset+limit):''}})
  }
  return route.fallback()
 })
 await page.route('**/api/nodes/*/ships-in',async route=>{
  const body=route.request().postDataJSON(),path=new URL(route.request().url()).pathname;writes.push({path,body})
  if(options.refuse)return route.fulfill({status:409,json:{code:'revision_changed',error:'The record changed; reopen the move.'}})
  const row=items.find(i=>i.item_id===path.split('/').at(-2))!;previous={...row}
  row.release_id=body.release_id??undefined;row.rank=body.position==='top'?'A':body.after_id?'W':'Z';row.revision++
  const destination=releases.find(r=>r.release_id===body.release_id);if(destination)destination.revision++
  return route.fulfill({json:{items:[{item_id:row.item_id,project_id:row.project_id,release_id:row.release_id,rank:row.rank,revision:row.revision,expedite:false,due_on:null}],release_revisions:destination?{[destination.release_id]:destination.revision}:{},undo_event_id:++receipt}})
 })
 await page.route('**/api/events/*/undo',route=>{
  const id=Number(new URL(route.request().url()).pathname.split('/').at(-2));undos.push(id)
  const row=items.find(i=>i.item_id===previous!.item_id)!;Object.assign(row,previous,{revision:row.revision+1})
  return route.fulfill({status:201,json:{id:++receipt,undo_of:id,type:'ships_in.changed',after:{members:[{item_id:row.item_id,project_id:row.project_id,release_id:row.release_id,rank:row.rank,revision:row.revision,expedite:false,due_on:null}]}}})
 })
 await page.goto('/p/pharos?section=releases')
 await expect(page.locator(`[data-release-id="${a}"]`)).toBeVisible()
 return {writes,undos,errors,items,releases}
}
const row=(page:Page,id:string)=>page.locator(`[data-planning-item="${id}"]`)
const block=(page:Page,id:string)=>page.locator(`[data-planning-block="${id}"]`)
async function open(page:Page,id:string) {await block(page,id).getByRole('button',{name:/^Expand /}).click();await expect(block(page,id).locator('.ticket-row').first()).toBeVisible()}
async function drag(page:Page,source:string,target:string,after=true) {
 const h=await row(page,source).getByRole('button',{name:/^Drag /}).boundingBox(),box=await row(page,target).boundingBox();expect(h).toBeTruthy();expect(box).toBeTruthy()
 await page.mouse.move(h!.x+h!.width/2,h!.y+h!.height/2);await page.mouse.down();await page.mouse.move(h!.x+12,h!.y+12);await page.mouse.move(box!.x+box!.width/2,after?box!.y+box!.height-4:box!.y+4);await page.mouse.up()
}
for(const width of [390,1024,1440])for(const theme of ['light','dark'])test(`move sheet stays stable ${width} ${theme}`,async({page})=>{
 await page.setViewportSize({width,height:1000});const world=await setup(page,{theme});await open(page,a)
 mkdirSync('test-results/aeon-596-p6c',{recursive:true});await page.screenshot({path:`test-results/aeon-596-p6c/releases-${width}-${theme}.png`})
 await row(page,work).getByRole('button',{name:'Move PHAROS-101',exact:true}).click()
 const sheet=page.getByRole('dialog',{name:'Release for PHAROS-101'});await expect(sheet).toBeVisible()
 await expectStableControls({controls:{destination:sheet.getByLabel('Move destination',{exact:true}),position:sheet.getByLabel('Move position',{exact:true}),anchor:sheet.getByLabel('Move anchor',{exact:true}),save:sheet.getByRole('button',{name:/Save move/}),cancel:sheet.getByRole('button',{name:/Cancel/}),commands:sheet.getByRole('group',{name:'Move commands'}),...(width===390?{frame:sheet}:{})},scrollAreas:{sheet},interactions:[{name:'destination with long German title',run:()=>sheet.getByLabel('Move destination',{exact:true}).selectOption(b)},{name:'top',run:()=>sheet.getByLabel('Move position',{exact:true}).selectOption('top')},{name:'after',run:async()=>{await sheet.getByLabel('Move position',{exact:true}).selectOption('after');await expect(sheet.getByLabel('Move anchor',{exact:true}).locator('option')).toHaveCount(3);await sheet.getByLabel('Move anchor',{exact:true}).selectOption(peer)}}]})
 mkdirSync('test-results/aeon-596-p6c',{recursive:true});await page.screenshot({path:`test-results/aeon-596-p6c/move-sheet-${width}-${theme}.png`})
 await sheet.getByRole('button',{name:/Cancel/}).click();expect(world.writes).toEqual([]);expect(world.errors).toEqual([])
})
test('drag gap and menu use one identical anchored write and exact Undo',async({page})=>{
 await page.setViewportSize({width:1440,height:1000});const world=await setup(page);await open(page,a);await open(page,b)
 const source=row(page,work).getByRole('button',{name:/^Drag /}),target=row(page,nextPeer)
 const header=page.getByRole('button',{name:'Expand all',exact:true})
 await expectStableControls({controls:{header,source,other:row(page,peer).getByRole('button',{name:'Move PHAROS-102',exact:true})},interactions:[{name:'pickup hover then cancel',run:async()=>{const h=await source.boundingBox(),box=await target.boundingBox();await page.mouse.move(h!.x+10,h!.y+10);await page.mouse.down();await page.mouse.move(h!.x+20,h!.y+20);await page.mouse.move(box!.x+30,box!.y+2);await expect(page.locator('.move-line')).toBeVisible();await page.keyboard.press('Escape');await page.mouse.up()}}]})
 expect(world.writes).toHaveLength(0)
 await drag(page,work,nextPeer,false);await expect.poll(()=>world.writes.length).toBe(1)
 const dragged=world.writes[0]!.body;expect(dragged.after_id).toBe(peer);expect(dragged.before_id).toBeUndefined();expect(dragged.release_id).toBe(b)
 await page.getByRole('button',{name:/^Undo/}).click();await expect.poll(()=>world.undos).toEqual([701]);await expect(row(page,work)).toBeVisible()
 await row(page,work).getByRole('button',{name:'Move PHAROS-101',exact:true}).click();const sheet=page.getByRole('dialog',{name:'Release for PHAROS-101'})
 await sheet.getByLabel('Move destination',{exact:true}).selectOption(b);await sheet.getByLabel('Move position',{exact:true}).selectOption('before');await sheet.getByLabel('Move anchor',{exact:true}).selectOption(nextPeer);await sheet.getByRole('button',{name:/Save move/}).click()
 await expect.poll(()=>world.writes.length).toBe(2);expect(world.writes[1]!.body.after_id).toBe(dragged.after_id);expect(world.writes[1]!.body.release_id).toBe(dragged.release_id);expect(world.items.find(i=>i.item_id===work)?.release_id).toBe(b);expect(world.errors).toEqual([])
})
test('collapsed append, frozen hover and authoritative refusal keep controls and honest results',async({page})=>{
 await page.setViewportSize({width:1440,height:1000});const world=await setup(page,{refuse:true});await open(page,a)
 const h=await row(page,work).getByRole('button',{name:/^Drag /}).boundingBox(),f=await block(page,frozen).locator('.release-row').boundingBox(),dest=await block(page,b).locator('.release-row').boundingBox()
 await page.mouse.move(h!.x+10,h!.y+10);await page.mouse.down();await page.mouse.move(h!.x+20,h!.y+20);await page.mouse.move(f!.x+100,f!.y+20);await expect(page.locator('.move-feedback')).toContainText('frozen');await page.mouse.up();expect(world.writes).toHaveLength(0)
 await page.mouse.move(h!.x+10,h!.y+10);await page.mouse.down();await page.mouse.move(h!.x+20,h!.y+20);await page.mouse.move(dest!.x+100,dest!.y+20);await page.mouse.up();await expect.poll(()=>world.writes.length).toBe(1)
 expect(world.writes[0]!.body.before_id).toBeUndefined();expect(world.writes[0]!.body.after_id).toBeUndefined();await expect(page.locator('.move-feedback')).toContainText('changed');await expect(block(page,b).getByRole('button',{name:/^Expand /})).toHaveAttribute('aria-expanded','false');await expect(row(page,work)).toBeVisible();expect(world.undos).toEqual([]);expect(world.errors).toEqual([])
})
test('500ms phone pickup uses clock, quick swipe cancels and edge scroll never reorders',async({page})=>{
 await page.setViewportSize({width:390,height:800});const world=await setup(page,{rows:40});await open(page,a);await open(page,b);await page.locator('#main').evaluate(el=>{el.scrollTop=0});const clockStart=new Date('2026-10-04T12:00:00Z');await page.clock.install({time:clockStart});await page.clock.pauseAt(new Date(clockStart.getTime()+60000))
 const el=row(page,work).getByRole('button',{name:/^Drag /});const box=await el.boundingBox();expect(box).toBeTruthy()
 const pointer=async(type:string,x:number,y:number)=>el.dispatchEvent(type,{pointerId:9,pointerType:'touch',button:0,clientX:x,clientY:y,bubbles:true})
 const x=box!.x+10,y=box!.y+10
 // Inject the touch pointer sequence and advance the installed clock exactly;
 // synthetic pointer IDs need a capture stub; the paused clock prevents wall
 // time between assertions from crossing the 500 ms boundary.
 await page.evaluate(()=>{HTMLElement.prototype.setPointerCapture=function(){}})
 await pointer('pointerdown',x,y);await page.clock.runFor(499);await expect(page.locator('.move-ghost')).toHaveCount(0);await pointer('pointermove',x,y+20);await page.clock.runFor(1);await expect(page.locator('.move-ghost')).toHaveCount(0);await pointer('pointerup',x,y+20);await page.clock.resume()
 await expectStableControls({controls:{source:el,sourceMenu:row(page,work).getByRole('button',{name:'Move PHAROS-101',exact:true}),otherChevron:block(page,b).getByRole('button',{name:/^Collapse /})},scrollAreas:{list:page.locator('#main')},interactions:[{name:'long pickup, edge scroll and cancel',run:async()=>{
 await page.clock.pauseAt(await page.evaluate(()=>Date.now()+1000));await pointer('pointerdown',x,y);await page.clock.runFor(499);await expect(page.locator('.move-ghost')).toHaveCount(0);await page.clock.runFor(1);await expect(page.locator('.move-ghost')).toBeVisible()
 const scroller=page.locator('#main');const beforeScroll=await scroller.evaluate(el=>el.scrollTop)
 await pointer('pointermove',200,770);await page.clock.runFor(160);expect(await scroller.evaluate(el=>el.scrollTop)).toBeGreaterThan(beforeScroll)
 expect(world.items.find(i=>i.item_id===work)?.release_id).toBe(a);expect(world.writes).toEqual([])
 await page.keyboard.press('Escape');await expect(page.locator('.move-ghost')).toHaveCount(0);expect(world.writes).toEqual([]);await page.clock.resume()
 }}]})
})
test('internal reorder and field/native keyboard conventions',async({page})=>{
 await page.setViewportSize({width:1440,height:1000});const world=await setup(page)
 await block(page,b).getByRole('button',{name:`Reorder ${long}`,exact:true}).focus();await page.keyboard.press('t')
 const sheet=page.getByRole('dialog',{name:`Release for ${long}`});await expect(sheet).toBeVisible();await expect(sheet.getByLabel('Move position',{exact:true})).toHaveValue('top')
 const search=sheet.getByLabel('Find move choices',{exact:true});await search.focus();await page.keyboard.type('g');await expect(search).toHaveValue('g');await page.keyboard.press('Control+A');await expect(search).toBeFocused();await page.keyboard.press('Escape');await expect(search).not.toBeFocused();await page.keyboard.press('Escape');await expect(sheet).toHaveCount(0);expect(world.writes).toEqual([])
 await block(page,b).getByRole('button',{name:`Reorder ${long}`,exact:true}).click();await sheet.getByLabel('Move position',{exact:true}).selectOption('top');await sheet.getByRole('button',{name:/Save move/}).click();await expect.poll(()=>world.writes.length).toBe(1);expect(world.writes[0]!.path).toContain('/rank');expect(world.writes[0]!.body).toEqual({expected_revision:1,position:'top'});expect(world.errors).toEqual([])
})

test('agent chooses a final later-release gap without a second promotion write',async({page})=>{
 await page.setViewportSize({width:1440,height:1000});const world=await setup(page,{agent:true});await open(page,a);await open(page,b)
 await row(page,work).getByRole('button',{name:'Move PHAROS-101',exact:true}).click();const sheet=page.getByRole('dialog',{name:'Release for PHAROS-101'})
 await sheet.getByLabel('Move position',{exact:true}).selectOption('top');await expect(sheet.locator('.move-body')).toContainText('Agents can only move work later');await expect(sheet.getByRole('button',{name:/Save move/})).toBeDisabled()
 await sheet.getByLabel('Move destination',{exact:true}).selectOption(b);await sheet.getByLabel('Move position',{exact:true}).selectOption('before');await sheet.getByLabel('Move anchor',{exact:true}).selectOption(nextPeer);await sheet.getByRole('button',{name:/Save move/}).click()
 await expect.poll(()=>world.writes.length).toBe(1);expect(world.writes[0]!.body).toMatchObject({release_id:b,after_id:peer,expected_revision:1,expected_project_id:'p-pharos',expected_release_revision:1});expect(world.writes[0]!.body.position).toBeUndefined();expect(world.errors).toEqual([])
})
test('Backlog and global top submit one write and retain the new tail',async({page})=>{
 await page.setViewportSize({width:1440,height:1000});const world=await setup(page);await open(page,a);await open(page,'backlog')
 await row(page,work).getByRole('button',{name:'Move PHAROS-101',exact:true}).focus();await page.keyboard.press('g');const sheet=page.getByRole('dialog',{name:'Release for PHAROS-101'})
 await sheet.getByLabel('Move destination',{exact:true}).selectOption('');await sheet.getByLabel('Move position',{exact:true}).selectOption('top');await sheet.getByRole('button',{name:/Save move/}).click();await expect.poll(()=>world.writes.length).toBe(1)
 expect(world.writes[0]!.body.release_id).toBeNull();expect(world.writes[0]!.body.position).toBe('top');expect(world.items.find(i=>i.item_id===rid(104))?.rank).toBeUndefined();await expect(block(page,'backlog').locator(`[data-planning-item="${work}"]`)).toBeVisible();expect(world.errors).toEqual([])
})
test('the header g-place chord crosses the release sheet without saving',async({page})=>{
 const world=await setup(page);await open(page,a);await row(page,work).getByRole('button',{name:'Move PHAROS-101',exact:true}).focus();await page.keyboard.press('g');await expect(page.getByRole('dialog',{name:'Release for PHAROS-101'})).toBeVisible();await page.keyboard.press('a');await expect(page).toHaveURL(/\/agents$/);expect(world.writes).toEqual([])
})
test('position choice pages remain bounded and explicit before saving',async({page})=>{
 const world=await setup(page,{rows:205});await open(page,a)
 await row(page,work).getByRole('button',{name:'Move PHAROS-101',exact:true}).click();const sheet=page.getByRole('dialog',{name:'Release for PHAROS-101'})
 await sheet.getByLabel('Move destination',{exact:true}).selectOption(b);await sheet.getByLabel('Move position',{exact:true}).selectOption('after');await expect(sheet.getByLabel('Move anchor',{exact:true}).locator('option')).toHaveCount(201)
 await sheet.getByLabel('Move anchor',{exact:true}).selectOption(peer);await sheet.getByRole('button',{name:'More work',exact:true}).click();await expect(sheet.getByLabel('Move anchor',{exact:true}).locator('option')).toHaveCount(9);await expect(sheet.getByLabel('Move anchor',{exact:true})).toHaveValue(peer);await expect(sheet.locator('.move-body')).toContainText('After PHAROS-102');await sheet.getByLabel('Move anchor',{exact:true}).selectOption(rid(404));await sheet.getByRole('button',{name:/Save move/}).click();await expect.poll(()=>world.writes.length).toBe(1);expect(world.writes[0]!.body.after_id).toBe(rid(404));expect(world.errors).toEqual([])
})

for(const width of [390,1024,1440])for(const theme of ['light','dark'])test(`terminal release hover refuses without moving controls ${width} ${theme}`,async({page})=>{
 await page.setViewportSize({width,height:1000});const world=await setup(page,{terminal:true,theme})
 const source=block(page,b).getByRole('button',{name:`Reorder ${long}`,exact:true}),terminal=block(page,rid(4)).locator('.release-row')
 await terminal.scrollIntoViewIfNeeded();await expect(source).toBeInViewport();await expect(terminal).toBeInViewport()
 const sourceBox=await source.boundingBox(),terminalBox=await terminal.boundingBox();expect(sourceBox).toBeTruthy();expect(terminalBox).toBeTruthy()
 const x=sourceBox!.x+sourceBox!.width/2,y=sourceBox!.y+sourceBox!.height/2,tx=terminalBox!.x+120,ty=terminalBox!.y+terminalBox!.height/2
 expect(await page.evaluate(({tx,ty})=>document.elementFromPoint(tx,ty)?.closest<HTMLElement>('[data-planning-block]')?.dataset.planningBlock,{tx,ty})).toBe(rid(4))
 const feedback=page.locator('.move-feedback')
 await expectStableControls({controls:{source,terminal,expand:page.getByRole('button',{name:'Expand all',exact:true})},interactions:[{name:width===390?'touch terminal target':'mouse terminal target',run:async()=>{
  if(width===390){
   await page.clock.install();await page.clock.pauseAt(await page.evaluate(()=>Date.now()+1000))
   await page.evaluate(()=>{HTMLElement.prototype.setPointerCapture=function(){}})
   await source.dispatchEvent('pointerdown',{pointerId:19,pointerType:'touch',button:0,clientX:x,clientY:y,bubbles:true})
   await page.clock.runFor(500);await expect(page.locator('.move-ghost')).toBeVisible()
   await source.dispatchEvent('pointermove',{pointerId:19,pointerType:'touch',button:0,clientX:tx,clientY:ty,bubbles:true})
  }else{
   await page.mouse.move(x,y);await page.mouse.down();await page.mouse.move(x+12,y+12);await page.mouse.move(tx,ty)
  }
  await expect(feedback).toHaveText('Only upcoming releases can be reordered.')
  await expect(block(page,rid(4))).toHaveClass(/move-refused/);await expect(page.locator('.move-line')).toHaveCount(0)
  mkdirSync('test-results/aeon-596-p6c-fix2',{recursive:true});await page.screenshot({path:`test-results/aeon-596-p6c-fix2/terminal-refusal-${width}-${theme}.png`})
  if(width===390){await source.dispatchEvent('pointerup',{pointerId:19,pointerType:'touch',button:0,clientX:tx,clientY:ty,bubbles:true});await page.clock.resume()}else await page.mouse.up()
 }}]})
 await expect(page.locator('.move-ghost')).toHaveCount(0);await expect(feedback).toHaveText('Only upcoming releases can be reordered.');expect(world.writes).toEqual([]);expect(world.errors).toEqual([])
})
