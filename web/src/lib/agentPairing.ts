// SPDX-License-Identifier: AGPL-3.0-only
// Person pairing review for AEON-238 and AEON-239. The website looks up a code,
// approves or denies with the signed-in person, and disconnects a computer or
// one enrollment. Device, runtime and lifecycle secrets never enter this module.
// Setup, redemption and tombstone reconciliation stay on the computer.

import { parseHostCapacity, type HostCapacityView } from './hostCapacity.ts'
import { parseJson } from './json.ts'
import { api, resilientFetch } from './api.ts'
import { onAccessChange } from './authz.ts'
import { brand } from './brand.ts'

function product(): string {
  return brand.value.short_name
}

export const PUBLIC_PAIRING_GUIDE_PATH = '/agents/register-agent'
export const LOOKUP_DEBOUNCE_MS = 400
export const MIN_POLL_INTERVAL_MS = 5_000
export const MAX_REQUEST_POLL_MS = 10 * 60 * 1000
export const DEFAULT_REVIEW_CHOICE = 'one_per_harness' as const

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const DIGEST = /^[0-9a-f]{64}$/i
const USER_CODE = /^(\d{3})-(\d{3})-(\d{3})$/
const SLUG = /^[a-z0-9][a-z0-9-]{0,62}$/
const TOKEN = /^[a-z0-9][a-z0-9_-]{0,31}$/
const PREFIX = /^[A-Za-z0-9]{1,32}$/

const REQUEST_STATES = ['pending', 'approved', 'denied', 'expired', 'redeemed', 'revoked'] as const
const COMPUTER_STATES = ['connected', 'draining', 'revoked'] as const
const CLEANUP_STATES = ['pending', 'confirmed'] as const
const PROCESS_STATES = ['unconfirmed', 'drained'] as const
const VERIFICATION_MODES = ['one_per_harness', 'connect_only'] as const
const DISCONNECT_MODES = ['drain', 'revoke_now'] as const
const HARNESS_STATES = ['ready', 'blocked', 'login_required', 'checking', 'draining'] as const
export type HarnessStatus = string

export type RequestState = (typeof REQUEST_STATES)[number]
export type ComputerState = (typeof COMPUTER_STATES)[number]
export type CleanupState = (typeof CLEANUP_STATES)[number]
export type ProcessState = (typeof PROCESS_STATES)[number]
export type VerificationMode = (typeof VERIFICATION_MODES)[number]
export type DisconnectMode = (typeof DISCONNECT_MODES)[number]
export type ReviewChoice = VerificationMode
export type SetupState = 'not_started' | 'approved' | 'provisioning' | 'login_required' | 'service_conflict' | 'connected' | 'setup_failed'
export type VerificationState = 'not_selected' | 'queued' | 'starting' | 'running' | 'waiting' | 'completed' | 'failed' | 'cancelled' | 'ownership_lost' | 'expired' | 'unavailable'
export type Connectivity = 'online' | 'offline' | 'unknown'
export type AccountingState = 'settled' | 'unconfirmed'

const SETUP_STATES: readonly SetupState[] = ['not_started', 'approved', 'provisioning', 'login_required', 'service_conflict', 'connected', 'setup_failed']
const VERIFICATION_STATES: readonly VerificationState[] = ['not_selected', 'queued', 'starting', 'running', 'waiting', 'completed', 'failed', 'cancelled', 'ownership_lost', 'expired', 'unavailable']
const CONNECTIVITY: readonly Connectivity[] = ['online', 'offline', 'unknown']
const VERIFICATION_ACTIVE: readonly VerificationState[] = ['queued', 'starting', 'running', 'waiting']
const VERIFICATION_FAILED: readonly VerificationState[] = ['failed', 'cancelled', 'ownership_lost']
const PAIRING_CODE_KEY = 'aeon.pairingUserCode'

export interface RequestedAccount {
  account_key: string
  harness: string
  label: string
  model_profile_id?: string
  provider?: string
  config_home_id?: string
}

/** Server-derived. A missing entry is not a claim that verification works. */
export interface VerificationCapability {
  supported: boolean
  policy: string
  reason: string
}

export type VerificationCapabilities = Record<string, VerificationCapability>

export interface VerificationTerms {
  mode: VerificationMode | null
  policy?: string
  runs_per_account: number
  max_parallel_runs: number
  max_duration_seconds: number
  allowance: number
  unit: string
  expires_at: string
  task: string
}

export interface PairingEnrollment {
  verified_at?: string
  verification_expires_at?: string
  last_used_at?: string
  account_id: string
  account_key: string
  harness: string
  label: string
  model_profile_id: string
  state: ComputerState
  local_cleanup: CleanupState
  verification_run_id: string | null
  active_run_ids: string[]
  /** Actual run result. A verification_run_id alone is not success. */
  verification_state?: VerificationState
  verification_stalled?: boolean
  verification_expired_ready?: boolean
  can_verify?: boolean
  verification_reason?: string
  verification_error?: string | null
  /** Drained only after the computer acknowledges cleanup. Revoke does not infer it. */
  local_processes?: ProcessState
  /** Open run accounting stays unconfirmed after revoke, even when local cleanup is acknowledged. */
  accounting_state?: AccountingState
}

export type HarnessReason = string
export interface HarnessFix { kind: 'repin' | 'add_harness' | 'login' | 'restart' | 'permissions'; command: string }
export interface AccountAttention { account_id: string; reason: string; reason_detail?: string }
export interface HarnessDetail {
  state: HarnessStatus
  reason?: HarnessReason
  reason_detail?: string
  fix?: HarnessFix
  attention_accounts?: AccountAttention[]
  /** Full count of enrolled accounts that need a fix, or a lower bound when an older report stopped at five names. */
  attention_count?: number
  /** True when the list may omit a blocked account. An omitted account is not ready. */
  attention_truncated?: boolean
}

/** Public pairing projection. Secret-bearing keys are not part of this type. */
export interface PairingView {
  host_capacity?: HostCapacityView
  local_auth_pinned?: boolean
  request_id: string
  tenant_id: string
  tenant_name: string
  state: RequestState
  request_digest: string
  expires_at: string
  computer_name: string
  platform: string
  arch: string
  workspace_path: string
  capabilities: string[]
  requested_accounts: RequestedAccount[]
  verification: VerificationTerms | null
  computer_id: string | null
  computer_state: ComputerState | null
  principal_id: string | null
  daemon_id: string | null
  runtime_prefix: string | null
  local_cleanup: CleanupState
  local_processes: ProcessState
  enrollments: PairingEnrollment[]
  /** Present only when the server sends a revision the next mutation must echo. */
  revision?: number
  interval_seconds?: number
  /** Local setup report. Absent or any value other than connected does not mean the daemon is connected. */
  setup_state?: SetupState
  /** Status codes only. An absent report does not imply harness readiness. */
  harness_statuses?: Partial<Record<string, HarnessStatus>>
  harness_details?: Partial<Record<string, HarnessDetail>>
  setup_error?: string | null
  last_seen_at?: string | null
  /** Set once a person removed the computer; removed computers leave the list (AEON-402). */
  archived_at?: string
  /** Recent probe evidence. Unknown and offline do not prove that local work stopped. */
  connectivity?: Connectivity
  /** Open run accounting. Unconfirmed means revoke did not settle it. */
  accounting_state?: AccountingState
  /** Set on a pending Add harness request, before this request's computer is provisioned. */
  existing_computer_id?: string
  /** Present when this Aeon published which harnesses can run harmless verification. */
  verification_capabilities?: VerificationCapabilities
  verification_helper_version?: string
  agent_compatibility?: AgentCompatibility
  agent_release?: { version: string }
}

export interface AgentCompatibility {
  status: 'compatible' | 'update_required' | 'protocol_mismatch' | 'unknown'
  action: string
}

/** Advice is displayed only for a known incompatibility of this computer. */
export function agentUpdateAdvice(view: PairingView): string {
  const result = view.agent_compatibility
  return result?.status === 'update_required' || result?.status === 'protocol_mismatch' ? result.action : ''
}

export interface InstallTarget {
  platform: string
  arch: string
  service: string
  qualification: string
  artifact_url: string
  checksums_url: string
  command: string
}

/** Published by this Aeon's guide. Commands are displayed, never invented or run here. */
export interface PairingGuide {
  instance_url: string
  default_tenant_slug: string
  protocol: string
  platforms: string[]
  version: string
  platform_qualification: string
  setup_command: string
  homebrew_command?: string
  install_available: boolean
  install_targets: InstallTarget[]
  managed_installation?: string
  managed_setup?: ManagedSetup
  verification_capabilities?: VerificationCapabilities
  verification_helper_version?: string
}

export interface ManagedSetup {
  command: string
  service_option: string
  module_url: string
  service_note: string
  platform_note?: string
  prerequisite_note?: string
}

export interface ApproveBody {
  request_digest: string
  verification: VerificationMode
  selected_account_keys: string[]
}

export interface PairingPermissions {
  canLookup: boolean
  canApprove: boolean
  canDeny: boolean
  canDisconnect: boolean
  canListComputers: boolean
  /** Lets agents use an approved computer's accounts (capacity/approve). */
  canApproveAccounts: boolean
  /** The runtime daemon never receives person approval or force-stop authority. */
  canForceStop: false
}

export class PairingError extends Error {
  readonly status: number
  readonly code: string
  readonly retryAfterSeconds: number | null
  readonly next: string
  /** Accounts approved before a later account stopped the batch. */
  readonly savedAccountIds: readonly string[]
  constructor(status: number, message: string, options: { code?: string; retryAfterSeconds?: number | null; next?: string; savedAccountIds?: readonly string[] } = {}) {
    super(message)
    this.name = 'PairingError'
    this.status = status
    this.code = options.code ?? 'unknown'
    this.retryAfterSeconds = options.retryAfterSeconds ?? null
    this.next = options.next ?? ''
    this.savedAccountIds = options.savedAccountIds ?? []
  }
}

const flights = new Map<string, Promise<PairingView>>()
let flightBusy = false
let readEpoch = 0
const personControllers = new Set<AbortController>()

export function clearPairingClientState(): void {
  flights.clear()
  flightBusy = false
}

/** Generation of signed-in pairing reads. A reset makes older responses unusable. */
export function pairingReadGeneration(): number {
  return readEpoch
}

/** Drop in-flight person reads after an authz or session reset. The human code is not stored here. */
export function discardPairingReads(): void {
  readEpoch += 1
  clearPairingClientState()
  for (const controller of personControllers) controller.abort()
  personControllers.clear()
}

onAccessChange(change => { if (change === 'reset') discardPairingReads() })

export function pairingPermissions(input: { permissions: readonly string[]; principalKind?: string }): PairingPermissions {
  const person = input.principalKind === undefined || input.principalKind === 'person'
  const manage = person && input.permissions.includes('account.manage')
  const read = person && (manage || input.permissions.includes('account.read'))
  return {
    canLookup: manage,
    canApprove: manage,
    canDeny: manage,
    canDisconnect: manage,
    canListComputers: read,
    canApproveAccounts: manage,
    canForceStop: false,
  }
}

export function isPublicPairingGuide(path: string): boolean {
  return path === PUBLIC_PAIRING_GUIDE_PATH || path.startsWith(`${PUBLIC_PAIRING_GUIDE_PATH}/`)
}

/** Paths that must be registered before `/agents/:sessionId`, or that param captures them. */
export function agentRouteKind(path: string): 'index' | 'usage' | 'register-agent' | 'session' | 'other' {
  if (path === '/agents' || path === '/agents/') return 'index'
  if (path === '/agents/usage' || path.startsWith('/agents/usage/')) return 'usage'
  if (isPublicPairingGuide(path)) return 'register-agent'
  if (path.startsWith('/agents/')) return 'session'
  return 'other'
}

/** The signed-out freeze keeps protected drafts. The anonymous guide stays usable. */
export function sessionFreezeApplies(path: string, requiresSignIn: boolean): boolean {
  if (!requiresSignIn || path === '/signin' || isPublicPairingGuide(path)) return false
  return true
}

export function rememberPairingCode(input: string): boolean {
  const code = canonicalUserCode(input)
  if (!code) return false
  try { sessionStorage.setItem(PAIRING_CODE_KEY, code); return true } catch { return false }
}

export function peekPairingCode(): string | null {
  try {
    const stored = sessionStorage.getItem(PAIRING_CODE_KEY)
    return stored && canonicalUserCode(stored) === stored ? stored : null
  } catch { return null }
}

export function takePairingCode(): string | null {
  const code = peekPairingCode()
  try { sessionStorage.removeItem(PAIRING_CODE_KEY) } catch { /* Storage may be disabled. */ }
  return code
}

export interface GuideSection { heading: string; paragraphs: string[] }

export type InstallMethod = 'homebrew' | 'manual' | 'nix'

export interface PublicGuidePresentation {
  address: string
  steps: string[]
  /** Shown beside the code. Entering a code is not approval. */
  note: string
  manualParagraphs: string[]
  setupCommand: string
  homebrewCommand: string
  nixLabel: string
  nixHint: string
  installAvailable: boolean
  installNote: string
  targets: InstallTarget[]
  managedSetup: ManagedSetup | null
}

export const PAIRING_INSTALL_KEY = 'aeon.pairingInstall'

