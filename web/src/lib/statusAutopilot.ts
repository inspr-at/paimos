// SPDX-License-Identifier: AGPL-3.0-only
import { api } from './api'
export type RuleKey = 'new' | 'backlog' | 'blocked' | 'progress' | 'done' | 'publish' | 'accept'
export interface Rule { enabled: boolean; days?: number }
export interface AutopilotSettings { enabled: boolean; rules: Record<RuleKey, Rule>; revision: number }
export interface ProjectOverride { mode: 'inherit' | 'on' | 'off'; effective_enabled: boolean; revision: number }
export interface AutomaticChange { event_id: number; node_id: string; key: string; title: string; actor: 'Status autopilot'; rule: RuleKey; reason: string; from: string; to: string; at: string; undone: boolean; undoable: boolean; changed_since?: boolean }
async function request<T>(path: string, body?: unknown): Promise<T> {
  const response = await api(path, body === undefined ? {} : { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!response.ok) throw new Error(response.status === 409 ? 'Another admin changed these settings. Reload to see their changes.' : 'Status autopilot could not be saved or loaded. Try again.')
  return response.json() as Promise<T>
}
export const getStatusAutopilot = () => request<AutopilotSettings>('/settings/status-autopilot')
export const saveStatusAutopilot = (s: AutopilotSettings) => request<AutopilotSettings>('/settings/status-autopilot', { enabled: s.enabled, rules: s.rules, expected_revision: s.revision })
export const getProjectAutopilot = (id: string) => request<ProjectOverride>(`/projects/${encodeURIComponent(id)}/status-autopilot`)
export const saveProjectAutopilot = (id: string, mode: ProjectOverride['mode'], revision: number) => request<ProjectOverride>(`/projects/${encodeURIComponent(id)}/status-autopilot`, { mode, expected_revision: revision })
export const getAutomaticChanges = (nodeId?: string) => request<{ items: AutomaticChange[] }>(`/status-autopilot/changes${nodeId ? `?node_id=${encodeURIComponent(nodeId)}` : ''}`)
export const getAutopilotSuggestions = () => request<{ items: AutomaticChange[] }>('/status-autopilot/changes?suggestions=true')
export const automaticTarget = (value: string) => ({ triage_list: 'Triage list', cancel_suggested: 'Cancel suggested', blocked_reminder: 'Reminder', missed_release: 'Missed release' } as Record<string, string>)[value] ?? ''
