// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1343: the agents access log spec died in open() because Access loaded
// every tab and every agent sheet in one HTTP/1.1 burst. These imports stay
// dynamic so that burst cannot come back. The deactivation sentence itself
// stays in tests/access.spec.ts.
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import viteConfig from '../vite.config.ts'

const section = readFileSync(new URL('../src/components/access/AccessSection.vue', import.meta.url), 'utf8')
const agents = readFileSync(new URL('../src/components/access/AgentsTab.vue', import.meta.url), 'utf8')

type Middleware = (req: { headers: Record<string, string> }, res: { setHeader(name: string, value: string): void }, next: () => void) => void

// The middlewares the connection plugin installs on the dev server in this mode.
function moduleMiddlewares(mode: string): Middleware[] {
  const config = viteConfig({ mode, command: 'serve' })
  const plugin = config.plugins.flat().find((p: { name?: string } | null) => p?.name === 'aeon-test-mode-module-connections')
  assert.ok(plugin, 'the connection plugin is registered')
  assert.equal(plugin.apply, 'serve')
  const used: Middleware[] = []
  plugin.configureServer({ middlewares: { use: (fn: Middleware) => { used.push(fn) } } })
  return used
}

function respond(middleware: Middleware, headers: Record<string, string>): { set: Record<string, string>, next: boolean } {
  const set: Record<string, string> = {}
  let next = false
  middleware({ headers }, { setHeader: (name, value) => { set[name] = value } }, () => { next = true })
  return { set, next }
}

function staticImport(source: string, file: string): boolean {
  return source.split('\n').some(line => {
    const trimmed = line.trim()
    if (!trimmed.startsWith('import ') || trimmed.startsWith('import type ')) return false
    return trimmed.includes(`'./${file}'`) || trimmed.includes(`"./${file}"`)
  })
}

test('Access loads one tab at a time, and agent sheets load when opened', () => {
  for (const file of ['PeopleTab.vue', 'InvitesTab.vue', 'RolesTab.vue', 'ProjectsTab.vue', 'AgentsTab.vue', 'AuditTab.vue', 'PersonSheet.vue', 'InviteSheet.vue']) {
    assert.equal(staticImport(section, file), false, `${file} is a static import`)
    assert.match(section, new RegExp(`import\\(['"]\\./${file}['"]\\)`))
  }
  assert.match(section, /import type \{ InvitePrefill \} from '\.\/InviteSheet\.vue'/)
  for (const file of ['KeysTable.vue', 'NewKeySheet.vue', 'NewAgentSheet.vue', 'EditKeyScopesSheet.vue', 'RolePicker.vue']) {
    assert.equal(staticImport(agents, file), false, `${file} is a static import`)
    assert.match(agents, new RegExp(`import\\(['"]\\./${file}['"]\\)`))
  }
  // The list paints these on the first frame, so they stay in the agents chunk.
  assert.equal(staticImport(agents, 'StatusChip.vue'), true)
  assert.match(agents, /import \{ problem, undoing \} from '\.\/accessText'/)
})

test('test-mode Vite closes each module response; dev mode and the HMR upgrade keep their connection', () => {
  const [close, ...extra] = moduleMiddlewares('test')
  assert.ok(close)
  assert.equal(extra.length, 0)
  assert.deepEqual(respond(close, { accept: '*/*' }), { set: { Connection: 'close' }, next: true })
  assert.deepEqual(respond(close, { upgrade: 'websocket' }), { set: {}, next: true })
  assert.deepEqual(moduleMiddlewares('development'), [])
})
