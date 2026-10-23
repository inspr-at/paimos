// SPDX-License-Identifier: AGPL-3.0-only
// Risk: saving a PNG, a missing reference, or a rewritten reference can turn a
// broken comparison into a green case. This synthetic document is only a
// counterexample for the comparator; it cannot supply a product reference.
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import { expect, type Page, type Response } from '@playwright/test'
import { agents, capacity, deliveryNumbers, publicQuote, quoteEditor, NOW, sessionChat, sessionChatUnavailable } from './fixtures'
import { test, checkVisual, compareVisualReference, visualCases, prepareVisualFonts, renderedFonts, fontEvidence } from './checkpoint'
import { visualChrome } from './chrome-fixture'
import { fixtures, mockWork } from '../work-fixtures'
import { NODES } from '../quote-inspector-fixtures'

// Risk: a healthy Agents reference accidentally depicts an unmocked permission
// service, or hiding that banner also hides a real load failure and its retry.
test('P02 Agents permission requests load successfully and retain explicit error recovery', async ({ page, context }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.clock.setFixedTime(new Date(NOW))
  await mockWork(page, fixtures(), { admin: true })
  // Exercise chrome without the shared agents handler, which also serves this
  // endpoint. Otherwise its fallback could conceal a missing chrome mock.
  await visualChrome(page, fixtures(), NOW, { agents: false })
  await page.goto('/')
  const chromeRead = await page.evaluate(async () => {
    const response = await fetch('/api/approvals?limit=200')
    return { status: response.status, body: await response.json() }
  })
  expect(chromeRead).toEqual({ status: 200, body: [] })
  // Force the shell's delayed history read through the real release control.
  // An unnamed reviewed running release must remain unnamed after that read.
  const release = page.locator('.app-footer .version-pill')
  await expect(release).toBeVisible()
  await expect(release.locator('.footer-codename')).toHaveCount(0)
  await release.click()
  await expect(page.getByRole('dialog', { name: 'PAIMOS AEON releases' })
    .getByRole('grid', { name: 'Releases, newest first' })).toBeVisible()
  await expect(release.locator('.footer-codename')).toHaveCount(0)
  // An old chrome poll must not satisfy the populated fixture's readiness read
  // while navigation destroys its response body. Bind each fixture to its page.
  await page.close()
  const agentsPage = await context.newPage()
  await agentsPage.clock.setFixedTime(new Date(NOW))
  await agents(agentsPage, 'desktop', 'light')
  let fail = true
  await agentsPage.route(url => url.pathname === '/api/approvals', route => {
    if (route.request().method() !== 'GET' || !fail) return route.fallback()
    return route.fulfill({ status: 503, json: { error: 'Permission service unavailable' } })
  })
  const failedRead = agentsPage.waitForResponse(response => new URL(response.url()).pathname === '/api/approvals')
  await agentsPage.reload()
  expect((await failedRead).status()).toBe(503)
  const error = agentsPage.getByRole('alert').filter({ hasText: 'Permission requests could not be loaded:' })
  await expect(error).toContainText('Permission service unavailable')
  await expect(error).toContainText('(503)')
  await expect(agentsPage.locator('.sessions [data-row]').first()).toBeVisible()
  fail = false
  const recoveredRead = agentsPage.waitForResponse(response => new URL(response.url()).pathname === '/api/approvals')
  await error.getByRole('button', { name: 'Try again' }).click()
  expect((await recoveredRead).status()).toBe(200)
  await expect(error).toHaveCount(0)
  await expect(agentsPage.locator('.sessions [data-row]').first()).toBeVisible()
})

