// SPDX-License-Identifier: AGPL-3.0-only
// Used only when this worktree has no web/node_modules. Playwright's
// defineConfig returns its argument unchanged; this hook does the same so the
// harness config can be loaded without installing browsers.
export function resolve(specifier, context, nextResolve) {
  if (specifier === '@playwright/test') {
    return { url: new URL('./aeon-1355-define-config.mjs', import.meta.url).href, shortCircuit: true }
  }
  return nextResolve(specifier, context)
}
