// SPDX-License-Identifier: AGPL-3.0-only
import type { NativeBinding, NativeSessionView, NativeStreamEvent } from '../nativeSessionContract'
import { bindingKey, type ExpertConsent, type ExpertViewer } from '../nativeExpert/ports'

export interface TerminalRequest {
  readonly binding: Readonly<NativeBinding>
  readonly generation: number
  readonly revision: number
  readonly principalId: string
  readonly consentId: string
  readonly view: 'terminal'
  readonly mode: 'view'
  readonly lastEventId: null
}

/** I2 supplies the authenticated, permission-filtered C0 view stream. Abort
 * detaches this viewer only. There is deliberately no input/resize/reply port. */
export interface NativeTerminalTransport {
  stream(request: TerminalRequest, receive: (event: NativeStreamEvent) => void, signal: AbortSignal): Promise<void>
}

/** Display metadata from I2's current controller projection, never a grant. */
export interface TerminalControl {
  binding: NativeBinding
  controllerEpoch: string
  source: 'web' | 'cli'
  holderName: string
  since: string
  atRisk: boolean
  remainingSeconds: number | null
}

export type TerminalAction = 'take' | 'return' | 'end' | 'cancel'
export interface TerminalActionView {
  kind: TerminalAction
  label: string
  disabled: boolean
  reason: string
  binding: NativeBinding
  revision: number
  controllerEpoch: string | null
}
export interface TerminalActionRequest {
  kind: TerminalAction
  binding: Readonly<NativeBinding>
  ownedSessionId: string
  revision: number
  controllerEpoch: string | null
}

export interface HolderMeasurement {
  binding: Readonly<NativeBinding>
  generation: number
  revision: number
  controllerEpoch: string
  clientId: string
  cols: number
  rows: number
}

export interface TerminalClock {
  setTimeout(callback: () => void, milliseconds: number): unknown
  clearTimeout(id: unknown): void
}
export const browserClock: TerminalClock = {
  setTimeout: (callback, milliseconds) => window.setTimeout(callback, milliseconds),
  clearTimeout: id => window.clearTimeout(id as number),
}

export function terminalRequest(session: NativeSessionView, viewer: ExpertViewer, consent: ExpertConsent | null): TerminalRequest | null {
  const binding = session.current_binding
  if (viewer.kind !== 'person' || !viewer.principalId || !viewer.canRead || !viewer.canWatch ||
      !binding || viewer.tenantId !== binding.tenant_id || binding.owned_session_id !== session.owned_session_id ||
      session.state !== 'taken' || session.mode !== 'terminal' || !session.capabilities.terminal_view ||
      !session.capabilities.qualification_digest || !consent?.active || !consent.id ||
      consent.ownerPersonId !== session.owner_person_id || bindingKey(consent.binding) !== bindingKey(binding)) return null
  return Object.freeze({ binding: Object.freeze({ ...binding }), generation: session.generation,
    revision: session.revision, principalId: viewer.principalId, consentId: consent.id, view: 'terminal', mode: 'view', lastEventId: null })
}

export function holderKey(session: NativeSessionView, viewer: ExpertViewer, control: TerminalControl | null, clientId: string): string {
  const lease = session.controller
  if (!lease || !clientId || !control || control.atRisk || control.source !== 'web' ||
      lease.actor_id !== viewer.principalId || lease.client_id !== clientId || lease.epoch !== control.controllerEpoch ||
      bindingKey(control.binding) !== bindingKey(session.current_binding)) return ''
  return JSON.stringify([bindingKey(session.current_binding), session.generation, lease.controller_id, lease.epoch, clientId])
}

/** I2's panel shortcut registry checks this before Esc, F or numbered tabs.
 * This view never forwards a key; the holder's local TUI remains the writer. */
export function isHolderTerminalTarget(target: EventTarget | null): boolean {
  return target instanceof Element && !!target.closest('[data-native-terminal-holder="true"]')
}
