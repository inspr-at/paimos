<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
const props = withDefaults(
  defineProps<{ modelValue: string; editable?: boolean; tag?: string; label?: string }>(),
  { tag: 'span', editable: false },
)
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const element = ref<HTMLElement>()
function sync() {
  if (element.value && document.activeElement !== element.value)
    element.value.textContent = props.modelValue
}
onMounted(sync)
watch(() => props.modelValue, sync)
function input(e: Event) {
  emit('update:modelValue', (e.target as HTMLElement).innerText)
}
function paste(e: ClipboardEvent) {
  if (!props.editable) return
  e.preventDefault()
  const selection = window.getSelection()
  if (!selection?.rangeCount) return
  const range = selection.getRangeAt(0)
  range.deleteContents()
  const node = document.createTextNode(e.clipboardData?.getData('text/plain') ?? '')
  range.insertNode(node)
  range.setStartAfter(node)
  range.collapse(true)
  selection.removeAllRanges()
  selection.addRange(range)
  if (element.value) emit('update:modelValue', element.value.innerText)
}
</script>
<template>
  <component
    :is="tag"
    ref="element"
    :contenteditable="editable ? 'plaintext-only' : undefined"
    :role="editable ? 'textbox' : undefined"
    :aria-label="label"
    :tabindex="editable ? 0 : undefined"
    @input="input"
    @paste="paste"
    @blur="sync"
  />
</template>
