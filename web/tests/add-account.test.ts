// SPDX-License-Identifier: AGPL-3.0-only
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import {
  addHarnessCommand, chosenInstall, harnessChoices, installOverrideKey, machinesForAdd, readInstallOverride, signInStep, writeInstallOverride,
  type AddMachine, type AddMachineSource, type AgentdInstall,
} from '../src/lib/addAccount.ts'
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
  assert.equal(addHarnessCommand('codex', 'homebrew'), 'env "$(brew --prefix)/bin/aeon-agentd" add-harness --harness codex')
  assert.equal(addHarnessCommand('claude', 'nix'), 'env "$HOME/.nix-profile/bin/aeon-agentd" add-harness --harness claude')
  assert.equal(addHarnessCommand('cursor', 'direct'), 'env "$HOME/.local/bin/aeon-agentd" add-harness --harness cursor')
  for (const harness of ['codex;rm', 'codex$(id)', 'codex rm', 'codex\nid', 'Codex', 'π', '', 'a'.repeat(65)]) {
    assert.equal(addHarnessCommand(harness, 'homebrew'), null, harness)
  }
  assert.equal(addHarnessCommand('a'.repeat(64), 'nix'), `env "$HOME/.nix-profile/bin/aeon-agentd" add-harness --harness ${'a'.repeat(64)}`)
  assert.match(addHarnessCommand('pi', 'homebrew')!, /^env "\$\(brew --prefix\)\/bin\/aeon-agentd"/)
})