test('visual comparison rejects a changed control and never creates a missing reference', async ({ page, browser }, info) => {
  expect(info.config.updateSnapshots).toBe('none')
  await page.route('**/__visual-comparator-proof', route => route.fulfill({ contentType: 'text/html', body: '<style>body { margin: 0; background: white; font-family: var(--font, -apple-system, system-ui, sans-serif); } button { font: inherit; width: 160px; height: 44px; background: white; color: black; border: 1px solid black; }</style><button>Review change</button>' }))
  await page.goto('/__visual-comparator-proof')
  await prepareVisualFonts(page, String(info.project.use.baseURL))
  await fontEvidence(page, 'projects')
  const reference = info.snapshotPath('synthetic-reference.png')
  mkdirSync(dirname(reference), { recursive: true })
  writeFileSync(reference, await page.screenshot({ path: info.outputPath('synthetic-reference.png') }))
  await expect(page).toHaveScreenshot('synthetic-reference.png', { maxDiffPixels: 0 })
  await page.getByRole('button').evaluate(element => { element.style.background = 'rgb(220, 0, 0)' })
  await expect(expect(page).toHaveScreenshot('synthetic-reference.png', { maxDiffPixels: 0, timeout: 1500 })).rejects.toThrow(/Screenshot comparison failed|pixels.*different/)
  await expect(expect(page).toHaveScreenshot('missing-reference.png', { timeout: 1500 })).rejects.toThrow(/snapshot.*doesn.t exist|snapshot.*not exist/i)

  // Exercise the product gate with a synthetic approved/pinned state. Neither
  // this fixture nor its pixels can become a product reference.
  const visual = JSON.parse(readFileSync(new URL('../../../scripts/test-prune/visual-manifest.json', import.meta.url), 'utf8'))
  visual.comparison.mode = 'blocking'
  visual.approval = { reviewer: 'Tick', status: 'approved', approvedAt: 'synthetic-only', evidence: 'Synthetic comparator proof', states: ['projects'] }
  visual.baselineAuthority.status = 'approved'
  Object.assign(visual.baselineAuthority.source, { attachmentId: 'synthetic-only' })
  visual.baselineAuthority.retention.verifiedAt = 'synthetic-only'
  visual.renderer.runner.imageVersion = 'synthetic-image'
  const renderer = structuredClone(visual.renderer)
  for (const state of visual.states) {
    delete state.approval
    for (const checkpoint of state.checkpoints) {
      checkpoint.reference = { attachmentMember: null, sha256: null }
      if (checkpoint.referenceStatus === 'needs-re-capture') {
        delete checkpoint.referenceStatus; delete checkpoint.deferredUntil; delete checkpoint.deferredReason
      }
    }
  }
  const png = await page.screenshot()
  const hash = (bytes: Buffer) => createHash('sha256').update(bytes).digest('hex')
  const project = visual.states.find((state: { id: string }) => state.id === 'projects')
  const files = project.checkpoints.map((checkpoint: { id: string; reference: unknown }) => {
    for (const field of ['referenceStatus', 'deferredUntil', 'deferredReason']) delete checkpoint[field]
    checkpoint.reference = { attachmentMember: `${checkpoint.id}.png`, sha256: hash(png) }
    return { name: `${checkpoint.id}.png`, base64: png.toString('base64') }
  })
  const bundle = Buffer.from(JSON.stringify({ version: 1, captureSHA: 'a'.repeat(40), renderer, files }))
  visual.baselineAuthority.source.format = 'json'
  visual.baselineAuthority.source.sha256 = hash(bundle)
  const baselineFile = info.outputPath('synthetic-pinned-bundle.json')
  writeFileSync(baselineFile, bundle)
  const checkpoint = project.checkpoints[0]
  expect(await compareVisualReference(page, info, checkpoint, { visual, baselineFile, renderer })).toBe(true)
  await page.getByRole('button').evaluate(element => { element.style.background = 'rgb(0, 0, 220)' })
  await expect(compareVisualReference(page, info, checkpoint, { visual, baselineFile, renderer, timeout: 1500 }))
    .rejects.toThrow(/Screenshot comparison failed|pixels.*different/)
  // The exact same differing pixels remain visible as evidence in report-only
  // mode, then reject again when only the mode field changes back to blocking.
  const reporting = structuredClone(visual)
  reporting.history = [] // Synthetic bytes, never the real set-2 history.
  reporting.comparison.mode = 'report-only'
  expect(await compareVisualReference(page, info, checkpoint, { visual: reporting, baselineFile, renderer: { ...renderer, locale: 'de-DE' }, timeout: 1500 })).toBe(false)
  expect(JSON.parse(readFileSync(info.outputPath(`${checkpoint.id}-comparison.md`), 'utf8')))
    .toMatchObject({ status: 'different', rendererMismatch: expect.stringContaining('renderer differs from reference: locale'),
      error: expect.stringMatching(/Screenshot comparison failed|pixels.*different/) })
  reporting.comparison.mode = 'blocking'
  await expect(compareVisualReference(page, info, checkpoint, { visual: reporting, baselineFile, renderer, timeout: 1500 }))
    .rejects.toThrow(/Screenshot comparison failed|pixels.*different/)
  await expect(compareVisualReference(page, info, checkpoint, { visual, baselineFile: info.outputPath('absent-bundle.json'), renderer: { ...renderer, locale: 'de-DE' } }))
    .rejects.toThrow('renderer differs from reference: locale')
  // A partial state must materialize its approved checkpoint without requiring
  // the deferred phone pins, even though a whole-state cut would reject them.
  const mixed = structuredClone(visual)
  const delivery = mixed.states.find((state: { id: string }) => state.id === 'delivery-numbers')
  const deliveryPNG = await page.screenshot()
  const desktopFiles = delivery.checkpoints.filter((checkpoint: { profile: string }) => checkpoint.profile === 'desktop')
    .map((checkpoint: { id: string; reference: unknown }) => {
      checkpoint.reference = { attachmentMember: `${checkpoint.id}.png`, sha256: hash(deliveryPNG) }
      return { name: `${checkpoint.id}.png`, base64: deliveryPNG.toString('base64') }
    })
  mixed.approval.checkpoints = [...project.checkpoints, ...delivery.checkpoints.filter((checkpoint: { profile: string }) => checkpoint.profile === 'desktop')]
    .map((checkpoint: { id: string }) => checkpoint.id)
  const mixedBytes = Buffer.from(JSON.stringify({ version: 1, captureSHA: 'a'.repeat(40), renderer, files: [...files, ...desktopFiles] }))
  mixed.baselineAuthority.source.sha256 = hash(mixedBytes)
  const mixedFile = info.outputPath('synthetic-mixed-bundle.json')
  writeFileSync(mixedFile, mixedBytes)
  expect(await compareVisualReference(page, info, delivery.checkpoints[2], { visual: mixed, baselineFile: mixedFile, renderer })).toBe(true)
  expect(await compareVisualReference(page, info, delivery.checkpoints[0], { visual: mixed, baselineFile: info.outputPath('absent-bundle.json'), renderer })).toBe(false)
  // Awaiting states retain strict quality checks without PNG captures.
  const qualityDocument = '<html lang="en"><head><title>Synthetic quality control</title><style>body { font-family: var(--font, -apple-system, system-ui, sans-serif); } button, input { font: inherit; } button { min-width: 80px; height: 44px; }</style></head><body><main><button>Continue</button></main></body></html>'
  const setupQuality = async (qualityPage: Page) => {
    await qualityPage.route('**/__visual-quality-proof', route => route.fulfill({ contentType: 'text/html', body: qualityDocument }))
    // Fonts stay on the app origin, as in real checkpoints. about:blank would
    // turn the self-hosted font into a cross-origin request from a null origin.
    await qualityPage.goto('/__visual-quality-proof')
  }
  const pending = visualCases('tickets')[0]!
  expect(await compareVisualReference(page, info, pending, { visual, baselineFile: info.outputPath('absent-bundle.json') })).toBe(false)
  await checkVisual(browser, info, pending, setupQuality, { visual })
  const report = JSON.parse(readFileSync(info.outputPath(`${pending.id}.md`), 'utf8'))
  expect(report).toMatchObject({ baselineCompared: false, pngSHA256: null, requirementsRun: true,
    reference: { mode: 'awaiting-reference' }, layout: [], axe: [], console: [], responses: [], fonts: { body: { renderedFamilies: ['Inter Variable'] }, painted: expect.arrayContaining([{ family: 'Inter Variable', custom: process.platform !== 'linux' }]) } })
  expect(existsSync(info.outputPath(`${pending.id}.png`))).toBe(false)
  // A shared fixture replacing the selected state's clock must fail before
  // screenshot or comparison. No wall-clock wait is needed to prove drift.
  await expect(checkVisual(browser, info, pending, async qualityPage => {
    await setupQuality(qualityPage)
    await qualityPage.clock.setFixedTime(new Date(NOW + 60_000))
  }, { visual })).rejects.toThrow(/fixture clock changed/)
  for (const id of ['goals', 'ticket-workspace', 'delivery-numbers', 'capacity']) {
    const deferred = visualCases(id)[0]!
    // The setup must never run; an unbuilt product is neither inspected nor
    // failed, and its explicit until/reason report is attached to the test.
    await checkVisual(browser, info, deferred, async () => { throw new Error('Deferred setup must not run') }, { visual })
    expect(JSON.parse(readFileSync(info.outputPath(`${deferred.id}.md`), 'utf8')))
      .toMatchObject({ requirementsRun: false, reference: { mode: 'deferred', reason: expect.stringContaining('deferred until AEON-') } })
    expect(info.annotations.some(row => row.type === 'deferred' && row.description?.includes(deferred.id))).toBe(true)
    expect(existsSync(info.outputPath(`${deferred.id}.png`))).toBe(false)
  }
  // Stale pins are explicit awaiting-reference reports, never comparisons or
  // quiet passes. Re-capture and person review belong to reference set 3.
  const current = JSON.parse(readFileSync(new URL('../../../scripts/test-prune/visual-manifest.json', import.meta.url), 'utf8'))
  current.comparison.mode = 'blocking'
  for (const state of current.states) {
    for (const checkpoint of state.checkpoints.filter(row => row.referenceStatus === 'needs-re-capture')) {
      const stale = visualCases(state.id).find(row => row.id === checkpoint.id)!
      await checkVisual(browser, info, stale, async () => { throw new Error('Stale reference setup must not run') }, { visual: current })
      expect(JSON.parse(readFileSync(info.outputPath(`${stale.id}.md`), 'utf8')))
        .toMatchObject({ requirementsRun: false, reference: { mode: 'awaiting-reference', status: 'needs-re-capture' } })
      expect(info.annotations.some(row => row.type === 'awaiting reference' && row.description?.includes(stale.id))).toBe(true)
      expect(existsSync(info.outputPath(`${stale.id}.png`))).toBe(false)
    }
  }
  await expect(checkVisual(browser, info, visualCases('hours')[0]!, async qualityPage => {
    await setupQuality(qualityPage)
    await qualityPage.evaluate(() => console.error('Synthetic quality failure must still block an awaiting state'))
  }, { visual })).rejects.toThrow(/unexpected console\/page failure/)
  reporting.comparison.mode = 'report-only'
  await checkVisual(browser, info, pending, async qualityPage => {
    await setupQuality(qualityPage)
    await qualityPage.evaluate(() => console.error('Synthetic quality failure must be reported'))
  }, { visual: reporting })
  expect(JSON.parse(readFileSync(info.outputPath(`${pending.id}.md`), 'utf8')))
    .toMatchObject({ requirementsRun: true, quality: { status: 'failed', error: expect.stringContaining('unexpected console/page failure') },
      comparison: { status: 'unavailable' } })
  for (const id of ['goals', 'ticket-workspace', 'delivery-numbers', 'capacity']) {
    const deferred = visualCases(id)[0]!
    let setupRan = false
    await checkVisual(browser, info, deferred, async () => {
      setupRan = true
      throw new Error('Synthetic deferred quality setup failure is reported')
    })
    expect(setupRan).toBe(true)
    expect(JSON.parse(readFileSync(info.outputPath(`${deferred.id}.md`), 'utf8')))
      .toMatchObject({ requirementsRun: true, reference: { mode: 'report-only' },
        quality: { status: 'failed', error: expect.stringContaining('Synthetic deferred quality setup failure is reported') } })
  }
  await setupQuality(page)
  await prepareVisualFonts(page, String(info.project.use.baseURL))
  expect(await renderedFonts(page)).toEqual([{ family: 'Inter Variable', custom: process.platform !== 'linux' }])
  await page.evaluate(() => {
    const hidden = document.createElement('span')
    hidden.textContent = 'Not painted'
    hidden.style.cssText = 'opacity: 0; font-family: Arial !important'
    document.body.append(hidden)
  })
  expect(await renderedFonts(page)).toEqual([{ family: 'Inter Variable', custom: process.platform !== 'linux' }])
  // The production Latin subset lacks these picker placeholder glyphs. Keep
  // the real text intact and require custom-font provenance for both the
  // native placeholder and ordinary painted text on every host.
  await page.evaluate(() => {
    const label = document.createElement('label')
    label.textContent = 'Model ←→ '
    const input = document.createElement('input')
    input.placeholder = 'Type to filter · ←→ level'
    label.append(input)
    document.querySelector('main')!.append(label)
  })
  expect(await renderedFonts(page)).toEqual([{ family: 'Inter Variable', custom: process.platform !== 'linux' }])
  await page.addStyleTag({ content: 'body, body * { font-family: "JetBrains Mono" !important; }' })
  await expect(fontEvidence(page, 'projects')).rejects.toThrow(/body text must resolve to Inter/)
  await expect(compareVisualReference(page, info, checkpoint, { visual, baselineFile, renderer })).rejects.toThrow(/body text must resolve to Inter/)
  await expect(checkVisual(browser, info, pending, async qualityPage => {
    await setupQuality(qualityPage)
    await qualityPage.addStyleTag({ content: 'body, body * { font-family: "JetBrains Mono" !important; }' })
  }, { visual })).rejects.toThrow(/body text must resolve to Inter/)

})

