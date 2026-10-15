<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { PANEL_HEAD_TITLE_MIN, PANEL_TAB_LABEL, panelTabs, type LeadTab, type PanelTab } from '../../lib/leadPanel'
import { shortcutHint } from '../../lib/shortcuts'
import AppIcon from '../AppIcon.vue'
const props = defineProps<{ selected: PanelTab; unread: number; lead?: boolean; terminal?: boolean; watch?: boolean; pinned?: LeadTab | null }>()
const emit = defineEmits<{ select: [tab: PanelTab] }>()
const tabs = computed(() => panelTabs(!!props.lead, !!props.terminal, !!props.watch))
const panelId = (tab: PanelTab) => tab === 'chat' ? 'messages' : tab === 'session' ? 'overview' : tab
const icon = (tab: PanelTab) => tab === 'lead' ? 'tree' : tab === 'chat' ? 'message' : tab === 'events' ? 'list' : tab === 'terminal' ? 'terminal' : 'info'
const menu = ref<HTMLElement>()
let resize: ResizeObserver | undefined
onMounted(() => {
  const element = menu.value, head = element?.closest<HTMLElement>('.pane-bar')
  const identity = head?.querySelector<HTMLElement>('.pane-identity')
  if (!element || !head || !identity) return
  const fit = () => {
    // Measure the complete menu synchronously, then choose before paint. Only
    // opening/resizing changes its form; selection and unread counts cannot.
    element.classList.remove('icons')
    const css = getComputedStyle(head), identityCSS = getComputedStyle(identity)
    const siblings = [...head.children].filter(child => child !== identity)
    const glyphs = [...identity.children].filter(child => !child.classList.contains('pane-title'))
    const labelsAt = Math.ceil(PANEL_HEAD_TITLE_MIN + parseFloat(css.paddingLeft) + parseFloat(css.paddingRight) +
      parseFloat(css.columnGap) * (head.children.length - 1) + parseFloat(identityCSS.columnGap) * glyphs.length +
      [...siblings, ...glyphs].reduce((width, child) => width + child.getBoundingClientRect().width, 0))
    // A measured design token: includes the actual tab count, font and actions
    // (the sidebar button is absent when a split cannot fit).
    head.style.setProperty('--panel-head-labels-at', `${labelsAt}px`)
    element.classList.toggle('icons', head.getBoundingClientRect().width < labelsAt)
  }
  fit()
  resize = new ResizeObserver(fit)
  resize.observe(head)
})
onBeforeUnmount(() => resize?.disconnect())
function move(event: KeyboardEvent) {
  if (event.altKey || event.ctrlKey || event.metaKey || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault(); event.stopPropagation()
  const index = tabs.value.indexOf(props.selected)
  const next = event.key === 'Home' ? tabs.value[0]! : event.key === 'End' ? tabs.value.at(-1)! : tabs.value[(index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.value.length) % tabs.value.length]!
  emit('select', next)
  ;(event.currentTarget as HTMLElement).querySelector<HTMLElement>(`#session-tab-${panelId(next)}`)?.focus()
}
</script>

<template>
  <div ref="menu" class="session-tabs" role="tablist" :aria-label="lead ? 'Lead panel' : 'Session'" @keydown="move">
    <button v-for="(tab, index) in tabs" :id="`session-tab-${panelId(tab)}`" :key="tab" type="button" role="tab" :data-tab="tab" :aria-label="PANEL_TAB_LABEL[tab]" :data-tip="[PANEL_TAB_LABEL[tab], shortcutHint(`agents.panel.tab.${(index + 1) as 1 | 2 | 3 | 4 | 5}`)].filter(Boolean).join(' · ')" :aria-selected="selected === tab" :aria-controls="`session-panel-${panelId(tab)}`" :tabindex="selected === tab ? 0 : -1" @click="emit('select', tab)">
      <AppIcon :name="icon(tab)" :size="15" /><span class="tab-label"><span class="label-width" aria-hidden="true">{{ PANEL_TAB_LABEL[tab] }}</span><span>{{ PANEL_TAB_LABEL[tab] }}</span></span>
      <span v-if="tab === 'chat' && unread" class="count" :aria-label="`${unread} unread`">{{ unread > 99 ? '99+' : unread }}</span>
      <span v-if="tab === 'lead' && pinned === 'lead'" class="pinned" aria-label="Your default"><AppIcon name="pin" :size="10" /></span>
    </button>
  </div>
</template>

<style scoped>
.session-tabs { display: inline-flex; align-items: center; flex: none; gap: 2px; height: 32px; padding: 3px; border-radius: 999px; background: var(--seg-bg); }
button { position: relative; display: inline-flex; align-items: center; justify-content: center; gap: 6px; height: 26px; min-width: 28px; padding: 0 10px; border: 0; border-radius: 999px; background: transparent; color: var(--ink-2); font-size: 12.5px; font-weight: 600; white-space: nowrap; }
button:hover { color: var(--ink); background: var(--row-hover); }
button[aria-selected="true"] { color: var(--teal-ink); background: var(--seg-on); font-weight: 700; box-shadow: 0 1px 2px color-mix(in srgb, var(--shadow-black) 12%, transparent); }
button:focus-visible { outline: 2px solid var(--teal); outline-offset: -2px; }
.tab-label { display: inline-grid; }
.tab-label > span { grid-area: 1 / 1; }
.label-width { font-weight: 700; visibility: hidden; }
.count { position: absolute; right: 1px; top: -2px; display: grid; place-items: center; min-width: 14px; height: 14px; padding: 0 3px; border-radius: 999px; background: var(--primary); color: var(--primary-on); font: 700 9px var(--font); }
.pinned { position: absolute; right: 2px; bottom: 1px; display: inline-grid; color: var(--ink-3); }
.icons .tab-label { display: none; }
.icons button { padding-inline: 5px; }
@media (max-width: 720px) { .session-tabs { height: 44px; padding: 0 2px; } button { min-width: 44px; height: 44px; padding-inline: 5px; gap: 4px; font-size: 12px; } .tab-label, .icons .tab-label { display: inline-grid; } }
@media (max-width: 360px) { .tab-label { display: none; } }
@media (pointer: coarse) { .session-tabs { height: 44px; padding-block: 0; } button { height: 44px; min-width: 44px; } }
</style>
