// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import document from '../../internal/nodes/work_icons.json' with { type: 'json' }
import { WORK_ICONS, WORK_ICON_LIST, isWorkIcon, searchWorkIcons, workIconLabel } from '../src/lib/workIcons.ts'
import { workIcon, workIconChoice } from '../src/lib/workVocabulary.ts'

// AEON-1142. Risk: the picker offers an icon the server rejects, or one AppIcon
// cannot draw (it would show the fallback compass). Go and the API enum are
// held to the same document by internal/nodes/work_icons_test.go.
test('the web offers the shared icon list, and AppIcon draws every icon in it', () => {
  assert.deepEqual(WORK_ICONS, document.icons.map(icon => icon.name))
  assert.ok(WORK_ICONS.length >= 100, `${WORK_ICONS.length} icons`)
  const source = readFileSync(new URL('../src/components/AppIcon.vue', import.meta.url), 'utf8')
  const union = source.slice(source.indexOf('export type IconName'), source.indexOf('</script>'))
  const drawn = new Set([...source.matchAll(/v-(?:else-)?if="name === '([a-z0-9-]+)'"/g)].map(match => match[1]))
  for (const name of WORK_ICONS) {
    assert.ok(union.includes(`'${name}'`), `${name} is not an IconName`)
    // compass is AppIcon's final v-else drawing.
    assert.ok(drawn.has(name) || name === 'compass', `AppIcon has no drawing for ${name}`)
  }
  // Existing PAIMOS drawings keep their names; a saved new icon draws itself.
  for (const name of ['ticket', 'epic', 'task', 'layers', 'tree', 'folder', 'check', 'box']) assert.ok(isWorkIcon(name))
  assert.equal(workIconChoice('rocket', 'layers'), 'rocket')
  assert.equal(workIconChoice('lucide-rocket', 'layers'), 'layers')
  assert.equal(workIcon({ kind_slug: 'work', is_leaf: true, level_icon: 'folder-kanban' }), 'folder-kanban')
  assert.equal(workIconLabel('flag-triangle-right'), 'Pennant')
})

test('picker search matches names first, then search words, and keeps list order on ties', () => {
  const names = (query: string) => searchWorkIcons(query).map(icon => icon.name)
  assert.deepEqual(names(''), [...WORK_ICONS])
  assert.deepEqual(names('   '), [...WORK_ICONS])
  assert.equal(names('rock')[0], 'rocket')
  assert.equal(names('ROCKET')[0], 'rocket')
  // A label word: "Pennant" is flag-triangle-right.
  assert.deepEqual(names('pennant'), ['flag-triangle-right'])
  // Name matches rank above search words, in list order among themselves.
  const checks = names('check')
  const byName = WORK_ICON_LIST.filter(icon => /check/.test(icon.name)).map(icon => icon.name)
  assert.deepEqual(checks.slice(0, byName.length), byName)
  assert.ok(checks.length > byName.length, 'search words add more matches after the names')
  // Search words find icons whose names do not say it.
  assert.ok(names('team').includes('users'))
  assert.deepEqual(names('lightning'), ['epic', 'bolt'])
  // Every word must match; separators are spaces or hyphens.
  assert.deepEqual(names('git pull'), ['git-pull-request'])
  assert.deepEqual(names('git-pull'), ['git-pull-request'])
  assert.deepEqual(names('rocket bug'), [])
  assert.deepEqual(names('zzz'), [])
  // Long input is bounded before matching.
  assert.deepEqual(names('rocket' + ' '.repeat(10_000) + 'zzz'), names('rocket'))
})
