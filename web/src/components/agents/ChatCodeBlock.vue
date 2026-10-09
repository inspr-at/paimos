<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { displayLanguage } from '../../lib/displayLanguage'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { toast } from '../../lib/toast'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{ code: string; language: string; state: { expanded: boolean; top: number; left: number; follow: boolean } }>()
const german = computed(() => displayLanguage() === 'de')
const lines = computed(() => props.code.replace(/\n$/, '').split('\n').length)
const long = computed(() => lines.value > 12)
const tail = computed(() => /^(sh|bash|zsh|shell|console|terminal|output|log)$/i.test(props.language))
const pre = ref<HTMLElement>()
const expanded = ref(props.state.expanded)
const copied = ref(false)
const follow = ref(props.state.follow)
const showAll = computed(() => expanded.value ? (german.value ? 'Weniger zeigen' : 'Show less') : (german.value ? `Alle ${lines.value} Zeilen zeigen` : `Show all ${lines.value} lines`))
function remember() {
  if (!pre.value) return
  props.state.expanded = expanded.value
  props.state.top = pre.value.scrollTop
  props.state.left = pre.value.scrollLeft
  props.state.follow = follow.value
}
function scroll() {
  const el = pre.value
  if (!el) return
  follow.value = el.scrollHeight - el.clientHeight - el.scrollTop <= 2
  remember()
}
function readerIntent(event: WheelEvent) { if (event.deltaY < 0) { follow.value = false; remember() } }
function readerKey(event: KeyboardEvent) { if (['ArrowUp', 'PageUp', 'Home'].includes(event.key) || event.key === ' ' && event.shiftKey) { follow.value = false; remember() } }
async function toggle() {
  expanded.value = !expanded.value
  await nextTick()
  if (tail.value && follow.value && pre.value) pre.value.scrollTop = pre.value.scrollHeight
  remember()
}
async function copy() {
  try { await navigator.clipboard.writeText(props.code); copied.value = true }
  catch { toast(german.value ? 'Code konnte nicht kopiert werden.' : 'Could not copy code.', { tone: 'error' }) }
}
onMounted(() => {
  const el = pre.value
  if (!el) return
  el.scrollTop = tail.value && follow.value ? el.scrollHeight : props.state.top
  el.scrollLeft = props.state.left
})
watch(() => props.code, async () => {
  copied.value = false
  await nextTick()
  if (tail.value && follow.value && pre.value) pre.value.scrollTop = pre.value.scrollHeight
})
onBeforeUnmount(remember)
</script>

<template>
  <div class="chat-code" :class="{ folded: long && !expanded, tail }">
    <div class="code-head">
      <span>{{ language || (german ? 'Text' : 'text') }}</span>
      <button type="button" class="code-copy" @click="copy"><AppIcon :name="copied ? 'check' : 'copy'" :size="13" /><span>{{ german ? 'Kopieren' : 'Copy' }}</span><span v-if="copied" class="sr-only" role="status">{{ german ? 'Kopiert' : 'Copied' }}</span></button>
    </div>
    <button v-if="long" type="button" class="code-fold" :aria-expanded="expanded" @click="toggle"><AppIcon :name="expanded ? 'chevron-up' : 'chevron'" :size="13" />{{ showAll }}</button>
    <pre ref="pre" tabindex="0" :aria-label="language || (german ? 'Code' : 'Code')" @scroll.passive="scroll" @wheel.passive="readerIntent" @keydown="readerKey"><code>{{ code.replace(/\n$/, '') }}</code></pre>
  </div>
</template>

<style scoped>
.chat-code { margin: 0 0 12px; border-radius: 12px; background: var(--code-bg); box-shadow: inset 0 0 0 1px var(--line); overflow: hidden; min-width: 0; }
.code-head { display: flex; align-items: center; justify-content: space-between; min-height: 32px; padding: 0 4px 0 12px; font: 500 11px var(--mono); color: var(--ink-3); }
.code-copy { display: inline-flex; align-items: center; justify-content: center; gap: 5px; min-height: 28px; padding: 0 8px; border: 0; border-radius: 8px; background: transparent; color: var(--ink-2); font: 600 11.5px var(--font); }
.code-copy:hover, .code-fold:hover { background: var(--row-hover); color: var(--teal-ink); }
.code-fold { display: flex; align-items: center; justify-content: center; gap: 6px; width: 100%; min-height: 34px; border: 0; border-bottom: 1px solid var(--line); background: transparent; color: var(--teal-ink); font: 600 12.5px var(--font); }
.chat-code pre { margin: 0; padding: 8px 14px 12px; overflow: auto; max-height: 65vh; border-radius: 0; background: transparent; box-shadow: none; font: 12.5px/1.55 var(--mono); color: var(--ink); overscroll-behavior: contain; overflow-anchor: none; }
.chat-code pre code { font: inherit; background: transparent; padding: 0; box-shadow: none; }
.folded pre { max-height: 236px; mask-image: linear-gradient(#000 62%, transparent); }
.folded.tail pre { mask-image: linear-gradient(transparent, #000 38%); }
button:focus-visible, pre:focus-visible { outline: 2px solid var(--teal-ink); outline-offset: -2px; }
@media (max-width: 720px), (pointer: coarse) { .code-head, .code-copy, .code-fold { min-height: 44px; } }
</style>
