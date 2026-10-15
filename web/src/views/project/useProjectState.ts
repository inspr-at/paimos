// SPDX-License-Identifier: AGPL-3.0-only

import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { type ListItem } from '../../lib/api'
import { can } from '../../lib/authz'
import { useDensity } from '../../lib/prefs'
import { useDeveloperSettings } from '../../lib/developerSettings'
import { useProjectHeader } from '../../lib/useProjectHeader'
import { filtersFromQuery } from '../../lib/ticketList'
import type { TicketGraphState } from '../../lib/ticketGraphRenderer'
import { scopeOwner } from '../../lib/identityScope'
import { useProjects, type Project } from '../../stores/projects'
import { useSession } from '../../stores/session'
import { useWorkQueue } from '../../stores/workQueue'
import { useWorkVocabulary } from '../../stores/workVocabulary'
import { useReleases } from '../../stores/releases'
import { usePoller } from '../../lib/usePolledData'
import FilterSheet from '../../components/work/FilterSheet.vue'
import ListToolbar from '../../components/work/ListToolbar.vue'
import { PROJECT_SECTIONS, projectSection, ticketView, type ProjectSection, type TicketView } from '../../components/work/projectNavigation'
import TicketTable from '../../components/work/TicketTable.vue'
import TicketWorkspace from '../../components/work/TicketWorkspace.vue'
import { isKnowledgeType } from '../../lib/knowledge'
import { filtersFromQuery as knowledgeFiltersFrom } from '../../lib/useKnowledge'

