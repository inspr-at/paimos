// SPDX-License-Identifier: AGPL-3.0-only
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { defineConfig, mergeConfig } from 'vite'
import base from './vite.config.ts'

const root = import.meta.dirname
const input: Record<string, string> = {}
function files(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = join(directory, entry.name)
    return entry.isDirectory() ? files(path) : [path]
  })
}

// Browser imports and fault-injection URLs must resolve to the same modules the
// app uses, including its router, Vue and Pinia. Build their public exports as
// entries in one graph instead of mixing compiled app code with dev instances.
for (const path of files(join(root, 'tests'))) {
  const name = relative(root, path).replaceAll('\\', '/')
  if (name.endsWith('.html') || name.endsWith('-harness.ts')) input[name] = path
  if (!name.endsWith('.spec.ts')) continue
  for (const match of readFileSync(path, 'utf8').matchAll(/['"`]\/(?:src|tests)\/[^'"`\s]+\.(?:ts|vue)['"`]/g)) {
    const module = match[0].slice(2, -1)
    input[module] = join(root, module)
  }
}
const vueEntry = '\0playwright-vue-exports'
input['node_modules/.vite/deps/vue.js'] = vueEntry

export default mergeConfig(base, defineConfig({
  plugins: [{
    name: 'playwright-vue-exports',
    resolveId(id) { if (id === vueEntry) return id },
    load(id) { if (id === vueEntry) return "export * from 'vue'" },
  }, {
    name: 'playwright-source-urls',
    generateBundle: {
      order: 'post',
      handler(_options, bundle) {
        // Keep .js output names for Vite's manifest, which otherwise confuses
        // extracted CSS with entries. Browser-facing URLs retain source names.
        const urls = (body: string) => body.replace(/\.(ts|vue)\.js(?=["'`])/g, '.$1')
        for (const output of Object.values(bundle)) {
          if (output.type === 'chunk') output.code = urls(output.code)
          else if (output.fileName.endsWith('.html') && typeof output.source === 'string') {
            // Multi-entry CSS extraction otherwise puts the global reset after
            // component rules, unlike main.ts's tokens/base/App import order.
            const globalStyles: string[] = []
            output.source = urls(output.source).replace(/<link[^>]+rel="stylesheet"[^>]+href="\/assets\/(?:tokens|base)-[^>]+>/g, link => {
              globalStyles.push(link)
              return ''
            })
            output.source = output.source.replace('<head>', `<head>\n${globalStyles.join('\n')}`)
          }
        }
      },
    },
  }],
  build: {
    manifest: 'ui-manifest.json',
    rollupOptions: {
      input,
      preserveEntrySignatures: 'strict',
      output: {
        // Keep source URLs stable for page.route fault injection and native
        // browser imports. The test server supplies their JavaScript MIME type.
        entryFileNames: chunk => chunk.name in input && !chunk.name.endsWith('.html')
          ? chunk.name.endsWith('.js') ? chunk.name : `${chunk.name}.js` : 'assets/[name]-[hash].js',
      },
    },
  },
}))
