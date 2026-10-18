// SPDX-License-Identifier: AGPL-3.0-only
// Risk: window scroll was added twice (window.scrollY and documentElement.scrollTop),
// so a control that stayed in the document looked like it moved by the scroll distance.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { contentBox } from './control-stability.ts'

const box = { x: 16, y: 180, width: 160, height: 44 }

test('a window scroll is not reported as control movement', () => {
  const before = contentBox(box, { x: 0, y: 0 }, [{ scrollLeft: 0, scrollTop: 0, root: true }])
  const scrolled = contentBox(
    { ...box, y: box.y - 40 },
    { x: 0, y: 40 },
    [{ scrollLeft: 0, scrollTop: 40, root: true }],
  )
  assert.equal(scrolled.y, before.y)
  assert.equal(scrolled.x, before.x)
  assert.deepEqual({ width: scrolled.width, height: scrolled.height }, { width: box.width, height: box.height })
})

test('a nested scroll container still counts once', () => {
  const before = contentBox(box, { x: 0, y: 0 }, [
    { scrollLeft: 0, scrollTop: 0, root: false },
    { scrollLeft: 0, scrollTop: 0, root: true },
  ])
  const nested = contentBox(
    { ...box, y: box.y - 40 },
    { x: 0, y: 0 },
    [
      { scrollLeft: 0, scrollTop: 40, root: false },
      { scrollLeft: 0, scrollTop: 0, root: true },
    ],
  )
  assert.equal(nested.y, before.y)
  assert.notEqual(contentBox({ ...box, y: box.y - 40 }, { x: 0, y: 0 }, [{ scrollLeft: 0, scrollTop: 0, root: true }]).y, before.y)
})
