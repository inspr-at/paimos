<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import AppIcon, { type IconName } from './AppIcon.vue'
import { useDeskConnections, type InstanceRead } from '../stores/deskConnections'
import { displayLanguage } from '../lib/displayLanguage'
import { relativeTime } from '../lib/work'
import { vClipTip } from '../lib/clipTip'

const props = withDefaults(defineProps<{ read: InstanceRead; variant?: 'goals' | 'desk' | 'preview' }>(), { variant: 'desk' })
const emit = defineEmits<{ navigate: [] }>()
const store = useDeskConnections()
const root = ref<HTMLElement>(), returnFocus = ref(false)
const text = (en: string, de: string) => displayLanguage() === 'de' ? de : en
const time = computed(() => props.read.summary ? new Date(props.read.summary.asOf).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '')
const message = computed(() => {
  const row = props.read
  switch (row.state) {
    case 'not connected': return text('see what waits for you there too', 'auch dort Wartendes anzeigen')
    case 'connecting': return text('confirm in the tab that opened', 'im geöffneten Tab bestätigen')
    case 'reading': return text('checking…', 'wird geprüft…')
    case 'ended': return text('connection ended', 'Verbindung beendet')
    case 'unreachable': return text(`couldn't be reached${time.value ? ` · showing ${time.value}` : ''}`, `nicht erreichbar${time.value ? ` · Stand ${time.value}` : ''}`)
    case 'ok': return row.summary?.open ? text(`${row.summary.open} wait for you`, `${row.summary.open} warten auf dich`) : text('nothing waits for you', 'nichts wartet auf dich')
  }
})
const headerMessage = computed(() => props.variant !== 'goals' && props.read.state === 'ok' ? String(props.read.summary?.open ?? 0) : message.value)
const action = computed(() => {
  switch (props.read.state) {
    case 'not connected': return text('Connect', 'Verbinden')
    case 'connecting': return text('Cancel', 'Abbrechen')
    case 'ended': return text('Reconnect', 'Neu verbinden')
    case 'unreachable': return text('Try again', 'Erneut versuchen')
    case 'ok': return props.variant === 'goals' && props.read.summary?.open ? (props.read.expanded ? text('Hide', 'Ausblenden') : text('Show', 'Anzeigen')) : props.variant === 'desk' && (props.read.summary?.open ?? 0) > 5 && !props.read.allItems ? text(`Show all ${props.read.summary!.open}`, `Alle ${props.read.summary!.open} anzeigen`) : ''
    default: return ''
  }
})
function act() {
  const { tag, state } = props.read
  if (state === 'not connected' || state === 'ended') { returnFocus.value = true; store.connect(tag) }
  else if (state === 'connecting') { returnFocus.value = false; store.cancel(tag) }
  else if (state === 'unreachable') void store.refresh(true)
  else if (state === 'ok' && props.variant === 'desk') void store.showAll(tag)
  else if (state === 'ok') store.expand(tag)
}
watch(() => props.read.state, state => {
  if (!returnFocus.value || !['ok', 'ended', 'unreachable'].includes(state)) return
  returnFocus.value = false
  void nextTick(() => (root.value?.querySelector<HTMLElement>('.instance-action') ?? root.value)?.focus({ preventScroll: true }))
})
const visible = computed(() => props.variant !== 'goals' || props.read.state !== 'not connected' || props.read.hiddenUntil <= Date.now())
const showItems = computed(() => props.variant !== 'goals' || props.read.expanded)
const lines = computed(() => [...(props.read.summary?.lines ?? [])].sort((a, b) => Number(b.held) - Number(a.held)).slice(0, props.variant === 'desk' && props.read.allItems ? 100 : 5))
const kindIcon = (kind: string): IconName => ({ approval: 'clock', action_request: 'send', doctrine: 'book', tier_request: 'gauge', key_trim: 'key', stepup: 'shield', account_matrix: 'users', routine_consent: 'repeat', lesson: 'book', definition_change: 'history' } as Record<string, IconName>)[kind] ?? 'help'
const tooltip = computed(() => `${props.read.tag} · ${new URL(props.read.origin).host}${props.read.account ? ` · ${text('as', 'als')} ${props.read.account}` : ''}`)
const announcement = computed(() => ['ok', 'unreachable', 'ended'].includes(props.read.state) ? `${props.read.tag} · ${message.value}` : '')
</script>

