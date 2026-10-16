// SPDX-License-Identifier: AGPL-3.0-only
import { defineAsyncComponent, type Component } from 'vue'
import type { RouteLocationNormalizedLoaded } from 'vue-router'
import type { IconName } from '../AppIcon.vue'

export interface ProjectTab { id: string; label: string; icon: IconName }
// One segment of a view switch (AEON-1453): a tab, a section or a .seg option.
export interface ViewOption { id: string; label: string; icon?: IconName; tip?: string; ariaLabel?: string; disabled?: boolean; data?: Record<string, string> }
export const PROJECT_SECTIONS = [
  { id: 'tickets', label: 'Tickets', icon: 'ticket' },
  { id: 'knowledge', label: 'Knowledge', icon: 'book' },
  { id: 'delivery', label: 'Delivery', icon: 'gauge' },
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

// A bare /p/KEY (the Projects list, the command palette): the router canonicalised it to
// the tickets, so this place may open the person's section instead (AEON-1453).
export function bareProjectEntry(route: Pick<RouteLocationNormalizedLoaded, 'params' | 'redirectedFrom'>): boolean {
  const from = route.redirectedFrom
  return !!from && !!from.params.projectKey && from.params.projectKey === route.params.projectKey && !from.params.ticketKey
    && !from.meta.projectSection && Object.keys(from.query).length === 0
}
export function projectSection(route: Pick<RouteLocationNormalizedLoaded, 'meta' | 'query'>): ProjectSection {
  const section = route.meta.projectSection ?? route.query.section
  return section === 'knowledge' || section === 'delivery' || section === 'settings' ? section : 'tickets'
}
