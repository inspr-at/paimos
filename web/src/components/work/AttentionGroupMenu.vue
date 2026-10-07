<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { AttentionGroup } from '../../lib/attention'
import FloatingPanel from './FloatingPanel.vue'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
const props = defineProps<{ anchor: HTMLElement; group: AttentionGroup; name: string; mode: 'menu' | 'off'; locale: string; loading: boolean; error: string; ruleEnabled?: boolean }>()
const emit = defineEmits<{ close: [restore: boolean]; dismiss: []; off: []; turnOff: [also: boolean]; follow: []; rule: []; open: [] }>()
const words = (en: string, de: string) => props.locale === 'de' ? de : en
const also = ref(false), phone = ref(window.innerWidth <= 600)
const resized = () => { phone.value = window.innerWidth <= 600 }
onMounted(() => window.addEventListener('resize', resized))
onBeforeUnmount(() => window.removeEventListener('resize', resized))
const labels: Record<string, [string, string]> = { triage: ['Stop listing for triage', 'Nicht mehr zur Triage listen'], cancel: ['Stop suggesting cancellations', 'Keine Abbrüche mehr vorschlagen'], blocked: ['Stop blocked reminders', 'Keine Blockiert-Erinnerungen mehr'], missed: ['Stop flagging missed releases', 'Verpasste Releases nicht mehr markieren'] }
const ruleLabel = computed(() => props.ruleEnabled === false ? words('Turn the rule on again', 'Regel wieder einschalten') : words(...(labels[props.group.kind || ''] || ['', ''])))
function menuKeys(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')]
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  event.preventDefault()
  items[event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (index + (event.key === 'ArrowUp' ? -1 : 1) + items.length) % items.length]?.focus()
}
</script>
<template>
  <FloatingPanel :anchor="anchor" align="end" :width="mode === 'off' ? 520 : 400" :tallest="640" :sheet="phone" :label="words(mode === 'off' ? `Turn autopilot off in ${name}?` : `More for ${name}`, mode === 'off' ? `Autopilot in ${name} ausschalten?` : `Mehr zu ${name}`)" :cycle="true" @close="restore => emit('close', restore)">
    <section v-if="mode === 'off'" class="off-form" :class="{ phone }" data-attention-off>
      <header><h3>{{ words(`Turn autopilot off in ${name}?`, `Autopilot in ${name} ausschalten?`) }}</h3></header>
      <div class="off-actions"><button type="button" class="btn sm primary" data-autofocus :disabled="loading || !!error" @click="emit('turnOff', also)">{{ words('Turn off', 'Ausschalten') }}</button><button type="button" class="btn sm ghost" @click="emit('close', true)">{{ words('Cancel', 'Abbrechen') }}<KeyCap k="esc" /></button></div>
      <div class="off-body">
        <label v-if="group.editable" class="also"><input v-model="also" type="checkbox" :disabled="loading" /><span>{{ words(`Also dismiss the ${group.editable} flags waiting in ${name}`, `Auch die ${group.editable} Markierungen verwerfen, die in ${name} warten`) }}</span></label>
        <p>{{ words(`${name} stops getting new flags and automatic moves, from every rule, not only triage.`, `${name} bekommt keine neuen Markierungen und keine automatischen Änderungen mehr, von keiner Regel, nicht nur Triage.`) }}</p>
        <p class="foot">{{ words('The same switch as Settings › Autopilot › Project overrides.', 'Derselbe Schalter wie Einstellungen › Autopilot › Projekt-Ausnahmen.') }}</p>
        <p v-if="error" role="alert">{{ error }}</p>
      </div>
    </section>
    <div v-else class="group-menu" role="menu" :aria-label="words(`More for ${name}`, `Mehr zu ${name}`)" @keydown="menuKeys">
      <div v-if="phone" class="menu-head"><h3>{{ words(`More for ${name}`, `Mehr zu ${name}`) }}</h3><button type="button" class="icon-btn" :aria-label="words('Close', 'Schließen')" @click="emit('close', true)"><AppIcon name="close" :size="16" /></button></div>
      <button v-if="group.editable" type="button" role="menuitem" @click="emit('dismiss')"><AppIcon name="close" :size="15" /><span>{{ words(`Dismiss all ${group.editable}`, `Alle ${group.editable} verwerfen`) }}<small>{{ words('The tickets stay as they are; these suggestions do not return.', 'Die Tickets bleiben, wie sie sind; diese Vorschläge kommen nicht wieder.') }}</small></span></button>
      <template v-if="group.project_id && group.can_manage">
        <button v-if="group.override_mode === 'off'" type="button" role="menuitem" :disabled="loading" @click="emit('follow')"><AppIcon name="play" :size="15" /><span>{{ words('Follow the workspace again', 'Wieder dem Workspace folgen') }}</span></button>
        <button v-else type="button" role="menuitem" :disabled="loading" @click="emit('off')"><AppIcon name="pause" :size="15" /><span>{{ words(`Turn autopilot off in ${name}`, `Autopilot in ${name} ausschalten`) }}<small>{{ words('Project override: no new flags or automatic moves here.', 'Projekt-Ausnahme: hier keine neuen Markierungen oder automatischen Änderungen.') }}</small></span></button>
      </template>
      <button v-if="group.kind && group.can_manage && labels[group.kind]" type="button" role="menuitem" :disabled="loading || ruleEnabled === undefined || !!error" @click="emit('rule')"><AppIcon :name="ruleEnabled === false ? 'play' : 'pause'" :size="15" /><span>{{ ruleLabel }}<small>{{ words('Workspace rule, for every project.', 'Workspace-Regel, für alle Projekte.') }}</small></span></button>
      <button type="button" role="menuitem" @click="emit('open')"><AppIcon name="arrow" :size="15" /><span>{{ words(group.project_id ? `Open ${name} tickets` : 'Open Autopilot settings', group.project_id ? `Tickets von ${name} öffnen` : 'Autopilot-Einstellungen öffnen') }}</span></button>
      <p v-if="error" role="alert">{{ error }}</p>
    </div>
  </FloatingPanel>
