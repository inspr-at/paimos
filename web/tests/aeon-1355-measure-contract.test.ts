// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire, registerHooks } from 'node:module'
import { test } from 'node:test'
import { headlessChromium } from '../playwright.policy.ts'
import { resolve as resolveWithoutPlaywright } from './aeon-1355-playwright-stub.mjs'
import {
  graphLaunchArgs, graphMeasurementFault, graphProbe, instrumentationSource, measurementPass, measurementReducedMotion,
} from '../perf/measure.mjs'

const swiftshader = ['--use-gl=angle', '--use-angle=swiftshader', '--enable-unsafe-swiftshader']
const specSource = readFileSync(new URL('../perf/baseline.spec.mjs', import.meta.url), 'utf8')
const longTask = [{ duration_ms: 80 }]

async function loadHarnessConfig() {
  const require = createRequire(import.meta.url)
  try { require.resolve('@playwright/test') }
  catch (error) {
    if (error.code !== 'MODULE_NOT_FOUND' && error.code !== 'ERR_MODULE_NOT_FOUND') throw error
    registerHooks({ resolve: resolveWithoutPlaywright })
  }
  return (await import('../perf/playwright.config.mjs')).default
}

test('AEON-1355 graph harness uses SwiftShader and allows motion', async () => {
  const config = await loadHarnessConfig()
  assert.equal(measurementReducedMotion, 'no-preference')
  assert.deepEqual(graphLaunchArgs, swiftshader)
  assert.equal(config.use.reducedMotion, 'no-preference')
  assert.deepEqual(config.use.launchOptions.args, swiftshader)
  assert.equal(config.use.launchOptions.args.includes('--disable-gpu'), false)
  assert.equal(config.use.browserName, 'chromium')
  assert.equal(config.use.headless, true)
  assert.equal('channel' in config.use, false)
  // The shared UI policy still disables the GPU. This harness must not inherit it.
  assert.deepEqual(headlessChromium.launchOptions.args, ['--disable-gpu'])
})

test('AEON-1355 time budget decides pass and long tasks fail only search and scroll', () => {
  const budgeted = [
    ['list', 1000], ['outline', 100], ['graph', 100], ['return-list', 100], ['filter', 100], ['sort', 100],
    ['hide-closed', 100], ['open-ticket', 100], ['close-ticket', 100], ['decision-desk', 100], ['agents', 100], ['header-hover', 100],
  ]
  for (const [action, budget] of budgeted) {
    assert.equal(measurementPass(action, budget, budget, longTask), true, `${action} inside budget keeps its long task`)
    assert.equal(measurementPass(action, budget, budget, []), true, action)
    assert.equal(measurementPass(action, budget + 0.1, budget, []), false, `${action} over budget`)
    assert.equal(measurementPass(action, budget + 0.1, budget, longTask), false, action)
  }
  // The review's case: a cold list inside 1 s with one long task is not a budget miss.
  assert.equal(measurementPass('list', 500, 1000, longTask), true)
  assert.equal(measurementPass('list', 1000, 1000, longTask), true)
  for (const action of ['search', 'scroll']) {
    assert.equal(measurementPass(action, 100, 100, []), true, action)
    assert.equal(measurementPass(action, 40, 100, longTask), false, `${action} long task`)
    assert.equal(measurementPass(action, 100, 100, longTask), false, action)
    assert.equal(measurementPass(action, 101, 100, []), false, action)
    assert.equal(measurementPass(action, 40, 100, undefined), false, action)
  }
  assert.equal(measurementPass('list', Number.NaN, 1000, []), false)
  assert.equal(measurementPass('list', 500, Number.NaN, []), false)
  assert.equal(measurementPass('search', Number.NaN, 100, []), false)
  assert.equal(measurementPass('outline', 80, 100, undefined), true)
})

test('AEON-1355 graph cells reject every canvas that is not 3d', () => {
  assert.equal(graphMeasurementFault('3d'), null)
  assert.equal(graphMeasurementFault('2d'), 'graph canvas data-dimension is 2d, expected 3d')
  assert.equal(graphMeasurementFault('3D'), 'graph canvas data-dimension is 3D, expected 3d')
  assert.equal(graphMeasurementFault(null), 'graph canvas data-dimension is missing, expected 3d')
  assert.equal(graphMeasurementFault(undefined), 'graph canvas data-dimension is missing, expected 3d')
  assert.equal(graphMeasurementFault(''), 'graph canvas data-dimension is missing, expected 3d')
  assert.deepEqual(graphProbe('2d', false), { reject: '2d' })
  assert.deepEqual(graphProbe('2d', true), { reject: '2d' })
  assert.deepEqual(graphProbe('', true), { reject: 'missing' })
  assert.deepEqual(graphProbe(null, true), { reject: 'missing' })
  assert.equal(graphProbe(null, false), null)
  assert.equal(graphProbe('3d', false), null)
  assert.deepEqual(graphProbe('3d', true), { ready: true })
  assert.equal(instrumentationSource.includes(graphProbe.toString()), true)
  assert.doesNotThrow(() => { new Function(instrumentationSource) })
  assert.match(instrumentationSource, /__baselineGraphDimension = probe\.reject/)
  assert.match(instrumentationSource, /if \(window\.__baselineGraphDimension\) return/)
})

test('AEON-1355 harness files keep the measurement contract wired', () => {
  assert.match(specSource, /measurementPass\(action, metrics\.usable_ms, budget, metrics\.long_tasks\)/)
  assert.match(specSource, /graphMeasurementFault\(/)
  assert.match(specSource, /reducedMotion: measurementReducedMotion/)
  assert.match(specSource, /addInitScript\(\{ content: instrumentationSource \}\)/)
  assert.match(specSource, /long_tasks: m\.tasks\.filter/)
  assert.match(specSource, /\.\.\.metrics/)
  assert.doesNotMatch(specSource, /reducedMotion:\s*'reduce'/)
  assert.doesNotMatch(specSource, /long_tasks\.length === 0/)
})
