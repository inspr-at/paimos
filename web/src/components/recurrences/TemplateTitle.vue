<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
const props = defineProps<{ modelValue: string; event: boolean; labelId: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const field = ref<HTMLElement>()
const labels: Record<string, string> = { occurrence: 'Number', date: 'Date', release_name: 'Release', release_version: 'Release version' }
let savedRange: Range | null = null
function read(node: Node): string {
  if (node instanceof HTMLElement && node.dataset.variable) return `{{${node.dataset.variable}}}`
  if (node.nodeType === Node.TEXT_NODE) return node.textContent || ''
  return [...node.childNodes].map(read).join('')
}
function token(key: string) {
  const span = document.createElement('span'); span.className = 'token'; span.contentEditable = 'false'
  span.dataset.variable = key; span.textContent = labels[key] || key; return span
}
function render() {
  const el = field.value; if (!el || read(el) === props.modelValue) return
  const nodes: Node[] = [], pattern = /\{\{(occurrence|date|release_name|release_version)\}\}/g
  let offset = 0
  for (const match of props.modelValue.matchAll(pattern)) {
    nodes.push(document.createTextNode(props.modelValue.slice(offset, match.index)), token(match[1]!)); offset = match.index! + match[0].length
  }
  nodes.push(document.createTextNode(props.modelValue.slice(offset))); el.replaceChildren(...nodes)
}
function remember() { const range = getSelection()?.rangeCount ? getSelection()!.getRangeAt(0) : null; if (range && field.value?.contains(range.commonAncestorContainer)) savedRange = range.cloneRange() }
function input() { if (field.value) emit('update:modelValue', read(field.value).replace(/[\r\n]/g, ' ')); remember() }
function insert(key: string) {
  const el = field.value; if (!el) return
  el.focus()
  const range = savedRange && el.contains(savedRange.commonAncestorContainer) ? savedRange : document.createRange()
  if (range !== savedRange) { range.selectNodeContents(el); range.collapse(false) }
  const span = token(key), space = document.createTextNode(' ')
  range.deleteContents(); range.insertNode(span); span.after(space); range.setStartAfter(space); range.collapse(true)
  getSelection()?.removeAllRanges(); getSelection()?.addRange(range); input()
}
function paste(event: ClipboardEvent) {
  event.preventDefault(); const text = event.clipboardData?.getData('text/plain').replace(/\s+/g, ' ') || ''
  const range = getSelection()?.rangeCount ? getSelection()!.getRangeAt(0) : null
  if (!range || !field.value?.contains(range.commonAncestorContainer)) return
  range.deleteContents(); const node = document.createTextNode(text); range.insertNode(node); range.setStartAfter(node); range.collapse(true); input()
}
watch(() => props.modelValue, render); onMounted(render)
</script>
<template>
  <div class="title-editor">
    <div ref="field" class="template-title field" :class="{ 'invalid-release': !event }" role="textbox" :aria-labelledby="labelId" aria-multiline="false" contenteditable="true" spellcheck="false" data-placeholder="Title of each ticket" @input="input" @keyup="remember" @mouseup="remember" @blur="remember" @paste="paste" @keydown.enter.exact.prevent />
    <div class="insert"><span>Insert</span><button v-for="(label, key) in { occurrence: 'Number', date: 'Date', release_name: 'Release' }" :key="key" type="button" :disabled="key === 'release_name' && !event" :data-tip="key === 'release_name' && !event ? 'Only with the Event trigger' : `Insert ${label}`" @mousedown.prevent @click="insert(key)">{{ label }}</button></div>
  </div>
</template>
<style scoped>
.template-title { min-height: 34px; height: auto; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 13px; line-height: 20px; }
.template-title:empty::before { content: attr(data-placeholder); color: var(--ink-3); }
.template-title :deep(.token) { display: inline-block; padding: 0 6px; margin: 0 1px; height: 20px; border-radius: 5px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); font: 600 11.5px/20px var(--mono); user-select: all; }
.invalid-release :deep([data-variable^="release_"]) { background: var(--danger-bg); color: var(--danger); box-shadow: inset 0 0 0 1px var(--danger-line); }
.insert { display: flex; align-items: center; gap: 6px; margin-top: 6px; font-size: 12px; color: var(--ink-3); }
.insert button { height: 26px; padding: 0 8px; border: 0; border-radius: 7px; background: transparent; color: var(--teal-ink); font: 600 11.5px var(--mono); }
.insert button:hover:not(:disabled) { background: var(--row-selected); }
</style>
