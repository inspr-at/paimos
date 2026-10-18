// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest'
import { compareModelVersions, profileLine } from '../src/lib/modelPrefs'
import { PREF_MODELS } from './model-prefs-fixtures'

describe('model preferences presentation', () => {
  it('uses model versions rather than profile revisions for latest and pins', () => {
    expect(profileLine(PREF_MODELS[0]!)).toEqual({ line: 'sol', version: '6.1' })
    expect(profileLine(PREF_MODELS[2]!)).toEqual({ line: 'fable', version: '5.1' })
    expect(profileLine({ ...PREF_MODELS[2]!, model: 'claude-opus' })).toEqual({ line: 'opus', version: 'alias' })
    expect(profileLine({ ...PREF_MODELS[0]!, harness: 'opencode', model: 'google/gemini-2.5-pro' })).toEqual({ line: 'gemini-pro', version: '2.5' })
    expect(compareModelVersions('5.10', '5.9')).toBe(1)
    expect(compareModelVersions('alias', '99')).toBe(1)
    expect(compareModelVersions('5.1.0', '5.1')).toBe(0)
  })
})
