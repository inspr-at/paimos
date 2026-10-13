// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import vue from '@vitejs/plugin-vue'

const web = fileURLToPath(new URL('..', import.meta.url))
const owners = [
  resolve(web, 'src/components/settings/ModelsSection.vue'),
  resolve(web, 'src/components/settings/models/ModelRegistryCard.vue'),
]
const stylesheet = resolve(web, 'src/styles/models.css')

test('AEON-1276 shared Models stylesheet stays unscoped through either descriptor registration order', async () => {
  const css = readFileSync(stylesheet, 'utf8')
  for (const order of [owners, [...owners].reverse()]) {
    const plugin = vue()
    plugin.configResolved({ root: web, isProduction: true, command: 'build', build: { sourcemap: false }, css: {}, define: {}, logger: { warn() {}, warnOnce() {} } })
    const context = {
      resolve: async (src: string, importer: string) => ({ id: resolve(dirname(importer), src) }),
      addWatchFile() {},
      error(message: string) { throw new Error(message) },
    }
    const requests: string[] = []
    // Register both real SFCs before transforming their CSS. This forces the
    // former shared style-src cache overwrite without sleeps or a lucky build.
    for (const owner of order) {
      const output = await plugin.transform.handler.call(context, readFileSync(owner, 'utf8'), owner)
      const script = plugin.load.handler.call(context, `${owner}?vue&type=script&setup=true&lang.ts`)
      const imports = [...(output.code + script.code).matchAll(/import\s*['"]([^'"]*models\.css[^'"]*)['"]/g)]
      assert.equal(imports.length, 1, `${owner} must retain its shared stylesheet`)
      requests.push(resolve(dirname(owner), imports[0][1]))
    }
    for (const request of requests) {
      const output = await plugin.transform.handler.call(context, css, request)
      const compiled = output?.code ?? css
      assert.doesNotMatch(compiled, /\[data-v-/, 'global Models selectors must not acquire an SFC scope')
      assert.match(compiled, /\.models-section\s*\{/)
    }
    assert.deepEqual(requests, [stylesheet, stylesheet], 'Vite receives one shared CSS module identity')
  }
})
