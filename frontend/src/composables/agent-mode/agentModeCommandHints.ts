/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 * AGPL-3.0-only — see LICENSE.
 */

import type { DetailLevel } from '@/components/agent-mode/AgentModeDetailLever.vue'
import type { HealthFilter } from './agentModeFilters'

export interface AgentModeCommandHint {
  id: string
  /** Exact deterministic grammar input sent through the typed-command path. */
  command: string
  /** Short visible label. Kept separate so grammar can evolve safely. */
  label: string
}

export interface AgentModeCommandHintContext {
  locale: string
  detailLevel: DetailLevel
  selected: boolean
  travelCount: number
  filtersActive: boolean
  health: HealthFilter
  blockedCount: number
  attentionCount: number
  staleCount: number
  candidateCount: number
  notePending: boolean
}

const hint = (id: string, command: string, label = command): AgentModeCommandHint => ({
  id,
  command,
  label,
})

/**
 * Produces only commands accepted by the deterministic PAI-808 grammar.
 * Context changes rebuild the list rather than letting presentation infer a
 * target or capability. Consequential PAI-809 controls are intentionally
 * absent: their server-owned challenge card already supplies exact wording.
 */
export function buildAgentModeCommandHints(
  context: AgentModeCommandHintContext,
): AgentModeCommandHint[] {
  const de = context.locale.toLowerCase().startsWith('de')
  if (context.notePending) {
    return de
      ? [hint('confirm-note', 'Notiz bestätigen'), hint('cancel-note', 'Notiz verwerfen')]
      : [hint('confirm-note', 'Confirm note'), hint('cancel-note', 'Cancel note')]
  }
  if (context.candidateCount > 0) {
    return Array.from({ length: Math.min(3, context.candidateCount) }, (_, index) => {
      const number = index + 1
      return hint(`candidate-${number}`, de ? `Kandidat ${number}` : `Candidate ${number}`)
    })
  }

  const hints: AgentModeCommandHint[] = []
  if (context.filtersActive) hints.push(hint('show-all', de ? 'Alle anzeigen' : 'Show all'))
  if (context.travelCount > 1) {
    hints.push(hint('next', de ? 'Weiter' : 'Next'))
    hints.push(hint('previous', de ? 'Zurück' : 'Previous'))
  }
  if (context.selected) {
    hints.push(hint('read-status', de ? 'Lies den Status' : 'Read status'))
    if (context.detailLevel !== 1)
      hints.push(hint('show-details', de ? 'Zeige Details' : 'Show details'))
  }
  if (context.blockedCount > 0 && context.health !== 'blocked') {
    hints.push(hint('show-blocked', de ? 'Zeige blockiert' : 'Show blocked'))
  }
  if (context.attentionCount > 0 && context.health !== 'attention') {
    hints.push(hint('show-attention', de ? 'Zeige Aufmerksamkeit' : 'Show attention'))
  }
  if (context.staleCount > 0 && context.health !== 'stale') {
    hints.push(hint('show-stale', de ? 'Zeige veraltet' : 'Show stale'))
  }
  if (context.detailLevel !== 100) hints.push(hint('overview', de ? 'Übersicht' : 'Overview'))
  if (context.detailLevel !== 10) hints.push(hint('detail-10', 'Detail 10'))
  return hints
}
