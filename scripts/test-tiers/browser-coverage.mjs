// SPDX-License-Identifier: AGPL-3.0-only
import { coverageSource } from './browser-impact.mjs'

// The automatic fixture installs this before test-scoped contexts/pages are
// created. Unknown targets never certify a complete dependency observation.
export function captureBrowser(browser) {
  const sources = new Set(), sessions = []
  let complete = browser.browserType().name() === 'chromium' && browser.contexts().length === 0
  const add = url => {
    if (typeof url !== 'string' || url.length > 4096 || sources.size >= 20_000) { complete = false; return }
    const source = coverageSource(url)
    if (source) sources.add(source)
    if (/\/assets\/.*\.js(?:\?|$)|\/@fs\/.*\/src\//.test(url)) complete = false
  }
  const observe = async (context, page) => {
    if (sessions.length >= 64) { complete = false; return page }
    try {
      const session = await context.newCDPSession(page)
      sessions.push({ session, page })
      session.on('Debugger.scriptParsed', event => add(event.url))
      for (const event of ['worker', 'frameattached', 'popup', 'crash']) page.on(event, () => { complete = false })
      await session.send('Debugger.enable')
      await session.send('Profiler.enable')
      await session.send('Profiler.startPreciseCoverage', { callCount: true, detailed: true })
    } catch { complete = false }
    return page
  }
  const newContext = browser.newContext
  browser.newContext = async (...args) => {
    const context = await newContext.apply(browser, args)
    if (context.pages().length || context.serviceWorkers().length) complete = false
    context.on('serviceworker', () => { complete = false })
    const newPage = context.newPage
    context.newPage = async (...pageArgs) => observe(context, await newPage.apply(context, pageArgs))
    return context
  }
  return {
    async finish() {
      browser.newContext = newContext
      for (const { session, page } of sessions) {
        try {
          const { result } = await session.send('Profiler.takePreciseCoverage')
          for (const entry of result) add(entry.url)
          await session.detach()
        } catch {
          // Closed pages retain all parsed scripts. An open target losing its
          // coverage channel leaves an unknown interval and cannot certify.
          if (!page.isClosed()) complete = false
        }
      }
      return { complete, sources: [...sources].sort() }
    },
  }
}
