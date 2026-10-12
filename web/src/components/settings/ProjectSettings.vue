<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, provide, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { Project } from '../../stores/projects'
import { useSession } from '../../stores/session'
import { scopeOwner } from '../../lib/identityScope'
import { can } from '../../lib/authz'
import { onAction, shortcutTyping } from '../../lib/shortcuts'
import { settingsSubmitKey } from '../../lib/settingsOverlays'
import { PROJECT_SETTINGS_OVERLAY, PROJECT_SETTINGS_SECTIONS, projectSettingsPath, projectSettingsSection, type ProjectSettingsOverlay, type ProjectSettingsSection } from '../../lib/projectSettings'
import { vClipTip } from '../../directives/clipTip'
import AppIcon from '../AppIcon.vue'
import SettingsCard from './SettingsCard.vue'
import ProjectGeneralSettings from './ProjectGeneralSettings.vue'

const RecurringWorkCard = defineAsyncComponent(() => import('../recurrences/RecurringWorkCard.vue'))
const ProjectWorkContextCard = defineAsyncComponent(() => import('./ProjectWorkContextCard.vue'))
const ProjectRepositorySettings = defineAsyncComponent(() => import('./ProjectRepositorySettings.vue'))
const ProjectDeliveryChecks = defineAsyncComponent(() => import('./ProjectDeliveryChecks.vue'))
const props = defineProps<{ project: Project }>()
const route = useRoute(), router = useRouter(), session = useSession()
const root = ref<HTMLElement>(), navColumn = ref<HTMLElement>(), picker = ref<HTMLButtonElement>()
const pickerOpen = ref(false), overlay = shallowRef<ProjectSettingsOverlay | null>(null)
const section = computed(() => projectSettingsSection(route.params.settingsSection))
const meta = computed(() => PROJECT_SETTINGS_SECTIONS.find(item => item.id === section.value)!)
const owner = computed(() => scopeOwner(session.identity))
const contextAllowed = computed(() => session.identity?.principal.kind === 'person' && can('account.use.manage'))
provide(PROJECT_SETTINGS_OVERLAY, value => {
  overlay.value?.close(); overlay.value = value
  return () => { if (overlay.value === value) overlay.value = null }
})
function closePicker(focus = false) { pickerOpen.value = false; if (focus) picker.value?.focus({ preventScroll: true }) }
function select(event: MouseEvent, id: ProjectSettingsSection) {
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return
  const wasOpen = pickerOpen.value
  closePicker()
  void router.push({ path: projectSettingsPath(props.project.routeKey, id) }).then(() => { if (wasOpen) picker.value?.focus({ preventScroll: true }) })
  event.preventDefault()
}
function outside(event: PointerEvent) {
  if (pickerOpen.value && !navColumn.value?.contains(event.target as Node)) closePicker()
}
function resize() { closePicker() }
function ownsKey(event: KeyboardEvent): boolean {
  if (!owner.value) return false
  const element = overlay.value?.element()
  if (element) return element.contains(event.target as Node)
  // Routines and unrelated menus retain their existing keyboard owner.
  return (section.value === 'general' || section.value === 'delivery' || pickerOpen.value) && !!root.value?.contains(event.target as Node)
    && !document.querySelector('dialog[open], .floating, [role="menu"]')
}
const disposeKeys = [
  onAction('project.settings.dismiss', event => {
    const element = overlay.value?.element()
    if (shortcutTyping(event.target)) {
      const field = event.target as HTMLElement
      field.blur()
      const frame = element ?? field.closest<HTMLElement>('form') ?? root.value
      if (frame) { frame.tabIndex = -1; frame.focus({ preventScroll: true }) }
    } else if (overlay.value) overlay.value.close()
    else if (pickerOpen.value) closePicker(true)
    else return false
  }, { enabled: () => !!owner.value, when: ownsKey }),
  onAction('project.settings.submit', event => {
    if (overlay.value) { if (!overlay.value.submit) return false; overlay.value.submit(); return }
    const form = (event.target as HTMLElement)?.closest<HTMLFormElement>('form[data-project-field]')
    if (!form) return false
    form.requestSubmit()
  }, { enabled: () => !!owner.value, when: event => ownsKey(event) && settingsSubmitKey(event) }),
]
let focusRun = 0
async function focusAnchor() {
  const run = ++focusRun
  await nextTick()
  if (run !== focusRun || !route.hash) return
  const target = root.value?.querySelector<HTMLElement>(`[id="${route.hash.slice(1)}"]`)
  if (target) { target.scrollIntoView({ block: 'nearest' }); if (target.matches('input, textarea')) target.focus({ preventScroll: true }) }
}
watch(() => route.fullPath, () => { overlay.value?.close(); closePicker(); void focusAnchor() }, { flush: 'post' })
watch(() => [props.project.id, owner.value], () => { overlay.value?.close(); closePicker() }, { flush: 'sync' })
onMounted(() => { window.addEventListener('pointerdown', outside, true); window.addEventListener('resize', resize); void focusAnchor() })
onBeforeUnmount(() => { focusRun++; overlay.value?.close(); disposeKeys.forEach(dispose => dispose()); window.removeEventListener('pointerdown', outside, true); window.removeEventListener('resize', resize) })
</script>

