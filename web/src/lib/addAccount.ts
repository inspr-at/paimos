// SPDX-License-Identifier: AGPL-3.0-only
// Steps to enroll another vendor account on a paired machine. Aeon never
// receives the vendor password. Commands come from LOGIN_COMMAND, the pairing
// guide's pi sentence, and the path-proof agentd entry points. Unknown vendors
// get no command.

import { HARNESS_NAME, LOGIN_COMMAND, POOL_ORDER } from './capacity.ts'

/** AddHarness accepts another identity on every guided harness. It rejects only the same harness and label. */
export const ANOTHER_ACCOUNT: ReadonlySet<string> = new Set(POOL_ORDER)

const HARNESS_TOKEN = /^[a-z][a-z0-9_-]{0,63}$/
/** Pairing guide sentence. Not a shell command. */
const PI_SIGN_IN = 'For pi, use /login and /model in pi first.'

export type AgentdInstall = 'homebrew' | 'nix' | 'direct'

export const AGENTD_INSTALLS: readonly { id: AgentdInstall; label: string }[] = [
  { id: 'homebrew', label: 'Homebrew' },
  { id: 'nix', label: 'Nix profile' },
  { id: 'direct', label: 'Direct download' },
]

export function isAgentdInstall(value: string): value is AgentdInstall {
  return AGENTD_INSTALLS.some(item => item.id === value)
}

export interface SignInStep {
  /** Copyable shell command, when one is already known. */
  command: string | null
  /** What the step shows. A command, the pi sentence, or a vendor with no command. */
  text: string
}

export function signInStep(harness: string): SignInStep {
  const command = LOGIN_COMMAND[harness]
  if (command) return { command, text: command }
  if (harness === 'pi') return { command: null, text: PI_SIGN_IN }
  const vendor = HARNESS_NAME[harness] ?? harness
  return { command: null, text: `Sign in with ${vendor}'s CLI.` }
}

/**
 * Path-proof `add-harness`: the binary is named by its install, so an older
 * `aeon-agentd` earlier on PATH cannot shadow it.
 * Each line starts with `env` so the path is an argument. Fish rejects a
 * command substitution in command position (`"$(brew --prefix)/…"`, exit 127);
 * `env` runs in fish ≥3.4, zsh, and bash.
 * Homebrew: `env "$(brew --prefix)/bin/aeon-agentd"`.
 * Nix: the profile entry discovery already treats as managed.
 * Direct: the checksum installer's `~/.local/bin` link.
 * A harness that is not a shell token produces no command.
 */
export function addHarnessCommand(harness: string, install: AgentdInstall): string | null {
  if (!HARNESS_TOKEN.test(harness)) return null
  const tail = `add-harness --harness ${harness}`
  switch (install) {
    case 'homebrew':
      return `env "$(brew --prefix)/bin/aeon-agentd" ${tail}`
    case 'nix':
      return `env "$HOME/.nix-profile/bin/aeon-agentd" ${tail}`
    case 'direct':
      return `env "$HOME/.local/bin/aeon-agentd" ${tail}`
  }
}

export interface AddMachineSource {
  computer_id: string | null
  computer_name: string
  computer_state: string | null
  platform: string
  /** What agentd reports; anything but a known install is unknown. */
  install_method?: string
  enrollments: readonly { harness: string; state: string }[]
}

export interface AddMachine {
  id: string
  name: string
  /** Harnesses still enrolled (connected or draining). Revoked ones are free again. */
  harnesses: readonly string[]
  /** The add-harness entry point agentd reports reaching it, or null when unknown. */
  install: AgentdInstall | null
}

export type InstallSource = 'reported' | 'chosen' | 'unknown'

/** The reported install wins until the person chooses another one for this machine. Unknown starts at Homebrew. */
export function chosenInstall(machine: AddMachine, override: AgentdInstall | null): { install: AgentdInstall; source: InstallSource } {
  if (override && override !== machine.install) return { install: override, source: 'chosen' }
  if (machine.install) return { install: machine.install, source: 'reported' }
  return { install: 'homebrew', source: 'unknown' }
}

type ReadStore = Pick<Storage, 'getItem'>
type WriteStore = Pick<Storage, 'setItem' | 'removeItem'>

/**
 * One remembered choice per signed-in person and machine. It records what the
 * machine reported when the person chose, so a later report replaces it.
 */
export function installOverrideKey(owner: string, machineId: string): string {
  return `aeon.addAccountInstall:${owner}:${machineId}`
}

export function readInstallOverride(storage: ReadStore | null, owner: string, machine: AddMachine): AgentdInstall | null {
  if (!storage || !owner) return null
  try {
    const raw = storage.getItem(installOverrideKey(owner, machine.id))
    if (!raw || raw.length > 128) return null
    const value = JSON.parse(raw) as { install?: unknown; reported?: unknown }
    if (typeof value?.install !== 'string' || !isAgentdInstall(value.install)) return null
    if (value.reported !== (machine.install ?? '')) return null
    return value.install
  } catch { return null }
}

/** Choosing the reported install forgets the override. Storage may be disabled; the choice then lasts for the open panel. */
export function writeInstallOverride(storage: WriteStore | null, owner: string, machine: AddMachine, install: AgentdInstall): void {
  if (!storage || !owner) return
  const key = installOverrideKey(owner, machine.id)
  try {
    if (install === machine.install) storage.removeItem(key)
    else storage.setItem(key, JSON.stringify({ install, reported: machine.install ?? '' }))
  } catch { /* Storage may be disabled. */ }
}

export function machinesForAdd(views: readonly AddMachineSource[]): AddMachine[] {
  const connected = views.filter(view => view.computer_state === 'connected' && !!view.computer_id)
  const counts = new Map<string, number>()
  for (const view of connected) {
    const name = view.computer_name.trim() || 'Paired machine'
    counts.set(name, (counts.get(name) ?? 0) + 1)
  }
  return connected.map(view => {
    const base = view.computer_name.trim() || 'Paired machine'
    const platform = view.platform.trim()
    const name = (counts.get(base) ?? 0) > 1 && platform ? `${base} · ${platform}` : base
    const harnesses = [...new Set(view.enrollments.filter(item => item.state === 'connected' || item.state === 'draining').map(item => item.harness))]
    const install = view.install_method && isAgentdInstall(view.install_method) ? view.install_method : null
    return { id: view.computer_id as string, name, harnesses, install }
  }).sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id))
}

export interface HarnessChoice {
  id: string
  harness: string
  another: boolean
  label: string
}

/** Missing harnesses first, then a second account of a vendor that allows one. */
export function harnessChoices(enrolled: readonly string[]): HarnessChoice[] {
  const have = new Set(enrolled)
  const missing: HarnessChoice[] = []
  const another: HarnessChoice[] = []
  for (const harness of POOL_ORDER) {
    const name = HARNESS_NAME[harness] ?? harness
    if (!have.has(harness)) missing.push({ id: harness, harness, another: false, label: name })
    else if (ANOTHER_ACCOUNT.has(harness)) another.push({ id: `another:${harness}`, harness, another: true, label: `Another ${name} account` })
  }
  return [...missing, ...another]
}