export function nixChoiceLabel(setup: ManagedSetup | null | undefined): string {
  const note = setup?.platform_note ?? ''
  if (note.includes('Linux only')) return 'Linux · Nix'
  if (note.includes('macOS and Linux')) return 'Nix / Home Manager'
  return 'macOS · Nix'
}

export function nixChoiceHint(setup: ManagedSetup | null | undefined): string {
  const label = nixChoiceLabel(setup)
  if (label === 'macOS · Nix') return 'Nix or Home Manager on this Mac? Choose macOS · Nix.'
  if (label === 'Linux · Nix') return 'Nix or Home Manager on this computer? Choose Linux · Nix.'
  return 'Nix or Home Manager? Choose Nix / Home Manager.'
}

export function installMethods(presented: PublicGuidePresentation): InstallMethod[] {
  const methods: InstallMethod[] = []
  if (presented.targets.length > 0 || presented.homebrewCommand) methods.push('manual')
  if (presented.homebrewCommand) methods.push('homebrew')
  if (presented.managedSetup) methods.push('nix')
  return methods
}

export function readPairingInstallMethod(storage: { getItem(key: string): string | null } | null): InstallMethod | '' {
  try {
    const value = storage?.getItem(PAIRING_INSTALL_KEY)
    if (value === 'homebrew' || value === 'manual' || value === 'nix') return value
  } catch { /* Storage may be disabled. */ }
  return ''
}

export function writePairingInstallMethod(storage: { setItem(key: string, value: string): void } | null, value: string): void {
  if (value !== 'homebrew' && value !== 'manual' && value !== 'nix') return
  try { storage?.setItem(PAIRING_INSTALL_KEY, value) } catch { /* Storage may be disabled. */ }
}

export function chooseInstallMethod(options: readonly InstallMethod[], stored: InstallMethod | ''): InstallMethod {
  if (stored && options.includes(stored)) return stored
  return options[0] ?? 'manual'
}

function unpublishedInstaller(): string {
  const name = product()
  return `This ${name} has not published a verified installer. Use an already verified setup tool for this instance, or wait until this ${name} publishes one. Do not run an installer supplied by a pairing message.`
}

/** Focused public page. Install commands stay in the manual details, never invented here. */
export function presentPublicGuide(guide: PairingGuide | null): PublicGuidePresentation {
  const address = guide ? registerAgentUrl(guide) : ''
  const platforms = guide?.platforms.length ? guide.platforms.join(', ') : ''
  const manual = [
    guide?.version ? `Published version ${guide.version}.` : '',
    platforms ? `This ${product()} publishes setup for ${platforms}.` : 'Supported computers appear here when the server publishes them.',
    guide?.platform_qualification ? `Platform note from this ${product()}: ${guide.platform_qualification}` : '',
    guide?.default_tenant_slug ? `The published workspace slug is ${guide.default_tenant_slug}.` : '',
    guide?.managed_setup ? '' : guide?.managed_installation ?? '',
    guide?.verification_capabilities?.pi ? 'For pi, use /login and /model in pi first; setup checks the configured provider without reading credential files.' : '',
    guide?.verification_helper_version ? `Verification helper published by this ${product()}: ${guide.verification_helper_version}.` : '',
    guide?.setup_command
      ? ''
      : `This ${product()} has not published a setup command. Do not run an install or setup command from another computer or from a pairing message.`,
  ].filter(Boolean)
  const publishedInstall = !!guide && guide.install_available && guide.install_targets.length > 0
  return {
    address,
    steps: [
      'Install on this computer.',
      `The computer shows a 9-digit code. It does not receive your ${product()} password, an API key, or a device secret.`,
      'Sign in, check the computer, folder and accounts, then connect the ones you want.',
    ],
    note: 'Entering the code does not grant access. A signed-in person who can manage accounts has to approve it.',
    manualParagraphs: manual,
    setupCommand: guide?.setup_command ?? '',
    homebrewCommand: guide?.homebrew_command ?? '',
    nixLabel: nixChoiceLabel(guide?.managed_setup),
    nixHint: guide?.managed_setup ? nixChoiceHint(guide.managed_setup) : '',
    installAvailable: publishedInstall,
    installNote: publishedInstall
      ? `Run only the published command for the platform you select. It comes from this ${product()}. A command in a pairing message is not an installer.`
      : unpublishedInstaller(),
    targets: guide && publishedInstall ? [...guide.install_targets] : [],
    managedSetup: guide?.managed_setup ?? null,
  }
}

/** Lead copy for the public page. Install commands stay on presentPublicGuide.targets. */
export function publicGuideSections(guide: PairingGuide | null): GuideSection[] {
  const presented = presentPublicGuide(guide)
  return [
    {
      heading: 'Connect your machine',
      paragraphs: [
        ...presented.steps,
        presented.address ? `Setting up another computer, or letting an agent do it? Share this address: ${presented.address}` : `This ${product()} has not published its address yet.`,
        presented.note,
      ],
    },
  ]
}

export function canonicalUserCode(input: string): string | null {
  const compact = input.trim().replace(/[\s-]/g, '')
  if (!/^\d{9}$/.test(compact)) return null
  const code = `${compact.slice(0, 3)}-${compact.slice(3, 6)}-${compact.slice(6)}`
  return USER_CODE.test(code) ? code : null
}

export function registerAgentUrl(guide: PairingGuide): string {
  return `${guide.instance_url}${PUBLIC_PAIRING_GUIDE_PATH}`
}

export function defaultSelectedAccountKeys(accounts: readonly RequestedAccount[]): string[] {
  const keys: string[] = []
  for (const group of groupAccounts(accounts).values()) {
    if (group.length === 1 || group.every(account => !!account.config_home_id) && new Set(group.map(account => account.config_home_id)).size === group.length) keys.push(...group.map(account => account.account_key))
  }
  return keys
}

/** Add harness is the pending request's existing computer, not a computer id guessed from an earlier row. */
export function isAddHarness(view: Pick<PairingView, 'state' | 'existing_computer_id'>): boolean {
  return view.state === 'pending' && !!view.existing_computer_id
}

/** One account for a harness, or none when the harness is left out. */
export function setHarnessAccount(accounts: readonly RequestedAccount[], selected: readonly string[], harness: string, accountKey: string | null): string[] {
  const rest = selected.filter(key => accounts.find(account => account.account_key === key)?.harness !== harness)
  if (!accountKey) return rest
  const account = accounts.find(item => item.account_key === accountKey && item.harness === harness)
  return account ? [...rest, account.account_key] : rest
}

/** Identity of the reviewed disconnect scope. A newer enrollment or revision must be confirmed again. */
export function pairingScopeKey(view: Pick<PairingView, 'revision' | 'computer_state' | 'enrollments'>): string {
  const rows = view.enrollments.map(item => `${item.account_id}:${item.state}:${[...item.active_run_ids].sort().join('+')}`).sort()
  return `${view.revision ?? 'none'}|${view.computer_state ?? 'none'}|${rows.join(',')}`
}

export function platformCaption(platform: string, arch: string): string {
  const os = platform === 'darwin' ? 'macOS' : platform === 'linux' ? 'Linux' : platform
  return arch ? `${os} · ${arch}` : os
}