<template>
  <div ref="root" class="project-settings" :class="{ 'nav-open': pickerOpen }" tabindex="-1">
    <div ref="navColumn" class="nav-col">
      <button ref="picker" type="button" class="nav-picker" :aria-expanded="pickerOpen" aria-controls="project-settings-nav" :aria-label="`Section: ${meta.label}. Choose another section`" @click="pickerOpen = !pickerOpen">
        <span class="link-icon"><AppIcon :name="meta.icon" :size="15" /></span><span class="link-text"><span class="link-label">{{ meta.label }}</span><span class="link-summary">{{ meta.summary }}</span></span><AppIcon name="chevron" :size="14" />
      </button>
      <nav id="project-settings-nav" class="section-nav" aria-label="Project settings sections">
        <RouterLink v-for="item in PROJECT_SETTINGS_SECTIONS" :key="item.id" class="section-link" :data-sec="item.id" :to="projectSettingsPath(project.routeKey, item.id)" :aria-current="section === item.id ? 'page' : undefined" @click="select($event, item.id)">
          <span class="link-icon"><AppIcon :name="item.icon" :size="15" /></span><span class="link-text"><span class="link-label">{{ item.label }}</span><span v-clip-tip="item.summary" class="link-summary">{{ item.summary }}</span></span>
        </RouterLink>
      </nav>
    </div>
    <div class="settings-body">
      <ProjectGeneralSettings v-if="section === 'general'" :key="`${owner}/${project.id}`" :project="project" />
      <template v-else-if="section === 'agents'">
        <ProjectWorkContextCard v-if="contextAllowed" :key="`${owner}/${project.id}`" :project="project" />
        <SettingsCard v-else title="Work context" icon="shield" anchor="project-work-context" variant="project"><p class="owner-line">Owners and admins choose where accounts may work in Settings › Accounts.</p><RouterLink class="link-btn quiet" to="/settings/accounts">Settings › Accounts <AppIcon name="chevron-right" :size="12" /></RouterLink></SettingsCard>
        <div class="owner-links">
          <p id="lead">Lead policy and limits: <RouterLink to="/settings/agents">Settings <AppIcon name="chevron-right" :size="12" /> Agents <AppIcon name="chevron-right" :size="12" /></RouterLink></p>
          <p>Model choice: <RouterLink to="/settings/models">Settings › Models <AppIcon name="chevron-right" :size="12" /></RouterLink></p>
          <p>Project rules: <RouterLink to="/settings/agent-rules">Settings › Agent rules <AppIcon name="chevron-right" :size="12" /></RouterLink></p>
          <p>Accounts and contexts: <RouterLink to="/settings/accounts">Settings › Accounts <AppIcon name="chevron-right" :size="12" /></RouterLink></p>
          <p>Computers: <RouterLink to="/settings/computers">Settings › Computers <AppIcon name="chevron-right" :size="12" /></RouterLink></p>
          <p id="review">Cross-family review: <RouterLink to="/settings/policies#cross-family-review">Settings › Policies <AppIcon name="chevron-right" :size="12" /></RouterLink></p>
          <p>Status autopilot: <RouterLink to="/settings/autopilot#status-autopilot">Settings › Autopilot <AppIcon name="chevron-right" :size="12" /></RouterLink></p>
        </div>
      </template>
      <SettingsCard v-else-if="section === 'access'" title="Project access" icon="users" anchor="members" variant="project">
        <template #lead>Workspace roles reach this project. Project roles add rights here.</template>
        <p class="owner-line">People with See members permission can read project access. People with Manage members permission change it.</p>
        <RouterLink class="link-btn quiet" :to="`/settings/access/projects/${encodeURIComponent(project.id)}`">Settings › Access › Projects <AppIcon name="chevron-right" :size="12" /></RouterLink>
      </SettingsCard>
      <RecurringWorkCard v-else-if="section === 'routines'" :key="`${owner}/${project.id}`" :project="project" :selected-id="typeof route.query.recurrence === 'string' ? route.query.recurrence : undefined" :consent="route.query.consent === '1'" />
      <slot v-else name="delivery" :project="project">
        <template v-if="can('delivery.read', project.id)">
          <ProjectRepositorySettings :key="`repository/${owner}/${project.id}`" :project="project" />
          <ProjectDeliveryChecks :key="`checks/${owner}/${project.id}`" :project="project" />
          <p class="owner-line">The GitHub App belongs to this workspace’s setup; this page shows only whether it can read the repository.</p>
        </template>
        <SettingsCard v-else title="Repository & delivery" icon="link" anchor="repository" variant="project"><p class="owner-line">People with Read delivery permission can see this project’s repository, checks and deadlines.</p></SettingsCard>
      </slot>
    </div>
  </div>
