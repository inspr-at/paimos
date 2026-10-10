<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import type { QuoteProfile } from '../../lib/quotes/profile'
import type { QuoteProfileSnapshot } from '../../lib/quotes/types'
import BizIcon from '../business/BizIcon.vue'
import FloatingPanel from '../work/FloatingPanel.vue'
import ProfileThumb from '../settings/profiles/ProfileThumb.vue'

// The quote's look, in its title bar. A draft picks any profile in use (or the
// standard document) and is told when its profile has a newer revision; an issued
// version shows the profile revision it was frozen with, read-only.
const props = defineProps<{
  current: QuoteProfileSnapshot | null; name: string; profiles: QuoteProfile[] | null; editable: boolean; busy?: boolean; compact?: boolean; admin?: boolean
}>()
const emit = defineEmits<{ open: []; choose: [profileId: string] }>()
const anchor = ref<HTMLElement | null>(null)
const button = ref<HTMLButtonElement>()
const live = computed(() => (props.profiles ?? []).filter(p => !p.archived))
const latest = computed(() => props.current ? props.profiles?.find(p => p.id === props.current!.id) ?? null : null)
const newer = computed(() => !!latest.value && !latest.value.archived && latest.value.revision > (props.current?.revision ?? 0))
const label = computed(() => props.current ? (props.name || 'Document profile') : 'Standard document')
const tip = computed(() => props.editable
  ? (newer.value ? `${label.value}: revision ${latest.value!.revision} is newer than this draft’s ${props.current!.revision}` : `How this quote looks: ${label.value}`)
  : props.current ? `Issued with ${label.value}, revision ${props.current.revision}. It never changes.` : 'Issued in the standard document look.')
function toggle(event: MouseEvent) {
  if (anchor.value) { anchor.value = null; return }
  anchor.value = event.currentTarget as HTMLElement
  emit('open')
}
function close(restore: boolean) { anchor.value = null; if (restore) button.value?.focus() }
function choose(id: string) { anchor.value = null; emit('choose', id); button.value?.focus() }
function keys(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const items = [...(event.currentTarget as HTMLElement).querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]:not(:disabled), [role="menuitem"]:not(:disabled)')]
  const index = items.indexOf(document.activeElement as HTMLButtonElement)
  event.preventDefault()
  const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : event.key === 'ArrowDown' ? Math.min(items.length - 1, index + 1) : Math.max(0, index - 1)
  items[next]?.focus()
}
</script>

