// SPDX-License-Identifier: AGPL-3.0-only
// AEON-1460: a ctrl/cmd press opens one tab when the row moves and the click
// never lands. A click that also lands does not open a second tab. A drag,
// a plain click and a keyboard activation do not take the press's tab.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { beginTitlePress, claimTitleClick, openTitlePress, releaseTitlePress } from '../src/lib/listTitlePress.ts'

const down = { button: 0, ctrlKey: true, metaKey: false, shiftKey: false, altKey: false, defaultPrevented: false, pointerId: 1, clientX: 10, clientY: 20, detail: 1 }

test('AEON-1460 a modifier press opens one tab when the row moves and the click never lands', () => {
  const press = beginTitlePress(down, '/p/PRJ-17/PHAROS-11')
  assert.ok(press)
  // The pointer stayed. The row may have moved under it; the click never arrives.
  assert.equal(releaseTitlePress(press, { ...down, clientX: 12, clientY: 21 }), '/p/PRJ-17/PHAROS-11')
  assert.equal(openTitlePress(press), '/p/PRJ-17/PHAROS-11')
  assert.equal(openTitlePress(press), null)
  const click = { ...down, detail: 1, defaultPrevented: false, preventDefault() { click.defaultPrevented = true } }
  assert.equal(claimTitleClick(press, click), null)
  assert.equal(click.defaultPrevented, true)

  const landing = beginTitlePress(down, '/p/PRJ-17/PHAROS-11')
  assert.ok(landing)
  assert.equal(releaseTitlePress(landing, down), '/p/PRJ-17/PHAROS-11')
  const landed = { ...down, detail: 1, defaultPrevented: false, preventDefault() { landed.defaultPrevented = true } }
  assert.equal(claimTitleClick(landing, landed), '/p/PRJ-17/PHAROS-11')
  assert.equal(landed.defaultPrevented, true)
  assert.equal(openTitlePress(landing), null)

  const drag = beginTitlePress(down, '/p/PRJ-17/PHAROS-11')
  assert.equal(releaseTitlePress(drag, { ...down, clientX: 40, clientY: 20 }), null)
  assert.equal(openTitlePress(drag), null)

  const key = { ...down, detail: 0, defaultPrevented: false, preventDefault() { key.defaultPrevented = true } }
  assert.equal(claimTitleClick(null, key), null)
  assert.equal(key.defaultPrevented, false)

  assert.equal(beginTitlePress({ ...down, ctrlKey: false }, '/p/PRJ-17/PHAROS-11'), null)
  assert.equal(beginTitlePress({ ...down, shiftKey: true }, '/p/PRJ-17/PHAROS-11'), null)
  assert.equal(beginTitlePress(down, 'https://example.test'), null)
})