export function formatAllowanceMoment(iso: string, now = Date.now()): string {
  const time = Date.parse(iso)
  if (!Number.isFinite(time)) return iso
  const date = new Date(time)
  const sameDay = new Date(now).toDateString() === date.toDateString()
  const clock = date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return sameDay ? clock : date.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export function lastActiveLabel(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return 'Not reported'
  const time = Date.parse(iso)
  if (!Number.isFinite(time)) return 'Not reported'
  const delta = now - time
  if (delta < 0) return 'Not reported'
  if (delta < 60_000) return 'Just now'
  if (delta < 3_600_000) return `${Math.floor(delta / 60_000)} min ago`
  if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)} h ago`
  return new Date(time).toLocaleDateString([], { month: 'short', day: 'numeric' })
}

export function activeRunIds(view: Pick<PairingView, 'enrollments'>): string[] {
  const ids: string[] = []
  for (const enrollment of view.enrollments) {
    for (const id of enrollment.active_run_ids) if (!ids.includes(id)) ids.push(id)
  }
  return ids
}

export function verificationWarning(terms: VerificationTerms | null): string | null {
  if (!terms) return 'The server did not include verification terms. A verification run cannot be approved until it does.'
  const expected = terms.allowance === 1 && terms.unit === 'requests' && terms.runs_per_account === 1
    && terms.max_parallel_runs === 1 && terms.max_duration_seconds === 60
  return expected ? null : 'These verification limits differ from 1 request per selected harness, 1 run at a time on that harness, and a 60 second maximum. Review the server values before connecting.'
}

function countNoun(count: number, singular: string, plural: string): string {
  return `${count} ${count === 1 ? singular : plural}`
}

/** Renders the server's terms. The cap of 1 is per selected harness, not one run for the whole computer. */
export function formatVerification(terms: VerificationTerms, now = Date.now()): string {
  const policy = terms.policy === 'read_only' ? 'Read-only. ' : ''
  const allowance = terms.unit === 'requests'
    ? countNoun(terms.allowance, 'request', 'requests')
    : `${terms.allowance} ${terms.unit}`
  const parallel = terms.max_parallel_runs === 1
    ? '1 run at a time on that harness'
    : `at most ${terms.max_parallel_runs} runs at a time on that harness`
  const runs = terms.runs_per_account === 1 && terms.max_parallel_runs === 1
    ? parallel
    : `${countNoun(terms.runs_per_account, 'run', 'runs')} on that harness, ${parallel}`
  return `${policy}${allowance} per selected harness, ${runs}, at most ${terms.max_duration_seconds} seconds, until ${formatAllowanceMoment(terms.expires_at, now)}. No repository changes or privileged actions. This is an ${product()} allowance, not the vendor subscription.`
}

export interface HarnessVerificationBlock {
  harness: string
  reason: string
}

/** Selected harnesses this Aeon cannot verify. An absent map blocks every selection; it is not success. */
export function unsupportedVerification(view: Pick<PairingView, 'verification_capabilities' | 'requested_accounts'>, keys: readonly string[]): HarnessVerificationBlock[] {
  const caps = view.verification_capabilities
  const blocked: HarnessVerificationBlock[] = []
  for (const key of keys) {
    const account = view.requested_accounts.find(item => item.account_key === key)
    if (!account || blocked.some(item => item.harness === account.harness)) continue
    const cap = caps?.[account.harness]
    if (cap?.supported) continue
    blocked.push({
      harness: account.harness,
      reason: cap?.reason?.trim() || (caps
        ? 'Verification is unavailable for this harness.'
        : `This ${product()} has not said whether this harness can be verified.`),
    })
  }
  return blocked
}

/** Keep only explicitly selected, published verifiable accounts; never add an account. */
export function verifiableAccountKeys(view: Pick<PairingView, 'verification_capabilities' | 'requested_accounts'>, keys: readonly string[]): string[] {
  return view.requested_accounts
    .filter(account => keys.includes(account.account_key) && view.verification_capabilities?.[account.harness]?.supported)
    .map(account => account.account_key)
}

export interface AddHarnessTargetProblem {
  code: 'missing' | 'mismatch' | 'name'
  message: string
  next: string
}

/** The button and its adjacent explanation share one source for every disabled state. */
export function connectDisabledReason(input: {
  busy: string
  canApprove: boolean
  selectedAccountKeys: readonly string[]
  targetProblem: AddHarnessTargetProblem | null
}): string | null {
  if (input.busy === 'approve') return 'Connecting this computer…'
  if (input.busy === 'deny') return 'Denying this request…'
  if (input.busy) return 'Wait for the current action to finish.'
  if (!input.canApprove) return 'Only a signed-in person who can manage accounts can connect or deny this computer.'
  if (!input.selectedAccountKeys.length) return 'Choose at least one harness.'
  if (input.targetProblem?.code === 'name') return 'The computer name must match the computer you opened.'
  if (input.targetProblem) return 'Use the matching code, or review this as a new computer.'
  return null
}

/** Deny is never disabled silently either (AEON-402); its reason shares Connect's line. */
export function denyDisabledReason(input: { busy: string; canDeny: boolean }): string | null {
  if (input.busy === 'deny') return 'Denying this request…'
  if (input.busy === 'approve') return 'Connecting this computer…'
  if (input.busy) return 'Wait for the current action to finish.'
  if (!input.canDeny) return 'Only a signed-in person who can manage accounts can connect or deny this computer.'
  return null
}

/** Connect stays off until the reviewed request is the opened computer, or the person starts a new-computer review. */
export function addHarnessTargetProblem(input: {
  view: Pick<PairingView, 'state' | 'existing_computer_id' | 'computer_name'>
  requestedComputerId: string
  targetName?: string | null
}): AddHarnessTargetProblem | null {
  if (!input.requestedComputerId || input.view.state !== 'pending') return null
  if (!input.view.existing_computer_id) {
    return {
      code: 'missing',
      message: 'This code starts a new computer. It does not add a harness to the computer you opened.',
      next: 'Enter the code from that computer, or review this request as a new computer.',
    }
  }
  if (input.view.existing_computer_id !== input.requestedComputerId) {
    return {
      code: 'mismatch',
      message: `This code adds a harness on ${input.view.computer_name}, not the computer you opened.`,
      next: 'Enter the matching code, or review this request as a new computer.',
    }
  }
  const target = input.targetName?.trim()
  if (target && target !== input.view.computer_name) {
    return {
      code: 'name',
      message: `The opened computer is ${target}, but this request names ${input.view.computer_name}.`,
      next: 'Connect only when the name you are granting matches the computer you opened.',
    }
  }
  return null
}

/** A failed refresh keeps the previous list. Silence is not cleanup or a disconnect. */
export function applyComputerListRefresh(
  previous: readonly PairingView[],
  result: { ok: true; computers: readonly PairingView[] } | { ok: false },
): { computers: PairingView[]; failed: boolean } {
  if (!result.ok) return { computers: [...previous], failed: true }
  return { computers: [...result.computers], failed: false }
}

export function planLookup(input: {
  raw: string
  explicit: boolean
  now: number
  typedAt: number
  inFlight: boolean
  retryAfterUntil?: number | null
  lastSentCode?: string | null
}): { action: 'send'; code: string } | { action: 'wait'; delayMs: number } | { action: 'idle' } | { action: 'blocked'; message: string; next: string } {
  if (input.inFlight) return { action: 'wait', delayMs: 0 }
  if (input.retryAfterUntil != null && input.now < input.retryAfterUntil) {
    const seconds = Math.max(1, Math.ceil((input.retryAfterUntil - input.now) / 1000))
    return {
      action: 'blocked',
      message: 'Too many attempts.',
      next: `Wait ${seconds} seconds, then look up the same code. Do not start a new pairing from this page.`,
    }
  }
  const code = canonicalUserCode(input.raw)
  if (!code) {
    if (!input.explicit && input.now - input.typedAt < LOOKUP_DEBOUNCE_MS) {
      return { action: 'wait', delayMs: LOOKUP_DEBOUNCE_MS - (input.now - input.typedAt) }
    }
    if (!input.explicit && input.raw.trim() === '') return { action: 'idle' }
    return {
      action: 'blocked',
      message: 'Enter the 9-digit code from the computer, with or without dashes.',
      next: 'A code match does not grant access. Review the request after you are signed in, then connect the computer.',
    }
  }
  if (!input.explicit && input.now - input.typedAt < LOOKUP_DEBOUNCE_MS) {
    return { action: 'wait', delayMs: input.typedAt + LOOKUP_DEBOUNCE_MS - input.now }
  }
  if (!input.explicit && input.lastSentCode === code) return { action: 'idle' }
  return { action: 'send', code }
}

export function planApproval(input: {
  view: PairingView
  choice: ReviewChoice
  selectedAccountKeys: readonly string[]
  permissions: PairingPermissions
  now?: number
  requestedComputerId?: string
  targetComputerName?: string | null
}): { ok: true; body: ApproveBody } | { ok: false; message: string; next: string } {
  if (!input.permissions.canApprove) {
    return { ok: false, message: 'Only a signed-in person who can manage accounts can connect a computer.', next: 'Ask a workspace admin. An agent session cannot approve this request.' }
  }
  const now = input.now ?? Date.now()
  const expired = Number.isFinite(Date.parse(input.view.expires_at)) && Date.parse(input.view.expires_at) <= now
  if (input.view.state === 'expired' || expired && input.view.state === 'pending') {
    return { ok: false, message: 'This code has expired.', next: 'Start setup again on the computer and enter the new code. This page will not renew the old request.' }
  }
  if (input.view.state === 'denied') {
    return { ok: false, message: 'This request was denied.', next: 'Start a new pairing on the computer if you still want to connect it. This page will not reverse the denial.' }
  }
  if (input.view.state === 'revoked' || input.view.computer_state === 'revoked') {
    return { ok: false, message: 'This computer is already disconnected.', next: 'Pair it again with a new approval if you want to reconnect. Old access stays revoked.' }
  }
  if (input.view.state === 'redeemed') {
    return { ok: false, message: 'This approval is already consumed.', next: 'Setup is underway or already recorded. Connecting again does not start another verification or refill its allowance.' }
  }
  const verification = input.choice === 'one_per_harness' ? 'one_per_harness' : 'connect_only'
  if (input.view.state === 'approved' && input.view.verification?.mode && input.view.verification.mode !== verification) {
    return { ok: false, message: 'This request was already approved with a different choice.', next: 'Look it up again. Changing the choice now conflicts with the approval already recorded.' }
  }
  if (input.view.state !== 'pending' && input.view.state !== 'approved') {
    return { ok: false, message: 'This request can no longer be approved.', next: 'Look it up again and review the current details.' }
  }
  const selected = orderedSelection(input.view.requested_accounts, input.selectedAccountKeys)
  if (!selected.ok) return selected
  const target = addHarnessTargetProblem({
    view: input.view,
    requestedComputerId: input.requestedComputerId ?? '',
    targetName: input.targetComputerName,
  })
  if (target) return { ok: false, message: target.message, next: target.next }
  if (verification === 'one_per_harness') {
    if (!input.view.verification) {
      return { ok: false, message: 'The server did not include verification terms.', next: 'Turn verification off, or look the code up again. This page will not invent a verification allowance.' }
    }
    const unsupported = unsupportedVerification(input.view, selected.keys)
    if (unsupported.length) {
      const names = unsupported.map(item => item.harness).join(', ')
      return {
        ok: false,
        message: `Verification is unavailable for ${names}.`,
        next: 'Turn verification off, or leave out the harnesses that cannot be verified. Nothing is granted until you do. This page does not treat every harness as verified.',
      }
    }
  }
  const body: ApproveBody = {
    request_digest: input.view.request_digest,
    verification,
    selected_account_keys: selected.keys,
  }
  return { ok: true, body }
}

export function planPoll(input: {
  startedAt: number
  now: number
  intervalSeconds?: number | null
  retryAfterSeconds?: number | null
  state?: string | null
  rateLimited?: boolean
  /** Transient read failures since the last success. The wait doubles, and stays capped. */
  failures?: number
}): { action: 'wait' | 'stop'; delayMs: number; reason: 'interval' | 'backoff' | 'retry_after' | 'lifetime' | 'terminal' } {
  if (input.state === 'denied' || input.state === 'expired' || input.state === 'revoked') {
    return { action: 'stop', delayMs: 0, reason: 'terminal' }
  }
  if (input.now - input.startedAt >= MAX_REQUEST_POLL_MS) return { action: 'stop', delayMs: 0, reason: 'lifetime' }
  if (input.rateLimited) {
    const seconds = input.retryAfterSeconds ?? MIN_POLL_INTERVAL_MS / 1000
    return { action: 'wait', delayMs: Math.max(MIN_POLL_INTERVAL_MS, seconds * 1000), reason: 'retry_after' }
  }
  const seconds = input.intervalSeconds ?? MIN_POLL_INTERVAL_MS / 1000
  const base = Math.max(MIN_POLL_INTERVAL_MS, seconds * 1000)
  const steps = Math.min(Math.max(0, input.failures ?? 0), 3)
  return { action: 'wait', delayMs: Math.min(base * 2 ** steps, 30_000), reason: steps > 0 ? 'backoff' : 'interval' }
}

// Wake hints on the existing tenant event stream. The page refetches the
// computer; the payload is not a substitute for that read.
export const PAIRING_LIVE_EVENTS = [
  'agent_pairing.approved',
  'agent_pairing.denied',
  'agent_pairing.reported',
  'agent_pairing.disconnected',
  'agent_pairing.removed',
  'agent_pairing.cleanup_acknowledged',
  'agent_pairing.verification_created',
  'agent_pairing.verification_withdrawn',
] as const

/** What the page follows: the events that name one of these belong to this pairing. */
export interface PairingEventScope {
  requestId: string | null
  computerId: string | null
  accountIds: readonly string[]
}

export function pairingEventScope(view: PairingView | null): PairingEventScope {
  return {
    requestId: view?.request_id || null,
    computerId: view?.computer_id ?? null,
    accountIds: view?.enrollments.map(item => item.account_id) ?? [],
  }
}

/** Does this stream event name the pairing in scope? Events of other pairings, and anything unreadable, do not. */
export function pairingEventMatches(data: string, scope: PairingEventScope): boolean {
  let event: unknown
  try { event = parseJson(data) } catch { return false }
  const after = event && typeof event === 'object' && 'after' in event ? event.after : undefined
  if (!after || typeof after !== 'object') return false
  const named = after as { computer_id?: unknown; request_id?: unknown; account_id?: unknown; account_ids?: unknown }
  if (scope.computerId && named.computer_id === scope.computerId) return true
  if (scope.requestId && named.request_id === scope.requestId) return true
  if (typeof named.account_id === 'string' && scope.accountIds.includes(named.account_id)) return true
  return Array.isArray(named.account_ids) && named.account_ids.some(id => typeof id === 'string' && scope.accountIds.includes(id))
}

interface PairingSource {
  onerror: ((this: EventSource, ev: Event) => unknown) | null
  addEventListener(type: string, listener: (event: MessageEvent) => void): void
  close(): void
}

export const PAIRING_EVENT_COALESCE_MS = 250

/**
 * Follow the tenant event stream in live mode (?after=latest): the page never
 * replays the log, only what happens while it is open. An event wakes the
 * caller only when it names the pairing in scope, and a burst of them wakes it
 * once. After a gap the stream cannot bridge (resumed: false past the first
 * connection) the caller is woken once to read again. Returns the unsubscribe.
 */
export function subscribePairingEvents(
  scope: () => PairingEventScope,
  wake: () => void,
  open: (url: string) => PairingSource | null = url => typeof EventSource === 'undefined' ? null : new EventSource(url),
): () => void {
  const stream = open('/api/events/stream?after=latest')
  if (!stream) return () => {}
  let timer: ReturnType<typeof setTimeout> | undefined
  let closed = false
  let connected = false
  const soon = () => {
    if (closed || timer) return
    timer = setTimeout(() => { timer = undefined; if (!closed) wake() }, PAIRING_EVENT_COALESCE_MS)
  }
  stream.addEventListener('stream.ready', event => {
    let ready: unknown
    try { ready = parseJson(event.data) } catch { return }
    // The first connection starts at the newest event, with nothing to bridge.
    if (connected && (!ready || typeof ready !== 'object' || !('resumed' in ready) || ready.resumed !== true)) soon()
    connected = true
  })
  for (const name of PAIRING_LIVE_EVENTS) stream.addEventListener(name, event => { if (pairingEventMatches(event.data, scope())) soon() })
  return () => {
    closed = true
    if (timer) clearTimeout(timer)
    timer = undefined
    stream.close()
  }
}

export interface PairingProgress {
  phase: 'enter_code' | 'review' | 'setup' | 'verify' | 'connected' | 'draining' | 'denied' | 'expired' | 'revoked'
  title: string
  detail: string
  next: string
  renewsAuthority: false
}

export function describeProgress(view: PairingView | null, now = Date.now()): PairingProgress {
  if (!view) {
    return { phase: 'enter_code', title: 'Enter the code from the computer', detail: 'The code identifies the request. It does not connect the computer.', next: 'Sign in, look up the code, and review the computer before connecting it.', renewsAuthority: false }
  }
  const expired = view.state === 'expired' || (view.state === 'pending' && Number.isFinite(Date.parse(view.expires_at)) && Date.parse(view.expires_at) <= now)
  if (expired) {
    return { phase: 'expired', title: 'This code has expired', detail: `${view.computer_name} was not connected.`, next: 'Start setup again on the computer. This page will not renew the expired request.', renewsAuthority: false }
  }
  if (view.state === 'denied') {
    return { phase: 'denied', title: 'The request was denied', detail: `${view.computer_name} was not connected.`, next: 'Start a new pairing on the computer if you still want to connect it.', renewsAuthority: false }
  }
  if (view.state === 'revoked' || view.computer_state === 'revoked') {
    const status = describeComputerStatus(view)
    return { phase: 'revoked', title: 'Access is revoked', detail: status.detail, next: status.next, renewsAuthority: false }
  }
  if (view.state === 'pending') {
    const adding = isAddHarness(view) ? ' This adds a harness on the existing computer.' : ''
    return { phase: 'review', title: 'Review this computer', detail: `${view.computer_name} · ${view.platform}/${view.arch} · ${view.workspace_path}.${adding}`, next: 'Connect the computer only after the tenant, folder, harnesses and accounts match what you expect.', renewsAuthority: false }
  }
  if (view.computer_state === 'draining' || view.enrollments.some(item => item.state === 'draining')) {
    const runs = activeRunIds(view)
    return { phase: 'draining', title: 'Finishing current work', detail: runs.length ? `${runs.length} run${runs.length === 1 ? '' : 's'} still active. New work is not accepted.` : 'New work is not accepted. No active runs were reported.', next: 'Disconnect completes after those runs finish. This page will not stop them.', renewsAuthority: false }
  }
  return setupProgress(view)
}

/** Fixed explanations only: telemetry never supplies vendor error text. */
export function verificationFailureText(enrollment: Pick<PairingEnrollment, 'verification_stalled' | 'verification_error'>): string {
  if (enrollment.verification_stalled) return 'The check has not settled after its time limit. The helper must confirm that it stopped before a new check can start.'
  switch (enrollment.verification_error) {
    case 'verification_timeout': case 'timed out': return 'The verification timed out. The helper must confirm that it stopped before a new check can start.'
    case 'reporter_unavailable': return 'The helper could not report this check. Update the helper, then use Verify again. Usage may be incomplete.'
    case 'app_server_protocol': return 'The helper could not complete the check protocol. Update the helper, then use Verify again.'
    case 'child_exit_failed': case 'turn_failed': return 'The verification process failed. Check the vendor sign-in, then use Verify again.'
    case 'vendor_limit': return 'The vendor limit stopped this check. Use Verify again after capacity is available.'
    default: return 'The verification did not succeed. The account owner can use Verify again.'
  }
}

function setupProgress(view: PairingView): PairingProgress {
  const progress = view.setup_state
  if (view.connectivity === 'offline' && progress !== 'login_required' && progress !== 'service_conflict' && progress !== 'setup_failed') {
    return { phase: 'setup', title: 'The computer is offline', detail: `${localProcessSentence(view)} Being offline does not show that work has stopped.`, next: 'Server access follows the pairing record. Accounting for runs that have not settled stays unconfirmed.', renewsAuthority: false }
  }
  if (progress === 'login_required') {
    return { phase: 'setup', title: 'Vendor sign-in is needed', detail: setupErrorText(view) || `Use that vendor’s own login on the computer. ${product()} does not take the vendor password.`, next: 'Finish the vendor sign-in, then let setup continue. This page will not approve the pairing again.', renewsAuthority: false }
  }
  if (progress === 'service_conflict' || progress === 'setup_failed') {
    return { phase: 'setup', title: progress === 'service_conflict' ? 'Setup found a conflict' : 'Setup did not finish', detail: setupErrorText(view) || 'The computer reported that setup did not finish.', next: 'Resolve it on the computer. Approving again does not replace another service or refill a verification.', renewsAuthority: false }
  }
  const stalled = view.enrollments.find(item => item.verification_stalled)
  if (stalled) return { phase: 'verify', title: 'Verification stalled', detail: verificationFailureText(stalled), next: 'Use Verify again to check whether the previous run has settled. It cannot replace unconfirmed work.', renewsAuthority: false }
  const unavailable = view.enrollments.find(item => item.verification_state === 'unavailable' || item.verification_error === 'verification_unavailable')
  if (unavailable) {
    return {
      phase: 'verify',
      title: 'Verification unavailable',
      detail: unavailable.verification_reason ? verificationRefusalText(unavailable) : verificationUnavailableDetail(unavailable.verification_error),
      next: 'The computer stays paired. Turn verification off on a new approval, or leave that harness out. This is not an installation failure.',
      renewsAuthority: false,
    }
  }
  if (view.enrollments.some(item => item.verification_state != null && (VERIFICATION_FAILED.includes(item.verification_state) || (item.verification_state === 'expired' && describeEnrollmentStatus(view, item) !== 'Ready')))) {
    const failed = view.enrollments.find(item => item.verification_error)
    return { phase: 'verify', title: 'Verification did not succeed', detail: verificationFailureText(failed ?? {}), next: 'The one-time allowance was not refilled. The account owner can use Verify again for a fresh read-only check.', renewsAuthority: false }
  }
  if (view.enrollments.some(item => item.verification_state != null && (VERIFICATION_ACTIVE as readonly string[]).includes(item.verification_state))) {
    return { phase: 'verify', title: 'Verification is running', detail: 'One short read-only run was approved for each selected harness. A later run on the computer is not a verification.', next: 'Ongoing work follows your account approval. This page will not start another verification.', renewsAuthority: false }
  }
  if (progress === 'provisioning' || progress === 'approved' || progress === 'not_started') {
    const consumed = view.state === 'redeemed'
    return {
      phase: 'setup',
      title: consumed ? 'Approval already consumed' : 'Setting up',
      detail: consumed
        ? 'This approval is already consumed. Setup is underway. The computer has not confirmed that setup finished.'
        : 'Approval is recorded. The computer has not confirmed that setup finished.',
      next: 'Wait for the computer. Looking the code up again does not grant a second approval.',
      renewsAuthority: false,
    }
  }
  const verificationAsked = view.verification?.mode === 'one_per_harness'
  const verificationRelevant = view.enrollments.filter(item => item.state !== 'revoked' && item.verification_state != null && item.verification_state !== 'not_selected')
  const verificationDone = !verificationAsked || (verificationRelevant.length > 0 && verificationRelevant.every(item => item.verification_state === 'completed' || (item.verification_state === 'expired' && describeEnrollmentStatus(view, item) === 'Ready')))
  if (progress === 'connected' && verificationDone && view.connectivity === 'online') {
    return { phase: 'connected', title: 'Connected', detail: verificationRelevant.some(item => item.verification_state === 'expired') ? 'Verification expired. A recent account probe is ready; the old check does not block ongoing work.' : 'The computer reported that setup finished, and a recent probe succeeded.', next: 'Add another harness from this computer, or set an ongoing allowance when you want more work.', renewsAuthority: false }
  }
  if (progress === 'connected' && verificationDone) {
    return { phase: 'setup', title: 'Setup finished', detail: 'The computer reported that setup finished. Current connectivity is unknown.', next: 'Wait for a probe before treating the daemon as online. This page will not start another verification.', renewsAuthority: false }
  }
  if (progress === 'connected') {
    return { phase: 'verify', title: 'The computer reported in', detail: 'Setup was confirmed. The verification result is not a success yet.', next: 'Wait for the verification result. A run id does not refill or repeat the allowance.', renewsAuthority: false }
  }
  if (view.state === 'redeemed') {
    return { phase: 'setup', title: 'Approval already consumed', detail: `${view.computer_name} already used this approval. Setup is underway. That does not show the daemon is connected or that setup finished.`, next: 'Wait for the computer to report setup. This page will not start another verification.', renewsAuthority: false }
  }
  if (view.state === 'approved' || view.computer_state === 'connected') {
    return { phase: 'setup', title: 'Approved', detail: `${view.computer_name} is approved. That does not show the daemon is connected or that setup finished.`, next: 'Wait for the computer to report setup. This page will not start another verification.', renewsAuthority: false }
  }
  return { phase: 'setup', title: 'Approved, waiting for the computer', detail: `${view.computer_name} can finish setup with the approval it already has.`, next: 'Keep this approval. Looking the code up again does not grant a second one.', renewsAuthority: false }
}

const SETUP_ERROR_COPY: Record<string, string> = {
  service_conflict: 'Setup found another service using this pairing.',
  unsupported_platform: 'This computer’s platform is not supported for setup.',
  managed_installation: 'This computer is managed by Nix or Home Manager. Change the owning configuration instead of overwriting it.',
  connectivity_failed: 'The computer could not confirm a connection.',
  private_storage_failed: 'Private setup storage could not be prepared.',
  installation_failed: 'The verified setup tool could not be installed.',
  verification_unavailable: 'Verification is unavailable for a selected harness. The computer stays paired.',
}

function harnessDisplayName(harness: string): string {
  return ({ claude: 'Claude', codex: 'Codex', cursor: 'Cursor', grok: 'Grok', pi: 'pi', gemini: 'Gemini CLI', opencode: 'OpenCode' } as Record<string, string>)[harness] ?? harness
}

function verificationRefusalText(item: { harness: string; verification_reason?: string }): string {
  const cause: Record<string, string> = { adapter_unsupported: 'the installed adapter cannot enforce safe verification', binding_incomplete: 'the verification binding is incomplete or unsafe', local_binding_missing: 'the approved account has no usable local binding', account_not_ready: 'the account availability check did not pass; repair the sign-in on this computer before trying again' }
  return `${harnessDisplayName(item.harness)} verification couldn't run on this computer (${cause[item.verification_reason ?? ''] ?? 'safe verification is unavailable'}). The computer is paired; re-run verification from /agents with a new approval.`
}

