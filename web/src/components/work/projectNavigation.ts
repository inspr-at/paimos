// SPDX-License-Identifier: AGPL-3.0-only
import { defineAsyncComponent, type Component } from 'vue'
import type { RouteLocationNormalizedLoaded } from 'vue-router'
import type { IconName } from '../AppIcon.vue'

export interface ProjectTab { id: string; label: string; icon: IconName }
export const PROJECT_SECTIONS = [
  { id: 'tickets', label: 'Tickets', icon: 'ticket' },
  { id: 'journey', label: 'Journey', icon: 'journey' },
  { id: 'knowledge', label: 'Knowledge', icon: 'book' },
  { id: 'settings', label: 'Settings', icon: 'gear' },
] as const satisfies readonly ProjectTab[]
export type ProjectSection = typeof PROJECT_SECTIONS[number]['id']

export interface TicketViewDefinition extends ProjectTab {
  // Routing and the switch discover lazy renderers here. TicketGraphView takes
  // project/filters, emits open(ticketKey) for the shared panel, and emits state
  // with its loaded projection and visible subset for toolbar facets/counts.
  component?: Component
}
export const TICKET_VIEWS = [
  { id: 'list', label: 'List', icon: 'list' },
  { id: 'outline', label: 'Outline', icon: 'outline' },
  { id: 'graph', label: 'Graph', icon: 'graph', component: defineAsyncComponent(() => import('./TicketGraphView.vue')) },
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
  return section === 'knowledge' || section === 'journey' || section === 'settings' ? section : 'tickets'
}
