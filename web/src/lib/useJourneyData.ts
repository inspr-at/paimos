// SPDX-License-Identifier: AGPL-3.0-only
import { computed, onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue'
import { APIError, getNode, type ListItem, type WorkNode } from './api'
import { listKnowledge, type KnowledgeItem } from './knowledge'
import {
  addPlanTicket, getHandoff, getIntake, getRequirements, getWalker, listReleases, listWork, putPlan, releaseRefs,
  type Handoff, type Intake, type Journey, type ReleaseRef, type Requirement, type Walker,
} from './journey'

// What the Journey view reads besides the projection, loaded when a stage needs
// it: intake, requirements, releases, the release walker, the project's work
// (epics, states) and the deploy and access handoffs.
type Status = 'idle' | 'loading' | 'ready' | 'error'
interface Slot<T> { value: Ref<T>; status: Ref<Status>; error: Ref<string> }
function slot<T>(initial: T): Slot<T> { return { value: shallowRef(initial) as Ref<T>, status: ref<Status>('idle'), error: ref('') } }
const message = (e: unknown, fallback: string) => e instanceof Error ? e.message : fallback
export interface PlanOwner { readonly projectId: string; readonly releaseId: string; readonly generation: number }

export function useJourneyData(projectId: Ref<string | null>, journey: Ref<Journey | null>) {
  const intake = slot<Intake>({ sources: [], turns: [], drafts: [] })
  const requirements = slot<Requirement[]>([])
  const releaseNodes = slot<WorkNode[]>([])
  const work = slot<ListItem[]>([])
  const walker = slot<Walker | null>(null)
  const handoffs = slot<Handoff[]>([])
  // What an imported project brought: its own node (the description) and its knowledge.
  const origin = slot<{ node: WorkNode | null; knowledge: KnowledgeItem[] }>({ node: null, knowledge: [] })
  let generation = 0

  async function fill<T>(target: Slot<T>, read: () => Promise<T>, fallback: string, force = false) {
    if (!force && (target.status.value === 'ready' || target.status.value === 'loading')) return
    const mine = generation
    target.status.value = 'loading'
    try {
      const value = await read()
      if (mine !== generation) return
      target.value.value = value; target.status.value = 'ready'; target.error.value = ''
    } catch (e) {
      if (mine !== generation) return
      target.status.value = 'error'; target.error.value = message(e, fallback)
    }
  }
  const id = () => projectId.value!
  // Before the journey records anything (a project that never took a journey step),
  // intake and requirements answer 404: that is "nothing yet", not a failure.
  const orEmpty = <T>(read: Promise<T>, empty: T) => read.catch(e => { if (e instanceof APIError && e.status === 404) return empty; throw e })
  const loadIntake = (force = false) => projectId.value ? fill(intake, () => orEmpty(getIntake(id()), { sources: [], turns: [], drafts: [] }), 'The sources could not be loaded.', force) : Promise.resolve()
  const loadRequirements = (force = false) => projectId.value ? fill(requirements, () => orEmpty(getRequirements(id()), []), 'The requirements could not be loaded.', force) : Promise.resolve()
  const loadReleases = (force = false) => projectId.value ? fill(releaseNodes, () => listReleases(id()), 'The releases could not be loaded.', force) : Promise.resolve()
  const loadWork = (force = false) => projectId.value ? fill(work, () => listWork(id()), 'The tickets could not be loaded.', force) : Promise.resolve()
  const loadOrigin = (force = false) => projectId.value ? fill(origin, async () => {
    // Each part is optional: a missing description or knowledge list leaves the rest standing.
    const [node, knowledge] = await Promise.all([getNode(id()).catch(() => null), listKnowledge({ project_id: id(), limit: 50 }).then(page => page.items).catch(() => [] as KnowledgeItem[])])
    return { node, knowledge }
  }, 'What came with the project could not be loaded.', force) : Promise.resolve()
  const loadHandoffs = (force = false) => {
    const ids = [...new Set((journey.value?.stages ?? []).map(s => s.handoff_id).filter((x): x is string => !!x))]
    return fill(handoffs, async () => (await Promise.allSettled(ids.map(getHandoff))).flatMap(r => r.status === 'fulfilled' ? [r.value] : []), 'The handoffs could not be loaded.', force)
  }
  // The walker of one release (the current one unless another is chosen).
  const walkerRelease = ref<string | null>(null)
  const planOwner = shallowRef<PlanOwner | null>(null)
  onScopeDispose(() => { planOwner.value = null })
  let walkerGeneration = 0
  function isPlanOwnerCurrent(owner: PlanOwner) {
    return planOwner.value?.generation === owner.generation && projectId.value === owner.projectId && walkerRelease.value === owner.releaseId
  }
  function capturePlanOwner() {
    const owner = planOwner.value, current = walker.value.value
    return owner && isPlanOwnerCurrent(owner) && walker.status.value === 'ready' && current?.project_node_id === owner.projectId && current.release_node_id === owner.releaseId ? owner : null
  }
  async function loadWalker(releaseId: string | null, force = false) {
    if (!projectId.value || !releaseId) { planOwner.value = null; walker.value.value = null; walker.status.value = 'idle'; walkerRelease.value = null; return }
    if (!force && walkerRelease.value === releaseId && walker.status.value === 'ready') return
    walkerRelease.value = releaseId
    const owner: PlanOwner = { projectId: projectId.value, releaseId, generation: ++walkerGeneration }
    planOwner.value = owner
    walker.status.value = 'loading'
    walker.error.value = ''
    try {
      const value = await getWalker(owner.projectId, owner.releaseId)
      if (!isPlanOwnerCurrent(owner)) return
      walker.value.value = value; walker.status.value = 'ready'
    } catch (e) {
      if (!isPlanOwnerCurrent(owner)) return
      walker.value.value = null; walker.status.value = 'error'
      // Some imported releases have no journey record yet; say so rather than "not found".
      walker.error.value = e instanceof APIError && e.status === 404 ? 'This release has no plan record yet (it came from Paimos without one), so its tickets cannot be listed here.' : message(e, 'This release could not be loaded.')
    }
  }
  // The plan write: the whole order and the included set, on the walker's revision.
  async function savePlan(owner: PlanOwner, order: string[], included: string[]) {
    if (!isPlanOwnerCurrent(owner)) return null
    const current = walker.value.value
    if (!current || current.project_node_id !== owner.projectId || current.release_node_id !== owner.releaseId) throw new Error('No release is loaded.')
    const saved = await putPlan(owner.projectId, owner.releaseId, { expected_revision: current.revision, ordered_ticket_ids: order, included_ticket_ids: included })
    if (!patchWalker(saved, owner)) return null
    return saved
  }
  function patchWalker(next: Walker, owner = capturePlanOwner()) {
    if (!owner || !isPlanOwnerCurrent(owner) || next.project_node_id !== owner.projectId || next.release_node_id !== owner.releaseId) return false
    walker.value.value = next
    return true
  }
  // A ticket added while planning: the server creates it and answers with the walker.
  async function addTicket(owner: PlanOwner, title: string, featureId: string | null, included: boolean) {
    if (!isPlanOwnerCurrent(owner)) return null
    const current = walker.value.value
    if (!current || current.project_node_id !== owner.projectId || current.release_node_id !== owner.releaseId) throw new Error('No release is loaded.')
    const saved = await addPlanTicket(owner.projectId, owner.releaseId, { title, feature_node_id: featureId, included, expected_revision: current.revision, idempotency_key: crypto.randomUUID() })
    if (!patchWalker(saved, owner)) return null
    void loadWork(true)
    return saved
  }

  watch(projectId, () => {
    generation++
    planOwner.value = null
    for (const s of [intake, requirements, releaseNodes, work, handoffs, walker, origin] as Slot<unknown>[]) { s.status.value = 'idle'; s.error.value = '' }
    intake.value.value = { sources: [], turns: [], drafts: [] }; requirements.value.value = []; releaseNodes.value.value = []; origin.value.value = { node: null, knowledge: [] }
    work.value.value = []; handoffs.value.value = []; walker.value.value = null; walkerRelease.value = null
  }, { flush: 'sync' })

  const releases = computed<ReleaseRef[]>(() => releaseRefs(releaseNodes.value.value))
  const workById = computed(() => new Map(work.value.value.map(item => [item.id, item])))
  const epicOf = (ticketId: string) => {
    const item = workById.value.get(ticketId)
    if (!item) return null
    if (item.epic) return item.epic
    return item.parent?.kind_slug === 'epic' ? { id: item.parent.id, key: item.parent.key, title: item.parent.title } : null
  }
  return {
    intake, requirements, releaseNodes, releases, work, workById, epicOf, walker, walkerRelease, planOwner, capturePlanOwner, isPlanOwnerCurrent, handoffs, origin,
    loadIntake, loadRequirements, loadReleases, loadWork, loadWalker, loadHandoffs, loadOrigin, savePlan, patchWalker, addTicket,
  }
}
export type JourneyData = ReturnType<typeof useJourneyData>