function verificationUnavailableDetail(error: string | null | undefined): string {
  const code = error?.trim() || ''
  if (!code || code === 'verification_unavailable' || code === 'installation_failed') {
    return 'A selected harness cannot run the harmless verification. The computer stays paired.'
  }
  return `Verification reported ${code}.`
}

function setupErrorText(view: PairingView): string {
  const code = view.setup_error?.trim() || ''
  if (!code) return ''
  if (code === 'login_required') return `Vendor sign-in is needed on the computer. ${product()} does not take the vendor password.`
  return SETUP_ERROR_COPY[code] ?? `Setup reported ${code}.`
}

function localProcessSentence(view: Pick<PairingView, 'local_processes' | 'enrollments'>): string {
  const runs = activeRunIds(view).length
  if (view.local_processes === 'drained' && runs) return 'Local processes are reported drained. Server accounting for the remaining runs is still unconfirmed.'
  if (view.local_processes === 'drained') return 'Local processes are reported drained.'
  return 'Local processes are unconfirmed.'
}

export interface ComputerStatusCopy {
  stateLabel: string
  cleanupLabel: string
  processLabel: string
  claimsProcessStopped: boolean
  detail: string
  next: string
}

type HarnessView = Pick<PairingView, 'computer_state' | 'connectivity' | 'harness_statuses' | 'harness_details'> & {
  enrollments?: PairingView['enrollments']
}

function harnessDetail(view: HarnessView, harness: string): HarnessDetail | undefined {
  const detail = view.harness_details?.[harness]
  return detail && (!view.harness_statuses?.[harness] || detail.state === view.harness_statuses[harness]) ? detail : undefined
}

// Never show a daemon-supplied command. Codes and fixes are the vocabulary of
// agentsetup.RecoveryFix, pinned for both sides by
// internal/agentsetup/testdata/harness_recovery.json. Unknown bounded codes
// from newer daemons stay visible; local paths and diagnostics never arrive.
function harnessCode(value: unknown): string | undefined {
  return typeof value === 'string' && /^[a-z][a-z0-9_]{0,63}$/.test(value) ? value : undefined
}

const PIN_REASONS = ['dependency_invalid', 'pin_missing', 'pin_partial', 'pin_drifted', 'pin_invalid', 'pin_unsafe']

// Mirror agentsetup.SafeProbeDetail: raw command output and local paths never
// become public copy, even when a newer or malformed report supplies them.
const PROBE_DETAILS = new Set([
  'the approved account profile is missing',
  'the approved account profile is not a physical directory',
  'the approved account profile is not private (requires mode 0700)',
  'the default Claude profile is not private (requires mode 0700)',
  'the sign-in command could not start or finish',
  'the sign-in command returned an unreadable or incomplete answer',
  'the approved account identity is missing',
  'the sign-in answer did not identify the account',
  'the sign-in command exited unsuccessfully',
])
const PAIRING_DETAILS = new Set([
  'the local pairing state could not be read or saved',
  'the pairing server could not confirm the lifecycle',
  'the pairing response did not match the approved computer',
  'the pairing response contains an unapproved account',
  'the local dispatch fence could not be confirmed',
  'the approved runtime configuration could not be refreshed',
])
function probeDetail(reason: unknown, detail: unknown, harness: string): string | undefined {
  if (reason === 'pairing_sync_failed') return typeof detail === 'string' && PAIRING_DETAILS.has(detail) ? detail : undefined
  if (reason === 'login_required' && detail === "the approved account is signed out in the daemon's view") return detail
  if (detail === 'the default Claude profile is not private (requires mode 0700)' && harness !== 'claude') return undefined
  return reason === 'probe_failed' && typeof detail === 'string' && PROBE_DETAILS.has(detail) ? detail : undefined
}

function harnessFix(harness: string, reason?: HarnessReason, detail?: string): HarnessFix | undefined {
  if (!['claude', 'codex', 'cursor', 'grok', 'pi', 'gemini', 'opencode'].includes(harness)) return
  if (harness === 'claude' && probeDetail(reason, detail, harness) === 'the default Claude profile is not private (requires mode 0700)') return { kind: 'permissions', command: 'chmod 700 "$HOME/.claude"' }
  // repin replaces Claude's shared pins; add-harness renews another harness's blocked pin.
  if (PIN_REASONS.includes(reason ?? '')) {
    return harness === 'claude'
      ? { kind: 'repin', command: 'aeon-agentd repin --harness claude' }
      : { kind: 'add_harness', command: `aeon-agentd add-harness --harness ${harness}` }
  }
  if (reason === 'binding_missing') return { kind: 'add_harness', command: `aeon-agentd add-harness --harness ${harness}` }
  if (['harness_failed', 'cli_unavailable', 'profile_permissions', 'probe_timeout', 'probe_failed', 'capacity_timeout'].includes(reason ?? '')) return { kind: 'restart', command: 'aeon-agentd setup' }
  if (reason === 'login_required') return { kind: 'login', command: harness === 'claude' ? 'claude auth login' : harness === 'pi' ? 'pi' : harness === 'gemini' ? 'gemini' : harness === 'opencode' ? 'opencode auth login' : `${harness === 'cursor' ? 'cursor-agent' : harness} login` }
}

/** The repair kind and exact command for one reason; exported for the shared-vocabulary test. */
export function harnessRecovery(harness: string, reason: string): HarnessFix | undefined {
  return harnessFix(harness, reason)
}

interface AttentionReport { accounts: AccountAttention[]; count: number; truncated: boolean }

function attentionItems(detail: HarnessDetail | undefined): AccountAttention[] {
  return detail && ['ready', 'blocked', 'login_required'].includes(detail.state) ? detail.attention_accounts ?? [] : []
}

// Parsed details already carry attention_truncated when a five-name list had no count.
// A matching count without that flag is complete, including a list of five.
function attentionReport(detail: HarnessDetail | undefined): AttentionReport | undefined {
  const accounts = attentionItems(detail)
  if (!accounts.length) return
  const count = typeof detail?.attention_count === 'number' && detail.attention_count >= accounts.length ? detail.attention_count : accounts.length
  return { accounts, count, truncated: detail?.attention_truncated === true || count > accounts.length }
}

