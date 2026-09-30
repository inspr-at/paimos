// SPDX-License-Identifier: AGPL-3.0-only
import { createServer, preview } from 'vite'

const port = Number(process.argv[2])
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Expected a test server port')

// Serve the compiled test-mode app, while preserving Vite's browser-imported
// source modules and test-only HTML galleries. Plain preview loses those inputs;
// serving every application module through dev adds work to every fresh context.
const dev = await createServer({
  mode: 'test', appType: 'mpa',
  server: { middlewareMode: true, hmr: false },
})
const server = await preview({
  mode: 'test',
  preview: { host: '127.0.0.1', port, strictPort: true, proxy: dev.config.server.proxy },
  plugins: [{
    name: 'playwright-source-harnesses',
    configurePreviewServer(previewServer) {
      previewServer.middlewares.use((request, response, next) => {
        const path = new URL(request.url ?? '/', 'http://localhost').pathname
        if (/^\/(?:tests\/|src\/|@|node_modules\/)/.test(path)) dev.middlewares(request, response, next)
        else next()
      })
    },
  }],
})
async function close() {
  await Promise.all([dev.close(), new Promise(resolve => server.httpServer.close(resolve))])
  process.exit(0)
}
process.once('SIGTERM', close)
process.once('SIGINT', close)
server.printUrls()