export function useProjectState() {
  const route = useRoute()
  const router = useRouter()
  const projects = useProjects()
  const session = useSession()
  // Delivery is absent, not disabled, for people who may not read it (AEON-994).
  const projectSections = computed(() => PROJECT_SECTIONS.filter(item => item.id !== 'delivery' || (!!project.value && can('delivery.read', project.value.id))))

  const projectKey = computed(() => String(route.params.projectKey ?? ''))
  const ticketKey = computed(() => typeof route.params.ticketKey === 'string' ? route.params.ticketKey : '')
  // Recovery belongs only to this mounted editor, never to the authorized cache.
  // Its node copy retains the revision the draft was based on; the actual draft
  // stays in the same TicketWorkspace instance while all grants are revoked.
  const recovery = shallowRef<{
    owner: string; projectKey: string; ticketKey: string; project: Project
    person: { id: string; name: string }; item: ListItem
  } | null>(null)
  const recovering = computed(() => !!recovery.value && session.requiresSignIn && !session.identity
    && recovery.value.projectKey === projectKey.value && recovery.value.ticketKey === ticketKey.value)
  const authorizedProject = computed(() => projects.byRouteKey(projectKey.value))
  const project = computed(() => authorizedProject.value ?? (recovering.value ? recovery.value!.project : undefined))
  // Reads and their generation fences still follow the authorized cache only.
  const projectId = computed(() => authorizedProject.value?.id ?? null)
  const routeKey = computed(() => project.value?.routeKey ?? projectKey.value)
  const queue = useWorkQueue(), releases = useReleases()
  const vocabulary = useWorkVocabulary()
  const queueAnchor = ref<HTMLElement | null>(null)
  const assigneeMenu = ref<{ row: ListItem; anchor: HTMLElement } | null>(null)
  const queueSnapshot = computed(() => projectId.value ? queue.snapshots[projectId.value] : undefined)
  const queueError = computed(() => projectId.value ? queue.errors[projectId.value] : undefined)
  const mayQueue = computed(() => session.identity?.principal.kind === 'person' && can('run.create', projectId.value ?? undefined))
  const queuePoller = usePoller(() => projectId.value ? queue.load(projectId.value) : undefined, 20_000)
  watch(projectId, id => { queueAnchor.value = null; assigneeMenu.value = null; if (id) { void queue.load(id); void releases.load() } }, { immediate: true })
  onMounted(() => queuePoller.start())
  onBeforeUnmount(() => queuePoller.stop())
  function closeQueue(restore: boolean) { const anchor = queueAnchor.value; queueAnchor.value = null; if (restore) anchor?.focus() }
  const section = computed(() => projectSection(route))
  const { headerDensity } = useProjectHeader()
  const ticketsHeader = computed(() => section.value === 'tickets')
  const sectionPath = (value: ProjectSection = section.value) => `/p/${encodeURIComponent(routeKey.value)}/${value}`
  const ticketSectionQuery = (value: ProjectSection = section.value): Record<string, string> => value === 'tickets' ? {} : { section: value }
  const onKnowledge = () => section.value === 'knowledge'
  const filters = computed(() => filtersFromQuery(section.value !== 'tickets' ? {} : route.query))
  const { density, set: setDensity } = useDensity()
  // AEON-1042: the header graph is a developer opt-in, off by default. Off, the
  // glimpse is not mounted and the header keeps its plain geometry.
  const { showHeaderGraph, loading: developerLoading } = useDeveloperSettings()
  const headerGraph = computed(() => !developerLoading.value && showHeaderGraph.value)
  const glimpseShown = ref(false)
  const glimpseActive = computed(() => headerGraph.value && !graphActive.value && glimpseShown.value)
  const now = ref(Date.now())
  // Sections own their views. The registry also supplies TG1's optional renderer.
  type ViewMode = TicketView | 'knowledge' | 'settings' | 'delivery'
  const activeTicketView = computed(() => ticketView(route.query.view))
  // Knowledge has its own address: /p/KEY/knowledge, and /p/KEY/knowledge/<type>/<slug> for one entry.
  const knowledgeActive = computed(onKnowledge)
  const knowledgeType = computed(() => isKnowledgeType(route.params.knowledgeType) ? route.params.knowledgeType : null)
  const knowledgeSlug = computed(() => typeof route.params.slug === 'string' ? route.params.slug : '')
  const knowledgeEntryOpen = computed(() => knowledgeActive.value && !!knowledgeType.value && !!knowledgeSlug.value)
  const knowledgeFilters = computed(() => knowledgeFiltersFrom(route.query))
  // Keep the chosen view when closing an entry or editing its filters.
  const knowledgeView = computed(() => route.query.view === 'graph' ? 'graph' : 'entries')
  const knowledgeDisplay = computed(() => ({ view: knowledgeView.value }))
  const fullViewQuery = computed(() => !!ticketKey.value && (route.query.panel === 'full' || route.query.view === 'full'))
  const viewMode = computed<ViewMode>(() => section.value === 'tickets' ? activeTicketView.value.id as TicketView : section.value)
  const settingsActive = computed(() => section.value === 'settings')
  const deliveryActive = computed(() => section.value === 'delivery')
  const graphActive = computed(() => viewMode.value === 'graph')
  const graphState = ref<TicketGraphState>({ data: { nodes: [], links: [], truncated: false }, visible: { nodes: [], links: [], truncated: false }, loading: true })
  const ticketGraphView = ref<{ focus: () => void }>()
  const outlineActive = computed(() => viewMode.value === 'outline')
  const toolbarWrap = ref<HTMLElement>()
  const stickMark = ref<HTMLElement>()
  const toolbar = ref<InstanceType<typeof ListToolbar>>()
  const table = ref<InstanceType<typeof TicketTable>>()
  const panel = ref<InstanceType<typeof TicketWorkspace>>()
  const filterSheet = ref<InstanceType<typeof FilterSheet>>()
  const scrollRoot = ref<HTMLElement | null>(null)
  const toolbarHeight = ref(52)
  const stuck = ref(false)
  const cursorId = ref<string | null>(null)
  const collapsed = ref(new Set<string>())
  const statusMenu = ref<{ row: ListItem; anchor: HTMLElement; from: 'list' | 'panel' } | null>(null)
  const creating = ref(false)
  // Full page: the same ticket workspace in a two-column page instead of the side panel.
  const fullView = fullViewQuery
  const me = computed(() => session.identity ? { id: session.identity.principal.id, name: session.identity.principal.name } : null)
  const workspaceOwner = computed(() => recovering.value ? recovery.value!.owner : scopeOwner(session.identity))
  const workspacePerson = computed(() => recovering.value ? recovery.value!.person : me.value)
  const writable = computed(() => can('nodes.write', projectId.value ?? undefined))
  const nodeDeletable = computed(() => can('nodes.delete', projectId.value ?? undefined))
  const nodeMovable = computed(() => can('nodes.move', projectId.value ?? undefined))
  const relationLinkable = computed(() => can('relations.write', projectId.value ?? undefined))
  const relationUnlinkable = computed(() => can('relations.delete', projectId.value ?? undefined))
  const commentable = computed(() => can('comments.write', projectId.value ?? undefined))
  const commentDeletable = computed(() => can('comments.delete', projectId.value ?? undefined))
  const attachable = computed(() => can('attachments.write', projectId.value ?? undefined) && can('attachments.delete', projectId.value ?? undefined))
  const knowledgeWritable = computed(() => can('knowledge.write', projectId.value ?? undefined))
  const knowledgeDeletable = computed(() => can('knowledge.delete', projectId.value ?? undefined))

  const skipGuard = ref(false)
  function ticketPath(key: string) { return `/p/${encodeURIComponent(routeKey.value)}/${encodeURIComponent(key)}` }

  return { route, router, projects, session, projectSections, projectKey, ticketKey, recovery, recovering, authorizedProject, project, projectId, routeKey, queue, releases, vocabulary, queueAnchor, assigneeMenu, queueSnapshot, queueError, mayQueue, queuePoller, closeQueue, section, headerDensity, ticketsHeader, sectionPath, ticketSectionQuery, onKnowledge, filters, density, setDensity, showHeaderGraph, developerLoading, headerGraph, glimpseShown, glimpseActive, now, activeTicketView, knowledgeActive, knowledgeType, knowledgeSlug, knowledgeEntryOpen, knowledgeFilters, knowledgeView, knowledgeDisplay, fullViewQuery, viewMode, settingsActive, deliveryActive, graphActive, graphState, ticketGraphView, outlineActive, toolbarWrap, stickMark, toolbar, table, panel, filterSheet, scrollRoot, toolbarHeight, stuck, cursorId, collapsed, statusMenu, creating, fullView, me, workspaceOwner, workspacePerson, writable, nodeDeletable, nodeMovable, relationLinkable, relationUnlinkable, commentable, commentDeletable, attachable, knowledgeWritable, knowledgeDeletable, skipGuard, ticketPath }
}