function attentionFix(harness: string, items: AccountAttention[]): string {
  const commands: string[] = []
  for (const item of items) {
    const command = harnessFix(harness, item.reason, item.reason_detail)?.command ?? ''
    if (command && !commands.includes(command)) commands.push(command)
  }
  return commands.join(' · ')
}

export function describeHarnessFix(view: HarnessView, harness: string): string {
  if (view.computer_state !== 'connected') return ''
  const detail = harnessDetail(view, harness)
  if (!detail) return ''
  const attention = attentionItems(detail)
  if (attention.length) return attentionFix(harness, attention)
  return ['blocked', 'login_required', 'checking'].includes(detail.state) ? harnessFix(harness, detail.reason, detail.reason_detail)?.command ?? '' : ''
}

/** Only this account's failure: a ready sibling must not inherit its fix. */
export function describeEnrollmentDiagnostic(view: HarnessView, enrollment: { account_id: string; harness: string }): { hint: string; command: string } | null {
  if (view.computer_state !== 'connected') return null
  const detail = harnessDetail(view, enrollment.harness)
  const report = attentionReport(detail)
  const sole = view.enrollments?.filter(item => item.harness === enrollment.harness && item.state !== 'revoked')
  const item = report ? report.accounts.find(item => item.account_id === enrollment.account_id)
    : detail && ['blocked', 'login_required'].includes(detail.state) && sole?.length === 1 && sole[0]?.account_id === enrollment.account_id ? detail : undefined
  if (!item) return null
  const cause = probeDetail(item.reason, item.reason_detail, enrollment.harness)
  return {
    hint: cause ? `${harnessDisplayName(enrollment.harness)}: ${item.reason === 'login_required' ? '' : item.reason === 'pairing_sync_failed' ? 'pairing sync failed: ' : 'sign-in check failed: '}${cause}.` : '',
    command: harnessFix(enrollment.harness, item.reason, cause)?.command ?? '',
  }
}

const HARNESS_HINTS: Record<string, string> = {
  repin_pending: 'Retries automatically.',
  cli_unavailable: 'Restore the approved executable, then retry.',
  harness_failed: 'Restore the approved installation, then retry.',
  profile_permissions: 'Make the pi profile private, then retry.',
}

function enrolledHarnessCount(view: HarnessView, harness: string): number {
  return (view.enrollments ?? []).filter(item => item.harness === harness && item.state !== 'revoked').length
}

/** One short sentence that the command alone does not say; empty otherwise. */
export function describeHarnessHint(view: HarnessView, harness: string): string {
  const detail = harnessDetail(view, harness)
  if (view.computer_state !== 'connected') return ''
  const refusal = view.enrollments?.find(item => item.harness === harness && item.state === 'connected' && item.verification_error === 'verification_unavailable')
  if (refusal) return verificationRefusalText(refusal)
  if (!detail) return ''
  const accounts = view.enrollments?.filter(item => item.harness === harness && item.state === 'connected') ?? []
  const accountName = accounts.length === 1 && accounts[0]?.label ? accounts[0].label : harnessDisplayName(harness)
  if (detail.reason === 'binding_missing') return `${harnessDisplayName(harness)} was approved but isn't set up on this computer. Add it here or remove it from this computer in ${product()}.`
  if (detail.reason === 'pairing_sync_failed') {
    const cause = probeDetail(detail.reason, detail.reason_detail, harness)
    return `${accountName}: pairing sync failed${cause ? `: ${cause}` : ''}.`
  }
  if (detail.reason === 'probe_pending') return `${accountName}: waiting for the sign-in and availability check; blocked after 60 seconds.`
  if (detail.reason === 'capacity_capture') return `${accountName}: a short capacity check is in progress; expected within 10 seconds.`
  if (['probe_timeout', 'probe_failed', 'capacity_timeout'].includes(detail.reason ?? '')) return `${accountName}: ${reasonLabel(detail.state, detail.reason).toLowerCase()}.`
  const attention = attentionReport(detail)
  const enrolled = enrolledHarnessCount(view, harness)
  if (attention && enrolled >= 2 && attention.count < enrolled) {
    const verb = attention.count === 1 ? 'needs' : 'need'
    const prefix = attention.truncated && attention.count === attention.accounts.length ? 'At least ' : ''
    return `${prefix}${attention.count} of ${enrolled} accounts ${verb} attention`
  }
  if (!detail.reason || detail.state !== 'blocked') return ''
  return HARNESS_HINTS[detail.reason] ?? ''
}

function reasonLabel(status: string, reason?: string): string {
  const labels: Record<string, string> = { ready: 'Ready', blocked: 'Needs attention', login_required: 'Sign in required', checking: 'Checking', draining: 'Draining' }
  const reasons: Record<string, string> = { pairing_sync_failed: 'Pairing sync failed', repin_pending: 'Waiting for repin', dependency_invalid: 'Dependency needs repair', pin_missing: 'Pin missing', login_required: 'Sign in required', starting: 'Starting', cli_unavailable: 'Executable unavailable', pin_partial: 'Pin incomplete', pin_drifted: 'Pin changed', pin_invalid: 'Pin invalid', pin_unsafe: 'Pin unsafe', harness_failed: 'Failed to start', profile_permissions: 'Profile permissions need repair', binding_missing: 'Approved, not set up here', probe_pending: 'Checking account (up to 60 seconds)', probe_timeout: 'Account check timed out after 60 seconds', probe_failed: 'Account availability check failed', capacity_capture: 'Capturing capacity (up to 10 seconds)', capacity_timeout: 'Capacity capture timed out after 10 seconds' }
  // A code from a newer daemon is shown raw rather than dropped or guessed.
  if (!HARNESS_STATES.includes(status as typeof HARNESS_STATES[number])) return `Needs attention · ${reason ?? status}`
  if (reason) return reasons[reason] ?? `Needs attention · ${reason}`
  return labels[status] ?? ''
}

function attentionStatusLabel(items: AccountAttention[]): string {
  const reason = items[0]?.reason
  if (reason && items.every(item => item.reason === reason)) return reasonLabel('blocked', reason)
  return 'Needs attention'
}

function freshLabel(view: HarnessView, label: string): string {
  if (!label) return ''
  return view.connectivity === 'online' ? label : `Last reported: ${label.toLowerCase()}`
}

export function describeHarnessStatus(view: HarnessView, harness: string): string {
  if (view.computer_state !== 'connected') return ''
  const detail = harnessDetail(view, harness)
  const status = view.harness_statuses?.[harness] ?? detail?.state
  if (!status) return ''
  const attention = attentionItems(detail)
  const label = attention.length ? attentionStatusLabel(attention) : reasonLabel(status, detail?.reason)
  return freshLabel(view, label)
}

/** Per-enrollment label. A complete attention list names the blocked accounts and leaves the others Ready. An account missing from a truncated list is not Ready. */
export function describeEnrollmentStatus(view: HarnessView, enrollment: { account_id: string; harness: string; verification_state?: VerificationState; verification_expired_ready?: boolean; verification_stalled?: boolean }): string {
  if (view.computer_state !== 'connected') return ''
  const detail = harnessDetail(view, enrollment.harness)
  const attention = attentionReport(detail)
  let status = describeHarnessStatus(view, enrollment.harness)
  if (attention) {
    const hit = attention.accounts.find(item => item.account_id === enrollment.account_id)
    if (hit) return freshLabel(view, reasonLabel('blocked', hit.reason))
    if (detail?.state !== 'ready' || attention.truncated || attention.count !== attention.accounts.length) return freshLabel(view, 'Needs attention')
    status = freshLabel(view, 'Ready')
  }
  if (status && status !== 'Ready') return status
  if (enrollment.verification_stalled) return freshLabel(view, 'Verification stalled')
  if (enrollment.verification_state === 'expired' && enrollment.verification_expired_ready === false) return freshLabel(view, 'Verification failed')
  const verification = enrollment.verification_state
  if (verification && VERIFICATION_ACTIVE.includes(verification)) return freshLabel(view, verification === 'queued' ? 'Verification queued' : 'Verifying account')
  if (verification === 'expired' && status !== 'Ready') return freshLabel(view, 'Verification failed')
  if (verification && VERIFICATION_FAILED.includes(verification)) return freshLabel(view, 'Verification failed')
  return status
}

export function describeComputerStatus(view: Pick<PairingView, 'computer_state' | 'local_cleanup' | 'local_processes' | 'enrollments' | 'setup_state' | 'connectivity' | 'accounting_state'>, hints: { httpStatus?: number; heartbeatMissing?: boolean } = {}): ComputerStatusCopy {
  let stateLabel = view.computer_state === 'connected' ? 'Connected' : view.computer_state === 'draining' ? 'Draining' : view.computer_state === 'revoked' ? 'Revoked' : 'Not connected yet'
  const cleanupLabel = view.local_cleanup === 'confirmed' ? 'Local cleanup confirmed' : 'Local cleanup pending'
  const enrollmentUnconfirmed = view.enrollments.some(item => item.local_processes === 'unconfirmed')
  const processesUnconfirmed = view.local_processes !== 'drained' || enrollmentUnconfirmed || hints.heartbeatMissing === true || hints.httpStatus === 401
  const processLabel = processesUnconfirmed ? 'Local processes unconfirmed' : 'Local processes drained'
  const claimsProcessStopped = !processesUnconfirmed
  let detail = `${stateLabel}. ${cleanupLabel}. ${processLabel}.`
  let next = 'Review the computer and try the action again.'
  if (view.computer_state === 'draining') {
    const count = activeRunIds(view).length
    detail = count ? `Draining. ${count} run${count === 1 ? '' : 's'} still active. ${cleanupLabel}.` : `Draining. No active runs were reported. ${cleanupLabel}.`
    next = 'New work stays stopped. Current runs are left to finish.'
  } else if (view.computer_state === 'revoked') {
    detail = `Access is revoked on the server. ${cleanupLabel}. ${processLabel}. ${localProcessSentence(view)}`
    next = processesUnconfirmed || activeRunIds(view).length
      ? 'Revoking server access does not show that work on the computer has stopped, and it does not settle accounting for runs that are still open.'
      : 'Pair the computer again only with a new approval. The old grant stays revoked.'
  } else if (view.computer_state === 'connected') {
    if (view.setup_state === 'connected' && view.connectivity === 'online') {
      detail = 'Connected. The computer confirmed that setup finished, and a recent probe succeeded.'
      next = 'Disconnect the computer or remove one harness when you want to unpair it.'
    } else if (view.setup_state === 'connected') {
      stateLabel = view.connectivity === 'offline' ? 'Offline' : 'Setup finished'
      detail = view.connectivity === 'offline'
        ? `Setup was reported complete. The computer is offline. ${localProcessSentence(view)}`
        : 'The computer reported that setup finished. Current connectivity is unknown.'
      next = 'A finished setup report is not a live probe. Local processes stay unconfirmed until the computer says otherwise.'
    } else {
      stateLabel = 'Setup unconfirmed'
      detail = 'The pairing record is open. The computer has not confirmed that setup finished.'
      next = 'Wait for a setup report. Approval alone does not mean the daemon is connected.'
    }
  }
  if (hints.httpStatus === 401 || hints.heartbeatMissing) {
    detail = `${detail} A rejected sign-in or a missing heartbeat does not show that local work has stopped.`
    next = 'Treat local processes as unconfirmed until the computer confirms cleanup.'
  }
  if (accountingUnconfirmed(view)) {
    detail = `${detail} Run accounting is unconfirmed.`
    next = 'Cleanup and revocation do not settle run accounting. Local processes stay unconfirmed until the computer says otherwise.'
  }
  return { stateLabel, cleanupLabel, processLabel, claimsProcessStopped, detail, next }
}

/**
 * Keep reading until the daemon is connected and verification is no longer
 * moving, or the request is closed. A failed setup, or a failed, cancelled,
 * expired or ownership-lost verification, is final whatever the connectivity
 * says: nothing more will change by waiting.
 */
export function pairingStillLive(view: PairingView): boolean {
  if (!view.computer_id) return false
  const phase = describeProgress(view).phase
  if (phase === 'denied' || phase === 'expired' || phase === 'revoked') return false
  if (view.setup_state === 'setup_failed') return false
  if (view.enrollments.some(item => item.verification_state != null && (VERIFICATION_FAILED.includes(item.verification_state) || (item.verification_state === 'expired' && describeEnrollmentStatus(view, item) !== 'Ready')))) return false
  const connected = describeComputerStatus(view).stateLabel === 'Connected'
  const moving = view.enrollments.some(item => item.verification_state != null && (VERIFICATION_ACTIVE as readonly string[]).includes(item.verification_state))
  return !(connected && !moving)
}

/**
 * What the register page shows after approval. A daemon that has finished
 * setup and answered a recent probe is Connected, including while a
 * verification run is still in progress. describeProgress keeps verification
 * as its own phase; this copy is the page title.
 */
export function pairingLiveCopy(view: PairingView): { title: string; detail: string; next: string } {
  const progress = describeProgress(view)
  if (describeComputerStatus(view).stateLabel !== 'Connected') return { title: progress.title, detail: progress.detail, next: progress.next }
  return {
    title: 'Connected',
    detail: progress.phase === 'verify' ? progress.detail : 'The computer reported that setup finished, and a recent probe succeeded.',
    next: progress.next,
  }
}

