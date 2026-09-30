// SPDX-License-Identifier: AGPL-3.0-only
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { loadConfigFromFile, preview } from 'vite'

const port = Number(process.argv[2])
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Expected a test server port')
const manifest = JSON.parse(readFileSync('dist/ui-manifest.json', 'utf8'))
const entries = new Map(Object.values(manifest).filter(entry => entry.isEntry && /^(?:src|tests|node_modules)\//.test(entry.file) && !entry.file.endsWith('.html')).map(entry => [entry.file, entry]))
function styles(entry, seen = new Set()) {
  if (seen.has(entry.file)) return []
  seen.add(entry.file)
  return [...(entry.imports ?? []).flatMap(key => styles(manifest[key], seen)), ...(entry.css ?? [])]
}
const config = (await loadConfigFromFile({ command: 'serve', mode: 'test' }, 'vite.ui.config.ts')).config
const source = new Map()
for (const [file, entry] of entries) {
  // Native browser imports have no HTML entry to load their extracted CSS.
  // Load the same compiled styles before exposing the module's public exports.
  const css = [...new Set(styles(entry))].map(file => `/${file}`)
  const prelude = `await Promise.all(${JSON.stringify(css)}.map(href => new Promise((resolve, reject) => {
    let link = [...document.querySelectorAll('link[rel="stylesheet"]')].find(node => node.getAttribute('href') === href);
    if (link?.sheet) return resolve();
    if (!link) { link = document.createElement('link'); link.rel = 'stylesheet'; link.href = href; }
    link.addEventListener('load', resolve, { once: true }); link.addEventListener('error', reject, { once: true });
    if (!link.isConnected) document.head.append(link);
  })));\n`
  const path = file.replace(/\.(ts|vue)\.js$/, '.$1')
  source.set(`/${path}`, prelude + readFileSync(resolve('dist', file), 'utf8'))
}
const server = await preview({
  ...config, configFile: false, mode: 'test',
  preview: { host: '127.0.0.1', port, strictPort: true, proxy: config.server.proxy },
  plugins: [...config.plugins, {
    name: 'playwright-browser-imports',
    configurePreviewServer(previewServer) {
      previewServer.middlewares.use((request, response, next) => {
        const path = new URL(request.url ?? '/', 'http://localhost').pathname
        const body = source.get(path)
        if (body === undefined) return next()
        response.setHeader('Content-Type', 'application/javascript')
        response.end(body)
      })
    },
  }],
})
function close() { server.httpServer.close(() => process.exit(0)) }
process.once('SIGTERM', close)
process.once('SIGINT', close)
server.printUrls()
