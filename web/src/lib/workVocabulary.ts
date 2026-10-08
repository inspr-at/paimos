// SPDX-License-Identifier: AGPL-3.0-only
import type { ListItem } from './api.ts'
import type { IconName } from '../components/AppIcon.vue'
import { kindLabel } from './work.ts'
export const WORK_ICONS = ['ticket', 'epic', 'task', 'layers', 'tree', 'folder', 'check', 'box'] as const
export interface WorkLevel { name: string; icon: string }
export interface LeadNames { singular: string; plural: string }
/** `lead` is absent while the workspace uses Lead/Leads (AEON-791). */
export interface WorkVocabulary { revision: number; leaf: WorkLevel; levels: WorkLevel[]; lead?: LeadNames }
// Keep custom spelling (including capitalized German nouns) in sentences.
export function workNoun(name: string): string { return ['Ticket', 'Epic', 'Task', 'Story'].includes(name) ? name.toLowerCase() : name }
export function workLevel(v: WorkVocabulary, leaf: boolean, depth: number): WorkLevel {
  const fallback = leaf ? { name: 'Ticket', icon: 'ticket' } : depth === 1 ? { name: 'Epic', icon: 'epic' } : { name: depth === 2 ? 'Story' : `Level ${depth}`, icon: 'layers' }
  const custom = leaf ? v.leaf : v.levels[depth - 1]
  return { name: custom?.name || fallback.name, icon: custom?.icon || fallback.icon }
}
/** The icon to draw for a stored choice: unknown or blank values draw the default. */
export function workIconChoice(icon: string, fallback: IconName): IconName {
  return WORK_ICONS.includes(icon as typeof WORK_ICONS[number]) ? icon as IconName : fallback
}
export const workIconName = (icon: string): string => icon.charAt(0).toUpperCase() + icon.slice(1)
/** Settings label of a level: levels[0] is the top level, the leaf comes last. */
export function vocabularyLabel(index: number, leaf = false): string {
  return leaf ? 'Leaf (work item)' : index === 0 ? 'Top level' : `Level ${index + 1}`
}
/** Where Arrow, Home and End keys move in a list of `count` options (-1: focus is outside the list); undefined for any other key. */
export function listKeyTarget(key: string, at: number, count: number): number | undefined {
  if (!count) return undefined
  if (key === 'Home') return 0
  if (key === 'End') return count - 1
  if (key === 'ArrowDown') return Math.min(count - 1, at + 1)
  if (key === 'ArrowUp') return Math.max(0, at - 1)
  return undefined
}
export interface VocabularyRow { key: string; leaf: boolean; index: number; depth: number; label: string; level: WorkLevel; placeholder: string; fallback: IconName }
/** The configured levels as they nest: top level first, the leaf last. */
export function vocabularyRows(v: WorkVocabulary): VocabularyRow[] {
  const row = (leaf: boolean, index: number): VocabularyRow => {
    const defaults = workLevel({ revision: 0, leaf: { name: '', icon: '' }, levels: [] }, leaf, index + 1)
    return { key: leaf ? 'leaf' : `level-${index + 1}`, leaf, index, depth: leaf ? v.levels.length : index, label: vocabularyLabel(index, leaf), level: leaf ? v.leaf : v.levels[index], placeholder: defaults.name, fallback: defaults.icon as IconName }
  }
  return [...v.levels.map((_, i) => row(false, i)), row(true, 0)]
}
/** What each configured level is called, top-down, ending with the leaf. */
export function vocabularyChain(v: WorkVocabulary): WorkLevel[] {
  return [...v.levels.map((_, i) => workLevel(v, false, i + 1)), workLevel(v, true, 1)]
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
