<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import '../../styles/crm.css'
import { computed, nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { brand } from '../../lib/brand'
import { TYPES, cliCommand, createKnowledge, entryPath, KnowledgeError, slugProblem, suggestSlug, type KnowledgeEntry, type KnowledgeItem, type KnowledgeType } from '../../lib/knowledge'
import { keyPrefix } from '../../lib/useTicket'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'

// A new entry: its kind, a title, and the slug agents will read it by. The slug
// follows the title until someone types their own; the body is written next, on
// the entry itself.
const props = defineProps<{ project: { id: string; routeKey: string; title: string }; taken: (type: KnowledgeType) => string[] }>()
const emit = defineEmits<{ created: [entry: KnowledgeEntry] }>()
const router = useRouter()
const dialog = ref<HTMLDialogElement>()
const titleInput = ref<HTMLInputElement>()
const slugInput = ref<HTMLInputElement>()
const type = ref<KnowledgeType>('runbook')
const title = ref('')
const slug = ref('')
const slugEdited = ref(false)
const busy = ref(false)
const touched = ref(false)
const error = ref('')
const conflict = ref<KnowledgeItem | null>(null)
let opener: HTMLElement | null = null

const suggestion = computed(() => suggestSlug(title.value, type.value, props.taken(type.value)))
watch(suggestion, value => { if (!slugEdited.value) slug.value = value })
const titleProblem = computed(() => !title.value.trim() ? 'A title is needed.' : title.value.trim().length > 500 ? 'At most 500 characters.' : '')
const slugIssue = computed(() => slugProblem(type.value, slug.value) || (props.taken(type.value).includes(slug.value) ? `Another ${TYPES.find(t => t.type === type.value)?.label.toLowerCase()} here already uses this slug.` : ''))
const command = computed(() => cliCommand(brand.value.product, props.project.routeKey, type.value, slug.value || 'slug'))
const dirty = computed(() => !!title.value.trim())

async function open(initial?: KnowledgeType) {
  opener = document.activeElement as HTMLElement
  type.value = initial && initial !== 'decision' ? initial : 'runbook'
  title.value = ''; slug.value = ''; slugEdited.value = false; touched.value = false; error.value = ''; conflict.value = null; busy.value = false
  dialog.value?.showModal()
  await nextTick(); titleInput.value?.focus()
}
function close() { dialog.value?.close(); opener?.focus({ preventScroll: true }) }
function typedSlug(event: Event) {
  const value = (event.target as HTMLInputElement).value
  slug.value = value
  slugEdited.value = value !== '' && value !== suggestion.value
  conflict.value = null
}
function useSuggestion() { slug.value = suggestion.value; slugEdited.value = false; slugInput.value?.focus() }
async function submit() {
  touched.value = true
  if (titleProblem.value) { titleInput.value?.focus(); return }
  if (slugIssue.value) { slugInput.value?.focus(); return }
  if (busy.value) return
  busy.value = true; error.value = ''; conflict.value = null
  try {
    const entry = await createKnowledge({ project_id: props.project.id, type: type.value, slug: slug.value, title: title.value.trim(), key_prefix: keyPrefix(props.project.routeKey) })
    dialog.value?.close()
    emit('created', entry)
  } catch (e) {
    if (e instanceof KnowledgeError && e.code === 'slug_taken') { conflict.value = e.conflict; slugInput.value?.focus() }
    error.value = e instanceof Error ? e.message : 'The entry was not created.'
  } finally { busy.value = false }
}
function openConflict() {
  const other = conflict.value
  if (!other) return
  close()
  void router.push(entryPath(props.project.routeKey, type.value, other.slug || slug.value))
}
function backdrop(event: MouseEvent) { if (event.target === dialog.value && !dirty.value) close() }
function keys(event: KeyboardEvent) {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') { event.preventDefault(); void submit() }
}
defineExpose({ open })
</script>

<template>
  <dialog ref="dialog" class="create" aria-labelledby="k-new-title" @cancel.prevent="close" @click="backdrop">
    <form class="create-card" novalidate @submit.prevent="submit" @keydown="keys">
      <header class="create-head">
        <div>
          <h2 id="k-new-title">New knowledge entry</h2>
          <p class="lead">In {{ project.title }}. You write the text next, on the entry itself.</p>
        </div>
        <button type="button" class="icon-btn sm flat" aria-label="Close" data-tip="Close · Esc" @click="close"><AppIcon name="close" :size="15" /></button>
      </header>

      <fieldset class="kinds-set">
        <legend class="f-label">Kind</legend>
        <div class="kinds">
          <label v-for="meta in TYPES.filter(meta => meta.type !== 'decision')" :key="meta.type" class="kind" :class="{ on: type === meta.type }">
            <input v-model="type" class="sr-only" type="radio" name="k-new-kind" :value="meta.type" />
            <span class="kind-icon"><AppIcon :name="meta.icon" :size="15" /></span>
            <strong class="kind-label">{{ meta.label }}</strong>
            <span class="kind-hint">{{ meta.hint }}</span>
            <span class="kind-check" aria-hidden="true"><AppIcon v-if="type === meta.type" name="check" :size="12" /></span>
          </label>
        </div>
      </fieldset>

      <div class="f-grid">
        <div class="f-row wide">
          <label class="f-label" for="k-new-name">Title</label>
          <input
            id="k-new-name" ref="titleInput" v-model="title" class="field" maxlength="500" autocomplete="off" spellcheck="true"
            :placeholder="type === 'runbook' ? 'Deploy a release to production' : type === 'guideline' ? 'No coloured edge accents' : type === 'memory' ? 'Deploys wait for a green CI run' : type === 'external-system' ? 'Hetzner Cloud' : 'Pharos fleet'"
            :aria-invalid="touched && !!titleProblem" aria-describedby="k-new-name-note"
          />
          <p v-if="touched && titleProblem" id="k-new-name-note" class="f-note bad" role="alert"><AppIcon name="alert" :size="12" />{{ titleProblem }}</p>
        </div>
        <div class="f-row wide">
          <label class="f-label" for="k-new-slug">Slug <span class="opt">how agents find it</span></label>
          <div class="slug-box" :class="{ bad: (touched || slugEdited) && !!slugIssue }">
            <span class="slug-prefix mono" aria-hidden="true">{{ type }}/</span>
            <input
              id="k-new-slug" ref="slugInput" :value="slug" class="slug-input mono" maxlength="64" autocomplete="off" spellcheck="false" autocapitalize="off"
              placeholder="deploy-a-release" :aria-invalid="(touched || slugEdited) && !!slugIssue" aria-describedby="k-new-slug-note" @input="typedSlug"
            />
          </div>
          <p v-if="(touched || slugEdited) && slugIssue && !conflict" id="k-new-slug-note" class="f-note bad" role="alert"><AppIcon name="alert" :size="12" />{{ slugIssue }}</p>
          <p v-else-if="conflict" id="k-new-slug-note" class="f-note bad" role="alert">
            <AppIcon name="alert" :size="12" /><span>“{{ conflict.title }}” already uses this slug. <button type="button" class="inline-link" @click="openConflict">Open it</button></span>
          </p>
          <p v-else id="k-new-slug-note" class="f-note">
            <AppIcon name="terminal" :size="12" /><span>Agents read it with <code class="cmd"><template v-for="(part, i) in command.split(' ')" :key="i"><span class="tok">{{ part }}</span>{{ ' ' }}</template></code><button v-if="slugEdited && suggestion && suggestion !== slug" type="button" class="inline-link" @click="useSuggestion">Use {{ suggestion }}</button></span>
          </p>
        </div>
      </div>

      <p v-if="error && !conflict" class="f-error" role="alert"><AppIcon name="alert" :size="14" />{{ error }}</p>
      <footer class="create-foot">
        <p class="f-hint"><KeyCap k="enter" /> creates · <kbd class="keycap">esc</kbd> closes</p>
        <button type="button" class="btn" @click="close">Cancel</button>
        <button type="submit" class="btn primary" :disabled="busy"><AppIcon name="edit" :size="14" />{{ busy ? 'Creating…' : 'Create and write' }}</button>
      </footer>
    </form>
  </dialog>
</template>

<style scoped>
.create { width: min(var(--dialog-l), calc(100vw - 24px)); max-height: calc(100dvh - 24px); padding: 0; border: 0; background: transparent; color: var(--ink); overflow: visible; }
.create::backdrop { background: var(--scrim); backdrop-filter: blur(2px); }
.create-card { display: grid; gap: 18px; max-height: calc(100dvh - 24px); overflow: auto; padding: 20px 22px 18px; border-radius: var(--radius); border: 1px solid var(--glass-edge); background: linear-gradient(165deg, var(--surface-raised), var(--surface-raised-2)); box-shadow: var(--shadow-pop), var(--shadow); }
.create-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
h2 { font-size: 18px; }
.lead { margin-top: 4px; font-size: 13px; color: var(--ink-2); }
.kinds-set { margin: 0; padding: 0; border: 0; min-width: 0; }
.kinds-set legend { margin-bottom: 8px; padding: 0; }
.kinds { display: grid; grid-template-columns: minmax(0, 1fr); gap: 4px; }
.kind { display: flex; align-items: center; gap: 11px; min-height: 46px; padding: 0 12px 0 8px; border-radius: 12px; background: var(--field-bg); box-shadow: inset 0 0 0 1px var(--line); cursor: pointer; }
@media (hover: hover) { .kind:hover { background: var(--row-hover); box-shadow: inset 0 0 0 1px var(--line-2); } }
.kind.on { background: var(--row-selected); box-shadow: inset 0 0 0 1px var(--chip-teal-line); }
.kind:focus-within { box-shadow: var(--focus-ring); }
.kind-icon { display: grid; place-items: center; flex-shrink: 0; width: 30px; height: 30px; border-radius: 9px; background: var(--chip-bg); box-shadow: inset 0 0 0 1px var(--chip-line); color: var(--ink-2); }
.kind.on .kind-icon { background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); color: var(--teal-ink); }
.kind-label { flex-shrink: 0; width: 132px; font-size: 13.5px; color: var(--ink); }
.kind-hint { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12.5px; color: var(--ink-2); }
.kind-check { display: grid; place-items: center; flex-shrink: 0; width: 18px; height: 18px; border-radius: 50%; color: var(--primary-on); }
.kind.on .kind-check { background: linear-gradient(180deg, var(--primary-hi), var(--primary)); }
.slug-box {
  display: flex; align-items: center; height: 38px; padding: 0 12px; border: 1px solid var(--glass-edge); border-radius: var(--radius-s);
  background: var(--field-bg); box-shadow: var(--field-inset), 0 0 0 1px var(--line); cursor: text;
}
.slug-box:focus-within { box-shadow: var(--focus-ring); }
.slug-box.bad { box-shadow: var(--field-inset), 0 0 0 1px var(--danger-line); }
.slug-prefix { flex-shrink: 0; font-size: 13px; color: var(--ink-3); font-variant-ligatures: none; }
.slug-input { flex: 1; min-width: 0; height: 100%; padding: 0; border: 0; background: transparent; color: var(--ink); font-size: 13px; font-variant-ligatures: none; }
.slug-input:focus { box-shadow: none; }
.slug-input::placeholder { color: var(--ink-3); }
.f-note span { min-width: 0; }
.cmd .tok { white-space: nowrap; }
.cmd { padding: 1px 5px; border-radius: 5px; background: var(--code-bg); font-size: 11.5px; color: var(--ink); overflow-wrap: anywhere; }
.inline-link { margin-left: 6px; padding: 0; border: 0; background: transparent; color: var(--teal-ink); font-size: 12px; font-weight: 600; text-decoration: underline; text-underline-offset: 2px; }
.inline-link:focus-visible { box-shadow: var(--focus-ring); border-radius: 4px; }
.create-foot { display: flex; align-items: center; justify-content: flex-end; gap: 8px; }
.create-foot .f-hint { flex: 1; }
@media (max-width: 600px) {
  .create-card { padding: 16px; }
  .kind { min-height: 48px; }
  .kind-label { width: auto; flex: 1; }
  .kind-hint { display: none; }
  .slug-box { height: 48px; }
  .slug-input, .slug-prefix { font-size: 16px; }
  .create-foot { flex-wrap: wrap; }
  .create-foot .f-hint { display: none; }
  .create-foot .btn { flex: 1; height: 44px; }
  /* A full-height sheet: the fields scroll, the actions stay pinned at the bottom (AEON-730). */
  .create { inset: 0; width: 100%; max-width: none; height: 100dvh; max-height: none; margin: 0; }
  .create-card { display: flex; flex-direction: column; height: 100%; max-height: none; border: 0; border-radius: 0; background: var(--surface-raised); }
  .create-foot { position: sticky; bottom: -16px; flex: none; margin: auto -16px -16px; padding: 10px 16px calc(14px + env(safe-area-inset-bottom)); border-top: 1px solid var(--line); background: var(--surface-raised); }
}
</style>
