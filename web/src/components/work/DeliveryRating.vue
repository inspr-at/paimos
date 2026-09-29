<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { crowdLine, loadSessionRating, saveSessionRating, signalLine, type SessionRating } from '../../lib/deliveryRating'

const props = defineProps<{ sessionId: string; initial?: SessionRating }>()

const tagOptions = [
  { id: 'quality', label: 'Quality' },
  { id: 'rework', label: 'Rework' },
  { id: 'taste', label: 'Taste' },
] as const

const rating = ref<SessionRating | null>(props.initial ?? null)
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

function toggle(tag: string) {
  tags.value = tags.value.includes(tag) ? tags.value.filter(item => item !== tag) : [...tags.value, tag]
}

const open = computed(() => score.value !== null)
const crowd = computed(() => rating.value ? crowdLine(rating.value.votes, rating.value.average) : '')
const signals = computed(() => signalLine(rating.value?.signals))
const dirty = computed(() => {
  if (score.value === null || !rating.value) return false
  const mine = rating.value.mine
  if (!mine) return true
  return mine.score !== score.value || mine.comment !== comment.value.trim() || mine.tags.slice().sort().join() !== tags.value.slice().sort().join()
})

async function save() {
  if (!dirty.value || score.value === null || saving.value) return
  saving.value = true
  error.value = ''
  try {
    apply(await saveSessionRating(props.sessionId, { score: score.value, tags: tags.value, comment: comment.value.trim() }))
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : 'Could not save the rating'
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section v-if="rating" class="rating" data-delivery-rating>
    <div class="head">
      <div class="seg" role="radiogroup" aria-label="Rating">
        <button v-for="n in 5" :key="n" type="button" role="radio" :aria-checked="score === n" :aria-label="`${n} out of 5`" @click="score = n">{{ n }}</button>
      </div>
      <p v-if="crowd || signals" class="aside"><span v-if="crowd">{{ crowd }}</span><template v-if="crowd && signals"> · </template><span v-if="signals">{{ signals }}</span></p>
    </div>
    <div v-if="open" class="tags" role="group" aria-label="Tags">
      <button v-for="tag in tagOptions" :key="tag.id" type="button" class="tag" :aria-pressed="tags.includes(tag.id)" @click="toggle(tag.id)">{{ tag.label }}</button>
    </div>
    <label v-if="open" class="comment">Comment
      <textarea v-model="comment" class="field" maxlength="2000" rows="2" />
    </label>
    <div v-if="open && (dirty || error)" class="actions">
      <button v-if="dirty" type="button" class="btn sm" :disabled="saving" @click="save">{{ saving ? 'Saving' : (rating.mine ? 'Update' : 'Save') }}</button>
      <p v-if="error" class="save-error" role="alert">{{ error }}</p>
    </div>
  </section>
</template>

<style scoped>
.rating { display: grid; gap: 8px; margin-top: 8px; min-width: 0; }
.head { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 10px; min-width: 0; }
.aside { margin: 0; min-width: 0; font-size: 12px; line-height: 1.4; color: var(--ink-3); }
.tags { display: flex; flex-wrap: wrap; gap: 6px; }
.tag { display: inline-flex; align-items: center; justify-content: center; height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: 999px; background: transparent; color: var(--ink-2); font: 600 12px/1 var(--font); }
.tag[aria-pressed="true"] { background: var(--seg-on); color: var(--teal-ink); border-color: transparent; }
.comment { display: grid; gap: 4px; min-width: 0; color: var(--ink-3); font-size: 12px; font-weight: 600; }
.comment textarea { width: 100%; min-height: 52px; resize: vertical; font-weight: 400; color: var(--ink); }
.actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.save-error { margin: 0; font-size: 12.5px; color: var(--ink-2); }
</style>