<template>
  <section v-if="visible" ref="root" class="instance-waiting" :class="variant" role="group" tabindex="-1" :aria-label="read.tag" :data-state="read.state" :data-testid="`instance-${read.tag}`">
    <div class="instance-line">
      <AppIcon name="server" :size="16" />
      <span class="instance-copy" :data-tip="`${read.tag} · ${message}`"><b :data-tip="tooltip">{{ variant === 'goals' ? read.tag : text(`On ${read.tag}`, `Auf ${read.tag}`) }}</b><span aria-hidden="true"> · </span>{{ headerMessage }}</span>
      <span class="instance-actions">
        <span class="action-slot">
          <span v-for="label in [text('Reconnect', 'Neu verbinden'), text('Try again', 'Erneut versuchen')]" :key="label" class="action-size" aria-hidden="true">{{ label }}</span>
          <span v-if="variant === 'desk' && (read.summary?.open ?? 0) > 5" class="action-size" aria-hidden="true">{{ text('Show all 0000000000', 'Alle 0000000000 anzeigen') }}</span>
          <button v-if="action" type="button" class="instance-action" @click="act">{{ action }}</button>
        </span>
        <button v-if="variant === 'goals' && read.state === 'not connected'" type="button" class="instance-dismiss" :aria-label="text('Not now', 'Nicht jetzt')" @click="store.dismiss(read.tag)"><AppIcon name="close" :size="14" /></button>
        <span v-else-if="variant === 'goals'" class="instance-dismiss-spacer" aria-hidden="true" />
        <a v-else-if="variant === 'desk'" class="instance-desk" :href="`${read.origin}/decision-desk`" target="_blank" rel="noopener noreferrer" @click="emit('navigate')">{{ text(`Open ${read.tag} Desk`, `${read.tag}-Desk öffnen`) }}<AppIcon name="external" :size="13" /></a>
      </span>
    </div>
    <span class="sr-only" aria-live="polite">{{ announcement }}</span>
    <div v-if="showItems" class="instance-items" :style="!read.summary && read.slots ? { minHeight: `${read.slots * 44}px` } : undefined">
      <p v-if="read.state === 'unreachable' && read.summary" class="instance-asof">{{ text(`As of ${time}`, `Stand ${time}`) }}</p>
      <a v-for="line in lines" :key="`${line.kind}:${line.id}`" class="instance-item" :href="line.url" target="_blank" rel="noopener noreferrer" :aria-label="`${line.title} · ${text(`Decide on ${read.tag}`, `Auf ${read.tag} entscheiden`)}`" @click="emit('navigate')">
        <AppIcon :name="kindIcon(line.kind)" :size="14" />
        <template v-if="variant === 'goals'">
          <span v-clip-tip class="instance-title">{{ line.title }}</span>
          <span class="instance-project">{{ line.projectKey }}</span>
          <time v-if="line.createdAt" :datetime="line.createdAt">{{ relativeTime(line.createdAt, { now: Date.parse(read.summary!.asOf) }) }}</time>
        </template>
        <span v-else class="instance-body"><span v-clip-tip class="instance-title">{{ line.title }}</span><small><span>{{ line.projectKey }}</span><span v-if="variant === 'preview'" class="instance-tag">{{ read.tag }}</span><time v-if="line.createdAt" :datetime="line.createdAt">{{ relativeTime(line.createdAt, { now: Date.parse(read.summary!.asOf) }) }}</time></small></span>
        <span class="instance-decide"><span v-if="variant === 'desk'" class="decide-words">{{ text(`Decide on ${read.tag}`, `Auf ${read.tag} entscheiden`) }}</span><AppIcon name="external" :size="13" /></span>
      </a>
      <p v-if="variant === 'desk' && read.allItems && (read.summary?.open ?? 0) > lines.length" class="instance-asof">{{ text(`${lines.length} of ${read.summary!.open} shown. The rest are on the home Desk.`, `${lines.length} von ${read.summary!.open} angezeigt. Weitere Einträge stehen auf dem eigenen Desk.`) }}</p>
      <a v-if="variant === 'goals' && read.summary?.open" class="instance-desk" :href="`${read.origin}/decision-desk`" target="_blank" rel="noopener noreferrer">{{ text(`Show all ${read.summary.open} on ${read.tag}`, `Alle ${read.summary.open} auf ${read.tag} anzeigen`) }}<AppIcon name="external" :size="13" /></a>
      <a v-if="variant === 'preview'" class="instance-desk" :href="`${read.origin}/decision-desk`" target="_blank" rel="noopener noreferrer" @click="emit('navigate')">{{ text(`Open ${read.tag} Desk`, `${read.tag}-Desk öffnen`) }}<AppIcon name="external" :size="13" /></a>
    </div>
  </section>
