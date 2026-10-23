// SPDX-License-Identifier: AGPL-3.0-only
// Risk: saving a PNG, a missing reference, or a rewritten reference can turn a
// broken comparison into a green case. This synthetic document is only a
// counterexample for the comparator; it cannot supply a product reference.
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import { test, expect, type Page, type Response } from '@playwright/test'
import { sessionChat, sessionChatUnavailable } from './visual/fixtures'
import { checkVisual, compareVisualReference, visualCases, prepareVisualFonts, renderedFonts } from './visual/checkpoint'

test('visual comparison rejects a changed control and never creates a missing reference', async ({ page, browser }, info) => {
  expect(info.config.updateSnapshots).toBe('none')
  await page.setContent('<style>body { margin: 0; background: white; } button { width: 160px; height: 44px; background: white; color: black; border: 1px solid black; }</style><button>Review change</button>')
  const reference = info.snapshotPath('synthetic-reference.png')
  mkdirSync(dirname(reference), { recursive: true })
  writeFileSync(reference, await page.screenshot({ path: info.outputPath('synthetic-reference.png') }))
  await expect(page).toHaveScreenshot('synthetic-reference.png', { maxDiffPixels: 0 })
  await page.getByRole('button').evaluate(element => { element.style.background = 'rgb(220, 0, 0)' })
  await expect(expect(page).toHaveScreenshot('synthetic-reference.png', { maxDiffPixels: 0, timeout: 1500 })).rejects.toThrow(/Screenshot comparison failed|pixels.*different/)
  await expect(expect(page).toHaveScreenshot('missing-reference.png', { timeout: 1500 })).rejects.toThrow(/snapshot.*doesn.t exist|snapshot.*not exist/i)

  // Exercise the product gate with a synthetic approved/pinned state. Neither
  // this fixture nor its pixels can become a product reference.
  const visual = JSON.parse(readFileSync(new URL('../../scripts/test-prune/visual-manifest.json', import.meta.url), 'utf8'))
  visual.approval = { reviewer: 'Tick', status: 'approved', approvedAt: 'synthetic-only', evidence: 'Synthetic comparator proof', states: ['projects'] }
  visual.baselineAuthority.status = 'approved'
  Object.assign(visual.baselineAuthority.source, { attachmentId: 'synthetic-only' })
  visual.baselineAuthority.retention.verifiedAt = 'synthetic-only'
  visual.renderer.runner.imageVersion = 'synthetic-image'
  const renderer = structuredClone(visual.renderer)
  for (const state of visual.states) {
    delete state.approval
    for (const checkpoint of state.checkpoints) checkpoint.reference = { attachmentMember: null, sha256: null }
  }
  const png = await page.screenshot()
  const hash = (bytes: Buffer) => createHash('sha256').update(bytes).digest('hex')
  const project = visual.states.find((state: { id: string }) => state.id === 'projects')
  const files = project.checkpoints.map((checkpoint: { id: string; reference: unknown }) => {
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
  await expect(compareVisualReference(page, info, checkpoint, { visual, baselineFile: info.outputPath('absent-bundle.json'), renderer: { ...renderer, locale: 'de-DE' } }))
    .rejects.toThrow('renderer differs from reference: locale')
  // Awaiting states retain strict quality checks without PNG captures.
  const qualityDocument = '<html lang="en"><head><title>Synthetic quality control</title><style>button { min-width: 80px; height: 44px; }</style></head><body><main><button>Continue</button></main></body></html>'
  const setupQuality = async (qualityPage: Page) => {
    await qualityPage.route('**/__visual-quality-proof', route => route.fulfill({ contentType: 'text/html', body: qualityDocument }))
    // Fonts stay on the app origin, as in real checkpoints. about:blank would
    // turn the self-hosted font into a cross-origin request from a null origin.
    await qualityPage.goto('/__visual-quality-proof')
  }
  const pending = visualCases('tickets')[0]!
  expect(await compareVisualReference(page, info, pending, { visual, baselineFile: info.outputPath('absent-bundle.json') })).toBe(false)
  await checkVisual(browser, info, pending, setupQuality)
  const report = JSON.parse(readFileSync(info.outputPath(`${pending.id}.md`), 'utf8'))
  expect(report).toMatchObject({ baselineCompared: false, pngSHA256: null, requirementsRun: true,
    reference: { mode: 'awaiting-reference' }, layout: [], axe: [], console: [], responses: [], fonts: [{ family: 'JetBrains Mono', custom: true }] })
  expect(existsSync(info.outputPath(`${pending.id}.png`))).toBe(false)
  for (const id of ['goals', 'ticket-workspace']) {
    const deferred = visualCases(id)[0]!
    // The setup must never run; an unbuilt product is neither inspected nor
    // failed, and its explicit until/reason report is attached to the test.
    await checkVisual(browser, info, deferred, async () => { throw new Error('Deferred setup must not run') })
    expect(JSON.parse(readFileSync(info.outputPath(`${deferred.id}.md`), 'utf8')))
      .toMatchObject({ requirementsRun: false, reference: { mode: 'deferred', reason: expect.stringContaining('deferred until AEON-') } })
    expect(info.annotations.some(row => row.type === 'deferred' && row.description?.includes(deferred.id))).toBe(true)
    expect(existsSync(info.outputPath(`${deferred.id}.png`))).toBe(false)
  }
  await expect(checkVisual(browser, info, visualCases('hours')[0]!, async qualityPage => {
    await setupQuality(qualityPage)
    await qualityPage.evaluate(() => console.error('Synthetic quality failure must still block an awaiting state'))
  })).rejects.toThrow(/unexpected console\/page failure/)
  await setupQuality(page)
  await prepareVisualFonts(page, String(info.project.use.baseURL))
  expect(await renderedFonts(page)).toEqual([{ family: 'JetBrains Mono', custom: true }])
  await page.evaluate(() => {
    const hidden = document.createElement('span')
    hidden.textContent = 'Not painted'
    hidden.style.cssText = 'opacity: 0; font-family: Arial !important'
    document.body.append(hidden)
  })
  expect(await renderedFonts(page)).toEqual([{ family: 'JetBrains Mono', custom: true }])
  await page.addStyleTag({ content: 'button { font-family: Arial !important; }' })
  await expect(page.getByRole('button')).toHaveCSS('font-family', 'Arial')
  await expect(renderedFonts(page)).rejects.toThrow(/System-font fallback/)

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
  const healthyContext = await browser.newContext({ baseURL: info.project.use.baseURL })
  try {
    const healthyPage = await healthyContext.newPage(), healthyReads: Response[] = [], healthyErrors: string[] = []
    healthyPage.on('response', response => healthyReads.push(response))
    healthyPage.on('pageerror', error => healthyErrors.push(error.message))
    await healthyPage.clock.setFixedTime(new Date('2026-09-23T12:00:00Z'))
    const healthyNativeRead = healthyPage.waitForResponse(response => new URL(response.url()).pathname.endsWith('/native-context'))
    await sessionChat(healthyPage, 'desktop', 'light')
    const healthyNative = await healthyNativeRead
    expect(healthyNative.status()).toBe(200)
    expect(await healthyNative.json()).toMatchObject({ profile: 'native', current_binding: { project_id: 'p-pharos', harness_session_id: session } })
    await expect(healthyPage.getByRole('tabpanel', { name: /^Chat/ })).toContainText('Owned session input from this panel is not available yet. Use the qualified session client.')
    await expect(healthyPage.getByText('The session’s live capabilities could not be read. Reopen the panel to try again.', { exact: true })).toHaveCount(0)
    expect(healthyReads.filter(response => response.status() >= 400).map(response => new URL(response.url()).pathname)).toEqual([])
    expect(healthyErrors).toEqual([])
  } finally { await healthyContext.close() }

})
