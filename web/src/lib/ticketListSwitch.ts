// SPDX-License-Identifier: AGPL-3.0-only
// The ticket list's view switch of one project (AEON-1453): List, Outline or Graph used
// last, and a default that may also be a saved view ("saved:<id>"). Before, a saved view
// the project opened with was kept in list:<id>.defaultView; it counts until the person
// sets or stops a default here, and it is never rewritten.
import { TICKET_VIEWS } from '../components/work/projectNavigation'
import { viewsOf } from './savedViews'
import { SAVED_VIEW, useViewSwitch, viewKey } from './viewSwitch'

export const savedViewId = (option: string | null | undefined) => option?.startsWith(SAVED_VIEW) ? option.slice(SAVED_VIEW.length) : null
export function useTicketListSwitch(projectId: () => string | null | undefined) {
  return useViewSwitch({
    key: () => { const id = projectId(); return id ? viewKey('tickets', id) : null },
    options: () => [...TICKET_VIEWS.map(item => item.id), ...viewsOf(projectId()).items.map(item => `${SAVED_VIEW}${item.id}`)],
    fallback: 'list',
    legacy: { key: () => { const id = projectId(); return id ? `list:${id}` : null }, read: value => ({ default: typeof value?.defaultView === 'string' ? `${SAVED_VIEW}${value.defaultView}` : null }) },
  })
}
