// SPDX-License-Identifier: AGPL-3.0-only
import type { NativeBinding, NativeEventPage, NativeSessionView, NativeStreamEvent } from '../nativeSessionContract'

/** Derived by the panel from the current authenticated identity and effective permissions. */
export interface ExpertViewer {
  tenantId: string
  principalId: string
  kind: 'person' | 'agent'
  canRead: boolean
  canWatch: boolean
  canReadStructured: boolean
}

/** Existing, server-approved owner consent; this component never creates or renews consent. */
export interface ExpertConsent {
  id: string
  ownerPersonId: string
  binding: NativeBinding
  active: boolean
}

export interface ExpertRequest {
  binding: NativeBinding
  generation: number
  revision: number
  principalId: string
  view: 'expert' | 'structured'
  consentId: string | null
  /** C0 Last-Event-ID, never a vendor token or a resume reference. */
  lastEventId: string | null
}

/** I2 supplies authenticated C0 attach/fetch-SSE/replay; abort must detach the viewer only.
 * Both methods return only the server's privacy-filtered C0 projection. No model/native I/O,
 * credentials, control capability, PTY input or consent mutation is part of this port.
 */
export interface NativeExpertTransport {
  stream(request: ExpertRequest, receive: (event: NativeStreamEvent) => void, signal: AbortSignal): Promise<void>
  events(request: ExpertRequest, before: string | null, limit: number, signal: AbortSignal): Promise<NativeEventPage>
}

export const bindingKey = (binding: NativeBinding | null) => binding ? JSON.stringify([
  binding.tenant_id, binding.project_id, binding.owned_session_id, binding.harness_session_id,
  binding.runtime_epoch, binding.daemon_id, binding.daemon_generation, binding.binding_revision, binding.run_id,
]) : ''

export function expertRequest(session: NativeSessionView, viewer: ExpertViewer, consent: ExpertConsent | null): ExpertRequest | null {
  const binding = session.current_binding
  if (!binding || !viewer.canRead || viewer.tenantId !== binding.tenant_id || !viewer.principalId || binding.owned_session_id !== session.owned_session_id) return null
  const approved = viewer.kind === 'person' && viewer.canWatch && session.capabilities.expert && consent?.active &&
    consent.id && consent.ownerPersonId === session.owner_person_id && bindingKey(consent.binding) === bindingKey(binding)
  const view = approved ? 'expert' : viewer.canReadStructured && session.capabilities.events_supported ? 'structured' : null
  return view ? { binding: { ...binding }, generation: session.generation, revision: session.revision,
    principalId: viewer.principalId, view, consentId: approved ? consent!.id : null, lastEventId: null } : null
}

/** Shared descriptor for I2's existing session-head overflow item (r2 + A1). */
export function liveTerminalLink(session: NativeSessionView, viewer: ExpertViewer) {
  const binding = session.current_binding
  if (viewer.kind !== 'person' || !viewer.canRead || !viewer.principalId || (binding && (viewer.tenantId !== binding.tenant_id || binding.owned_session_id !== session.owned_session_id))) return null
  const value = session.terminal_viewer_url
  if (!value || value.length > 2048) return null
  try {
    const url = new URL(value)
    if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash) return null
    return { href: url.href, label: 'Open live terminal', target: '_blank', rel: 'noopener noreferrer' } as const
  } catch { return null }
}
