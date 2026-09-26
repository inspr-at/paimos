// SPDX-License-Identifier: AGPL-3.0-only
// Gates for the project-header ticket graph. The glimpse is a decoration:
// wide screens, motion allowed, enough tickets and at least one link.

export const GLIMPSE_MIN_WIDTH = 1280
export const GLIMPSE_MIN_TICKETS = 8
export const GLIMPSE_MIN_LINKS = 1

export function headerGlimpseAllowed(input: { enabled: boolean; reduced: boolean; wide: boolean; tickets: number }): boolean {
  return input.enabled && input.wide && !input.reduced && input.tickets >= GLIMPSE_MIN_TICKETS
}

export function headerGlimpseGraphReady(nodes: number, links: number): boolean {
  return nodes >= GLIMPSE_MIN_TICKETS && links >= GLIMPSE_MIN_LINKS
}
