// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test, type BrowserContext, type Page } from '@playwright/test'

const quoteId='aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
const tenantId='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
const sectionId='11111111-1111-4111-8111-111111111111'
const nodeId='22222222-2222-4222-8222-222222222222'
const positionId='33333333-3333-4333-8333-333333333333'
const shellIdentity={principal:{id:nodeId,name:'Test Person'},tenant:{id:tenantId,name:'Test Workspace'}}
async function mockShell(page:Page){await page.route('**/api/**',route=>{
  const path=new URL(route.request().url()).pathname
  return route.fulfill({json:path==='/api/me'?shellIdentity:path==='/api/approvals'?[]:
    ['/api/projects','/api/nodes','/api/project-groups','/api/harness-sessions','/api/plugins'].includes(path)?{items:[]}:{}})
})}
function documentFixture(){return {schema_version:1,minimum_writer_version:1,title:'Base',subtitle:'Original',project_ref:'',offer_date:'2026-09-24',valid_until:'2026-10-24',currency:'EUR',sender:{},recipient:{},legal:{},layout:{},sections:[{id:sectionId,heading:'Scope',body:'',nodes:[{id:nodeId,kind:'paragraph',text:'A😀B'}]}],positions:[{id:positionId,pricing_source:'manual',short_text:'Work',long_text:'',quantity:'1.25',unit_label:'hour',unit_price_cents:105,total_cents:131,currency:'EUR'}],net_total_cents:131}}
type Doc=ReturnType<typeof documentFixture>
function mockServer(){
  let revision=1;let doc:Doc=documentFixture();const receipts=new Map<string,{mutation_id:string;acknowledged_revision:number;acknowledged_quote_revision:number;current_revision:number;current_quote_revision:number;replayed:boolean;document?:Doc}>()
  let dropNext=false
  const install=async(context:BrowserContext)=>{await context.route('**/api/**',async route=>{
    const req=route.request();if(!req.url().includes(`/api/quotes/${quoteId}/draft`)){await route.fulfill({status:200,contentType:'application/json',body:'{}'});return}
    if(req.method()==='GET'){await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({document:doc,document_sha256:'a'.repeat(64),draft_revision:revision,quote_revision:revision,schema_version:1,minimum_writer_version:1,base_version:0,updated_at:'2026-09-24T00:00:00Z',updated_by_principal_id:nodeId})});return}
    const body=req.postDataJSON() as {client_session_id:string;mutation_id:string;document:Doc}
    const key=`${body.client_session_id}:${body.mutation_id}`;const prior=receipts.get(key)
    if(prior){if(JSON.stringify(prior.document)!==JSON.stringify(body.document)){await route.fulfill({status:409,contentType:'application/json',body:'{"error":"mutation reused"}'});return}
      await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({...prior,current_revision:revision,current_quote_revision:revision,replayed:true,document:undefined})});return}
    if(req.headers()['if-match']!==`"qd-${revision}"`){await route.fulfill({status:412,contentType:'application/json',body:'{"error":"stale"}'});return}
    doc=structuredClone(body.document);revision++;const receipt={mutation_id:body.mutation_id,acknowledged_revision:revision,acknowledged_quote_revision:revision,current_revision:revision,current_quote_revision:revision,replayed:false,document:structuredClone(doc)};receipts.set(key,receipt)
    if(dropNext){dropNext=false;await route.abort('failed');return}
    await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(receipt)})
  })}
  return {install,revision:()=>revision,document:()=>doc,drop:()=>{dropNext=true}}
}
async function open(page:Page,principalId:string){await page.goto('/');return page.evaluate(async({quoteId,tenantId,principalId})=>{
  const {QuoteSession}=await import('/src/lib/quoteSession.ts');const session=new QuoteSession({quoteId,tenantId,principalId});(window as unknown as {quoteCollab:typeof session}).quoteCollab=session
  const recovery=await session.open();return {recovery:!!recovery,revision:session.view.baseRevision}
},{quoteId,tenantId,principalId})}
async function edit(page:Page,key:'title'|'subtitle',value:string){await page.evaluate(({key,value})=>{const s=(window as any).quoteCollab;const next=structuredClone(s.view.working);next[key]=value;s.edit(next)},{key,value})}
async function save(page:Page){return page.evaluate(async()=>{const s=(window as any).quoteCollab;await s.save();return {local:s.view.local,remote:s.view.remote,revision:s.view.baseRevision,review:s.view.review}})}

