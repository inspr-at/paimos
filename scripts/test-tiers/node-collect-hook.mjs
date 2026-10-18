// SPDX-License-Identifier: AGPL-3.0-only
import { registerHooks } from 'node:module'
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === 'node:test') return { url: new URL('./node-collect-mock.mjs', import.meta.url).href, shortCircuit: true }
    return nextResolve(specifier, context)
  },
})
