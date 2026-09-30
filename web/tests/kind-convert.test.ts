// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { blockingChildren, convertTargets, fieldsMovingToHistory, isIssueKind, parentAllows } from '../src/lib/kindConvert.ts'

describe('kind conversion', () => {
  it('offers the other configured issue kinds', () => {
    assert.deepEqual(convertTargets('ticket', ['epic', 'ticket', 'task', 'project']), ['epic', 'task'])
    assert.deepEqual(convertTargets('task', ['epic', 'ticket']), ['epic', 'ticket'])
    assert.deepEqual(convertTargets('project', ['epic', 'ticket', 'task']), [])
    assert.equal(isIssueKind('epic'), true)
    assert.equal(isIssueKind('release'), false)
    assert.deepEqual(convertTargets('story', [
      { slug: 'story', icon: 'book', field_schema: { issue_family: true } },
      { slug: 'initiative', icon: 'flag', field_schema: { issue_family: true } },
      { slug: 'release', icon: 'box', field_schema: {} },
    ]), ['initiative'])
    assert.equal(isIssueKind({ slug: 'memo', icon: 'ticket', field_schema: { issue_family: false } }), false)
    assert.equal(isIssueKind({ slug: 'chore', icon: 'ticket', field_schema: {} }), true)
    assert.deepEqual(fieldsMovingToHistory({ type: 'object', additionalProperties: false, properties: { priority: { type: 'string' } } }, { priority: 'high', scratch: 'keep' }), ['scratch'])
    assert.deepEqual(fieldsMovingToHistory({ type: 'object' }, { scratch: 'keep' }), [])
  })

  it('blocks only children the target kind does not allow', () => {
    const children = [
      { key: 'PHAROS-11', title: 'Connect', kind: 'ticket' },
      { key: 'PHAROS-13', title: 'Check', kind: 'task' },
    ]
    assert.deepEqual(blockingChildren(null, children), [])
    assert.deepEqual(blockingChildren(['task'], children).map(child => child.key), ['PHAROS-11'])
    assert.deepEqual(blockingChildren([], children).map(child => child.key), ['PHAROS-11', 'PHAROS-13'])
    assert.equal(parentAllows(null, 'epic'), true)
    assert.equal(parentAllows(['ticket', 'task'], 'epic'), false)
    assert.equal(parentAllows(['ticket', 'task'], 'task'), true)
  })
})
