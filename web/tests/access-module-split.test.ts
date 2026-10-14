// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1343: the agents access log spec died in open() because Access loaded
// every tab and every agent sheet in one HTTP/1.1 burst. These imports stay
// dynamic so that burst cannot come back. The deactivation sentence itself
// stays in tests/access.spec.ts.
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'

const section = readFileSync(new URL('../src/components/access/AccessSection.vue', import.meta.url), 'utf8')
const agents = readFileSync(new URL('../src/components/access/AgentsTab.vue', import.meta.url), 'utf8')
const vite = readFileSync(new URL('../vite.config.ts', import.meta.url), 'utf8')

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
  // Test-mode Vite closes module responses. Dev mode keeps keep-alive.
  assert.match(vite, /if \(mode !== 'test'\) return/)
  assert.match(vite, /if \(!req\.headers\.upgrade\) res\.setHeader\('Connection', 'close'\)/)
})
