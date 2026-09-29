// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { addHarnessCommand, harnessChoices, machinesForAdd, signInStep, type AddMachineSource } from '../src/lib/addAccount.ts'
import { LOGIN_COMMAND } from '../src/lib/capacity.ts'

const machine = (overrides: Partial<AddMachineSource> & Pick<AddMachineSource, 'computer_id' | 'computer_name' | 'computer_state'>): AddMachineSource => ({
  platform: 'darwin', enrollments: [], ...overrides,
})

test('sign-in reuses known commands and invents none', () => {
  assert.deepEqual(signInStep('claude'), { command: 'claude /login', text: 'claude /login' })
  assert.deepEqual(signInStep('codex'), { command: 'codex login', text: 'codex login' })
  assert.deepEqual(signInStep('cursor'), { command: 'cursor-agent login', text: 'cursor-agent login' })
  assert.equal(LOGIN_COMMAND.claude, 'claude /login')
  assert.equal(LOGIN_COMMAND.grok, undefined)
  assert.equal(LOGIN_COMMAND.pi, undefined)
  assert.deepEqual(signInStep('pi'), { command: null, text: 'For pi, use /login and /model in pi first.' })
  assert.deepEqual(signInStep('grok'), { command: null, text: "Sign in with Grok's CLI." })
  assert.deepEqual(signInStep('mystery'), { command: null, text: "Sign in with mystery's CLI." })
  assert.doesNotMatch(signInStep('grok').text, /grok login/)
  assert.doesNotMatch(signInStep('pi').text, /grok login|^pi login/)
})

test('add-harness names the binary by its install and rejects a harness that is not a shell token', () => {
  assert.equal(addHarnessCommand('codex', 'homebrew'), '"$(brew --prefix)/bin/aeon-agentd" add-harness --harness codex')
  assert.equal(addHarnessCommand('claude', 'nix'), '"$HOME/.nix-profile/bin/aeon-agentd" add-harness --harness claude')
  assert.equal(addHarnessCommand('cursor', 'direct'), '"$HOME/.local/bin/aeon-agentd" add-harness --harness cursor')
  for (const harness of ['codex;rm', 'codex$(id)', 'codex rm', 'codex\nid', 'Codex', 'π', '', 'a'.repeat(65)]) {
    assert.equal(addHarnessCommand(harness, 'homebrew'), null, harness)
  }
  assert.equal(addHarnessCommand('a'.repeat(64), 'nix'), `"$HOME/.nix-profile/bin/aeon-agentd" add-harness --harness ${'a'.repeat(64)}`)
  assert.match(addHarnessCommand('pi', 'homebrew')!, /^"\$\(brew --prefix\)\/bin\/aeon-agentd"/)
})

test('only a connected computer is offered, and a revoked enrollment is free again', () => {
  const rows = machinesForAdd([
    machine({ computer_id: 'c-drain', computer_name: 'draining-box', computer_state: 'draining', enrollments: [{ harness: 'codex', state: 'connected' }] }),
    machine({ computer_id: null, computer_name: 'unnamed', computer_state: 'connected' }),
    machine({ computer_id: '', computer_name: 'blank', computer_state: 'connected' }),
    machine({ computer_id: 'c-revoked', computer_name: 'revoked-box', computer_state: 'revoked' }),
    machine({
      computer_id: 'c-live', computer_name: 'mbp2607', computer_state: 'connected', platform: 'darwin',
      enrollments: [
        { harness: 'codex', state: 'connected' },
        { harness: 'codex', state: 'connected' },
        { harness: 'claude', state: 'draining' },
        { harness: 'cursor', state: 'revoked' },
        { harness: 'grok', state: 'disconnected' },
      ],
    }),
  ])
  assert.deepEqual(rows, [{ id: 'c-live', name: 'mbp2607', harnesses: ['codex', 'claude'] }])
})

test('duplicate machine names keep the platform, and a blank name is Paired machine', () => {
  const rows = machinesForAdd([
    machine({ computer_id: 'b', computer_name: 'studio', computer_state: 'connected', platform: 'linux' }),
    machine({ computer_id: 'a', computer_name: 'studio', computer_state: 'connected', platform: 'darwin' }),
    machine({ computer_id: 'd', computer_name: '   ', computer_state: 'connected', platform: 'linux' }),
    machine({ computer_id: 'c', computer_name: '', computer_state: 'connected', platform: 'darwin' }),
    machine({ computer_id: 'e', computer_name: 'solo', computer_state: 'connected', platform: '' }),
  ])
  assert.deepEqual(rows.map(row => row.name), ['Paired machine · darwin', 'Paired machine · linux', 'solo', 'studio · darwin', 'studio · linux'])
  assert.deepEqual(rows.map(row => row.id), ['c', 'd', 'e', 'a', 'b'])
})

test('missing harnesses come first, then another account of a vendor that allows one', () => {
  assert.deepEqual(harnessChoices([]).map(choice => choice.label), ['Codex', 'Claude', 'Grok', 'Cursor', 'Pi'])
  assert.deepEqual(harnessChoices(['cursor', 'codex']).map(choice => [choice.id, choice.label, choice.another]), [
    ['claude', 'Claude', false],
    ['grok', 'Grok', false],
    ['pi', 'Pi', false],
    ['another:codex', 'Another Codex account', true],
    ['another:cursor', 'Another Cursor account', true],
  ])
  assert.deepEqual(harnessChoices(['codex', 'claude', 'grok', 'cursor', 'pi']).map(choice => choice.label), [
    'Another Codex account', 'Another Claude account', 'Another Grok account', 'Another Cursor account', 'Another Pi account',
  ])
})
