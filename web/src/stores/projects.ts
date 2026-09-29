// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { getProjects, listNodes, updateNode, type ProjectPerson, type ProjectSummary } from '../lib/api'
import { projectProgressPercent } from '../lib/projectColumns'
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
  const summaries = ref<ProjectSummary[]>([])
  const details = ref(new Map<string, { routeKey: string; description: string; updatedAt: string }>())
  const loaded = ref(false)
  const loading = ref(false)
  const error = ref('')
  let request: Promise<void> | undefined
  let loadedAt = 0

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
    if (request) return request
    if (loaded.value && !force && Date.now() - loadedAt < 60_000) return Promise.resolve()
    loading.value = true
    if (!loaded.value) error.value = ''
    request = (async () => {
      try {
        const [summary, nodes] = await Promise.all([getProjects(true), listNodes({ kind: ['project'], limit: 500 })])
        const map = new Map<string, { routeKey: string; description: string; updatedAt: string }>()
        for (const node of nodes.items) map.set(node.id, { routeKey: projectRouteKey(node.key, node.fields), description: projectDescription(node.body, node.fields), updatedAt: node.updated_at })
        summaries.value = summary.items
        details.value = map
        loaded.value = true
        loadedAt = Date.now()
        error.value = ''
      } catch (e) {
        error.value = e instanceof Error ? e.message : 'Projects could not be loaded'
      } finally {
        loading.value = false
        request = undefined
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

  return { projects, loaded, loading, error, load, byRouteKey, byId, setState }
})