</template>

<style scoped>
.project-settings { position: relative; display: grid; grid-template-columns: 236px minmax(0, 1fr); gap: 28px; align-items: start; container: project-settings / inline-size; }
.project-settings:focus { outline: none; }
.nav-col { align-self: stretch; min-width: 0; position: relative; }
.section-nav { position: sticky; top: 16px; display: grid; gap: 2px; }
.section-link { display: grid; grid-template-columns: 30px minmax(0, 1fr); align-items: center; gap: 10px; min-height: 50px; padding: 7px 10px; border-radius: 12px; color: var(--ink); text-decoration: none; }
.section-link:hover { background: var(--row-hover); }
.section-link[aria-current="page"] { background: var(--surface-raised); box-shadow: 0 0 0 1px var(--line), 0 6px 18px -12px color-mix(in srgb, var(--ink) 40%, transparent); }
.link-icon { display: grid; place-items: center; width: 30px; height: 30px; border-radius: 9px; background: var(--surface-2); color: var(--ink-2); }
.section-link[aria-current="page"] .link-icon { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.link-text { display: grid; min-width: 0; }
.link-label { font-weight: 600; font-size: 13.5px; }
.link-summary { font-size: 12px; color: var(--ink-2); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.nav-picker { display: none; }
.settings-body { display: grid; gap: 20px; min-width: 0; max-width: 820px; container: project-settings-body / inline-size; }
.owner-line, .owner-links { font-size: 12.5px; line-height: 1.5; color: var(--ink-3); }
.owner-line { margin-bottom: 12px; }
.owner-links { display: grid; gap: 8px; padding: 0 6px; }
.owner-links a, .link-btn { color: var(--ink-2); display: inline-flex; align-items: center; gap: 2px; }
@media (max-width: 1000px) {
  .project-settings { grid-template-columns: minmax(0, 1fr); gap: 14px; }
  .nav-picker { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 52px; padding: 8px 12px; border: 1px solid var(--glass-edge); border-radius: 12px; background: var(--glass); box-shadow: var(--shadow-btn); text-align: left; color: var(--ink); }
  .nav-picker .link-text { flex: 1; }
  .section-nav { display: none; position: absolute; z-index: 30; top: calc(100% + 8px); left: 0; right: 0; padding: 8px; border-radius: 14px; background: var(--surface-raised); box-shadow: var(--shadow-pop); }
  .nav-open .section-nav { display: grid; }
}
@media (pointer: coarse), (max-width: 600px) {
  .owner-links a, .link-btn, .settings-body :deep(.btn.sm), .settings-body :deep(select.field) { min-height: 44px; }
  .settings-body :deep(.icon-btn.sm) { width: 44px; height: 44px; }
}
</style>