</template>
<style scoped>
.menu-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 4px 10px; } .menu-head .icon-btn { width: auto; flex: none; }
.group-menu { padding: 4px; text-align: left; }
.group-menu button { display: flex; align-items: center; gap: 10px; min-height: 44px; width: 100%; padding: 10px; border: 0; border-radius: 6px; background: transparent; color: var(--ink); font-size: 13px; text-align: left; }
.group-menu button:hover { background: var(--row-hover); } .group-menu button:focus-visible { outline: 2px solid var(--teal-ink); outline-offset: -2px; } .group-menu button svg { flex: none; }
small { display: block; margin-top: 4px; color: var(--ink-3); font-size: 12px; line-height: 1.4; }
.off-form { display: flex; flex-direction: column; padding: 10px; max-height: calc(var(--floating-max) - 12px); text-align: left; font-weight: 400; }
h3 { font-size: 15px; overflow-wrap: anywhere; } .off-actions { display: flex; flex-wrap: wrap; flex: none; gap: 8px; padding: 12px 0; }
.off-actions .btn.primary { box-shadow: none; }
.off-body { min-height: 0; overflow: auto; } .also { display: flex; align-items: center; gap: 10px; min-height: 44px; padding-block: 8px; cursor: pointer; font-size: 13px; } .also input { flex: none; accent-color: var(--teal); }
p { font-size: 13px; color: var(--ink-2); line-height: 1.5; margin: 8px 0; } .foot { font-size: 12px; color: var(--ink-3); }
[role=alert] { color: var(--danger); overflow-wrap: anywhere; }
.phone { height: 100%; max-height: 100%; padding: 12px; } .phone .off-body { flex: 1; } .phone .off-actions { order: 2; border-top: 1px solid var(--line); padding-bottom: calc(10px + env(safe-area-inset-bottom)); }
@media (max-width: 600px), (pointer: coarse) { .off-actions .btn { min-height: 44px; } }
</style>