<template>
  <button
    v-if="editable" ref="button" type="button" class="btn sm picker" :class="{ compact, newer }" :disabled="busy" aria-haspopup="menu" :aria-expanded="!!anchor"
    :aria-label="`Document profile: ${label}${newer ? ', a newer revision is available' : ''}`" :data-tip="tip" @click="toggle"
  >
    <ProfileThumb v-if="current" :definition="current.definition" :size="18" />
    <span v-else class="standard-mark" aria-hidden="true"><BizIcon name="document" :size="13" /></span>
    <span class="picker-name">{{ label }}</span>
    <span v-if="newer" class="newer-dot" aria-hidden="true" />
    <BizIcon name="chevron" :size="12" class="picker-chev" />
  </button>
  <span v-else class="frozen-profile" :class="{ compact }" :data-tip="tip">
    <ProfileThumb v-if="current" :definition="current.definition" :size="18" />
    <span v-else class="standard-mark" aria-hidden="true"><BizIcon name="document" :size="13" /></span>
    <span class="picker-name">{{ label }}<template v-if="current"> · rev. {{ current.revision }}</template></span>
    <span class="sr-only">{{ tip }}</span>
    <BizIcon name="lock" :size="12" class="picker-chev" />
  </span>
  <FloatingPanel v-if="anchor" :anchor="anchor" :width="300" align="end" label="Document profile" @close="close">
    <p class="menu-head" aria-hidden="true">How this quote looks</p>
    <div role="menu" aria-label="Document profile" class="profile-menu" @keydown="keys">
      <button type="button" role="menuitemradio" class="menu-item" :aria-checked="!current" data-autofocus @click="choose('')">
        <span class="standard-mark big" aria-hidden="true"><BizIcon name="document" :size="14" /></span>
        <span class="item-text"><span class="item-name">Standard document</span><span class="item-meta">The built-in look</span></span>
        <BizIcon v-if="!current" name="check" :size="14" class="tick" />
      </button>
      <p v-if="profiles === null" class="menu-note">Loading profiles…</p>
      <button v-for="p in live" :key="p.id" type="button" role="menuitemradio" class="menu-item" :aria-checked="current?.id === p.id" @click="choose(p.id)">
        <ProfileThumb :definition="p.definition" :size="30" />
        <span class="item-text">
          <span class="item-name">{{ p.name }}</span>
          <span class="item-meta">{{ p.definition.layout_variant === 'classic-v1' ? 'Classic' : 'Standard' }} · rev. {{ p.revision }}<template v-if="current?.id === p.id && p.revision > current.revision"> · this draft has rev. {{ current.revision }}</template></span>
        </span>
        <BizIcon v-if="current?.id === p.id" name="check" :size="14" class="tick" />
      </button>
      <template v-if="newer && latest">
        <div class="menu-sep" role="separator" />
        <button type="button" role="menuitem" class="menu-item" @click="choose(latest.id)"><BizIcon name="refresh" :size="14" />Use revision {{ latest.revision }} of {{ latest.name }}</button>
      </template>
      <template v-if="admin">
        <div class="menu-sep" role="separator" />
        <RouterLink role="menuitem" class="menu-item" to="/settings/business/profiles" @click="anchor = null"><BizIcon name="edit" :size="14" />Edit document profiles</RouterLink>
      </template>
    </div>
  </FloatingPanel>
</template>

<style scoped>
.picker, .frozen-profile { gap: 7px; max-width: 220px; padding: 0 9px 0 7px; font-weight: 600; color: var(--ink-2); }
.frozen-profile { display: inline-flex; align-items: center; height: 30px; border-radius: 999px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line); font-size: 12.5px; }
.picker-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.picker-chev { flex-shrink: 0; color: var(--ink-3); }
.standard-mark { display: grid; place-items: center; flex-shrink: 0; width: 18px; height: 18px; border-radius: 4px; background: var(--surface-2); box-shadow: inset 0 0 0 1px var(--line-2); color: var(--ink-3); }
.standard-mark.big { width: 22px; height: 30px; }
.newer-dot { flex-shrink: 0; width: 7px; height: 7px; border-radius: 50%; background: var(--secondary-line); box-shadow: 0 0 0 2px var(--surface-raised-2); }
/* Narrow title bars: the sheet alone, with the newer-revision dot on its corner. */
.compact .picker-name, .compact .picker-chev { display: none; }
.compact.picker, .compact.frozen-profile { position: relative; width: 32px; padding: 0; justify-content: center; }
.compact .newer-dot { position: absolute; top: 4px; right: 4px; }
.frozen-profile { max-width: 280px; }
.profile-menu { display: grid; gap: 1px; }
.menu-head { padding: 6px 10px 4px; font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.menu-note { padding: 6px 10px; font-size: 12.5px; color: var(--ink-2); }
.menu-item { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 40px; padding: 5px 10px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 13.5px; text-align: left; text-decoration: none; }
.menu-item > svg { flex-shrink: 0; color: var(--ink-3); }
@media (hover: hover) { .menu-item:hover { background: var(--row-hover); } }
.menu-item:focus-visible { background: var(--row-selected); box-shadow: none; outline: none; }
.item-text { display: grid; flex: 1; min-width: 0; }
.item-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.item-meta { font-size: 12px; color: var(--ink-2); }
.tick { color: var(--teal-ink) !important; }
.menu-sep { height: 1px; margin: 4px 6px; background: var(--line); }
</style>
