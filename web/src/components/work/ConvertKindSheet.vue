<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { APIError, type Kind, type ListItem } from '../../lib/api'
import { blockingChildren, convertTargets, parentAllows } from '../../lib/kindConvert'
import { toast } from '../../lib/toast'
import { kinds } from '../../lib/useTicket'
import { kindLabel } from '../../lib/work'
import AppIcon from '../AppIcon.vue'

const props = defineProps<{
  item: ListItem
  children: ListItem[]
  childrenLoading: boolean
  convert: (toKind: string) => Promise<void>
}>()
const emit = defineEmits<{ close: []; converted: [] }>()

const dialog = ref<HTMLDialogElement>()
const primary = ref<HTMLButtonElement>()
const known = ref<Kind[]>([])
const choice = ref('')
const busy = ref(false)
const serverChildren = ref<{ key: string; kind: string; title?: string }[]>([])
const serverFields = ref<string[]>([])
let focused = false

onMounted(async () => {
  dialog.value?.showModal()
  try {
    known.value = await kinds()
  } catch (error) {
    toast(error instanceof Error ? error.message : 'Kinds could not be loaded', { tone: 'error' })
  }
})

const targets = computed(() => convertTargets(props.item.kind_slug, known.value.map(kind => kind.slug)))
function kindBySlug(slug: string) { return known.value.find(kind => kind.slug === slug) }
function localBlockers(slug: string) {
  const rows = props.children.map(child => ({ key: child.key, title: child.title, kind: child.kind_slug }))
  return blockingChildren(kindBySlug(slug)?.allowed_child_kinds, rows)
}
function parentOk(slug: string) {
  const parentSlug = props.item.parent?.kind_slug
  if (!parentSlug) return true
  const parent = kindBySlug(parentSlug)
  return parent ? parentAllows(parent.allowed_child_kinds, slug) : true
}
const waiting = computed(() => props.childrenLoading && (props.item.kind_slug === 'epic' || props.item.children_count > 0))
const parentBlocked = computed(() => !!choice.value && !parentOk(choice.value))
const blockers = computed(() => {
  if (serverChildren.value.length) return serverChildren.value.map(child => ({ ...child, title: child.title ?? kindLabel(child.kind) }))
  return choice.value ? localBlockers(choice.value) : []
})
const canSubmit = computed(() => !!choice.value && !waiting.value && !busy.value && !parentBlocked.value && blockers.value.length === 0 && serverFields.value.length === 0)
const sentence = computed(() => {
  const kept = `${props.item.key} keeps its key, history, relations, comments and attachments`
  if (!choice.value) return `${kept}.`
  const name = kindLabel(choice.value).toLowerCase()
  const article = /^[aeiou]/.test(name) ? 'an' : 'a'
  return `${kept}, and becomes ${article} ${name}.`
})

watch(targets, list => {
  const open = list.find(slug => parentOk(slug) && localBlockers(slug).length === 0)
  choice.value = open ?? list[0] ?? ''
}, { immediate: true })
watch(canSubmit, async ok => {
  if (!ok || focused) return
  focused = true
  await nextTick()
  primary.value?.focus()
})

function pick(slug: string) {
  choice.value = slug
  serverChildren.value = []
  serverFields.value = []
}
function close() {
  dialog.value?.close()
  emit('close')
}
function backdrop(event: MouseEvent) { if (event.target === dialog.value) close() }
async function submit() {
  if (!canSubmit.value) return
  busy.value = true
  try {
    await props.convert(choice.value)
    emit('converted')
  } catch (error) {
    if (error instanceof APIError && error.status === 409) {
      const rows = Array.isArray(error.body.children) ? error.body.children : []
      serverChildren.value = rows.flatMap(row => {
        if (!row || typeof row !== 'object') return []
        const record = row as Record<string, unknown>
        const key = typeof record.key === 'string' ? record.key : ''
        const kind = typeof record.kind === 'string' ? record.kind : ''
        return key ? [{ key, kind }] : []
      })
      serverFields.value = Array.isArray(error.body.fields) ? error.body.fields.filter((field): field is string => typeof field === 'string') : []
      if (!serverChildren.value.length && !serverFields.value.length) toast(error.message, { tone: 'error' })
      return
    }
    toast(error instanceof Error ? error.message : 'The kind was not changed', { tone: 'error' })
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <dialog ref="dialog" class="convert" aria-labelledby="convert-title" @cancel.prevent="close" @click="backdrop">
    <div class="convert-card">
      <h2 id="convert-title">Convert {{ item.key }}</h2>
      <p class="lead">{{ sentence }}</p>
      <div v-if="targets.length > 1" class="choices" role="radiogroup" aria-label="New kind">
        <button v-for="slug in targets" :key="slug" type="button" class="choice" role="radio" :aria-checked="choice === slug" :class="{ on: choice === slug }" @click="pick(slug)">
          <AppIcon :name="slug === 'epic' ? 'epic' : slug === 'task' ? 'task' : 'ticket'" :size="14" />
          <span>{{ kindLabel(slug) }}</span>
        </button>
      </div>
      <p v-if="waiting" class="note">Checking children…</p>
      <ul v-else-if="blockers.length" class="blockers" aria-label="Blocking children">
        <li v-for="child in blockers" :key="child.key"><span class="mono">{{ child.key }}</span> {{ child.title }}</li>
      </ul>
      <p v-if="parentBlocked" class="note">The parent does not allow a {{ kindLabel(choice).toLowerCase() }}.</p>
      <ul v-if="serverFields.length" class="blockers" aria-label="Fields that do not fit">
        <li v-for="field in serverFields" :key="field">{{ field }}</li>
      </ul>
      <div class="actions">
        <button type="button" class="btn" @click="close">Cancel</button>
        <button ref="primary" type="button" class="btn on" :disabled="!canSubmit" @click="submit">{{ choice ? `Convert to ${kindLabel(choice).toLowerCase()}` : 'Convert' }}</button>
      </div>
    </div>
  </dialog>
</template>

<style scoped>
.convert { width: min(420px, calc(100vw - 32px)); max-width: calc(100vw - 32px); padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.convert::backdrop { background: var(--scrim); backdrop-filter: blur(2px); }
.convert-card { padding: 22px 24px 18px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
h2 { font-size: 18px; overflow-wrap: anywhere; }
.lead, .note { margin-top: 8px; font-size: 13.5px; line-height: 1.45; color: var(--ink-2); overflow-wrap: anywhere; }
.choices { display: grid; gap: 4px; margin-top: 14px; }
.choice { display: flex; align-items: center; gap: 10px; width: 100%; min-height: 40px; padding: 0 12px; border: 0; border-radius: 8px; background: transparent; color: var(--ink); font-size: 14px; text-align: left; }
.choice svg { flex: none; color: var(--ink-2); }
.choice.on { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--glass-rim); }
.choice:focus-visible { box-shadow: var(--focus-ring); }
.blockers { display: grid; gap: 4px; margin: 12px 0 0; padding: 0; list-style: none; }
.blockers li { min-width: 0; overflow-wrap: anywhere; font-size: 13.5px; line-height: 1.4; color: var(--ink-2); }
.mono { font-family: var(--mono); color: var(--ink); }
.actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 8px; margin-top: 20px; }
.actions .btn { min-width: 0; }
.actions .btn:disabled { opacity: .45; }
</style>
