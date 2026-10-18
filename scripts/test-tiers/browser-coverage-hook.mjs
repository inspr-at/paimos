// SPDX-License-Identifier: AGPL-3.0-only
import { registerHooks, createRequire } from 'node:module'
import { pathToFileURL } from 'node:url'
import { resolve } from 'node:path'

// Nightly only. Existing specs keep their fixture imports and assertions.
// Both ESM imports and Playwright's transformed require use this Node hook.
const fixture = new URL('./browser-coverage-fixture.mjs', import.meta.url).href
const require = createRequire(resolve(process.cwd(), 'package.json'))
const original = pathToFileURL(resolve(require.resolve('@playwright/test'), '..', 'index.mjs')).href
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === '@playwright/test' || specifier === 'playwright/test') {
      if (context.parentURL?.includes('/node_modules/')) return nextResolve(specifier, context)
      if (context.parentURL === fixture) return { url: original, shortCircuit: true }
      return { url: fixture, shortCircuit: true }
    }
    return nextResolve(specifier, context)
  },
})
