// SPDX-License-Identifier: AGPL-3.0-only
import { expect, it } from 'vitest'
import { decorativeVersionContrast, expectedMockConsole } from './ui-audit-rules'

it('P02 audit exceptions require the exact fixture, HTTP outcome and decorative accessible counterpart', () => {
  const failure = (status: number, path: string) => `Failed to load resource: the server responded with a status of ${status} (Fixture outcome) http://127.0.0.1:5175${path}`
  for (const [state, status, path, expected] of [
    ['sign in', 401, '/api/me', true],
    ['sign in', 404, '/api/version', true],
    ['sign in', 403, '/api/me', false],
    ['sign in', 500, '/api/me', false],
    ['sign in', 404, '/api/me', false],
    ['projects', 401, '/api/me', false],
    ['agents', 404, '/api/models', true],
    ['agents', 401, '/api/models', false],
    ['agents', 404, '/api/models/private', false],
    ['agents', 404, '/api/agent-accounts', false],
    ['models', 404, '/api/models', false],
  ] as const) {
    expect(expectedMockConsole(state, failure(status, path)), `${state}: ${status} ${path}`).toBe(expected)
  }
  expect(expectedMockConsole('agents', 'Uncaught TypeError http://127.0.0.1:5175/api/models')).toBe(false)
  expect(expectedMockConsole('sign in', 'Failed to load resource: status of 401 without a URL')).toBe(false)
  for (const labelled of [true, false]) for (const decorative of [true, false]) for (const rule of ['color-contrast', 'aria-required-attr']) {
    expect(decorativeVersionContrast(decorative ? '.yy[aria-hidden="true"]' : '.release-name', rule, labelled))
      .toBe(labelled && decorative && rule === 'color-contrast')
  }
})
