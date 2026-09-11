import { describe, expect, it } from 'vitest'
import {
  parseHabitatIntent,
  parseHabitatRequest,
  parseHabitatRuntimes,
  habitatAccountChoices,
  habitatChoiceId,
  type HabitatStartRequest,
} from './habitatLifecycle'
const id = (suffix: string) => `00000000-0000-4000-8000-${suffix.padStart(12, '0')}`
const request: HabitatStartRequest = {
  request_key: id('1'),
  operation: 'start',
  runtime_id: id('2'),
  runtime_generation: id('3'),
  account_label: 'chatgpt',
  ttl_seconds: 120,
  workspace_handle: id('4'),
  agent_name: 'fixture',
  dispatch_profile_id: 'fixture-profile',
  dispatch_profile_version: '1',
  ticket_id: null,
  work_shape: 'unknown',
  role: 'worker',
  parent_harness_session_id: null,
}
const runtime = {
  id: id('2'),
  project_id: 1,
  generation: id('3'),
  machine_id: 'fixture-machine',
  account_label: 'chatgpt',
  workspaces: [{ handle: id('4'), identity: 'a'.repeat(64) }],
  profiles: [{ id: 'fixture-profile', version: '1' }],
  sessions: [{ session_id: id('8'), generation: id('9') }],
  expires_at: '2026-09-06T12:00:00Z',
}
function intent() {
  return {
    schema_version: 1,
    id: id('5'),
    project_id: 1,
    request,
    state: 'requested',
    revision: 1,
    reason: '',
    created_at: '2026-09-06T11:59:00Z',
    updated_at: '2026-09-06T11:59:00Z',
    expires_at: '2026-09-06T12:01:00Z',
    new_generation: id('6'),
  }
}
describe('Habitat lifecycle response authority', () => {
  it('accepts safe workspace labels and only server-proved handles from the runtime advertisement', () => {
    const mapped = {
      ...runtime,
      workspaces: [{ ...runtime.workspaces[0], label: 'Paimos - feature work' }],
      sessions: [{ ...runtime.sessions[0], workspace_handle: id('4') }],
    }
    expect(
      parseHabitatRuntimes({ schema_version: 1, runtimes: [mapped] }, 1)[0].sessions[0]
        .workspace_handle,
    ).toBe(id('4'))
    for (const changed of [
      { ...mapped, sessions: [{ ...mapped.sessions[0], workspace_handle: id('99') }] },
      { ...mapped, workspaces: [{ ...mapped.workspaces[0], label: '/private/local/path' }] },
      { ...mapped, workspaces: [{ ...mapped.workspaces[0], label: 'a'.repeat(49) }] },
    ])
      expect(() => parseHabitatRuntimes({ schema_version: 1, runtimes: [changed] }, 1)).toThrow()
  })
  it('accepts advertised named accounts on schema 2 and rejects forged keys or class-only extras', () => {
    const named = {
      ...runtime,
      schema_version: 2,
      accounts: [
        { key: 'coordinator', label: 'Coordinator' },
        { key: 'personal', label: 'Personal' },
      ],
    }
    expect(parseHabitatRuntimes({ schema_version: 1, runtimes: [named] }, 1)[0].accounts).toEqual(
      named.accounts,
    )
    expect(
      parseHabitatRuntimes({ schema_version: 1, runtimes: [runtime] }, 1)[0].accounts,
    ).toBeUndefined()
    for (const changed of [
      { ...named, schema_version: 1 },
      { ...named, accounts: [{ key: 'chatgpt', label: 'Coordinator' }] },
      { ...named, accounts: [{ key: 'coordinator', label: 'chatgpt' }] },
      { ...named, accounts: [{ key: named.generation, label: 'Coordinator' }] },
      { ...runtime, accounts: named.accounts },
      { ...named, accounts: [named.accounts[0], named.accounts[0]] },
    ])
      expect(() => parseHabitatRuntimes({ schema_version: 1, runtimes: [changed] }, 1)).toThrow()
    const namedRequest = { ...request, account_key: 'coordinator' }
    expect(
      parseHabitatIntent({ ...intent(), schema_version: 2, request: namedRequest }, 1, namedRequest)
        .state,
    ).toBe('requested')
    expect(() =>
      parseHabitatIntent({ ...intent(), request: namedRequest }, 1, namedRequest),
    ).toThrow()
    expect(() => parseHabitatIntent({ ...intent(), schema_version: 2 }, 1, request)).toThrow()
  })
  it('accepts v3 scoped runtimes and rejects mixed-version bodies', () => {
    const scoped = {
      id: id('2'),
      project_id: 1,
      generation: id('3'),
      machine_id: 'fixture-machine',
      schema_version: 3 as const,
      workspaces: [{ handle: id('4'), identity: 'a'.repeat(64) }],
      account_scopes: [
        {
          account_label: 'chatgpt',
          accounts: [
            { key: 'codex-work', label: 'Work' },
            { key: 'codex-home', label: 'Home' },
          ],
          profiles: [
            { id: 'codex-sol-high', version: '1' },
            { id: 'codex-terra-high', version: '1' },
          ],
        },
        {
          account_label: 'cursor_context',
          accounts: [{ key: 'cursor-op', label: 'Cursor' }],
          profiles: [{ id: 'cursor-composer', version: '1' }],
        },
      ],
      sessions: [],
      expires_at: '2026-09-06T12:00:00Z',
    }
    expect(
      parseHabitatRuntimes({ schema_version: 1, runtimes: [scoped] }, 1)[0].account_scopes,
    ).toEqual(scoped.account_scopes)
    for (const changed of [
      { ...scoped, account_label: 'chatgpt' },
      { ...scoped, profiles: [{ id: 'codex-sol-high', version: '1' }] },
      {
        ...scoped,
        account_scopes: [{ ...scoped.account_scopes[0] }, { ...scoped.account_scopes[0] }],
      },
      {
        ...scoped,
        account_scopes: [
          {
            ...scoped.account_scopes[0],
            accounts: [
              { key: 'codex-work', label: 'Work' },
              { key: 'codex-work', label: 'Other' },
            ],
          },
          scoped.account_scopes[1],
        ],
      },
      {
        ...scoped,
        account_scopes: [
          { account_label: 'chatgpt', accounts: [], profiles: scoped.account_scopes[0].profiles },
        ],
      },
      {
        ...scoped,
        account_scopes: [
          {
            account_label: 'claude_ai_max',
            accounts: [{ key: 'claude-home', label: 'Claude' }],
            profiles: [{ id: 'claude-opus-xhigh', version: '1' }],
          },
        ],
      },
      {
        ...scoped,
        account_scopes: [
          { account_label: 'cursor_context', profiles: [{ id: 'cursor-composer', version: '1' }] },
        ],
      },
    ])
      expect(() => parseHabitatRuntimes({ schema_version: 1, runtimes: [changed] }, 1)).toThrow()
  })
  it('chooses class+account tuples and does not flatten mixed-harness scopes', () => {
    const scoped = parseHabitatRuntimes(
      {
        schema_version: 1,
        runtimes: [
          {
            id: id('2'),
            project_id: 1,
            generation: id('3'),
            machine_id: 'fixture-machine',
            schema_version: 3,
            workspaces: [{ handle: id('4'), identity: 'a'.repeat(64) }],
            account_scopes: [
              {
                account_label: 'chatgpt',
                accounts: [{ key: 'codex-work', label: 'Work' }],
                profiles: [{ id: 'codex-sol-high', version: '1' }],
              },
              {
                account_label: 'api_key',
                accounts: [{ key: 'codex-api', label: 'API' }],
                profiles: [{ id: 'codex-sol-high', version: '1' }],
              },
              {
                account_label: 'cursor_context',
                accounts: [{ key: 'cursor-op', label: 'Cursor' }],
                profiles: [{ id: 'cursor-composer', version: '1' }],
              },
            ],
            sessions: [],
            expires_at: '2026-09-06T12:00:00Z',
          },
        ],
      },
      1,
    )[0]
    expect(habitatAccountChoices(scoped, { id: 'codex-sol-high', version: '1' })).toEqual([
      { account_label: 'chatgpt', account_key: 'codex-work', label: 'Work' },
      { account_label: 'api_key', account_key: 'codex-api', label: 'API' },
    ])
    expect(
      habitatChoiceId({ account_label: 'chatgpt', account_key: 'codex-work', label: 'Work' }),
    ).toBe('chatgpt\0codex-work')
    expect(habitatAccountChoices(scoped, { id: 'cursor-composer', version: '1' })).toEqual([
      { account_label: 'cursor_context', account_key: 'cursor-op', label: 'Cursor' },
    ])
  })
  it('accepts lifecycle-aware choices and exposes no launch choice for explicit unavailable state', () => {
    const lifecycle = parseHabitatRuntimes(
      {
        schema_version: 1,
        runtimes: [
          {
            id: id('2'),
            project_id: 1,
            generation: id('3'),
            machine_id: 'fixture-machine',
            schema_version: 4,
            workspaces: [{ handle: id('4'), identity: 'a'.repeat(64) }],
            account_scopes: [
              {
                account_label: 'chatgpt',
                accounts: [{ key: 'codex-work', label: 'Work' }],
                profiles: [{ id: 'codex-sol-high', version: '1' }],
                attachment_revision: 7,
                account_availability: 'available',
              },
              {
                account_label: 'cursor_context',
                profiles: [{ id: 'cursor-composer', version: '1' }],
                attachment_revision: 9,
                account_availability: 'unavailable',
              },
            ],
            sessions: [],
            expires_at: '2026-09-06T12:00:00Z',
          },
        ],
      },
      1,
    )[0]
    expect(habitatAccountChoices(lifecycle)).toEqual([
      {
        account_label: 'chatgpt',
        account_key: 'codex-work',
        label: 'Work',
        attachment_revision: 7,
      },
    ])
    const reviewed = { ...request, account_key: 'codex-work', attachment_revision: 7 }
    expect(parseHabitatRequest(reviewed)).toEqual(reviewed)
    const legacyNamed = {
      ...lifecycle,
      account_scopes: [
        lifecycle.account_scopes![0],
        {
          account_label: 'cursor_context',
          accounts: [{ key: 'cursor-op', label: 'Cursor' }],
          profiles: [{ id: 'cursor-composer', version: '1' }],
        },
      ],
    }
    expect(
      parseHabitatRuntimes({ schema_version: 1, runtimes: [legacyNamed] }, 1)[0].account_scopes?.[1]
        .attachment_revision,
    ).toBeUndefined()
    for (const changed of [
      {
        ...lifecycle,
        account_scopes: [{ ...lifecycle.account_scopes![0], attachment_revision: 0 }],
      },
      {
        ...lifecycle,
        account_scopes: [
          {
            account_label: 'chatgpt',
            accounts: [{ key: 'codex-work', label: 'Work' }],
            profiles: [{ id: 'codex-sol-high', version: '1' }],
            account_availability: 'available',
          },
        ],
      },
      {
        ...lifecycle,
        account_scopes: [
          { ...lifecycle.account_scopes![1], accounts: [{ key: 'cursor-op', label: 'Cursor' }] },
        ],
      },
    ])
      expect(() => parseHabitatRuntimes({ schema_version: 1, runtimes: [changed] }, 1)).toThrow()
  })
  it('accepts scoped proof and rejects hidden fields, cross-project identities and duplicate mapping', () => {
    expect(parseHabitatRuntimes({ schema_version: 1, runtimes: [runtime] }, 1)[0].sessions).toEqual(
      runtime.sessions,
    )
    for (const changed of [
      { ...runtime, project_id: 2 },
      { ...runtime, account_label: 'unknown' },
      { ...runtime, private_field: 'fixture-canary' },
      { ...runtime, sessions: [runtime.sessions[0], runtime.sessions[0]] },
    ])
      expect(() => parseHabitatRuntimes({ schema_version: 1, runtimes: [changed] }, 1)).toThrow()
  })
  it('accepts pi_context and cursor_context runtimes and requests and still rejects unknown labels and closed keys', () => {
    const piRuntime = { ...runtime, account_label: 'pi_context' }
    expect(
      parseHabitatRuntimes({ schema_version: 1, runtimes: [piRuntime] }, 1)[0].account_label,
    ).toBe('pi_context')
    expect(parseHabitatRequest({ ...request, account_label: 'pi_context' }).account_label).toBe(
      'pi_context',
    )
    expect(
      parseHabitatIntent({ ...intent(), request: { ...request, account_label: 'pi_context' } }, 1, {
        ...request,
        account_label: 'pi_context',
      }).state,
    ).toBe('requested')
    const cursorRuntime = { ...runtime, account_label: 'cursor_context' }
    expect(
      parseHabitatRuntimes({ schema_version: 1, runtimes: [cursorRuntime] }, 1)[0].account_label,
    ).toBe('cursor_context')
    expect(parseHabitatRequest({ ...request, account_label: 'cursor_context' }).account_label).toBe(
      'cursor_context',
    )
    expect(() =>
      parseHabitatRuntimes(
        { schema_version: 1, runtimes: [{ ...runtime, account_label: 'not-a-label' }] },
        1,
      ),
    ).toThrow()
    expect(() => parseHabitatRequest({ ...request, account_label: 'not-a-label' })).toThrow()
    expect(() =>
      parseHabitatRequest({
        ...request,
        account_label: 'pi_context',
        account_key: 'pi_context',
      }),
    ).toThrow()
    expect(() =>
      parseHabitatRequest({
        ...request,
        account_label: 'cursor_context',
        account_key: 'cursor_context',
      }),
    ).toThrow()
  })
  it('preserves pending evidence while tolerating server omitted nulls', () => {
    expect(parseHabitatIntent(intent(), 1, request).state).toBe('requested')
    const { ticket_id: _ticket, parent_harness_session_id: _parent, ...withoutNulls } = request
    expect(parseHabitatIntent({ ...intent(), request: withoutNulls }, 1, request).state).toBe(
      'requested',
    )
  })
  it('refuses changed request identity, unknown reason codes and unproved completion', () => {
    for (const response of [
      { ...intent(), project_id: 2 },
      { ...intent(), request: { ...request, account_label: 'console' } },
      { ...intent(), request: { ...request, runtime_generation: id('7') } },
      { ...intent(), request: { ...request, request_key: id('7') } },
      { ...intent(), state: 'completed', reason: 'applied' },
      { ...intent(), reason: 'unexpected_reason' },
    ])
      expect(() => parseHabitatIntent(response, 1, request)).toThrow()
    expect(
      parseHabitatIntent(
        { ...intent(), state: 'completed', reason: 'applied', result_session_id: id('8') },
        1,
        request,
      ).resultSessionId,
    ).toBe(id('8'))
    expect(
      parseHabitatIntent({ ...intent(), state: 'expired', reason: 'outcome_unknown' }, 1, request)
        .state,
    ).toBe('expired')
  })
})
