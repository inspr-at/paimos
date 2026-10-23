// SPDX-License-Identifier: AGPL-3.0-only
// Risk: nightly's coverage loader changes @playwright/test resolution and can
// fail the shared font setup before a single browser test starts.
import { execFile } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { promisify } from 'node:util'
import { expect, test } from '@playwright/test'

const execute = promisify(execFile)
const web = fileURLToPath(new URL('../', import.meta.url))
const coverage = new URL('../../scripts/test-tiers/browser-coverage-hook.mjs', import.meta.url).href

test('AEON-1711: shared font setup runs PR and nightly browser cases with the font negative control', async ({}, info) => {
  test.setTimeout(120_000)
  const directory = info.outputPath('font-setup')
  mkdirSync(directory, { recursive: true })
  const config = join(directory, 'playwright.config.mjs')
  writeFileSync(config, `export default ${JSON.stringify({
    testDir: directory, testMatch: 'font-probe.spec.mjs',
    globalSetup: fileURLToPath(new URL('../../scripts/playwright-global-setup.mjs', import.meta.url)),
    workers: 1, retries: 0, forbidOnly: true,
    use: { browserName: 'chromium', headless: true, launchOptions: { args: ['--disable-gpu'] } },
  })}\n`)
  writeFileSync(join(directory, 'font-probe.spec.mjs'), `
import { test, expect } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { sampleRenderedFont, assertBodyFontEvidence } from ${JSON.stringify(new URL('../../scripts/test-prune/visual-renderer.mjs', import.meta.url).href)}
test('font setup reaches a browser and rejects a monospace body', async ({ page }) => {
  const tokens = readFileSync(new URL(${JSON.stringify(new URL('../src/styles/tokens.css', import.meta.url).href)}), 'utf8')
  await page.setContent('<style>' + tokens + '\\nbody { font-family: var(--font); }</style><p>Proportional body Wiim</p>')
  await expect(page.locator('p')).toHaveText('Proportional body Wiim')
  if (process.platform === 'linux') {
    const body = await sampleRenderedFont(page, 'body')
    expect(body.renderedFamilies).toEqual(['Inter Variable'])
    assertBodyFontEvidence(body)
    await page.addStyleTag({ content: 'body { font-family: "JetBrains Mono" !important; }' })
    const mono = await sampleRenderedFont(page, 'body')
    expect(mono.renderedFamilies).toEqual(['JetBrains Mono'])
    expect(() => assertBodyFontEvidence(mono)).toThrow(/body text must resolve to Inter sans/)
    console.log('Font negative control rejected the rendered monospace body')
  }
})
`)

  // These child CLIs inherit the outer supervisor's lock and process tracking.
  // They run serially, with no browser fixture in the outer test. A nested
  // progress reporter must not compare these cases with the outer chunk list.
  for (const lane of ['pr', 'nightly']) {
    const env = { ...process.env }
    for (const name of ['PW_TEST_REPORTER', 'AEON_PW_CHUNK_EXPECTED', 'AEON_PW_CHUNK_PLAN',
      'PLAYWRIGHT_JSON_OUTPUT_FILE', 'PLAYWRIGHT_BLOB_OUTPUT_FILE', 'PLAYWRIGHT_LAST_RUN_OUTPUT_FILE']) delete env[name]
    const preload = `--import=${coverage}`
    env.NODE_OPTIONS = (env.NODE_OPTIONS ?? '').replaceAll(preload, '') + (lane === 'nightly' ? ` ${preload}` : '')
    const args = [join(web, 'node_modules/@playwright/test/cli.js'), 'test', '--config', config,
      '--workers=1', '--retries=0', '--reporter=line', '--output', info.outputPath(lane)]
    const result = await execute(process.execPath, args, { cwd: web, env, timeout: 45_000, maxBuffer: 1024 * 1024 })
      .then(value => ({ ...value, code: 0 }), error => ({ stdout: error.stdout ?? '', stderr: error.stderr ?? '', code: error.code }))
    const output = result.stdout + result.stderr
    writeFileSync(info.outputPath(`${lane}.txt`), output)
    expect(result.code, `${lane}: ${output}`).toBe(0)
    expect(output, lane).toMatch(/1 passed/)
    if (process.platform === 'linux') {
      expect(output, lane).toContain('Browser font guard ok;')
      expect(output, lane).toContain('Font negative control rejected the rendered monospace body')
    }
    console.log(`${lane}: browser case passed${process.platform === 'linux' ? '; Inter body verified; monospace negative control rejected' : ''}`)
  }
})