function accountingUnconfirmed(view: Pick<PairingView, 'accounting_state' | 'enrollments'>): boolean {
  return view.accounting_state === 'unconfirmed' || view.enrollments.some(item => item.accounting_state === 'unconfirmed')
}

export interface DisconnectConfirm {
  title: string
  body: string
  points: string[]
  confirmLabel: string
  cancelLabel: string
  danger: boolean
}

export function disconnectConfirm(input: {
  scope: 'computer' | 'enrollment'
  computerName: string
  mode: DisconnectMode
  activeRunCount: number
  otherConnectedCount: number
  enrollment?: { harness: string; label: string }
}): DisconnectConfirm {
  const target = input.scope === 'computer'
    ? input.computerName
    : `${input.enrollment?.label ?? 'This harness'} on ${input.computerName}`
  const points = [
    'Vendor sign-in, the vendor subscription, and project files stay on the computer.',
  ]
  if (input.mode === 'drain') {
    points.push(input.activeRunCount
      ? `${input.activeRunCount} active run${input.activeRunCount === 1 ? '' : 's'} will finish. No new work is accepted.`
      : 'No active runs were reported. No new work is accepted.')
  } else {
    points.push('Server access ends now, including when the computer is offline.')
    points.push('Local processes stay unconfirmed. This does not show that they have stopped.')
  }
  if (input.scope === 'enrollment' && input.otherConnectedCount > 0) {
    points.push('Other harnesses on this computer stay connected.')
  }
  if (input.scope === 'enrollment' && input.otherConnectedCount === 0) {
    points.push(`This is the last harness still connected. The ${product()} service is removed only after its own work has drained.`)
  }
  return {
    title: input.mode === 'drain' ? `Disconnect ${target}` : `Revoke access for ${target}`,
    body: input.mode === 'drain'
      ? `${product()} stops new work and disconnects after current runs finish.`
      : `${product()} revokes this access now. Use this only when the computer is lost or must lose access before its work finishes.`,
    points,
    confirmLabel: input.mode === 'drain' ? 'Finish runs and disconnect' : 'Revoke access now',
    cancelLabel: 'Cancel',
    danger: input.mode === 'revoke_now',
  }
}

export function retryAfterSeconds(header: string | null, now = Date.now()): number {
  if (!header) return MIN_POLL_INTERVAL_MS / 1000
  const seconds = Number(header)
  if (Number.isFinite(seconds) && seconds >= 0) return Math.max(MIN_POLL_INTERVAL_MS / 1000, seconds)
  const when = Date.parse(header)
  if (Number.isFinite(when)) return Math.max(MIN_POLL_INTERVAL_MS / 1000, Math.ceil((when - now) / 1000))
  return MIN_POLL_INTERVAL_MS / 1000
}

export async function getPairingGuide(signal?: AbortSignal): Promise<PairingGuide> {
  const response = await resilientFetch('/api/agent-pairing/guide', {
    method: 'GET', credentials: 'same-origin', cache: 'no-store', signal,
    headers: { Accept: 'application/json' },
  })
  const data = await readBody(response)
  if (!response.ok) throw httpError(response, data)
  assertNoSecrets(data)
  return parseGuide(data)
}

export async function lookupPairing(userCode: string, signal?: AbortSignal): Promise<PairingView> {
  const code = canonicalUserCode(userCode)
  if (!code) throw new PairingError(0, 'Enter the 9-digit code from the computer, with or without dashes.', {
    code: 'invalid_request', next: 'A code match does not grant access.',
  })
  return personJson('/agent-pairing/lookup', 'POST', { user_code: code }, signal).then(parseView)
}

export async function submitApproval(input: {
  view: PairingView
  choice: ReviewChoice
  selectedAccountKeys: readonly string[]
  permissions: PairingPermissions
  now?: number
  requestedComputerId?: string
  targetComputerName?: string | null
  signal?: AbortSignal
}): Promise<PairingView> {
  const plan = planApproval(input)
  if (!plan.ok) throw new PairingError(0, plan.message, { code: 'blocked', next: plan.next })
  const key = `approve:${input.view.request_id}:${JSON.stringify(plan.body)}`
  return oneFlight(key, () => personJson(`/agent-pairing/requests/${pathId(input.view.request_id)}/approve`, 'POST', plan.body, input.signal).then(parseView))
}

// Separate, idempotent permission grants. The pairing grant itself never
// creates limits or silently opts in on a later page visit.
export async function approvePairedAccounts(view: PairingView, keys: readonly string[], permissions: PairingPermissions): Promise<string[]> {
  if (!permissions.canApproveAccounts) throw new PairingError(403, 'Only a person who can manage accounts can allow agents.', { code: 'forbidden' })
  const started = readEpoch
  const saved: string[] = []
  for (const key of keys) {
    if (started !== readEpoch) throw sessionResetError()
    const enrollment = view.enrollments.find(item => item.account_key === key && item.state === 'connected')
    if (!enrollment) throw new PairingError(409, 'This account is not connected yet.', { code: 'blocked', savedAccountIds: saved, next: 'Retry account approval after setup finishes.' })
    try {
      await personJson(`/agent-accounts/${pathId(enrollment.account_id)}/capacity/approve`, 'POST', {})
      if (started !== readEpoch) throw sessionResetError()
      saved.push(enrollment.account_id)
    } catch (error) {
      if (started !== readEpoch) throw sessionResetError()
      throw new PairingError(error instanceof PairingError ? error.status : 0, `Agent access for ${enrollment.label} was not confirmed.`, { code: 'approval_failed', savedAccountIds: saved, next: 'Retry account approval; the computer is already approved.' })
    }
  }
  return saved
}

export async function denyPairing(view: PairingView, permissions: PairingPermissions, signal?: AbortSignal): Promise<PairingView> {
  if (!permissions.canDeny) throw new PairingError(0, 'Only a signed-in person who can manage accounts can deny a pairing.', { code: 'forbidden', next: 'An agent session cannot deny it either.' })
  if (view.state === 'denied') return view
  if (view.state !== 'pending') throw new PairingError(0, 'This request can no longer be denied.', { code: 'blocked', next: 'Look it up again and review the current state.' })
  return oneFlight(`deny:${view.request_id}`, () => personJson(`/agent-pairing/requests/${pathId(view.request_id)}/deny`, 'POST', {}, signal).then(parseView))
}

export async function listPairingComputers(signal?: AbortSignal): Promise<PairingView[]> {
  const data = await personJson('/agent-pairing/computers', 'GET', undefined, signal)
  const record = asRecord(data, 'computers')
  if (!Array.isArray(record.computers)) invalid('computers')
  return record.computers.map(parseView)
}

export async function getPairingComputer(computerId: string, signal?: AbortSignal): Promise<PairingView> {
  return personJson(`/agent-pairing/computers/${pathId(computerId)}`, 'GET', undefined, signal).then(parseView)
}

export async function disconnectComputer(view: PairingView, mode: DisconnectMode, permissions: PairingPermissions, signal?: AbortSignal): Promise<PairingView> {
  assertDisconnect(view.computer_id, mode, permissions, view.computer_state)
  const body = disconnectBody(view, mode)
  return oneFlight(`disconnect:${view.computer_id}:${mode}:${body.expected_revision}`, () => personJson(`/agent-pairing/computers/${pathId(view.computer_id!)}/disconnect`, 'POST', body, signal).then(parseView))
}

/**
 * Remove, for a computer that is revoked, or approved but never confirmed
 * (AEON-402). The server archives it with its account bindings; the history
 * stays. Open run accounting keeps it listed. `reason` says why not, or ''.
 */
export function computerRemoval(view: Pick<PairingView, 'computer_id' | 'computer_state' | 'state' | 'last_seen_at' | 'enrollments'>, permissions: Pick<PairingPermissions, 'canDisconnect'>): { allowed: boolean; reason: string } {
  if (!view.computer_id || !permissions.canDisconnect) return { allowed: false, reason: '' }
  const neverConfirmed = view.computer_state !== 'revoked' && view.state !== 'redeemed' && !view.last_seen_at
  if (view.computer_state !== 'revoked' && !neverConfirmed) return { allowed: false, reason: '' }
  if (view.enrollments.some(item => item.active_run_ids.length)) return { allowed: false, reason: 'A run on this computer is not settled yet.' }
  return { allowed: true, reason: '' }
}

export async function removeComputer(view: PairingView, permissions: PairingPermissions, signal?: AbortSignal): Promise<PairingView> {
  if (!computerRemoval(view, permissions).allowed) throw new PairingError(0, 'This computer cannot be removed yet.', { code: 'blocked', next: 'Disconnect it first, and wait until its runs are settled.' })
  return oneFlight(`remove:${view.computer_id}`, () => personJson(`/agent-pairing/computers/${pathId(view.computer_id!)}/remove`, 'POST', undefined, signal).then(parseView))
}

export async function disconnectEnrollment(view: PairingView, accountId: string, mode: DisconnectMode, permissions: PairingPermissions, signal?: AbortSignal): Promise<PairingView> {
  if (!view.computer_id) throw new PairingError(0, 'This enrollment is not on a connected computer.', { code: 'invalid_request', next: 'Refresh the computer list and choose the enrollment again.' })
  const enrollment = view.enrollments.find(item => item.account_id === accountId)
  if (!enrollment) throw new PairingError(0, 'That harness is not on this computer.', { code: 'not_found', next: 'Refresh the computer and choose the harness again.' })
  assertDisconnect(view.computer_id, mode, permissions, enrollment.state)
  const body = disconnectBody(view, mode)
  const path = `/agent-pairing/computers/${pathId(view.computer_id)}/enrollments/${pathId(accountId)}/disconnect`
  return oneFlight(`disconnect:${view.computer_id}:${accountId}:${mode}:${body.expected_revision}`, () => personJson(path, 'POST', body, signal).then(parseView))
}

function disconnectBody(view: PairingView, mode: DisconnectMode): { mode: DisconnectMode; expected_revision: number } {
  if (typeof view.revision !== 'number' || !Number.isSafeInteger(view.revision) || view.revision < 1) {
    throw new PairingError(0, 'This computer needs a fresh review before it can be disconnected.', {
      code: 'stale_revision',
      next: 'Refresh the computer and confirm the harnesses again. Disconnecting without the current revision could include a harness you have not reviewed.',
    })
  }
  return { mode, expected_revision: view.revision }
}

function assertDisconnect(computerId: string | null, mode: DisconnectMode, permissions: PairingPermissions, state: ComputerState | null): void {
  if (!permissions.canDisconnect) throw new PairingError(0, 'Only a signed-in person who can manage accounts can disconnect a computer.', { code: 'forbidden', next: 'An agent session cannot force this to stop.' })
  if (!computerId) throw new PairingError(0, 'This request has no computer to disconnect yet.', { code: 'invalid_request', next: 'Deny the pending request, or wait until the computer is connected.' })
  if (!(DISCONNECT_MODES as readonly string[]).includes(mode)) throw new PairingError(0, 'Choose finish-and-disconnect, or revoke access now.', { code: 'invalid_request', next: 'Those are separate confirmations.' })
  if (state === 'revoked') throw new PairingError(0, 'Access is already revoked.', { code: 'blocked', next: 'Local cleanup stays pending until the computer confirms it. Revoking again does not stop unconfirmed processes.' })
}

async function oneFlight(key: string, run: () => Promise<PairingView>): Promise<PairingView> {
  const existing = flights.get(key)
  if (existing) return existing
  if (flightBusy) {
    throw new PairingError(0, 'Another pairing change is still being sent.', { code: 'busy', next: 'Wait for it to finish, then review the result before trying again.' })
  }
  flightBusy = true
  const promise = run().finally(() => {
    if (flights.get(key) === promise) {
      flightBusy = false
      flights.delete(key)
    }
  })
  flights.set(key, promise)
  return promise
}

function sessionResetError(): PairingError {
  return new PairingError(0, 'Your session changed.', {
    code: 'session_reset',
    next: 'Sign in again. The previous workspace’s review and computers were cleared. The pairing code stays in this box.',
  })
}

async function personJson(path: string, method: string, body?: unknown, signal?: AbortSignal): Promise<unknown> {
  if (body !== undefined) assertNoSecrets(body)
  const started = readEpoch
  const controller = new AbortController()
  personControllers.add(controller)
  const requestSignal = signal ? AbortSignal.any([signal, controller.signal]) : controller.signal
  try {
    if (started !== readEpoch) throw sessionResetError()
    const response = await api(path, {
      method, signal: requestSignal,
      ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
    })
    const data = await readBody(response)
    if (started !== readEpoch) throw sessionResetError()
    if (!response.ok) throw httpError(response, data)
    assertNoSecrets(data)
    if (started !== readEpoch) throw sessionResetError()
    return data
  } catch (error) {
    if (started !== readEpoch) throw sessionResetError()
    throw error
  } finally {
    personControllers.delete(controller)
  }
}

async function readBody(response: Response): Promise<unknown> {
  return response.json().catch(() => ({}))
}

