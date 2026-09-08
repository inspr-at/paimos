import { describe, expect, it } from 'vitest'

import {
  isClassOnlyScope,
  runtimeChoiceProblem,
  scopedAccounts,
  scopedProfiles,
  type RuntimeChoice,
} from './projectBaselineBatch'

const mixed: RuntimeChoice = {
  runtime_id: 'rt-v3',
  runtime_generation: 'gen-v3',
  schema_version: 3,
  accounts: [],
  profiles: [],
  account_scopes: [
    {
      account_label: 'chatgpt',
      accounts: [{ key: 'codex-work', label: 'Work' }],
      profiles: [{ id: 'codex-sol-high', version: '1' }],
    },
    {
      account_label: 'cursor_context',
      accounts: [{ key: 'cursor-op', label: 'Cursor' }],
      profiles: [{ id: 'cursor-composer', version: '1' }],
    },
  ],
  workspaces: [{ handle: 'ws-a', identity: 'a'.repeat(64) }],
}

describe('projectBaselineBatch scoped choices', () => {
  it('does not flatten mixed v3 profiles across classes', () => {
    expect(scopedProfiles(mixed, 'chatgpt')).toEqual([{ id: 'codex-sol-high', version: '1' }])
    expect(scopedAccounts(mixed, 'cursor_context')).toEqual([{ key: 'cursor-op', label: 'Cursor' }])
    expect(isClassOnlyScope(mixed, 'chatgpt')).toBe(false)
  })

  it('treats omitted v3 accounts as class-only and fails closed on unknown schema', () => {
    const claude: RuntimeChoice = {
      ...mixed,
      account_scopes: [{ account_label: 'claude_ai_max', profiles: [{ id: 'claude-opus-xhigh', version: '1' }] }],
    }
    expect(isClassOnlyScope(claude, 'claude_ai_max')).toBe(true)
    expect(runtimeChoiceProblem({ ...mixed, schema_version: 9 })).toContain('Unknown runtime account schema')
    expect(runtimeChoiceProblem({ ...mixed, account_scopes: [] })).toContain('no account scopes')
  })
})
