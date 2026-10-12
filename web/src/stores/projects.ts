// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import { getProjects, listNodes, updateNode, type ProjectPerson, type ProjectSummary, type WorkNode } from '../lib/api'
import { projectProgressPercent } from '../lib/projectColumns'
import { compareRevision } from '../lib/liveUpdates'
import { preferenceOwner } from '../lib/preferences'
import { projectDescription, projectRouteKey } from '../lib/work'

export interface Project extends ProjectSummary {
  routeKey: string
  description: string
  archived: boolean
  frozen: boolean
  cancelled: number
  percent: number
  people: ProjectPerson[]
  // The project node's own updated_at, for changes that must not overwrite another's.
  nodeUpdatedAt: string | null
}

// Projects are few and change rarely: one summaries request plus one list of the
// project nodes (for classic keys and descriptions), cached for the session and
// refreshed in the background when a page asks again after a minute.
export const useProjects = defineStore('projects', () => {
  const owner = preferenceOwner
  const summaries = ref<ProjectSummary[]>([])
  const details = ref(new Map<string, { routeKey: string; description: string; updatedAt: string }>())
  const loaded = ref(false)
  const loading = ref(false)
  const error = ref('')
  let request: Promise<void> | undefined
  let loadedAt = 0, generation = 0
  watch(owner, () => {
    generation++; summaries.value = []; details.value = new Map(); loaded.value = false
    loading.value = false; error.value = ''; loadedAt = 0; request = undefined
    if (owner.value) void load()
  }, { flush: 'sync' })

  const projects = computed<Project[]>(() => summaries.value.map(summary => {
    const detail = details.value.get(summary.id)
    const state = summary.state.toLowerCase()
    return {
      ...summary,
      routeKey: detail?.routeKey ?? summary.key,
      description: detail?.description ?? '',
      archived: state === 'archived' || state === 'deleted',
      frozen: state === 'frozen',
      cancelled: summary.cancelled ?? 0,
      people: summary.people ?? [],
      nodeUpdatedAt: detail?.updatedAt ?? null,
      // Cancelled and archived work leave the share: done out of open, doing and done.
      percent: projectProgressPercent(summary.open, summary.in_progress, summary.done),
    }
  }))

  function load(force = false): Promise<void> {
    if (!owner.value) return Promise.resolve()
    const started = generation
    if (request) return request
    if (loaded.value && !force && Date.now() - loadedAt < 60_000) return Promise.resolve()
    const knownIDs = new Set(summaries.value.map(project => project.id))
    loading.value = true
    if (!loaded.value) error.value = ''
    request = (async () => {
      try {
        const [summary, nodes] = await Promise.all([getProjects(true), listNodes({ kind: ['project'], limit: 500 })])
        if (started !== generation) return
        const map = new Map<string, { routeKey: string; description: string; updatedAt: string }>()
        const current = new Map(summaries.value.map(item => [item.id, item]))
        // Only a confirmed creation adds a summary while this read is pending.
        // Keep it and its node details until a read started after creation.
        const created = summaries.value.filter(item => !knownIDs.has(item.id))
        for (const project of created) {
          const detail = details.value.get(project.id)
          if (detail) map.set(project.id, detail)
        }
        const names = new Map<string, { title: string; state: string }>()
        for (const node of nodes.items) {
          const cached = details.value.get(node.id), shown = current.get(node.id)
          if (cached && shown && compareRevision(cached.updatedAt, node.updated_at) > 0) {
            map.set(node.id, cached); names.set(node.id, shown)
          } else {
            map.set(node.id, { routeKey: projectRouteKey(node.key, node.fields), description: projectDescription(node.body, node.fields), updatedAt: node.updated_at })
            names.set(node.id, node)
          }
        }
        const listedIDs = new Set(summary.items.map(item => item.id))
        summaries.value = [...summary.items.map(item => {
          const latest = names.get(item.id)
          return latest ? { ...item, title: latest.title, state: latest.state } : item
        }), ...created.filter(item => !listedIDs.has(item.id))]
        details.value = map
        loaded.value = true
        loadedAt = Date.now()
        error.value = ''
      } catch (e) {
        if (started === generation) error.value = e instanceof Error ? e.message : 'Projects could not be loaded'
      } finally {
        if (started === generation) { loading.value = false; request = undefined }
      }
    })()
    return request
  }

  function byRouteKey(key: string): Project | undefined {
    const wanted = key.toLowerCase()
    return projects.value.find(project => project.routeKey.toLowerCase() === wanted)
      ?? projects.value.find(project => project.key.toLowerCase() === wanted)
  }
  function byId(id: string): Project | undefined {
    return projects.value.find(project => project.id === id)
  }

  // A current settings read or confirmed write supplies this node revision.
  // A slower background read cannot put an earlier name or state back.
  function applyNode(node: WorkNode): void {
    if (compareRevision(node.updated_at, details.value.get(node.id)?.updatedAt) < 0) return
    summaries.value = summaries.value.map(summary => summary.id === node.id ? { ...summary, title: node.title, state: node.state } : summary)
    details.value = new Map(details.value).set(node.id, {
      routeKey: projectRouteKey(node.key, node.fields), description: projectDescription(node.body, node.fields), updatedAt: node.updated_at,
    })
  }

  // Only a confirmed project creation supplies this empty summary. A server
  // read started after creation replaces it; no sample counts are used for
  // existing projects.
  function addCreatedProject(node: WorkNode): void {
    if (!summaries.value.some(project => project.id === node.id)) summaries.value = [...summaries.value, {
      id: node.id, key: node.key, title: node.title, state: node.state,
      open: 0, in_progress: 0, done: 0, total: 0, people: [], last_activity: node.updated_at,
    }]
    applyNode(node)
  }

  // Archives or restores a project (its node state). The summary shows the new
  // state at once; a failure puts the old one back and rethrows.
  async function setState(id: string, state: string): Promise<void> {
    const index = summaries.value.findIndex(summary => summary.id === id)
    if (index === -1) return
    const before = summaries.value[index]
    summaries.value = summaries.value.map(summary => summary.id === id ? { ...summary, state } : summary)
    try {
      const updatedAt = details.value.get(id)?.updatedAt
      const node = await updateNode(id, { state }, updatedAt ? { ifUnmodifiedSince: updatedAt } : {})
      const detail = details.value.get(id)
      if (detail) details.value = new Map(details.value).set(id, { ...detail, updatedAt: node.updated_at })
    } catch (error) {
      summaries.value = summaries.value.map(summary => summary.id === id ? before : summary)
      throw error
    }
  }

  return { projects, loaded, loading, error, load, byRouteKey, byId, setState, applyNode, addCreatedProject }
})
