// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { defaultStatusHelp, ruleLive, ruleCopy, statusHint } from '../src/lib/statusDefinitions.ts'
import { statusOptions, statusMeta } from '../src/lib/work.ts'
import { apiParams, clearedFilters, facetOptions, filtersFromQuery, filtersToQuery, valueLabel } from '../src/lib/ticketList.ts'
import { describeChange } from '../src/lib/activity.ts'

test('people, agents and menus share all approved states, definitions, rule defaults and exits', () => {
  const help = defaultStatusHelp()
  const states = ['new', 'backlog', 'open', 'blocked', 'in_progress', 'qa', 'done', 'delivered', 'accepted', 'cancelled', 'archived']
  assert.deepEqual(help.definitions.map(def => def.state), states)
  assert.deepEqual(statusOptions().map(option => option.value), states)
  for (const [index, def] of help.definitions.entries()) {
    assert.equal(statusMeta(def.state).order, index)
    assert.equal(def.exit, index >= 9)
    assert.ok(def.meaning && def.hint && def.set_by)
  }
  assert.equal(help.queued.is_status, false)
  assert.match(help.queued.meaning, /Blocked waits on its named blocker/)
  assert.match(help.queued.meaning, /Open or Blocked plus a place in the work queue \(AEON-522\)/)
  assert.equal(statusOptions().some(option => option.value === 'queued'), false)
  const limits = { new: 7, backlog: 90, blocked: 14, progress: 3, done: 14, publish: undefined, accept: 30 }
  for (const [key, days] of Object.entries(limits)) {
    assert.equal(help.autopilot.rules[key].days, days)
    assert.equal(ruleLive(help, key), true)
    assert.ok(ruleCopy(key).event || ruleCopy(key).before || ruleCopy(key).after)
    help.autopilot.rules[key].enabled = false
    assert.equal(ruleLive(help, key), false)
    help.autopilot.rules[key].enabled = true
    help.autopilot.effective_enabled = false
    assert.equal(ruleLive(help, key), false)
    help.autopilot.effective_enabled = true
  }
})

test('Accepted hints follow the live period, individual rule and effective project master', () => {
  const help = defaultStatusHelp()
  for (const days of [1, 45, 365]) {
    help.autopilot.rules.accept.days = days
    assert.match(statusHint(help, 'accepted'), new RegExp(`${days} ${days === 1 ? 'day' : 'days'} after delivery`))
  }
  help.autopilot.rules.accept.enabled = false
  assert.equal(statusHint(help, 'accepted'), 'Confirmed by a person or customer')
  help.autopilot.rules.accept.enabled = true; help.autopilot.effective_enabled = false
  assert.equal(statusHint(help, 'accepted'), 'Confirmed by a person or customer')
  assert.equal(defaultStatusHelp().autopilot.rules.accept.days, 30)
})

test('human-check filter preserves pending and exclusion choices in links, API and saved view state', () => {
  const filter = filtersFromQuery({ human_check: 'pending,!none,invalid', closed: '1' })
  assert.deepEqual(filter.human_check, ['pending', '!none'])
  assert.deepEqual(filtersToQuery(filter), { human_check: 'pending,!none', closed: '1' })
  assert.deepEqual(apiParams('project', filter).human_check, ['pending', '!none'])
  assert.deepEqual(clearedFilters().human_check, [])
  assert.equal(valueLabel('human_check', 'pending'), 'Needs a human check')
  assert.deepEqual(facetOptions('human_check', { pending: 3, none: 8 }).map(option => [option.value, option.count]), [['pending', 3], ['none', 8]])
  assert.equal(describeChange({ field: 'human_check', from: 'Touch ID', to: null }).label, 'completed the human check')
  assert.equal(describeChange({ field: 'human_check', from: null, to: 'Touch ID' }).label, 'added a human check')
})