function httpError(response: Response, data: unknown): PairingError {
  const body = data && typeof data === 'object' ? data as Record<string, unknown> : {}
  const code = typeof body.code === 'string' && body.code ? body.code : codeForStatus(response.status)
  const serverMessage = typeof body.error === 'string' && body.error ? body.error : typeof body.message === 'string' && body.message ? body.message : ''
  const retry = response.status === 429 ? retryAfterSeconds(response.headers.get('Retry-After')) : null
  const explained = explain(response.status, code, serverMessage, retry)
  return new PairingError(response.status, explained.message, { code, retryAfterSeconds: retry, next: explained.next })
}

function codeForStatus(status: number): string {
  if (status === 401) return 'forbidden'
  if (status === 403) return 'forbidden'
  if (status === 404) return 'not_found'
  if (status === 409) return 'conflict'
  if (status === 429) return 'rate_limited'
  return 'internal_error'
}

function explain(status: number, code: string, serverMessage: string, retryAfter: number | null): { message: string; next: string } {
  const fallback: Record<string, { message: string; next: string }> = {
    rate_limited: {
      message: 'Too many attempts.',
      next: `Wait ${retryAfter ?? MIN_POLL_INTERVAL_MS / 1000} seconds, then try the same code again. This page will not start a new pairing.`,
    },
    expired_token: {
      message: 'This code has expired.',
      next: 'Start setup again on the computer and enter the new code. This page will not renew the old request.',
    },
    access_denied: {
      message: 'This request was denied.',
      next: 'Start a new pairing on the computer if you still want to connect it. This page will not reverse the denial.',
    },
    forbidden: {
      message: status === 401 ? 'Your session has ended.' : 'You do not have permission to manage accounts.',
      next: status === 401
        ? 'Sign in again. Entering the code does not grant access.'
        : 'Ask a workspace admin. An agent session cannot approve, deny or disconnect.',
    },
    not_found: {
      message: 'No pairing request uses that code.',
      next: 'Check the code on the computer. Looking it up does not create a request.',
    },
    conflict: {
      message: 'This pairing changed.',
      next: 'Look it up again and review the current details. The previous action was not retried.',
    },
    verification_unavailable: {
      message: 'Verification is unavailable for a selected harness.',
      next: 'Turn verification off, or leave that harness out, and connect again. Nothing was granted.',
    },
    pairing_revoked: {
      message: 'This computer is already disconnected.',
      next: 'Pair it again with a new approval if you want to reconnect. Old access stays revoked.',
    },
    enrollment_revoked: {
      message: 'That harness is already removed.',
      next: 'The other harnesses stay as they are. Add a harness only with a new approval.',
    },
    enrollment_draining: {
      message: 'That harness is finishing current work.',
      next: 'Wait for its runs to finish. This page will not stop them.',
    },
    computer_connected: {
      message: 'This computer is still connected.',
      next: 'Disconnect it first. A connected computer is never removed from the list.',
    },
    runs_unsettled: {
      message: 'A run on this computer is not settled yet.',
      next: 'The computer stays listed until its runs report back or are settled.',
    },
    authorization_pending: {
      message: 'The computer is still waiting for approval.',
      next: 'Review the request and connect it explicitly. Entering the code again does not approve it.',
    },
  }
  const known = fallback[code]
  return {
    message: serverMessage || known?.message || `The pairing request failed (${status || 'local'}).`,
    next: known?.next || 'Review the details and try again. This page will not renew an expired or denied request.',
  }
}

function parseGuide(data: unknown): PairingGuide {
  const record = asRecord(data, 'guide')
  const instance = httpOrigin(record.instance_url, 'instance_url')
  const slug = asString(record.default_tenant_slug, 'default_tenant_slug')
  if (!SLUG.test(slug)) invalid('default_tenant_slug')
  const protocol = asString(record.protocol, 'protocol')
  if (!Array.isArray(record.platforms) || record.platforms.length === 0 || record.platforms.some(item => typeof item !== 'string' || !item)) invalid('platforms')
  if (!Array.isArray(record.install_targets)) invalid('install_targets')
  if (typeof record.install_available !== 'boolean') invalid('install_available')
  const guide: PairingGuide = {
    instance_url: instance,
    default_tenant_slug: slug,
    protocol,
    platforms: record.platforms.map(String),
    version: bounded(record.version, 'version', 64),
    platform_qualification: bounded(record.platform_qualification, 'platform_qualification', 500),
    setup_command: bounded(record.setup_command, 'setup_command', 4000),
    install_available: record.install_available,
    install_targets: record.install_targets.map(parseInstallTarget),
  }
  if (typeof record.managed_installation === 'string' && record.managed_installation) guide.managed_installation = record.managed_installation.slice(0, 500)
  const homebrew = optionalBounded(record.homebrew_command, 'homebrew_command', 4000)
  if (homebrew) guide.homebrew_command = homebrew
  if (record.managed_setup != null) {
    const managed = asRecord(record.managed_setup, 'managed_setup')
    guide.managed_setup = {
      command: bounded(managed.command, 'managed_setup.command', 4000),
      service_option: bounded(managed.service_option, 'managed_setup.service_option', 200),
      module_url: httpsUrl(managed.module_url, 'managed_setup.module_url'),
      service_note: bounded(managed.service_note, 'managed_setup.service_note', 1000),
    }
    const platform = optionalBounded(managed.platform_note, 'managed_setup.platform_note', 500)
    const prerequisites = optionalBounded(managed.prerequisite_note, 'managed_setup.prerequisite_note', 1000)
    if (platform) guide.managed_setup.platform_note = platform
    if (prerequisites) guide.managed_setup.prerequisite_note = prerequisites
  }
  const capabilities = parseCapabilities(record.verification_capabilities, 'verification_capabilities')
  if (capabilities) guide.verification_capabilities = capabilities
  const helper = optionalBounded(record.verification_helper_version, 'verification_helper_version', 64)
  if (helper) guide.verification_helper_version = helper
  return guide
}

function parseInstallTarget(value: unknown): InstallTarget {
  const record = asRecord(value, 'install_targets')
  const platform = asString(record.platform, 'install_targets.platform')
  const arch = asString(record.arch, 'install_targets.arch')
  const service = asString(record.service, 'install_targets.service')
  if (!['darwin', 'linux'].includes(platform) || !['arm64', 'amd64'].includes(arch) || !['launchd-user', 'systemd-user'].includes(service)) invalid('install_targets')
  return {
    platform,
    arch,
    service,
    qualification: bounded(record.qualification, 'install_targets.qualification', 500),
    artifact_url: httpsUrl(record.artifact_url, 'install_targets.artifact_url'),
    checksums_url: httpsUrl(record.checksums_url, 'install_targets.checksums_url'),
    command: bounded(record.command, 'install_targets.command', 8000),
  }
}

function attentionTotal(value: unknown, enrolled: number): number | undefined {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value >= enrolled) return
  return value
}

function parseAttention(raw: unknown, enrolled: Set<string>, declaredCount: unknown, declaredTruncated: unknown, harness: string, allowAll = false): { accounts: AccountAttention[]; count: number; truncated: boolean } | undefined {
  if (!Array.isArray(raw) || enrolled.size < (allowAll ? 1 : 2)) return
  const seen = new Set<string>()
  const items: AccountAttention[] = []
  for (const entry of raw) {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) continue
    const item = entry as Record<string, unknown>
    const id = typeof item.account_id === 'string' && UUID.test(item.account_id) ? item.account_id : ''
    const reason = harnessCode(item.reason)
    if (!id || !reason || !enrolled.has(id) || seen.has(id)) continue
    seen.add(id)
    const reasonDetail = probeDetail(reason, item.reason_detail, harness)
    items.push({ account_id: id, reason, ...(reasonDetail ? { reason_detail: reasonDetail } : {}) })
  }
  items.sort((a, b) => a.account_id < b.account_id ? -1 : a.account_id > b.account_id ? 1 : 0)
  if (items.length === 0 || (!allowAll && items.length >= enrolled.size)) return
  const declared = attentionTotal(declaredCount, enrolled.size + (allowAll ? 1 : 0))
  let count = items.length
  // Five or more names without a total may be the old cap: absence is not readiness.
  // A declared total equal to the list, including exactly five, is complete.
  let truncated = declaredTruncated === true || (declared === undefined && items.length >= 5)
  if (declared !== undefined && declared > items.length) {
    count = declared
    truncated = true
  }
  if (!allowAll && count >= enrolled.size) return
  return { accounts: items, count, truncated }
}

function parseView(data: unknown): PairingView {
  const record = asRecord(data, 'pairing')
  const view: PairingView = {
    request_id: uuid(record.request_id, 'request_id'),
    tenant_id: uuid(record.tenant_id, 'tenant_id'),
    tenant_name: bounded(record.tenant_name, 'tenant_name', 200),
    state: oneOf(record.state, REQUEST_STATES, 'state'),
    request_digest: digest(record.request_digest),
    expires_at: timestamp(record.expires_at, 'expires_at'),
    computer_name: bounded(record.computer_name, 'computer_name', 128),
    platform: token(record.platform, 'platform'),
    arch: token(record.arch, 'arch'),
    workspace_path: bounded(record.workspace_path, 'workspace_path', 1024),
    capabilities: stringList(record.capabilities, 'capabilities'),
    requested_accounts: accounts(record.requested_accounts),
    verification: record.verification == null ? null : verification(record.verification),
    computer_id: optionalUuid(record.computer_id, 'computer_id'),
    computer_state: record.computer_state == null ? null : oneOf(record.computer_state, COMPUTER_STATES, 'computer_state'),
    principal_id: optionalUuid(record.principal_id, 'principal_id'),
    daemon_id: optionalBounded(record.daemon_id, 'daemon_id', 128),
    runtime_prefix: publicPrefix(record.runtime_prefix),
    local_cleanup: oneOf(record.local_cleanup, CLEANUP_STATES, 'local_cleanup'),
    local_processes: oneOf(record.local_processes, PROCESS_STATES, 'local_processes'),
    enrollments: enrollments(record.enrollments),
  }
  if (record.host_capacity != null) view.host_capacity = parseHostCapacity(record.host_capacity)
  if (typeof record.local_auth_pinned === 'boolean') view.local_auth_pinned = record.local_auth_pinned
  if (typeof record.revision === 'number' && Number.isSafeInteger(record.revision) && record.revision >= 0) view.revision = record.revision
  if (typeof record.interval_seconds === 'number' && record.interval_seconds > 0) view.interval_seconds = record.interval_seconds
  const setup = optionalEnum(record.setup_state, SETUP_STATES)
  if (setup) view.setup_state = setup
  if (record.harness_statuses != null) {
    const statuses = asRecord(record.harness_statuses, 'harness_statuses')
    view.harness_statuses = {}
    // Keep future code tokens visible without inventing readiness. Never carry
    // arbitrary diagnostics into the public computer projection.
    for (const harness of ['claude', 'codex', 'cursor', 'grok', 'pi', 'gemini', 'opencode']) {
      const status = harnessCode(statuses[harness])
      if (status) view.harness_statuses[harness] = status
    }
  }
  if (record.harness_details != null) {
    const details = asRecord(record.harness_details, 'harness_details')
    view.harness_details = {}
    for (const harness of ['claude', 'codex', 'cursor', 'grok', 'pi', 'gemini', 'opencode']) {
      const raw = details[harness]
      if (!raw || typeof raw !== 'object' || Array.isArray(raw)) continue
      const item = raw as Record<string, unknown>
      const state = harnessCode(item.state)
      if (!state || view.harness_statuses?.[harness] && view.harness_statuses[harness] !== state) continue
      const reason = harnessCode(item.reason)
      if (item.reason && !reason) continue
      const enrolled = new Set(view.enrollments.filter(entry => entry.harness === harness && entry.state !== 'revoked').map(entry => entry.account_id))
      const attention = ['ready', 'blocked', 'login_required'].includes(state) ? parseAttention(item.attention_accounts, enrolled, item.attention_count, item.attention_truncated, harness, state !== 'ready') : undefined
      // A ready or draining detail keeps no top-level reason. Valid attention
      // still survives a mistaken reason so the partial block stays visible.
      if (['ready', 'draining'].includes(state) && reason && !attention) continue
      const storedReason = ['ready', 'draining'].includes(state) ? undefined : reason
      const reasonDetail = ['blocked', 'login_required'].includes(state) ? probeDetail(storedReason, item.reason_detail, harness) : undefined
      const fix = ['blocked', 'login_required', 'checking'].includes(state) ? harnessFix(harness, storedReason, reasonDetail) : undefined
      view.harness_details[harness] = { state, ...(storedReason ? { reason: storedReason } : {}), ...(reasonDetail ? { reason_detail: reasonDetail } : {}), ...(fix ? { fix } : {}), ...(attention ? { attention_accounts: attention.accounts, attention_count: attention.count, ...(attention.truncated ? { attention_truncated: true } : {}) } : {}) }
    }
  }
  if (typeof record.setup_error === 'string' && record.setup_error) view.setup_error = record.setup_error.slice(0, 500)
  else if (record.setup_error === null || record.setup_error === '') view.setup_error = null
  if (record.last_seen_at === null) view.last_seen_at = null
  else if (typeof record.last_seen_at === 'string' && Number.isFinite(Date.parse(record.last_seen_at))) view.last_seen_at = record.last_seen_at
  if (typeof record.archived_at === 'string' && Number.isFinite(Date.parse(record.archived_at))) view.archived_at = record.archived_at
  const connectivity = optionalEnum(record.connectivity, CONNECTIVITY)
  if (connectivity) view.connectivity = connectivity
  const accounting = readAccounting(record.accounting_state, 'accounting_state')
  if (accounting) view.accounting_state = accounting
  if (typeof record.existing_computer_id === 'string' && record.existing_computer_id) view.existing_computer_id = uuid(record.existing_computer_id, 'existing_computer_id')
  const capabilities = parseCapabilities(record.verification_capabilities, 'verification_capabilities')
  if (capabilities) view.verification_capabilities = capabilities
  const helper = optionalBounded(record.verification_helper_version, 'verification_helper_version', 64)
  if (helper) view.verification_helper_version = helper
  if (record.agent_compatibility != null) {
    const result = asRecord(record.agent_compatibility, 'agent_compatibility')
    const status = asString(result.status, 'agent_compatibility.status')
    if (!['compatible', 'update_required', 'protocol_mismatch', 'unknown'].includes(status)) invalid('agent_compatibility.status')
    if (typeof result.action !== 'string' || result.action.length > 500) invalid('agent_compatibility.action')
    view.agent_compatibility = { status: status as AgentCompatibility['status'], action: result.action }
  }
  // Advisory: the helper's own version, shown beside the computer's name (AEON-499).
  if (record.agent_release && typeof record.agent_release === 'object' && !Array.isArray(record.agent_release)) {
    const version = (record.agent_release as Record<string, unknown>).version
    if (typeof version === 'string' && /^[\w.+-]{1,64}$/.test(version)) view.agent_release = { version }
  }
  return view
}

