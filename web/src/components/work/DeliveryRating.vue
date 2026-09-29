<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import AppIcon from '../AppIcon.vue'
import { clearSessionRating, loadSessionRating, saveSessionRating, signalLine, type SessionRating } from '../../lib/deliveryRating'

const props = defineProps<{ sessionId: string; initial?: SessionRating }>()

const tagOptions = [
  { id: 'quality', label: 'Quality' },
  { id: 'rework', label: 'Rework' },
  { id: 'taste', label: 'Taste' },
] as const

const rating = ref<SessionRating | null>(props.initial ?? null)
const editing = ref(false)
const scoreOpen = ref(false)
const score = ref<number | null>(props.initial?.mine?.score ?? null)
const tags = ref<string[]>([...(props.initial?.mine?.tags ?? [])])
const comment = ref(props.initial?.mine?.comment ?? '')
const saving = ref(false)
const error = ref('')
let generation = 0
let abort: AbortController | undefined

watch(() => props.initial, value => { if (value) apply(value) })
watch(() => props.sessionId, id => { if (!props.initial) void load(id) }, { immediate: !props.initial })
onBeforeUnmount(() => abort?.abort())

async function load(id: string) {
  const request = ++generation
  abort?.abort()
  abort = new AbortController()
  rating.value = null
  editing.value = false
  try {
    const next = await loadSessionRating(id, abort.signal)
    if (request !== generation) return
    if (next) apply(next)
  } catch {
    if (request === generation && !abort?.signal.aborted) rating.value = null
  }
}

function apply(next: SessionRating) {
  rating.value = next
  score.value = next.mine?.score ?? null
  tags.value = [...(next.mine?.tags ?? [])]
  comment.value = next.mine?.comment ?? ''
  error.value = ''
}

function openForm() {
  if (!rating.value) return
  apply(rating.value)
  scoreOpen.value = rating.value.mine?.score != null
  editing.value = true
}

function cancel() {
  if (rating.value) apply(rating.value)
  editing.value = false
}

function toggle(tag: string) {
  tags.value = tags.value.includes(tag) ? tags.value.filter(item => item !== tag) : [...tags.value, tag]
}

function pick(n: number) {
  score.value = score.value === n ? null : n
}

const reason = computed(() => (rating.value?.mine?.comment ?? '').replace(/\s+/g, ' ').trim())
const signals = computed(() => signalLine(rating.value?.signals))
const canSave = computed(() => {
  const text = comment.value.trim()
  if (!text || !rating.value) return false
  const mine = rating.value.mine
  if (!mine) return true
  return mine.comment !== text || mine.score !== score.value || mine.tags.slice().sort().join() !== tags.value.slice().sort().join()
})

async function save() {
  if (!canSave.value || saving.value) return
  saving.value = true
  error.value = ''
  try {
    apply(await saveSessionRating(props.sessionId, { score: score.value, tags: tags.value, comment: comment.value.trim() }))
    editing.value = false
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : 'Could not save the mark'
  } finally {
    saving.value = false
  }
}

async function undo() {
  if (saving.value) return
  saving.value = true
  error.value = ''
  try {
    apply(await clearSessionRating(props.sessionId))
    editing.value = false
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : 'Could not undo the mark'
  } finally {
    saving.value = false
  }
}

function onScoreToggle(event: Event) {
  scoreOpen.value = (event.target as HTMLDetailsElement).open
}
</script>

