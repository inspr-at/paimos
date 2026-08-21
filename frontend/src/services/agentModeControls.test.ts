/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/api/client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
  ApiError: class ApiError extends Error { constructor(public status: number) { super('api') } },
  isSessionExpiredError: () => false,
  isStalePermissionsEpochError: () => false,
}))

import { api } from '@/api/client'
import {
  AgentModeControlTransportError,
  createControlCommand,
  issueControlGrant,
  parseControlCommand,
  parseControlGrant,
  transitionControlCommand,
} from './agentModeControls'

const GRANT_ID = '11111111-1111-4111-8111-111111111111'
const COMMAND_ID = '22222222-2222-4222-8222-222222222222'
const INPUT_ID = '33333333-3333-4333-8333-333333333333'
const KEY = '44444444-4444-4444-8444-444444444444'

function grant(overrides: Record<string, unknown> = {}) {
  return {
    grant_id: GRANT_ID,
    revision: 2,
    delivery_key: 'dlv-812',
    issue_key: 'PAI-812',
    actions: ['issue.priority.set', 'run.cancel.running', 'input.respond', 'run.pause'],
    targets: [
      { action: 'issue.priority.set' },
      { action: 'run.cancel.running', run_id: 42 },
      { action: 'input.respond', input_request_id: INPUT_ID, input_request_revision: 3, input_kind: 'choice', option_codes: ['choice_1', 'choice_2'] },
      { action: 'run.pause', run_id: 42, runtime_state: 'running', runtime_revision: 7 },
    ],
    expires_at: '2026-08-21T20:00:00Z',
    ...overrides,
  }
}

function command(overrides: Record<string, unknown> = {}) {
  return {
    command_id: COMMAND_ID,
    status_revision: 1,
    action: 'run.cancel.running',
    status: 'pending_confirmation',
    challenge_template: 'run_cancel_running',
    expires_at: '2026-08-21T20:00:00Z',
    display: { issue_key: 'PAI-812', delivery_key: 'dlv-812', run_id: 42 },
    ...overrides,
  }
}