/** Single-quoted shell literal. The stub and the fake brew embed absolute paths. */
function shQuote(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`
}

test('each generated add-harness line runs in bash, zsh, and fish against a stub', () => {
  const root = mkdtempSync(join(tmpdir(), 'aeon-add-harness-'))
  try {
    const prefix = join(root, 'prefix')
    const home = join(root, 'home')
    const bin = join(root, 'bin')
    const config = join(root, 'config')
    mkdirSync(join(prefix, 'bin'), { recursive: true })
    mkdirSync(join(home, '.nix-profile', 'bin'), { recursive: true })
    mkdirSync(join(home, '.local', 'bin'), { recursive: true })
    mkdirSync(bin, { recursive: true })
    mkdirSync(config, { recursive: true })
    writeFileSync(join(bin, 'brew'), `#!/bin/sh\n[ "$1" = "--prefix" ] || exit 1\nprintf '%s\\n' ${shQuote(prefix)}\n`)
    chmodSync(join(bin, 'brew'), 0o755)

    const cases: { install: AgentdInstall; harness: string; stub: string }[] = [
      { install: 'homebrew', harness: 'codex', stub: join(prefix, 'bin', 'aeon-agentd') },
      { install: 'nix', harness: 'claude', stub: join(home, '.nix-profile', 'bin', 'aeon-agentd') },
      { install: 'direct', harness: 'cursor', stub: join(home, '.local', 'bin', 'aeon-agentd') },
    ]
    const shells: { name: string; bin: string; args: string[] }[] = [
      { name: 'bash', bin: '/bin/bash', args: ['--noprofile', '--norc', '-c'] },
      { name: 'zsh', bin: '/bin/zsh', args: ['-f', '-c'] },
    ]
    const fish = '/Users/markus/.nix-profile/bin/fish'
    if (existsSync(fish)) shells.push({ name: 'fish', bin: fish, args: ['--no-config', '-c'] })

    const out = join(root, 'args')
    for (const item of cases) {
      writeFileSync(item.stub, `#!/bin/sh\nprintf '%s\\n' "$0" "$@" > ${shQuote(out)}\n`)
      chmodSync(item.stub, 0o755)
      const line = addHarnessCommand(item.harness, item.install)
      assert.ok(line, item.install)
      for (const shell of shells) {
        writeFileSync(out, '')
        const result = spawnSync(shell.bin, [...shell.args, line], {
          encoding: 'utf8',
          env: {
            HOME: home,
            PATH: `${bin}:/usr/bin:/bin`,
            XDG_CONFIG_HOME: config,
            XDG_DATA_HOME: join(root, 'data'),
            ZDOTDIR: config,
            TMPDIR: root,
            LANG: 'C',
          },
        })
        assert.equal(result.status, 0, `${shell.name} ${item.install} exit ${result.status}: ${result.stderr}`)
        assert.equal(result.stdout, '', `${shell.name} ${item.install} stdout`)
        assert.equal(readFileSync(out, 'utf8'), `${item.stub}\nadd-harness\n--harness\n${item.harness}\n`, `${shell.name} ${item.install}`)
      }
    }
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('only a connected computer is offered, and a revoked enrollment is free again', () => {
  const rows = machinesForAdd([
    machine({ computer_id: 'c-drain', computer_name: 'draining-box', computer_state: 'draining', enrollments: [{ harness: 'codex', state: 'connected' }] }),
    machine({ computer_id: null, computer_name: 'unnamed', computer_state: 'connected' }),
    machine({ computer_id: '', computer_name: 'blank', computer_state: 'connected' }),
    machine({ computer_id: 'c-revoked', computer_name: 'revoked-box', computer_state: 'revoked' }),
    machine({
      computer_id: 'c-live', computer_name: 'build-7', computer_state: 'connected', platform: 'darwin',
      enrollments: [
        { harness: 'codex', state: 'connected' },
        { harness: 'codex', state: 'connected' },
        { harness: 'claude', state: 'draining' },
        { harness: 'cursor', state: 'revoked' },
        { harness: 'grok', state: 'disconnected' },
      ],
    }),
  ])
  assert.deepEqual(rows, [{ id: 'c-live', name: 'build-7', harnesses: ['codex', 'claude'], install: null }])
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
  assert.deepEqual(harnessChoices([]).map(choice => choice.label), ['Codex', 'Claude', 'Grok', 'Cursor', 'Pi', 'Gemini CLI', 'OpenCode'])
  assert.deepEqual(harnessChoices(['cursor', 'codex']).map(choice => [choice.id, choice.label, choice.another]), [
    ['claude', 'Claude', false],
    ['grok', 'Grok', false],
    ['pi', 'Pi', false],
    ['gemini', 'Gemini CLI', false],
    ['opencode', 'OpenCode', false],
    ['another:codex', 'Another Codex account', true],
    ['another:cursor', 'Another Cursor account', true],
  ])
  assert.deepEqual(harnessChoices(['codex', 'claude', 'grok', 'cursor', 'pi', 'gemini', 'opencode']).map(choice => choice.label), [
    'Another Codex account', 'Another Claude account', 'Another Grok account', 'Another Cursor account', 'Another Pi account',
    'Another Gemini CLI account', 'Another OpenCode account',
  ])
})

/** Storage stand-in; a Map is what the browser's Storage holds. */
function memoryStorage() {
  const items = new Map<string, string>()
  return { items, getItem: (key: string) => items.get(key) ?? null, setItem: (key: string, value: string) => { items.set(key, value) }, removeItem: (key: string) => { items.delete(key) } }
}
const enrollCommand = (row: AddMachine, override: AgentdInstall | null = null) => addHarnessCommand('codex', chosenInstall(row, override).install)

test('each machine starts at the install its agentd reports, so the copied command matches it', () => {
  const rows = machinesForAdd([
    machine({ computer_id: 'brew', computer_name: 'brew-box', computer_state: 'connected', install_method: 'homebrew' }),
    machine({ computer_id: 'nix', computer_name: 'nix-box', computer_state: 'connected', install_method: 'nix' }),
    machine({ computer_id: 'direct', computer_name: 'plain-box', computer_state: 'connected', install_method: 'direct' }),
  ])
  assert.deepEqual(rows.map(row => [row.name, row.install]), [['brew-box', 'homebrew'], ['nix-box', 'nix'], ['plain-box', 'direct']])
  assert.deepEqual(rows.map(row => chosenInstall(row, null)), [
    { install: 'homebrew', source: 'reported' }, { install: 'nix', source: 'reported' }, { install: 'direct', source: 'reported' },
  ])
  assert.deepEqual(rows.map(row => enrollCommand(row)), [
    'env "$(brew --prefix)/bin/aeon-agentd" add-harness --harness codex',
    'env "$HOME/.nix-profile/bin/aeon-agentd" add-harness --harness codex',
    'env "$HOME/.local/bin/aeon-agentd" add-harness --harness codex',
  ])
})

test('a missing or unrecognised report stays usable through an explicit choice', () => {
  const rows = machinesForAdd([
    machine({ computer_id: 'old', computer_name: 'old-agentd', computer_state: 'connected' }),
    machine({ computer_id: 'odd', computer_name: 'odd-agentd', computer_state: 'connected', install_method: 'winget' }),
    machine({ computer_id: 'shell', computer_name: 'shell-agentd', computer_state: 'connected', install_method: '$(id)' }),
  ])
  assert.deepEqual(rows.map(row => row.install), [null, null, null])
  for (const row of rows) {
    assert.deepEqual(chosenInstall(row, null), { install: 'homebrew', source: 'unknown' }, row.name)
    assert.deepEqual(chosenInstall(row, 'nix'), { install: 'nix', source: 'chosen' }, row.name)
    assert.equal(enrollCommand(row, 'direct'), 'env "$HOME/.local/bin/aeon-agentd" add-harness --harness codex')
  }
  const reported = machinesForAdd([machine({ computer_id: 'brew', computer_name: 'brew-box', computer_state: 'connected', install_method: 'homebrew' })])[0]
  assert.deepEqual(chosenInstall(reported, 'homebrew'), { install: 'homebrew', source: 'reported' })
})

test('an override belongs to one machine and one person, and a new report replaces it', () => {
  const storage = memoryStorage()
  const [brew, nix] = machinesForAdd([
    machine({ computer_id: 'm-brew', computer_name: 'brew-box', computer_state: 'connected', install_method: 'homebrew' }),
    machine({ computer_id: 'm-nix', computer_name: 'nix-box', computer_state: 'connected', install_method: 'nix' }),
  ])
  const ada = 't-1/p-ada', bo = 't-1/p-bo', otherTenant = 't-2/p-ada'
  writeInstallOverride(storage, ada, brew, 'direct')
  assert.equal(readInstallOverride(storage, ada, brew), 'direct')
  assert.equal(enrollCommand(brew, readInstallOverride(storage, ada, brew)), 'env "$HOME/.local/bin/aeon-agentd" add-harness --harness codex')
  assert.equal(readInstallOverride(storage, ada, nix), null, 'another machine')
  assert.equal(readInstallOverride(storage, bo, brew), null, 'another person')
  assert.equal(readInstallOverride(storage, otherTenant, brew), null, 'another tenant')
  assert.deepEqual(chosenInstall(nix, readInstallOverride(storage, ada, nix)), { install: 'nix', source: 'reported' })

  // The machine now reports something else: the old correction no longer applies.
  assert.equal(readInstallOverride(storage, ada, { ...brew, install: 'nix' }), null)
  assert.equal(readInstallOverride(storage, ada, { ...brew, install: null }), null)

  // Choosing the reported install forgets the override.
  writeInstallOverride(storage, ada, brew, 'homebrew')
  assert.equal(storage.items.has(installOverrideKey(ada, brew.id)), false)
  assert.equal(readInstallOverride(storage, ada, brew), null)

  // Garbage and disabled storage read as no override.
  storage.setItem(installOverrideKey(ada, brew.id), '{"install":"rm -rf","reported":"homebrew"}')
  assert.equal(readInstallOverride(storage, ada, brew), null)
  storage.setItem(installOverrideKey(ada, brew.id), 'not json')
  assert.equal(readInstallOverride(storage, ada, brew), null)
  assert.equal(readInstallOverride({ getItem() { throw new Error('blocked') } }, ada, brew), null)
  writeInstallOverride({ setItem() { throw new Error('blocked') }, removeItem() { throw new Error('blocked') } }, ada, brew, 'nix')
  assert.equal(readInstallOverride(null, ada, brew), null)
})