// Risk: the phone's page-flow panel remains below project chrome, so a capture
// omits the on-target headline even though the off-screen summary is visible.
test('P02 Delivery phone capture includes its heading and on-target headline in both themes', async ({ browser }, info) => {
  // Inspect the pending crop through the same quality runner without claiming
  // either a Linux renderer match or approval of these synthetic test gates.
  const visual = JSON.parse(readFileSync(new URL('../../../scripts/test-prune/visual-manifest.json', import.meta.url), 'utf8'))
  const delivery = visual.states.find((state: { id: string }) => state.id === 'delivery-numbers')
  for (const checkpoint of delivery.checkpoints) if (checkpoint.profile === 'phone') {
    delete checkpoint.referenceStatus; delete checkpoint.deferredUntil; delete checkpoint.deferredReason
  }
  visual.comparison.mode = 'blocking'
  delivery.approval = { decision: 'changes-requested', reason: 'Synthetic quality-only inspection of the pending phone crop; no product reference authority' }
  const positions: unknown[] = []
  for (const theme of ['light', 'dark'] as const) {
    const checkpoint = visualCases('delivery-numbers').find(checkpoint => checkpoint.profile === 'phone' && checkpoint.theme === theme)!
    await checkVisual(browser, info, checkpoint, async page => {
      await deliveryNumbers(page, checkpoint.profile, theme)
      await fontEvidence(page, 'delivery-numbers')
      const heading = page.getByRole('heading', { name: 'Delivery', exact: true })
      const headline = page.getByTestId('delivery-summary').locator('.big')
      await expect(heading).toBeInViewport({ ratio: 1 })
      await expect(headline).toHaveText(/\d+ of 13 numbers are on target\./)
      await expect(headline).toBeInViewport({ ratio: 1 })
      const top = await heading.evaluate(element => element.getBoundingClientRect().top)
      const headerBottom = await page.locator('.app-header').evaluate(element => element.getBoundingClientRect().bottom)
      expect(top - headerBottom, 'Delivery starts at least 12 px below the app header').toBeGreaterThanOrEqual(12)
      positions.push({ heading: await heading.boundingBox(), headline: await headline.boundingBox() })
      await page.screenshot({ path: info.outputPath(`delivery-numbers-390-${theme}.png`), animations: 'disabled', caret: 'hide' })
    }, { visual })
    expect(JSON.parse(readFileSync(info.outputPath(`${checkpoint.id}.md`), 'utf8')))
      .toMatchObject({ requirementsRun: true, baselineCompared: false, layout: [], axe: [], console: [], responses: [] })
  }
  expect(positions[1], 'both themes depict the same phone crop').toEqual(positions[0])
})