describe('agentModeControls strict projection', () => {
  beforeEach(() => vi.clearAllMocks())

  it('parses the closed grant and replaces wire names without retaining hidden fields', () => {
    expect(parseControlGrant(grant())).toEqual({
      grantId: GRANT_ID, revision: 2, deliveryKey: 'dlv-812', issueKey: 'PAI-812',
      actions: ['issue.priority.set', 'run.cancel.running', 'input.respond', 'run.pause'],
      targets: [
        { action: 'issue.priority.set' },
        { action: 'run.cancel.running', runId: 42 },
        { action: 'input.respond', inputRequestId: INPUT_ID, inputRequestRevision: 3, inputKind: 'choice', optionCodes: ['choice_1', 'choice_2'] },
        { action: 'run.pause', runId: 42, runtimeState: 'running', runtimeRevision: 7 },
      ],
      expiresAt: '2026-08-21T20:00:00Z',
    })
  })

  it.each([
    ['extra grant field', () => grant({ actor: 'admin' })],
    ['zero revision', () => grant({ revision: 0 })],
    ['fraction revision', () => grant({ revision: 1.2 })],
    ['bad instant', () => grant({ expires_at: 'tomorrow' })],
    ['impossible RFC3339 calendar date', () => grant({ expires_at: '2026-02-31T20:00:00Z' })],
    ['target outside actions', () => grant({ actions: ['issue.priority.set'] })],
    ['extra target field', () => grant({ targets: [{ action: 'issue.priority.set', run_id: 42 }] })],
    ['null target placeholder', () => grant({ targets: [{ action: 'run.cancel.running', run_id: null }] })],
    ['approval with options', () => grant({ targets: [{ action: 'input.respond', input_request_id: INPUT_ID, input_request_revision: 1, input_kind: 'approval', option_codes: ['choice_1'] }] })],
    ['choice without options', () => grant({ targets: [{ action: 'input.respond', input_request_id: INPUT_ID, input_request_revision: 1, input_kind: 'choice' }] })],
    ['choice options out of ordinal order', () => grant({ targets: [{ action: 'input.respond', input_request_id: INPUT_ID, input_request_revision: 1, input_kind: 'choice', option_codes: ['choice_2', 'choice_1'] }] })],
  ])('rejects %s', (_label, wire) => {
    expect(() => parseControlGrant(wire())).toThrow(AgentModeControlTransportError)
  })

  it('requires approval option codes to be omitted rather than null or an empty placeholder', () => {
    const projection = parseControlGrant(grant({
      targets: [{ action: 'input.respond', input_request_id: INPUT_ID, input_request_revision: 1, input_kind: 'approval' }],
    }))
    expect(projection.targets).toEqual([{
      action: 'input.respond', inputRequestId: INPUT_ID, inputRequestRevision: 1, inputKind: 'approval',
    }])
    expect(() => parseControlGrant(grant({
      targets: [{ action: 'input.respond', input_request_id: INPUT_ID, input_request_revision: 1, input_kind: 'approval', option_codes: null }],
    }))).toThrow(AgentModeControlTransportError)
  })

  it('accepts only template/action-matched command display and omitted empty optionals', () => {
    expect(parseControlCommand(command())).toEqual({
      commandId: COMMAND_ID, statusRevision: 1, action: 'run.cancel.running',
      status: 'pending_confirmation', challengeTemplate: 'run_cancel_running',
      expiresAt: '2026-08-21T20:00:00Z',
      display: { issueKey: 'PAI-812', deliveryKey: 'dlv-812', runId: 42 }, outcome: null, reason: null,
    })
    expect(() => parseControlCommand(command({ outcome: null }))).toThrow(AgentModeControlTransportError)
    expect(() => parseControlCommand(command({ display: { delivery_key: 'dlv-812', run_id: 42 } })))
      .toThrow(AgentModeControlTransportError)
    expect(() => parseControlCommand(command({ display: { issue_key: 'pai-812', delivery_key: 'dlv-812', run_id: 42 } })))
      .toThrow(AgentModeControlTransportError)
    expect(() => parseControlCommand(command({ display: { issue_key: 'PAI-812', delivery_key: ' dlv-812', run_id: 42 } })))
      .toThrow(AgentModeControlTransportError)
    expect(() => parseControlCommand(command({ display: { issue_key: 'PAI-812', delivery_key: 'dlv-812', run_id: 42, priority: 'high' } })))
      .toThrow(AgentModeControlTransportError)
    expect(() => parseControlCommand(command({ challenge_template: 'run_cancel_queued' })))
      .toThrow(AgentModeControlTransportError)
  })

  it('sends exact paths, bodies, and one idempotency key without null placeholders', async () => {
    vi.mocked(api.post).mockResolvedValueOnce(grant() as never).mockResolvedValueOnce(command() as never).mockResolvedValueOnce(command({ status_revision: 2, status: 'accepted' }) as never)
    await issueControlGrant('dlv-812', KEY)
    await createControlCommand('dlv-812', {
      grantId: GRANT_ID, grantRevision: 2, action: 'run.cancel.running', runId: 42,
    }, KEY)
    await transitionControlCommand(COMMAND_ID, 'confirm', 1, KEY)
    expect(api.post).toHaveBeenNthCalledWith(1,
      '/agent-mode/deliveries/dlv-812/control-capability-grants', {},
      { signal: undefined, headers: { 'Idempotency-Key': KEY } })
    expect(api.post).toHaveBeenNthCalledWith(2,
      '/agent-mode/deliveries/dlv-812/control-commands',
      { grant_id: GRANT_ID, grant_revision: 2, action: 'run.cancel.running', run_id: 42 },
      { signal: undefined, headers: { 'Idempotency-Key': KEY } })
    expect(api.post).toHaveBeenNthCalledWith(3,
      `/agent-mode/control-commands/${COMMAND_ID}`,
      { operation: 'confirm', status_revision: 1 },
      { signal: undefined, headers: { 'Idempotency-Key': KEY } })
  })

  it('rejects malformed runtime mutation objects before transport', async () => {
    await expect(createControlCommand('dlv-812', {
      grantId: GRANT_ID, grantRevision: 2, action: 'run.cancel.running', runId: 0,
    }, KEY)).rejects.toBeInstanceOf(AgentModeControlTransportError)
    await expect(createControlCommand('dlv-812', {
      grantId: GRANT_ID, grantRevision: 2, action: 'input.respond', inputRequestId: INPUT_ID,
      inputRequestRevision: 1, inputResponse: 'approve', choiceOrdinal: 1,
    } as never, KEY)).rejects.toBeInstanceOf(AgentModeControlTransportError)
    await expect(transitionControlCommand(COMMAND_ID, 'retry' as never, 1, KEY))
      .rejects.toBeInstanceOf(AgentModeControlTransportError)
    expect(api.post).not.toHaveBeenCalled()
  })
})
