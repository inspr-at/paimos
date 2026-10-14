// SPDX-License-Identifier: AGPL-3.0-only
import { spawn, spawnSync } from 'node:child_process'
import { mkdirSync, readFileSync } from 'node:fs'
import { homedir, hostname } from 'node:os'
import { resolve, join } from 'node:path'
import { fixtureSQL } from './fixtures.mjs'

if (!/mbp2606/i.test(hostname())) throw Error('This baseline server runs only on mbp2606 through LEAD remote-spec.sh')
const port = Number(process.argv[2])
if (!Number.isInteger(port) || port < 6000 || port > 7000) throw Error('Invalid isolated port')
const artifacts = resolve('test-results', 'perf1355-server')
mkdirSync(artifacts, { recursive: true })
const psql = join(homedir(), 'ci.noindex/pg18v/bin/psql')
const database = `aeon_perf1355_${process.pid}`
const run = (command, args, options = {}) => {
  const result = spawnSync(command, args, { timeout: 600000, stdio: 'pipe', ...options })
  if (result.status !== 0) throw Error(`${command.split('/').at(-1)} failed (${result.status}); process output withheld`)
  return result.stdout?.toString() ?? ''
}
const sql = (db, input) => run(psql, ['-X', '-v', 'ON_ERROR_STOP=1', '-h', '127.0.0.1', '-p', '55433', '-U', 'aeon', '-d', db], { input })
let server, created = false, shuttingDown = false
async function cleanup(code) {
  if (shuttingDown) return
  shuttingDown = true
  if (server && server.exitCode === null) {
    const stopped = new Promise(resolve => server.once('exit', resolve))
    server.kill('SIGTERM')
    await stopped
  }
  if (created) sql('postgres', `DROP DATABASE ${database} WITH (FORCE);`)
  process.exit(code)
}
process.on('SIGTERM', () => void cleanup(0))
process.on('SIGINT', () => void cleanup(0))
try {
  // Both compilations remain inside the LEAD host guard and single browser slot.
  run('npm', ['run', 'build'])
  const binary = join(artifacts, 'paimos')
  run('nix', ['develop', '-c', 'go', 'build', '-p', '2', '-o', binary, './cmd/aeon'], {
    cwd: resolve('..'), env: { ...process.env, GOMAXPROCS: '2', GOTOOLCHAIN: 'local', GOCACHE: join(homedir(), 'ci.noindex/gocache') },
  })
  sql('postgres', `CREATE DATABASE ${database};`)
  created = true
  // Dev authentication is restricted to this disposable loopback DB. The web
  // assets and all measured handlers are the unchanged production build.
  const childEnv = { ...process.env }
  for (const key of Object.keys(childEnv)) if (key.startsWith('AEON_')) delete childEnv[key]
  server = spawn(binary, ['serve'], { stdio: ['ignore', 'ignore', 'ignore'], env: {
    ...childEnv, AEON_DATABASE_URL: `postgres://aeon@127.0.0.1:55433/${database}?sslmode=disable`,
    AEON_ENV: 'dev', AEON_ADDR: `127.0.0.1:${port}`, AEON_WEB_DIR: resolve('dist'),
    AEON_PUBLIC_URL: `http://127.0.0.1:${port}`, AEON_BOOTSTRAP_ADMIN_EMAIL: 'perf@example.invalid',
    AEON_BOOTSTRAP_TENANT_SLUG: 'perf1355', AEON_BOOTSTRAP_TENANT_NAME: 'Synthetic benchmark',
    AEON_FILES_DIR: join(artifacts, 'files'), AEON_STATUS_AUTOPILOT: 'off', GOMAXPROCS: '2',
  } })
  const deadline = Date.now() + 180000
  let started = false
  while (Date.now() < deadline && server.exitCode === null) {
    try { const response = await fetch(`http://127.0.0.1:${port}/api/health`, { signal: AbortSignal.timeout(2000) }); if (response.ok) { started = true; break } } catch {}
    await new Promise(resolve => setTimeout(resolve, 250)) // Startup poll, never a measurement barrier.
  }
  if (!started) throw Error('Disposable Aeon server did not start')
  const login = await fetch(`http://127.0.0.1:${port}/api/auth/dev-login`, {
    method: 'POST', headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ email: 'perf@example.invalid', tenant: 'perf1355' }), signal: AbortSignal.timeout(10000),
  })
  if (!login.ok) throw Error('Synthetic person bootstrap failed')
  sql(database, fixtureSQL(JSON.parse(readFileSync(new URL('./counts.json', import.meta.url), 'utf8'))))
  // Playwright probes this marker after data is ready, not the earlier health.
  process.stdout.write('AEON-1355 production assets and count-sized local DB ready\n')
  const { createServer } = await import('node:http')
  const ready = createServer((request, response) => { response.writeHead(200); response.end('ready') })
  ready.listen(port + 1000, '127.0.0.1')
  server.once('exit', () => void cleanup(1))
} catch (error) {
  process.stderr.write(`${error.message}\n`)
  await cleanup(1)
}
