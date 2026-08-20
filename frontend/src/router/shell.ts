/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

// PAI-805 — route → application shell contract.
//
// App.vue picks the chrome around a route from its meta. The decision is
// kept pure here so it can be tested for parity: every route that does not
// opt into a reduced shell keeps the standard AppLayout exactly as before.
//
//   portal    → PortalLayout (external users)
//   public    → bare (login, reset, first-login)
//   agent     → AgentModeLayout (PAI-805 reduced shell: rail + canvas)
//   standard  → AppLayout (sidebar, header, footer chrome)

export type AppShell = 'agent'

export type AppLayoutKind = 'portal' | 'public' | 'agent' | 'standard'

export interface ShellMeta {
  portal?: boolean
  public?: boolean
  shell?: AppShell
}

export function resolveLayout(meta: ShellMeta | null | undefined): AppLayoutKind {
  if (!meta) return 'standard'
  if (meta.portal) return 'portal'
  if (meta.public) return 'public'
  if (meta.shell === 'agent') return 'agent'
  return 'standard'
}