// Risk: optional native/chat reads escape the fixture to a backend 404, so
// the intended chat reference depicts an outage before comparison can run.
test('P02 Session Chat fixture binds its chat reads and native refusal to the displayed session', async ({ page, browser }, info) => {
  const errors: string[] = [], reads: Response[] = []
  page.on('pageerror', error => errors.push(error.message))
  page.on('response', response => reads.push(response))
  const nativeRead = page.waitForResponse(response => new URL(response.url()).pathname.endsWith('/native-context'))
  const threadRead = page.waitForResponse(response => /^\/api\/chat-sessions\/[^/]+\/thread$/.test(new URL(response.url()).pathname))
  await page.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
  await sessionChatUnavailable(page, 'desktop', 'light')
  const [native, thread] = await Promise.all([nativeRead, threadRead])
  expect(native.status(), 'legacy chat requires the typed native refusal, not a backend 404 or a native snapshot').toBe(503)
  expect(thread.status(), 'chat binding must be mocked, not a backend refusal').toBe(200)
  const session = page.url().split('/').at(-1)!.split('?')[0]!
  const binding = await thread.json()
  expect(new URL(native.url()).pathname).toBe(`/api/projects/p-pharos/harness-sessions/${session}/native-context`)
  expect(new URL(thread.url()).pathname).toBe(`/api/chat-sessions/${session}/thread`)
  expect(await native.json()).toEqual({ reason: 'feature_disabled', request_id: null, retryable: false })
  expect(binding).toMatchObject({ contract: 'chat-v1', conversation_id: '7c000000-0000-4000-8000-000000001674' })
  const historyPath = `/api/chat-threads/${binding.conversation_id}/messages`
  await expect.poll(() => reads.some(response => new URL(response.url()).pathname === historyPath)).toBe(true)
  const history = reads.find(response => new URL(response.url()).pathname === historyPath)!
  expect(history.status()).toBe(200)
  expect(new URL(history.url()).pathname).toBe(`/api/chat-threads/${binding.conversation_id}/messages`)
  expect(await history.json()).toMatchObject({ contract: 'chat-v1', items: [], has_more_before: false, has_more_after: false })
  await page.getByRole('complementary', { name: 'Session details' }).getByRole('tab', { name: /^Events/ }).click()
  await expect(page.getByText('Live events are not available for this session yet.', { exact: true })).toBeVisible()
  await page.getByRole('complementary', { name: 'Session details' }).getByRole('tab', { name: /^Chat/ }).click()
  await expect(page.getByRole('textbox', { name: 'Message to Release coordination', exact: true })).toBeVisible()
  expect(errors).toEqual([])
  expect(reads.filter(response => response.status() >= 400).map(response => ({ path: new URL(response.url()).pathname, status: response.status() })))
    .toEqual([{ path: new URL(native.url()).pathname, status: 503 }])
  // Refusal and healthy fixtures get separate contexts. Replacing route stacks
  // under an old thread poll could send the old session's read to the new mock.
  await page.close()
  const healthyContext = await browser.newContext({ baseURL: info.project.use.baseURL, viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' })
  try {
    const healthyPage = await healthyContext.newPage(), healthyReads: Response[] = [], healthyErrors: string[] = []
    healthyPage.on('response', response => healthyReads.push(response))
    healthyPage.on('pageerror', error => healthyErrors.push(error.message))
    await healthyPage.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
    await prepareVisualFonts(healthyPage, String(info.project.use.baseURL))
    const healthyNativeRead = healthyPage.waitForResponse(response => new URL(response.url()).pathname.endsWith('/native-context'))
    await sessionChat(healthyPage, 'desktop', 'light')
    const healthyNative = await healthyNativeRead
    expect(healthyNative.status()).toBe(503)
    expect(await healthyNative.json()).toEqual({ reason: 'feature_disabled', request_id: null, retryable: false })
    const thread = healthyPage.getByRole('list', { name: 'Messages', exact: true })
    await expect(thread.locator('.msg.desk')).toHaveCount(2)
    const day = thread.locator(':scope > .day')
    await expect(day).toHaveCount(1)
    await expect(day).toHaveText('Today')
    await expect(thread.locator(':scope > .msg').first()).toHaveAccessibleDescription('Today')
    await expect(thread.locator('.msg.mine').last()).toContainText('Start it now. Keep the CI fixes in their own PR.')
    await expect(healthyPage.getByText('The session’s live capabilities could not be read. Reopen the panel to try again.', { exact: true })).toHaveCount(0)
    expect(healthyReads.filter(response => response.status() >= 400).map(response => ({ path: new URL(response.url()).pathname, status: response.status() }))).toEqual([{ path: new URL(healthyNative.url()).pathname, status: 503 }])
    expect(healthyErrors).toEqual([])
    await fontEvidence(healthyPage, 'session-chat')
    await healthyPage.screenshot({ path: info.outputPath('session-chat-1440.png'), animations: 'disabled', caret: 'hide' })
    await healthyPage.setViewportSize({ width: 390, height: 844 })
    await healthyPage.locator('.thread-scroll').evaluate(element => { element.scrollTop = 0 })
    await expect(day).toBeInViewport({ ratio: 1 })
    await healthyPage.screenshot({ path: info.outputPath('session-chat-390.png'), animations: 'disabled', caret: 'hide' })
  } finally { await healthyContext.close() }

})

// Risk: pagination initially exposes only the hidden measurement copy. A broad
// first() click can keep waiting on that copy after the real paragraph appears.
test('P02 quote editor selects the rendered paragraph when its hidden measurement copy comes first', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.clock.setFixedTime(new Date(NOW))
  page.setDefaultTimeout(5000)
  // Keep the actual inert measurement tree ahead of the rendered pages. This
  // deterministically exposes the same duplicate-ID ordering as initial layout,
  // without depending on font/network speed or changing the quote's content.
  await page.addInitScript(() => {
    new MutationObserver(() => {
      const measure = document.querySelector('.quote-measure')
      if (measure?.parentElement && measure.parentElement.firstElementChild !== measure) measure.parentElement.prepend(measure)
    }).observe(document, { childList: true, subtree: true })
  })
  for (const theme of ['light', 'dark'] as const) {
    await quoteEditor(page, 'desktop', theme)
    const copies = page.locator(`[data-node-id="${NODES[0]}"]`)
    await expect(copies).toHaveCount(2)
    await expect(copies.first()).toBeHidden()
    await expect(page.locator(`.quote-page [data-node-id="${NODES[0]}"]`)).toBeVisible()
    await expect(page.locator('.quote-document')).toHaveAttribute('data-quote-ready', 'true')
    const panel = page.getByRole('complementary', { name: 'Format' })
    await expect(panel.getByRole('tab', { selected: true })).toHaveText('Text')
    await expect(panel.locator('.scope')).toContainText('Ausgangslage')
    await expect(panel.locator('.scope')).toContainText('Paragraph')
  }
})

// Risk: a missing profile route silently falls back to the shell's sans or mono,
// leaving both the reference and future comparisons equally wrong.
test('P02 quote font guard rejects an unloaded profile face', async ({ page }, info) => {
  await page.clock.setFixedTime(new Date(NOW))
  await prepareVisualFonts(page, String(info.project.use.baseURL))
  await publicQuote(page, 'desktop', 'light')
  const evidence = await fontEvidence(page, 'public-quote')
  expect(evidence.profile).toHaveLength(7)
  expect(evidence.document?.body.renderedFamilies).toEqual(['Source Sans 3'])
  expect(evidence.document?.display.renderedFamilies).toEqual(['Source Serif 4 Display'])
  // Replace an unused italic face with its declared but unloaded equivalent.
  // The regular body/display still paint correctly, so only the face guard
  // can reject this control. It uses no network request or elapsed-time check.
  await page.evaluate(() => {
    const face = [...document.fonts].find(face => face.style === 'italic' && face.weight === '700')!
    document.fonts.delete(face)
    document.fonts.add(new FontFace(face.family, 'url("/not-a-profile-font.woff2")', { weight: face.weight, style: face.style }))
  })
  await expect(fontEvidence(page, 'public-quote')).rejects.toThrow(/quote profile faces must all be loaded/)
})

// Risk: asynchronous font/layout changes dismiss only one theme's popover or
// change its scroll, so the two references depict different interactions.
test('P02 capacity reference keeps the same open popover and scroll across themes', async ({ browser }, info) => {
  for (const width of [390, 1440]) {
    const states: unknown[] = []
    for (const theme of ['light', 'dark'] as const) {
      const context = await browser.newContext({ baseURL: info.project.use.baseURL, viewport: { width, height: width === 390 ? 844 : 900 },
        colorScheme: theme, reducedMotion: 'reduce', deviceScaleFactor: 1, hasTouch: width === 390 })
      try {
        const page = await context.newPage()
        await prepareVisualFonts(page, String(info.project.use.baseURL))
        await capacity(page, width === 390 ? 'phone' : 'desktop', theme)
        await fontEvidence(page, 'capacity')
        // Include a paint after all font reads: a delayed scroll dismissal
        // cannot pass just because the panel was present immediately on click.
        await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
        await expect(page.getByRole('dialog', { name: 'Daily limit for Claude Code', exact: true })).toBeVisible()
        states.push(await page.locator('button[data-pace="claude"]').evaluate(element => ({
          expanded: element.getAttribute('aria-expanded'),
          top: element.getBoundingClientRect().top,
          scroll: [...document.querySelectorAll('*')].filter(element => element.scrollTop > 0).map(element => ({ tag: element.tagName, class: element.className, top: element.scrollTop })),
        })))
      } finally { await context.close() }
    }
    expect(states[1], `capacity themes must depict the same ${width}px state`).toEqual(states[0])
  }
})
