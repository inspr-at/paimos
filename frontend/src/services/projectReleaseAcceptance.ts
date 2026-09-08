/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 */

import { api } from '@/api/client'

export type Gap = {
  gap_ref: string
  statement: string
}

export type Party = {
  party_ref: string
  kind: 'linked_user' | 'manual_email'
  user_id?: number | null
  email: string
  display_name: string
  roles: string[]
}

export type Confirmation = {
  party_ref: string
  decision: string
  source: string
  actor_user_id: number
  attestation?: string
  confirmed_at: string
}

export type EmailEvidence = {
  message_ref: string
  acceptance_revision: number
  recipient_party_refs: string[]
  state: 'pending' | 'sent' | 'failed'
  source: string
  recorded_at: string
  sent_at: string | null
  actor_user_id: number
  attestation?: string
  body_sha256: string
}

export type ReleaseRecord = {
  id: number
  project_id: number
  batch_id: number
  release_ref: string
  batch_key: string
  baseline_ref: string
  content_digest: string
  revision_seal: string
  artifact_digest: string
  artifact_coordinate: string
  version_scheme: string
  release_channel: string
  release_sequence: number
  version: string
  commit_sha: string
  state: string
  revision: number
  created_at: string
}

export type Acceptance = {
  id: number
  release: ReleaseRecord
  revision: number
  status: string
  operating_mode: string
  operating_mode_label: string
  agreement_ref: string
  disclosed_gaps: Gap[]
  required_party_refs: string[]
  delivery_party_ref: string
  operator_party_ref: string
  support_party_ref?: string | null
  parties: Party[]
  confirmations: Confirmation[]
  email_evidence: EmailEvidence[]
  preview_subject: string
  preview_body: string
  preview_revision: number
  missing: {
    confirmations: string[]
    email_coverage: string[]
    preview: boolean
    send: boolean
  }
  defaults: { agreement_ref: string; notes?: string }
  offer_disclaimer: string
}

export type StandingPolicy = {
  id: number
  policy_ref: string
  operating_mode: string
  content_digest: string
  revision_seal: string
  target_ref: string
  parties: string[]
  model_ref: string
  agreement_ref: string
  gaps: Gap[]
  release_channel: string
  artifact_digest: string
  bounded_use: string
  expires_at: string
  revoked_at?: string | null
  created_at: string
}

export type ConfigureRequest = {
  expected_revision?: number
  operating_mode: string
  agreement_ref: string
  disclosed_gaps: Gap[]
  parties: {
    party_ref: string
    kind: Party['kind']
    user_id?: number
    email: string
    display_name: string
    roles: string[]
  }[]
  delivery_party_ref: string
  operator_party_ref: string
  support_party_ref?: string | null
  required_party_refs: string[]
}

export function listReleaseRecords(projectId: number) {
  return api.get<ReleaseRecord[]>(`/projects/${projectId}/release-records`)
}

export function mintReleaseRecord(projectId: number, batchId: number) {
  return api.post<Acceptance>(`/projects/${projectId}/baseline-batches/batches/${batchId}/release-record`, {})
}

export function getReleaseAcceptance(projectId: number, releaseId: number) {
  return api.get<Acceptance>(`/projects/${projectId}/release-records/${releaseId}/acceptance`)
}

export function configureReleaseAcceptance(projectId: number, releaseId: number, body: ConfigureRequest) {
  return api.put<Acceptance>(`/projects/${projectId}/release-records/${releaseId}/acceptance`, body)
}

export function confirmReleaseAcceptance(projectId: number, releaseId: number, partyRef: string, attestation = '') {
  return api.post<Acceptance>(`/projects/${projectId}/release-records/${releaseId}/acceptance/confirm`, {
    party_ref: partyRef,
    attestation,
  })
}

export function saveAcceptancePreview(projectId: number, releaseId: number, subject: string, body: string) {
  return api.post<Acceptance>(`/projects/${projectId}/release-records/${releaseId}/acceptance/email/preview`, {
    subject,
    body,
  })
}

export function authorizeAcceptanceSend(
  projectId: number,
  releaseId: number,
  body: { request_key: string; recipient_party_refs: string[]; preview_revision: number; confirm_send: boolean },
) {
  return api.post<Acceptance>(`/projects/${projectId}/release-records/${releaseId}/acceptance/email/authorize-send`, body)
}

export function recordExternalAcceptanceEmail(
  projectId: number,
  releaseId: number,
  body: { request_key: string; recipient_party_refs: string[]; raw_message: string; attestation: string; attested_party_refs: string[] },
) {
  return api.post<Acceptance>(`/projects/${projectId}/release-records/${releaseId}/acceptance/email/record-external`, body)
}

export function listStandingPolicies(projectId: number) {
  return api.get<StandingPolicy[]>(`/projects/${projectId}/acceptance-standing-policies`)
}

export function approveStandingPolicy(projectId: number, body: Record<string, unknown>) {
  return api.post<StandingPolicy>(`/projects/${projectId}/acceptance-standing-policies`, body)
}

export function revokeStandingPolicy(projectId: number, policyId: number) {
  return api.post<StandingPolicy>(`/projects/${projectId}/acceptance-standing-policies/${policyId}/revoke`, {})
}

export function applyStandingPolicy(projectId: number, releaseId: number, policyId: number) {
  return api.post<Acceptance>(`/projects/${projectId}/release-records/${releaseId}/acceptance/apply-policy`, {
    policy_id: policyId,
  })
}

export function portalListReleaseRecords(projectId: number) {
  return api.get<ReleaseRecord[]>(`/portal/projects/${projectId}/release-records`)
}

export function portalGetReleaseAcceptance(projectId: number, releaseId: number) {
  return api.get<Acceptance>(`/portal/projects/${projectId}/release-records/${releaseId}/acceptance`)
}

export function portalConfirmReleaseAcceptance(projectId: number, releaseId: number, partyRef: string) {
  return api.post<Acceptance>(`/portal/projects/${projectId}/release-records/${releaseId}/acceptance/confirm`, {
    party_ref: partyRef,
    attestation: '',
  })
}

export async function downloadAcceptanceEvidence(projectId: number, releaseId: number, format: 'json' | 'eml' | 'html') {
  const res = await fetch(`/api/projects/${projectId}/release-records/${releaseId}/acceptance/evidence?format=${format}`, {
    credentials: 'include',
  })
  if (!res.ok) {
    throw new Error('export failed')
  }
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `release-acceptance.${format}`
  a.click()
  URL.revokeObjectURL(url)
}