</template>

<style scoped>
.instance-waiting { grid-column: 1 / -1; min-width: 0; border-top: 1px solid var(--line); }
.instance-line { display: flex; align-items: center; gap: 8px; height: 44px; }
.instance-line > svg { flex: none; color: var(--ink-3); }
.instance-copy { flex: 1; min-width: 0; color: var(--ink-2); font-size: 13px; line-height: 18px; }
.instance-copy b { color: var(--ink); font-weight: 600; }
.instance-actions { display: flex; align-items: center; flex: none; gap: 6px; }
.action-slot { display: grid; justify-items: end; align-items: center; }
.action-slot > * { grid-area: 1 / 1; }
.action-size { visibility: hidden; padding: 0 4px; font-size: 13px; font-weight: 600; }
.instance-action, .instance-dismiss { display: inline-flex; align-items: center; justify-content: center; min-height: 44px; padding: 0 4px; border: 0; background: transparent; color: var(--teal-ink); font: inherit; font-size: 13px; font-weight: 600; cursor: pointer; }
.instance-action { width: 100%; }
.instance-dismiss, .instance-dismiss-spacer { width: 44px; flex: none; }
.instance-action:focus-visible, .instance-dismiss:focus-visible, a:focus-visible { outline: none; border-radius: 4px; box-shadow: var(--focus-ring); }
.instance-desk { display: inline-flex; align-items: center; gap: 6px; min-height: 44px; color: var(--teal-ink); font-size: 13px; text-decoration: none; }
.instance-items { min-width: 0; }
.instance-item { display: flex; align-items: center; gap: 10px; height: 44px; color: var(--ink); text-decoration: none; border-top: 1px solid var(--line); font-size: 13px; }
.instance-item:hover { background: var(--row-hover); }
.instance-item svg { flex: none; color: var(--ink-3); }
.instance-body { flex: 1; display: grid; min-width: 0; gap: 2px; }
.instance-body > .instance-title { font-weight: 600; }
.instance-body small { display: flex; align-items: center; gap: 8px; color: var(--ink-3); font-size: 11.5px; }
.instance-tag { padding: 0 4px; border: 1px solid var(--line); border-radius: 3px; font-size: 11px; }
.instance-decide { display: inline-flex; align-items: center; gap: 5px; flex: none; color: var(--ink-2); }
.instance-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.instance-project, time, .instance-asof { color: var(--ink-3); font-size: 12px; }
.instance-asof { margin: 0; padding: 4px 0; }
.instance-project { max-width: 10ch; overflow-wrap: anywhere; }
@media (max-width: 520px) {
  .instance-copy { overflow: hidden; max-height: 36px; }
  .instance-project { max-width: 7ch; }
  .desk .instance-line, .preview .instance-line { flex-wrap: wrap; height: auto; }
  .desk .instance-actions, .preview .instance-actions { margin-left: auto; }
  .desk .instance-actions { flex-wrap: wrap; flex-shrink: 1; justify-content: flex-end; }
  .decide-words { display: none; }
}
</style>