function accounts(value: unknown): RequestedAccount[] {
  if (!Array.isArray(value)) invalid('requested_accounts')
  return value.map(item => {
    const record = asRecord(item, 'requested_accounts')
    const account: RequestedAccount = {
      account_key: bounded(record.account_key, 'account_key', 128),
      harness: token(record.harness, 'harness'),
      label: bounded(record.label, 'label', 128),
    }
    if (record.config_home_id != null) {
      const home = bounded(record.config_home_id, 'config_home_id', 64)
      if (!/^[0-9a-f]{64}$/.test(home)) invalid('config_home_id')
      account.config_home_id = home
    }
    if (record.model_profile_id != null) account.model_profile_id = uuid(record.model_profile_id, 'model_profile_id')
    if (record.provider != null) {
      const provider = bounded(record.provider, 'provider', 64)
      if (account.harness !== 'pi' || !/^[a-z][a-z0-9_-]{0,63}$/.test(provider)) invalid('provider')
      account.provider = provider
    }
    return account
  })
}

function enrollments(value: unknown): PairingEnrollment[] {
  if (!Array.isArray(value)) invalid('enrollments')
  return value.map(item => {
    const record = asRecord(item, 'enrollments')
    const enrollment: PairingEnrollment = {
      account_id: uuid(record.account_id, 'account_id'),
      account_key: bounded(record.account_key, 'account_key', 128),
      harness: token(record.harness, 'harness'),
      label: bounded(record.label, 'label', 128),
      model_profile_id: uuid(record.model_profile_id, 'model_profile_id'),
      state: oneOf(record.state, COMPUTER_STATES, 'enrollment.state'),
      local_cleanup: oneOf(record.local_cleanup, CLEANUP_STATES, 'enrollment.local_cleanup'),
      verification_run_id: optionalUuid(record.verification_run_id, 'verification_run_id'),
      ...(typeof record.can_verify === 'boolean' ? { can_verify: record.can_verify } : {}),
      ...(typeof record.verification_stalled === 'boolean' ? { verification_stalled: record.verification_stalled } : {}),
      ...(typeof record.verification_expired_ready === 'boolean' ? { verification_expired_ready: record.verification_expired_ready } : {}),
      active_run_ids: uuidList(record.active_run_ids, 'active_run_ids'),
      ...optionalVerification(record),
    }
    for (const name of ['verified_at', 'verification_expires_at', 'last_used_at'] as const) if (typeof record[name] === 'string' && Number.isFinite(Date.parse(record[name]))) enrollment[name] = record[name]
    const processes = readProcess(record.local_processes, 'enrollment.local_processes')
    if (processes) enrollment.local_processes = processes
    const accounting = readAccounting(record.accounting_state, 'enrollment.accounting_state')
    if (accounting) enrollment.accounting_state = accounting
    return enrollment
  })
}

function optionalVerification(record: Record<string, unknown>): { verification_state?: VerificationState; verification_error?: string | null; verification_reason?: string } {
  const extra: { verification_state?: VerificationState; verification_error?: string | null; verification_reason?: string } = {}
  if (typeof record.verification_reason === 'string' && ['adapter_unsupported', 'binding_incomplete', 'local_binding_missing', 'account_not_ready'].includes(record.verification_reason)) extra.verification_reason = record.verification_reason
  const state = optionalEnum(record.verification_state, VERIFICATION_STATES)
  if (state) extra.verification_state = state
  if (typeof record.verification_error === 'string' && record.verification_error) extra.verification_error = record.verification_error.slice(0, 500)
  else if (record.verification_error === null || record.verification_error === '') extra.verification_error = null
  return extra
}

function verification(value: unknown): VerificationTerms {
  const record = asRecord(value, 'verification')
  return {
    mode: record.mode == null ? null : oneOf(record.mode, VERIFICATION_MODES, 'verification.mode'),
    ...(typeof record.policy === 'string' && record.policy ? { policy: record.policy.slice(0, 64) } : {}),
    runs_per_account: finite(record.runs_per_account, 'runs_per_account'),
    max_parallel_runs: finite(record.max_parallel_runs, 'max_parallel_runs'),
    max_duration_seconds: finite(record.max_duration_seconds, 'max_duration_seconds'),
    allowance: finite(record.allowance, 'allowance'),
    unit: bounded(record.unit, 'unit', 32),
    expires_at: timestamp(record.expires_at, 'verification.expires_at'),
    task: bounded(record.task, 'task', 2000),
  }
}

function groupAccounts(accounts: readonly RequestedAccount[]): Map<string, RequestedAccount[]> {
  const groups = new Map<string, RequestedAccount[]>()
  for (const account of accounts) {
    const list = groups.get(account.harness) ?? []
    list.push(account)
    groups.set(account.harness, list)
  }
  return groups
}

function orderedSelection(accounts: readonly RequestedAccount[], selected: readonly string[]): { ok: true; keys: string[] } | { ok: false; message: string; next: string } {
  const known = new Map(accounts.map(account => [account.account_key, account]))
  const keys: string[] = []
  const harnesses = new Map<string, string | undefined>()
  const homes = new Set<string>()
  for (const account of accounts) {
    if (!selected.includes(account.account_key) || keys.includes(account.account_key)) continue
    const match = known.get(account.account_key)
    if (!match) continue
    if (harnesses.has(match.harness) && (!harnesses.get(match.harness) || !match.config_home_id)) {
      return { ok: false, message: `Choose one ${match.harness} account without isolation.`, next: 'Several accounts for the same harness require a separate isolated config home for each account.' }
    }
    if (match.config_home_id && homes.has(match.config_home_id)) {
      return { ok: false, message: 'Selected accounts share a config home.', next: 'Give each account a separate isolated config home.' }
    }
    harnesses.set(match.harness, match.config_home_id)
    if (match.config_home_id) homes.add(match.config_home_id)
    keys.push(account.account_key)
  }
  if (selected.some(key => !known.has(key))) {
    return { ok: false, message: 'One of the selected accounts is not part of this request.', next: 'Look the code up again and choose from the accounts the computer asked for.' }
  }
  if (!keys.length) return { ok: false, message: 'Choose at least one harness to connect.', next: 'Deselected harnesses are not enrolled.' }
  return { ok: true, keys }
}

function assertNoSecrets(value: unknown): void {
  if (!value || typeof value !== 'object') return
  for (const [key, child] of Object.entries(value)) {
    if (SECRET_KEYS.has(key)) {
      throw new PairingError(0, 'A pairing secret was blocked before it left the browser.', { code: 'invalid_request', next: 'Reload the page. The website never sends a device, runtime or lifecycle secret.' })
    }
    assertNoSecrets(child)
  }
}

const SECRET_KEYS = new Set([
  'device_secret', 'runtime_secret', 'lifecycle_secret', 'existing_lifecycle_secret',
  'device_hash', 'runtime_hash', 'lifecycle_hash', 'runtime_token', 'runtime_key',
  'device_secret_hash', 'runtime_key_hash', 'runtime_credential', 'device_credential',
])

function asRecord(value: unknown, field: string): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) invalid(field)
  return value as Record<string, unknown>
}
function asString(value: unknown, field: string): string {
  if (typeof value !== 'string' || !value.trim()) invalid(field)
  return value
}
function bounded(value: unknown, field: string, max: number): string {
  const text = asString(value, field)
  if (text.length > max) invalid(field)
  return text
}
function optionalBounded(value: unknown, field: string, max: number): string | null {
  if (value == null) return null
  return bounded(value, field, max)
}
function parseCapabilities(value: unknown, field: string): VerificationCapabilities | undefined {
  if (value == null) return undefined
  const record = asRecord(value, field)
  const caps: VerificationCapabilities = {}
  for (const [harness, item] of Object.entries(record)) {
    if (!TOKEN.test(harness)) invalid(field)
    const row = asRecord(item, field)
    if (typeof row.supported !== 'boolean') invalid(field)
    caps[harness] = {
      supported: row.supported,
      policy: typeof row.policy === 'string' ? row.policy.slice(0, 64) : '',
      reason: typeof row.reason === 'string' ? row.reason.slice(0, 500) : '',
    }
  }
  return caps
}
function uuid(value: unknown, field: string): string {
  const text = asString(value, field)
  if (!UUID.test(text)) invalid(field)
  return text
}
function optionalUuid(value: unknown, field: string): string | null {
  if (value == null) return null
  return uuid(value, field)
}
function digest(value: unknown): string {
  const text = asString(value, 'request_digest')
  if (!DIGEST.test(text)) invalid('request_digest')
  return text.toLowerCase()
}
function timestamp(value: unknown, field: string): string {
  const text = asString(value, field)
  if (!Number.isFinite(Date.parse(text))) invalid(field)
  return text
}
function token(value: unknown, field: string): string {
  const text = asString(value, field)
  if (!TOKEN.test(text)) invalid(field)
  return text
}
function finite(value: unknown, field: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) invalid(field)
  return value
}
function readAccounting(value: unknown, field: string): AccountingState | undefined {
  if (value == null || value === '') return undefined
  if (value === 'settled' || value === 'unconfirmed') return value
  if (typeof value === 'string') return 'unconfirmed'
  invalid(field)
}

function readProcess(value: unknown, field: string): ProcessState | undefined {
  if (value == null || value === '') return undefined
  if (value === 'unconfirmed' || value === 'drained') return value
  if (typeof value === 'string') return 'unconfirmed'
  invalid(field)
}

function optionalEnum<T extends string>(value: unknown, allowed: readonly T[]): T | undefined {
  if (typeof value !== 'string' || !value) return undefined
  return (allowed as readonly string[]).includes(value) ? value as T : undefined
}
function oneOf<T extends string>(value: unknown, allowed: readonly T[], field: string): T {
  if (typeof value !== 'string' || !(allowed as readonly string[]).includes(value)) invalid(field)
  return value as T
}
function stringList(value: unknown, field: string): string[] {
  if (!Array.isArray(value) || value.some(item => typeof item !== 'string' || !TOKEN.test(item))) invalid(field)
  return value as string[]
}
function uuidList(value: unknown, field: string): string[] {
  if (!Array.isArray(value)) invalid(field)
  return value.map(item => uuid(item, field))
}
function publicPrefix(value: unknown): string | null {
  if (value == null) return null
  if (typeof value !== 'string' || !PREFIX.test(value)) return null
  return value
}
function httpsUrl(value: unknown, field: string): string {
  const text = asString(value, field)
  let url: URL
  try { url = new URL(text) } catch { invalid(field) }
  if (url.protocol !== 'https:') invalid(field)
  return text
}

function httpOrigin(value: unknown, field: string): string {
  const text = asString(value, field)
  let url: URL
  try { url = new URL(text) } catch { invalid(field) }
  if (url.protocol !== 'https:' && url.protocol !== 'http:') invalid(field)
  return url.origin
}
function pathId(id: string): string {
  if (!UUID.test(id)) throw new PairingError(0, 'That pairing id is not valid.', { code: 'invalid_request', next: 'Refresh the page and choose the computer again.' })
  return encodeURIComponent(id)
}
function invalid(field: string): never {
  throw new PairingError(0, `The pairing response was incomplete (${field}).`, {
    code: 'invalid_response',
    next: 'Reload the page. If this continues, start a new pairing code on the computer.',
  })
}

// Pin readiness is separate from advisory hardware capability and connectivity.
export function touchIDConfirmation(view: Pick<PairingView, 'platform' | 'computer_state' | 'local_auth_pinned'>): string {
  if (view.computer_state === 'revoked') return 'Touch ID confirmation: unavailable (pairing disconnected)'
  if (view.platform !== 'darwin') return 'Touch ID confirmation: unsupported on this platform'
  if (view.local_auth_pinned === true) return 'Touch ID confirmation: ready (pairing key pinned)'
  if (view.local_auth_pinned === false) return 'Touch ID confirmation: needs pairing upgrade — run aeon-agentd status on this computer for its exact pairing command.'
  return 'Touch ID confirmation: pairing readiness unknown — run aeon-agentd status on this computer.'
}