<template>
  <section v-if="rating" class="mark" data-delivery-rating>
    <div v-if="rating.mine && !editing" class="saved">
      <p class="state">
        <AppIcon name="thumbs-down" :size="14" />
        <span class="phrase">Marked for rework</span>
        <span class="dot" aria-hidden="true">·</span>
        <span class="reason" :title="reason">{{ reason }}</span>
      </p>
      <div class="links">
        <button type="button" class="ask" @click="openForm">Edit</button>
        <button type="button" class="ask" :disabled="saving" @click="undo">Undo</button>
      </div>
    </div>

    <form v-else-if="editing" class="form" @submit.prevent="save">
      <label class="reason-field">Reason
        <textarea v-model="comment" class="field" name="reason" maxlength="2000" rows="2" required />
      </label>
      <div class="tags" role="group" aria-label="Tags">
        <button v-for="tag in tagOptions" :key="tag.id" type="button" class="tag" :aria-pressed="tags.includes(tag.id)" @click="toggle(tag.id)">{{ tag.label }}</button>
      </div>
      <details class="score" :open="scoreOpen" @toggle="onScoreToggle">
        <summary><AppIcon name="chevron-right" :size="12" class="disclosure-chev" />Score<span v-if="score !== null"> {{ score }}</span></summary>
        <div class="seg" role="radiogroup" aria-label="Score">
          <button v-for="n in 5" :key="n" type="button" role="radio" :aria-checked="score === n" :aria-label="`${n} out of 5`" @click="pick(n)">{{ n }}</button>
        </div>
      </details>
      <div class="actions">
        <button type="submit" class="btn sm" :disabled="saving || !canSave">{{ saving ? 'Saving' : (rating.mine ? 'Update' : 'Mark for rework') }}</button>
        <button type="button" class="ask" :disabled="saving" @click="cancel">Cancel</button>
      </div>
    </form>

    <button v-else type="button" class="ask needs" @click="openForm">
      <AppIcon name="thumbs-down" :size="14" />
      Needs rework
    </button>

    <p v-if="signals" class="aside">{{ signals }}</p>
    <p v-if="error" class="save-error" role="alert">{{ error }}</p>
  </section>
</template>

<style scoped>
.mark { display: grid; gap: 6px; margin-top: 8px; min-width: 0; }
.saved { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 12px; min-width: 0; }
.state { display: flex; align-items: center; gap: 6px; min-width: 0; flex: 1 1 12rem; margin: 0; color: var(--ink-2); font-size: 12.5px; line-height: 1.4; }
.state svg { flex-shrink: 0; }
.phrase { flex-shrink: 0; font-weight: 600; }
.dot { flex-shrink: 0; color: var(--ink-3); }
.reason { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--ink-3); }
.links { display: flex; align-items: center; gap: 12px; flex-shrink: 0; }
.ask {
  display: inline-flex; align-items: center; justify-content: center; gap: 6px;
  height: 28px; padding: 0; border: 0; background: transparent; color: var(--ink-3);
  font: 500 12.5px/1 var(--font);
}
.ask:hover { color: var(--ink); }
.ask:focus-visible { box-shadow: var(--focus-ring); border-radius: 6px; }
.ask:disabled { opacity: .55; }
.needs { width: fit-content; }
.form { display: grid; gap: 8px; min-width: 0; }
.reason-field { display: grid; gap: 4px; min-width: 0; color: var(--ink-3); font-size: 12px; font-weight: 600; }
.reason-field textarea { width: 100%; min-height: 52px; resize: vertical; font-weight: 400; color: var(--ink); }
.tags { display: flex; flex-wrap: wrap; gap: 6px; }
.tag { display: inline-flex; align-items: center; justify-content: center; height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: 999px; background: transparent; color: var(--ink-2); font: 600 12px/1 var(--font); }
.tag[aria-pressed="true"] { background: var(--seg-on); color: var(--teal-ink); border-color: transparent; }
.score { min-width: 0; }
.score summary { width: fit-content; color: var(--ink-3); font-size: 12px; font-weight: 600; cursor: pointer; }
.score summary:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
.score .seg { margin-top: 6px; }
.actions { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
.aside { margin: 0; min-width: 0; font-size: 12px; line-height: 1.4; color: var(--ink-3); }
.save-error { margin: 0; font-size: 12.5px; color: var(--ink-2); }
</style>
