// SPDX-License-Identifier: AGPL-3.0-only
import document from '../../../internal/nodes/work_icons.json' with { type: 'json' }
import type { IconName } from '../components/AppIcon.vue'

/** An icon a work level may use (AEON-1142). The server and the API enum read the same list. */
export interface WorkIcon { name: IconName; label: string; tags: readonly string[] }
export const WORK_ICON_LIST: readonly WorkIcon[] = document.icons as WorkIcon[]
export const WORK_ICONS: readonly IconName[] = WORK_ICON_LIST.map(icon => icon.name)
const byName = new Map<string, WorkIcon>(WORK_ICON_LIST.map(icon => [icon.name, icon]))
export const isWorkIcon = (icon: string): icon is IconName => byName.has(icon)
export const workIconLabel = (icon: string): string => byName.get(icon)?.label ?? icon.charAt(0).toUpperCase() + icon.slice(1)

const words = (text: string) => text.toLowerCase().split(/[\s-]+/).filter(Boolean)
/**
 * Icons matching every word of `query`, best first: a word that starts a name or
 * label word ranks above one inside a name, which ranks above a search tag.
 * Ties keep the list order, so related icons stay together.
 */
export function searchWorkIcons(query: string, icons: readonly WorkIcon[] = WORK_ICON_LIST): WorkIcon[] {
  const terms = words(query.slice(0, 60)).slice(0, 6)
  if (!terms.length) return [...icons]
  const hits: { icon: WorkIcon; score: number; order: number }[] = []
  icons.forEach((icon, order) => {
    const named = [...words(icon.name), ...words(icon.label)], joined = named.join(' '), tagged = icon.tags.flatMap(words)
    let score = 0
    for (const term of terms) {
      if (named.some(word => word.startsWith(term))) score += 3
      else if (joined.includes(term)) score += 2
      else if (tagged.some(word => word.startsWith(term))) score += 1
      else return
    }
    hits.push({ icon, score, order })
  })
  return hits.sort((a, b) => b.score - a.score || a.order - b.order).map(hit => hit.icon)
}
