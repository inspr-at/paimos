// SPDX-License-Identifier: AGPL-3.0-only
import { spawn } from 'node:child_process'
import { appendFileSync, writeFileSync } from 'node:fs'
const [mode, ready, pidFile] = process.argv.slice(2)

if (mode === 'browser' || mode === 'browser-fail' || mode === 'browser-hang') {
  const { chromium } = await import('../../../web/node_modules/playwright/index.mjs')
  const browser = await chromium.launch({ headless: true, args: ['--disable-gpu'] })
  const page = await browser.newPage()
  await page.setContent('<h1>Isolated browser lifecycle probe</h1>')
  if (mode === 'browser-hang') {
    // Deliberately ignore termination to exercise the supervisor backstop.
    process.on('SIGTERM', () => {})
    process.on('SIGINT', () => {})
    setInterval(() => {}, 1000)
    writeFileSync(ready, 'ready')
  } else {
    writeFileSync(ready, 'ready')
    await new Promise(resolve => setTimeout(resolve, 400))
    await browser.close()
    process.exit(mode === 'browser-fail' ? 7 : 0)
  }
} else {
  if (process.env.AEON_PW_REUSE_PID) appendFileSync(process.env.AEON_PW_GROUP_LOG, `${JSON.stringify({ pid: Number(process.env.AEON_PW_REUSE_PID), started: 'Mon Jan 1 00:00:00 2001' })}\n`)
  const orphan = spawn(process.execPath, ['-e', 'process.on("SIGTERM", () => {}); process.on("SIGINT", () => {}); setInterval(() => {}, 1000)'], { stdio: 'ignore', detached: true })
  writeFileSync(pidFile, String(orphan.pid))
  // Keep unrelated child alive until this parent is terminated; normal exit
  // deliberately leaves a descendant for the supervisor to collect.
  setTimeout(() => {
    writeFileSync(ready, 'ready')
    if (mode === 'normal') process.exit(0)
    if (mode === 'fail') process.exit(7)
  }, 100)
  if (mode === 'hang') {
    process.on('SIGTERM', () => {})
    process.on('SIGINT', () => {})
  }
  setInterval(() => {}, 1000)
}
