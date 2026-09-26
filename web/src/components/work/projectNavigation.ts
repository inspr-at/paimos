// SPDX-License-Identifier: AGPL-3.0-only
import type { Component } from 'vue'
import type { RouteLocationNormalizedLoaded } from 'vue-router'
import type { IconName } from '../AppIcon.vue'

export interface ProjectTab { id: string; label: string; icon: IconName }
export const PROJECT_SECTIONS = [
  { id: 'tickets', label: 'Tickets', icon: 'ticket' },
  { id: 'journey', label: 'Journey', icon: 'journey' },
  { id: 'knowledge', label: 'Knowledge', icon: 'book' },
] as const satisfies readonly ProjectTab[]
export type ProjectSection = typeof PROJECT_SECTIONS[number]['id']

export interface TicketViewDefinition extends ProjectTab {
  // TG1: add one entry with id 'graph' and its lazy component here. Routing and
  // the switch discover it automatically. The component receives project and
  // filters, and emits open(ticketKey) to use the shared ticket side panel.
  component?: Component
}
export const TICKET_VIEWS = [
  { id: 'list', label: 'List', icon: 'list' },
  { id: 'outline', label: 'Outline', icon: 'outline' },
] as const satisfies readonly TicketViewDefinition[]
export type TicketView = typeof TICKET_VIEWS[number]['id']
export function ticketView(value: unknown): TicketViewDefinition {
  return TICKET_VIEWS.find(view => view.id === value) ?? TICKET_VIEWS[0]
}
export const KNOWLEDGE_VIEWS = [
  { id: 'entries', label: 'Entries', icon: 'rows-comfortable' },
  { id: 'graph', label: 'Graph', icon: 'graph' },
] as const satisfies readonly ProjectTab[]

export function projectSection(route: Pick<RouteLocationNormalizedLoaded, 'meta' | 'query'>): ProjectSection {
  const section = route.meta.projectSection ?? route.query.section
  return section === 'knowledge' || section === 'journey' ? section : 'tickets'
}