test('two browser contexts preserve both concurrent edits through CAS and reviewed merge',async({browser})=>{
  const server=mockServer();const a=await browser.newContext(),b=await browser.newContext();await Promise.all([server.install(a),server.install(b)])
  try{const pa=await a.newPage(),pb=await b.newPage();await Promise.all([open(pa,'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa'),open(pb,'bbbbbbbb-0000-4000-8000-bbbbbbbbbbbb')]);
    await edit(pa,'title','Editor A');await edit(pb,'subtitle','Editor B')
    const results=await Promise.all([save(pa),save(pb)]);expect(results.map(x=>x.local).sort()).toEqual(['clean','conflict'])
    const loser=results[0].local==='conflict'?pa:pb
    const reviewed=await loser.evaluate(async()=>{const s=(window as any).quoteCollab;return s.acceptReview({})})
    expect(reviewed).toBe(true);expect(server.revision()).toBe(3);expect(server.document().title).toBe('Editor A');expect(server.document().subtitle).toBe('Editor B')
  }finally{await a.close();await b.close()}
})

test('dirty reload preserves local text, page reload recovers it, and a lost ACK replays after reconnect',async({browser})=>{
  const server=mockServer();const a=await browser.newContext(),b=await browser.newContext();await Promise.all([server.install(a),server.install(b)])
  try{const pa=await a.newPage(),pb=await b.newPage();const personA='aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa',personB='bbbbbbbb-0000-4000-8000-bbbbbbbbbbbb'
    await Promise.all([open(pa,personA),open(pb,personB)]);await edit(pb,'title','Remote first');expect((await save(pb)).local).toBe('clean')
    const clean=await pa.evaluate(async()=>{const s=(window as any).quoteCollab;await s.check();return {remote:s.view.remote,reload:await s.reload(),title:s.view.working.title}})
    expect(clean).toEqual({remote:'newer',reload:'loaded',title:'Remote first'})
    await edit(pa,'title','My unsaved text');await edit(pb,'title','Remote second');expect((await save(pb)).local).toBe('clean')
    const dirty=await pa.evaluate(async()=>{const s=(window as any).quoteCollab;await s.check();const result=await s.reload();return {result,title:s.view.working.title,conflicts:s.view.review?.conflicts.length}})
    expect(dirty).toEqual({result:'review',title:'My unsaved text',conflicts:1})
    await expect.poll(()=>pa.evaluate(async()=>{const s=(window as any).quoteCollab;const stored=await(await import('/src/lib/quoteRecovery.ts')).loadRecovery({tenantId:s.tenantId,principalId:s.principalId,quoteId:s.quoteId,sessionId:s.clientSessionId});return stored?.mine.title})).toBe('My unsaved text')
    await pa.reload();const restored=await open(pa,personA)
    expect(restored.recovery).toBe(true)
    const recovered=await pa.evaluate(async()=>{const s=(window as any).quoteCollab;const r=await (await import('/src/lib/quoteRecovery.ts')).loadRecovery({tenantId:s.tenantId,principalId:s.principalId,quoteId:s.quoteId,sessionId:s.clientSessionId});s.restore(r);return {title:s.view.working.title,remote:s.view.remote}})
    expect(recovered).toEqual({title:'My unsaved text',remote:'newer'})
    await pa.evaluate(async()=>{const s=(window as any).quoteCollab;await s.reload(true)})
    await edit(pa,'subtitle','After reconnect');server.drop();const failed=await save(pa);expect(['offline','failed']).toContain(failed.local)
    const retried=await pa.evaluate(async()=>{const s=(window as any).quoteCollab;await s.retry();return {local:s.view.local,revision:s.view.baseRevision}})
    expect(retried).toEqual({local:'clean',revision:4});expect(server.document().subtitle).toBe('After reconnect')
  }finally{await a.close();await b.close()}
})

test('revision watch backs off after outages and rechecks immediately on focus',async({page})=>{
  let reads=0
  await mockShell(page)
  await page.route(`**/api/quotes/${quoteId}/draft`,async route=>{
    reads++
    if(reads>1 && reads<5){await route.fulfill({status:503,json:{error:'temporarily unavailable'}});return}
    await route.fulfill({json:{document:documentFixture(),document_sha256:'a'.repeat(64),draft_revision:reads===1?1:2,quote_revision:2,schema_version:1,minimum_writer_version:1,base_version:0,updated_at:'2026-09-24T00:00:00Z',updated_by_principal_id:nodeId}})
  })
  await open(page,'aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa')
  const delays=[]
  for(let i=0;i<3;i++)delays.push(await page.evaluate(async()=>{const s=(window as any).quoteCollab;await s.check();return s.checkDelay}))
  expect(delays).toEqual([8000,16000,20000])
  await page.evaluate(()=>window.dispatchEvent(new Event('focus')))
  // reads increments when the route starts, before the session applies the draft.
  // Wait until this client shows the reset delay, then require that it was the fifth read.
  await expect.poll(()=>page.evaluate(()=>{const s=(window as any).quoteCollab;return {delay:s.checkDelay,remote:s.view.remote}})).toEqual({delay:4000,remote:'newer'})
  expect(reads).toBe(5)
})

