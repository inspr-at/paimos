// SPDX-License-Identifier: AGPL-3.0-only
import type { ListItem } from './api.ts'
import type { IconName } from '../components/AppIcon.vue'
import { kindLabel } from './work.ts'
export const WORK_ICONS = ['ticket', 'epic', 'task', 'layers', 'tree', 'folder', 'check', 'box'] as const
export interface WorkLevel { name: string; icon: string }
export interface WorkVocabulary { revision: number; leaf: WorkLevel; levels: WorkLevel[] }
// Keep custom spelling (including capitalized German nouns) in sentences.
export function workNoun(name: string): string { return ['Ticket', 'Epic', 'Task', 'Story'].includes(name) ? name.toLowerCase() : name }
export function workLevel(v: WorkVocabulary, leaf: boolean, depth: number): WorkLevel {
  const fallback = leaf ? { name: 'Ticket', icon: 'ticket' } : depth === 1 ? { name: 'Epic', icon: 'epic' } : { name: depth === 2 ? 'Story' : `Level ${depth}`, icon: 'layers' }
  const custom = leaf ? v.leaf : v.levels[depth - 1]
  return { name: custom?.name || fallback.name, icon: custom?.icon || fallback.icon }
}
export function workLabel(row: Pick<ListItem, 'kind_slug'> & { level_name?: string; is_leaf?: boolean; depth?: number }, vocabulary?: WorkVocabulary): string {
  return row.level_name || (row.kind_slug === 'work'
    ? workLevel(vocabulary ?? { revision: 0, leaf: { name: '', icon: '' }, levels: [] }, row.is_leaf !== false, row.depth ?? 1).name
    : kindLabel(row.kind_slug))
}
export function workIcon(row: Pick<ListItem, 'kind_slug'> & { level_icon?: string; is_leaf?: boolean }): IconName {
  if (WORK_ICONS.includes(row.level_icon as typeof WORK_ICONS[number])) return row.level_icon as IconName
  return row.is_leaf === false || row.kind_slug === 'epic' ? 'epic' : row.kind_slug === 'task' ? 'task' : 'ticket'
}
export function isWorkParent(row: { is_leaf?: boolean; kind_slug: string }): boolean { return row.is_leaf === false || (row.is_leaf === undefined && row.kind_slug === 'epic') }
export const WORK_KINDS = ['work', 'ticket', 'task', 'epic']
export function isWorkItem(row: { kind_slug: string }): boolean { return WORK_KINDS.includes(row.kind_slug) }
export function isWorkLeaf(row: { is_leaf?: boolean; kind_slug: string }): boolean { return isWorkItem(row) && !isWorkParent(row) }
