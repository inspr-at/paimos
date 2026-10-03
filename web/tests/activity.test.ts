// SPDX-License-Identifier: AGPL-3.0-only
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildTimeline, commentEditable, describeChange, parseWorkerMarker } from '../src/lib/activity.ts'
import type { ActivityItem } from '../src/lib/api.ts'

const mba = { id: 'p-1', name: 'mba' }, mira = { id: 'p-2', name: 'Mira' }
const at = (minutes: number) => new Date(Date.parse('2026-09-23T12:00:00Z') + minutes * 60_000).toISOString()
const change = (id: string, minutes: number, changes: ActivityItem['changes'], author = mba): ActivityItem => ({ id, at: at(minutes), type: 'change', author, changes })

test('the timeline runs oldest first and collapses one person’s changes within five minutes', () => {
  const items: ActivityItem[] = [
    change('5', 30, [{ field: 'status', from: 'qa', to: 'done' }]),
    { id: '4', at: at(29), type: 'comment', author: mba, body_markdown: 'Shipped.' },
    change('3', 3, [{ field: 'priority', from: 'medium', to: 'high' }]),
    change('2', 1, [{ field: 'status', from: 'backlog', to: 'in-progress' }]),
    change('1', 0, [{ field: 'status', from: 'new', to: 'backlog' }]),
    { id: '0', at: at(-60), type: 'created', author: mba },
  ]
  const timeline = buildTimeline(items)
  assert.deepEqual(timeline.map(entry => entry.kind), ['created', 'changes', 'comment', 'changes'])
  assert.deepEqual((timeline[1] as { changes: unknown }).changes, [
    { field: 'status', from: 'new', to: 'in-progress' },
    { field: 'priority', from: 'medium', to: 'high' },
  ])
})

test('changes that cancel out disappear; another author or a longer gap starts a new line', () => {
  assert.deepEqual(buildTimeline([
    change('2', 2, [{ field: 'priority', from: 'high', to: 'medium' }]),
    change('1', 0, [{ field: 'priority', from: 'medium', to: 'high' }]),
  ]), [])
  assert.equal(buildTimeline([
    change('2', 2, [{ field: 'status', from: 'backlog', to: 'qa' }], mira),
    change('1', 0, [{ field: 'status', from: 'new', to: 'backlog' }]),
  ]).length, 2)
  assert.equal(buildTimeline([
    change('2', 9, [{ field: 'status', from: 'backlog', to: 'qa' }]),
    change('1', 0, [{ field: 'status', from: 'new', to: 'backlog' }]),
  ]).length, 2)
})

test('changes read in product words', () => {
  assert.deepEqual(describeChange({ field: 'status', from: 'in-progress', to: 'qa' }), { label: 'changed status', from: 'In progress', to: 'QA' })
  assert.deepEqual(describeChange({ field: 'assignee', from: null, to: 'mba' }), { label: 'assigned it to', to: 'mba' })
  assert.deepEqual(describeChange({ field: 'priority', from: null, to: 'low' }), { label: 'changed priority', from: 'No priority', to: 'Low' })
  assert.equal(describeChange({ field: 'parent', from: 'a', to: 'b' }).label, 'moved it to another parent')
  assert.deepEqual(describeChange({ field: 'tags', from: null, to: 'process-learning' }), { label: 'added the label', to: 'process-learning' })
  assert.deepEqual(describeChange({ field: 'tags', from: 'ops', to: 'ops, process-learning' }), { label: 'changed labels', from: 'ops', to: 'ops, process-learning' })
  assert.deepEqual(describeChange({ field: 'kind', from: 'ticket', to: 'epic' }), { label: 'changed the type', from: 'Ticket', to: 'Epic' })
})

test('an automatic change keeps the job that wrote it', () => {
  const auto = { id: 'sys', name: 'System', automatic: true, job: 'learning-tagger', reason: 'method learning tagger' }
  const timeline = buildTimeline([change('1', 0, [{ field: 'tags', from: 'ops', to: 'ops, process-learning' }], auto)])
  assert.equal(timeline.length, 1)
  assert.equal(timeline[0].author.automatic, true)
  assert.equal(timeline[0].author.job, 'learning-tagger')
  assert.equal(timeline[0].author.reason, 'method learning tagger')
})

test('own comments stay editable for 15 minutes', () => {
  const now = Date.parse(at(14))
  assert.equal(commentEditable({ at: at(0), author: { id: 'p-1' } }, 'p-1', now), true)
  assert.equal(commentEditable({ at: at(0), author: { id: 'p-1' } }, 'p-1', Date.parse(at(15))), false)
  assert.equal(commentEditable({ at: at(0), author: { id: 'p-2' } }, 'p-1', now), false)
})

test('worker markers parse in their real variants; other comments stay comments', () => {
  const plain = parseWorkerMarker('I work on this — session: cursor-harbor-fleet (70648dfe-5a0c-4a6f-86f4-dab0870dde5c); role: builder; started: 2026-09-21T17:46:47.609430+00:00\n\nDelegated via Cursor CLI.')!
  assert.equal(plain.session, 'cursor-harbor-fleet')
  assert.equal(plain.sessionId, '70648dfe-5a0c-4a6f-86f4-dab0870dde5c')
  assert.equal(plain.role, 'builder')
  assert.equal(plain.started, '2026-09-21T17:46:47.609430+00:00')
  assert.equal(plain.rest, 'Delegated via Cursor CLI.')
  const model = parseWorkerMarker('I work on this — session: codex-w5-inbox via claude-code/hausv-org gauntlet (01BocDumGcga4tMjtsEBuHHN); role: builder; model: gpt-5.6-sol; started: 2026-09-03T08:25:11Z')!
  assert.equal(model.session, 'codex-w5-inbox via claude-code/hausv-org gauntlet')
  assert.deepEqual(model.extras, [{ key: 'model', value: 'gpt-5.6-sol' }])
  assert.equal(model.rest, '')
  const tail = parseWorkerMarker('I work on this — session: Camy (01a07aa8-5a0e-7042-aa28-fe463343bb9e); role: operator; started: 2026-09-11T11:49:00Z. Prepare isolated pin-only deployment.')!
  assert.equal(tail.started, '2026-09-11T11:49:00Z')
  assert.equal(tail.rest, 'Prepare isolated pin-only deployment.')
  assert.equal(parseWorkerMarker('I work on this (review only): Claude reviewer.'), null)
  assert.equal(parseWorkerMarker('Shipped and verified.'), null)
})