test('verified overlay follows its stable node and stays out of print and saved JSON',async({page})=>{
  await page.route('**/api/**',route=>route.fulfill({status:200,contentType:'application/json',body:'{}'}));await page.goto('/')
  const result=await page.evaluate(async({sectionId,nodeId,doc})=>{
    const {mountOverlay}=await import('/tests/quote-collaboration-harness.ts')
    return mountOverlay(doc,sectionId,nodeId)
  },{sectionId,nodeId,doc:documentFixture()})
  await expect(page.locator('.quote-presence-overlays .mark.precise')).toBeVisible()
  expect(result).not.toContain('Alex');expect(result).not.toContain('session-1')
  const before = await page.locator('.quote-presence-overlays .mark').boundingBox()
  await page.evaluate(() => { const section = document.querySelector('[data-section-id]')!; section.parentElement!.style.transformOrigin = 'top left'; section.parentElement!.style.transform = 'translate(80px, 50px) scale(1.25)' })
  await expect.poll(async () => (await page.locator('.quote-presence-overlays .mark').boundingBox())?.x).toBeGreaterThan(before!.x + 70)
  const moved = await page.locator('.quote-presence-overlays .mark').boundingBox()
  await page.evaluate(() => { const section = document.querySelector('[data-section-id]')!; const before = document.createElement('section'); before.style.height = '120px'; section.before(before) })
  await expect.poll(async () => (await page.locator('.quote-presence-overlays .mark').boundingBox())?.y).toBeGreaterThan(moved!.y + 100)
  await page.evaluate(() => { const section = document.querySelector('[data-section-id]')!; section.parentElement!.append(section) })
  await expect(page.locator('.quote-presence-overlays .mark.precise')).toBeVisible()
  await page.evaluate(()=>{const model=(window as any).quoteOverlayModel;model.value={...model.value,sections:[{...model.value.sections[0],nodes:[{...model.value.sections[0].nodes[0],text:'A😀B edited locally'}]}]};document.querySelector('[data-text-id]')!.textContent='A😀B edited locally'})
  await expect(page.locator('.quote-presence-overlays .mark.section')).toBeVisible()
  await page.emulateMedia({media:'print'});expect(await page.locator('.quote-presence-overlays').evaluate(el=>getComputedStyle(el).display)).toBe('none')
  await page.emulateMedia({media:'screen'});await page.locator(`[data-section-id="${sectionId}"]`).evaluate(el=>el.remove());await expect(page.locator('.quote-presence-overlays .mark')).toHaveCount(0)
})

test('same-person tabs keep distinct mutation sessions after window.open clones storage',async({browser})=>{
  const server=mockServer(),context=await browser.newContext();await server.install(context)
  try{const first=await context.newPage(),person='aaaaaaaa-0000-4000-8000-aaaaaaaaaaaa';await open(first,person)
    const [second]=await Promise.all([first.waitForEvent('popup'),first.evaluate(()=>window.open('/'))]);await open(second,person)
    const firstId=await first.evaluate(()=>(window as any).quoteCollab.clientSessionId)
    const secondId=await second.evaluate(()=>(window as any).quoteCollab.clientSessionId)
    expect(secondId).not.toBe(firstId)
    await edit(second,'title','Other tab');expect((await save(second)).local).toBe('clean')
    const external=await first.evaluate(({secondId,quoteId})=>{const s=(window as any).quoteCollab;s.onNotice({id:1,quote_node_id:quoteId,type:'quote.draft_updated',actor_principal_id:s.principalId,draft_revision:2,quote_revision:2,state:'draft',client_session_id:secondId,mutation_id:'11111111-1111-4111-8111-111111111111'});return s.view.remote},{secondId,quoteId})
    expect(external).toBe('newer')
  }finally{await context.close()}
})

// Temporary same-host worker timing; removed before handoff.
import { writeFileSync as writeCutATiming } from 'node:fs'
import { hostname as cutAHost } from 'node:os'
let cutAStarted = 0
test.beforeEach(() => { cutAStarted = performance.now() })
test.afterEach(async ({}, testInfo) => {
  writeCutATiming(testInfo.outputPath('p10-cutA-timing.md'), JSON.stringify({
    host: cutAHost(), title: testInfo.title, seconds: (performance.now() - cutAStarted) / 1000,
    status: testInfo.status, retry: testInfo.retry,
  }))
})
