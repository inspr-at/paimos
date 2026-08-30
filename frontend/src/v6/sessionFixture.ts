/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 *
 * PAI-854 — deterministic, development-only Paimos 6 preview contract.
 * These rows are product-session-shaped fixtures. They are deliberately not
 * derived from 5.x issue deliveries, runs, or harness-session API responses.
 */

export type Paimos6SessionStatus = 'working' | 'waiting' | 'quiet'
export type Paimos6ManagementMode = 'managed' | 'unmanaged'

export interface Paimos6SessionCapabilities {
  directSteer: boolean
  interrupt: boolean
  stop: boolean
  paimosSteer: boolean
}

export interface Paimos6SessionFixture {
  id: string
  title: string
  summary: string
  agent: string
  status: Paimos6SessionStatus
  statusLabel: string
  attention: boolean
  attentionReason: string | null
  node: { id: string; label: string } | null
  unread: number
  inboxSummary: string
  mode: Paimos6ManagementMode
  capabilities: Paimos6SessionCapabilities
}

export interface Paimos6PreviewContract {
  source: 'deterministic-local-fixture'
  live: false
  initialSelection: null
  mutationMode: 'local-noop'
  mobileSurface: 'responsive-web-stub'
  nativeAvailable: false
  pushAvailable: false
}

export const PAIMOS6_PREVIEW_CONTRACT: Paimos6PreviewContract = Object.freeze({
  source: 'deterministic-local-fixture',
  live: false,
  initialSelection: null,
  mutationMode: 'local-noop',
  mobileSurface: 'responsive-web-stub',
  nativeAvailable: false,
  pushAvailable: false,
})

export const PAIMOS6_SESSION_FIXTURES: readonly Paimos6SessionFixture[] = Object.freeze([
  {
    id: 'fixture-session-shell',
    title: 'Shape the six preview shell',
    summary: 'Checking the quiet home, truthful controls, and the 390px seam.',
    agent: 'codex:amy',
    status: 'working',
    statusLabel: 'Working · fixture heartbeat 2m ago',
    attention: false,
    attentionReason: null,
    node: { id: 'PAI-854', label: 'PAI-854 · Paimos 6.0 cut' },
    unread: 1,
    inboxSummary: 'One fixture note about spacing',
    mode: 'managed',
    capabilities: {
      directSteer: true,
      interrupt: true,
      stop: true,
      paimosSteer: true,
    },
  },
  {
    id: 'fixture-session-isolation',
    title: 'Confirm instance isolation',
    summary: 'Waiting for a human decision; attention is independent of selection.',
    agent: 'claude:jan',
    status: 'waiting',
    statusLabel: 'Waiting · fixture needs a decision',
    attention: true,
    attentionReason: 'Choose whether related-project inheritance is denied or namespaced.',
    node: { id: 'PAI-857', label: 'PAI-857 · Instance isolation firewall' },
    unread: 4,
    inboxSummary: 'Three questions and one proposed decision',
    mode: 'managed',
    capabilities: {
      directSteer: true,
      interrupt: true,
      stop: false,
      paimosSteer: true,
    },
  },
  {
    id: 'fixture-session-loose',
    title: 'Plan the operator morning',
    summary: 'A loose planning session that can attach to a node later.',
    agent: 'codex:ops',
    status: 'quiet',
    statusLabel: 'Quiet · fixture last activity 18m ago',
    attention: false,
    attentionReason: null,
    node: null,
    unread: 0,
    inboxSummary: 'Inbox clear in this fixture',
    mode: 'unmanaged',
    capabilities: {
      directSteer: false,
      interrupt: false,
      stop: false,
      paimosSteer: true,
    },
  },
])
