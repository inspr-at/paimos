// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { accountLinkCode, validAccountLinkCode } from '../src/lib/accountLink.ts'

test('account code accepts the one-line format without URL or command parsing', () => {
  assert.equal(accountLinkCode('482 913'), '482913')
  for (const value of ['482913', '482 913', '482-913']) assert.equal(validAccountLinkCode(value), true)
  for (const value of ['', '48291', '4829131', 'https://aeon.test/link#482913', '<b>482913</b>', '482\n913']) assert.equal(validAccountLinkCode(value), false)
})
