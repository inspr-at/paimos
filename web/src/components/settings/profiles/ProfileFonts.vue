<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, reactive, ref, useId, watch } from 'vue'
import { WEIGHT_NAMES, fontInfo } from '../../../lib/quotes/fontInfo'
import { FAMILY } from '../../../lib/quotes/profileForm'
import { profileAssetUrl, uploadProfileAsset } from '../../../lib/quotes/profile'
import type { QuoteProfileDefinition } from '../../../lib/quotes/types'
import AppIcon from '../../AppIcon.vue'

// The profile's own typefaces: drop TTF, OTF or WOFF2 files (or choose them); each
// is read for its family, weight and style, uploaded to this workspace and shown
// in its own face. Body text and headings each take one family; the quote's PDF
// loads the files from the same origin, so nothing comes from a font service.
const props = defineProps<{ definition: QuoteProfileDefinition; disabled?: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const id = useId()
const input = ref<HTMLInputElement>()
const over = ref(false)
const busy = ref(0)
const notes = ref<{ tone: 'error' | 'info'; text: string }[]>([])
const MAX = 10 * 1024 * 1024

type Face = QuoteProfileDefinition['fonts'][number]
const ROLES: { value: Face['role']; label: string }[] = [{ value: 'body', label: 'Body text' }, { value: 'display', label: 'Headings' }]
const groups = computed(() => ROLES.map(role => ({ ...role, faces: props.definition.fonts.map((face, index) => ({ face, index })).filter(({ face }) => face.role === role.value) })))

// A specimen shows the uploaded file itself, loaded same-origin under its own name.
const specimens = reactive(new Map<string, 'loading' | 'ready' | 'failed'>())
const specimenFamily = (asset: string) => `ProfileFace${asset.replaceAll('-', '')}`
function loadSpecimen(asset: string) {
  if (specimens.has(asset)) return
  specimens.set(asset, 'loading')
  const face = new FontFace(specimenFamily(asset), `url("${profileAssetUrl(asset)}")`)
  face.load().then(loaded => { document.fonts.add(loaded); specimens.set(asset, 'ready') }).catch(() => specimens.set(asset, 'failed'))
}

function roleFor(family: string): Face['role'] {
  const fonts = props.definition.fonts
  const body = fonts.find(f => f.role === 'body')?.family, display = fonts.find(f => f.role === 'display')?.family
  if (!body || body === family) return 'body'
  if (!display || display === family) return 'display'
  return 'body'
}
async function add(files: FileList | File[]) {
  notes.value = []
  for (const file of [...files]) {
    if (props.definition.fonts.length >= 12) { notes.value.push({ tone: 'error', text: 'A profile holds at most 12 font files. Remove one first.' }); break }
    if (file.size > MAX) { notes.value.push({ tone: 'error', text: `${file.name} is larger than 10 MB.` }); continue }
    const info = fontInfo(await file.arrayBuffer(), file.name)
    if (!info) { notes.value.push({ tone: 'error', text: `${file.name} is not a TTF, OTF or WOFF2 font.` }); continue }
    busy.value++
    try {
      const asset = await uploadProfileAsset(file)
      const role = roleFor(info.family)
      const same = props.definition.fonts.findIndex(f => f.family === info.family && f.weight === info.weight && f.style === info.style && f.role === role)
      const face: Face = { role, family: info.family, weight: info.weight, style: info.style, asset_id: asset.id }
      if (same >= 0) { props.definition.fonts.splice(same, 1, face); notes.value.push({ tone: 'info', text: `${file.name} replaces the ${WEIGHT_NAMES[info.weight]?.toLowerCase() ?? info.weight}${info.style === 'italic' ? ' italic' : ''} of ${info.family}.` }) }
      else props.definition.fonts.push(face)
      if (info.from === 'name') notes.value.push({ tone: 'info', text: `${file.name}: weight and style read from the file name. Check them below.` })
      loadSpecimen(asset.id)
      emit('changed')
    } catch (e) {
      notes.value.push({ tone: 'error', text: `${file.name} was not uploaded. ${e instanceof Error ? e.message : ''}`.trim() })
    } finally { busy.value-- }
  }
}
function drop(event: DragEvent) {
  over.value = false
  if (props.disabled) return
  const files = event.dataTransfer?.files
  if (files?.length) void add(files)
}
function chosen(event: Event) {
  const el = event.target as HTMLInputElement
  if (el.files?.length) void add(el.files)
  el.value = ''
}
function remove(index: number) { props.definition.fonts.splice(index, 1); emit('changed') }
function setRole(index: number, role: Face['role']) {
  const face = props.definition.fonts[index]!
  // A role takes one family: the other faces of this family move with it.
  for (const f of props.definition.fonts) if (f.family === face.family) f.role = role
  emit('changed')
}
const familyBad = (face: Face) => !FAMILY.test(face.family)
// The upright regular (or nearest) face of a role, as the specimen shows it; headings
// fall back to the body family and both to the built-in face, like the document.
function faceOf(role: Face['role']): Face | undefined {
  const faces = props.definition.fonts.filter(f => f.role === role)
  return faces.find(f => f.weight === 400 && f.style === 'normal') ?? faces.find(f => f.style === 'normal') ?? faces[0]
}
function familyStyle(role: Face['role']) {
  const face = faceOf(role) ?? (role === 'display' ? faceOf('body') : undefined)
  return face && specimens.get(face.asset_id) === 'ready' ? { fontFamily: `'${specimenFamily(face.asset_id)}'` } : undefined
}
const specimenNote = computed(() => {
  const body = faceOf('body')?.family, display = faceOf('display')?.family
  return `${display ?? body ?? 'the built-in face'} for headings, ${body ?? 'the built-in face'} for text.`
})
watch(() => props.definition.fonts.map(face => face.asset_id), ids => ids.forEach(loadSpecimen), { immediate: true })
</script>

<template>
  <div class="fonts">
    <div
      class="drop" :class="{ over, disabled }" @dragenter.prevent="over = !disabled" @dragover.prevent="over = !disabled" @dragleave.self="over = false" @drop.prevent="drop"
    >
      <span class="drop-icon" aria-hidden="true"><AppIcon name="upload" :size="16" /></span>
      <div class="drop-text">
        <p class="drop-title">{{ busy ? 'Uploading…' : 'Drop font files here' }}</p>
        <p class="drop-sub">TTF, OTF or WOFF2, up to 10 MB each. Weight and style are read from the font.</p>
      </div>
      <button type="button" class="btn sm" :disabled="disabled || !!busy" @click="input?.click()"><AppIcon name="plus" :size="13" />Choose files</button>
      <input :id="`${id}-files`" ref="input" type="file" class="sr-only" accept=".ttf,.otf,.woff2,font/ttf,font/otf,font/woff2" multiple tabindex="-1" aria-label="Font files" @change="chosen" />
    </div>
    <ul v-if="notes.length" class="notes" role="status">
      <li v-for="(note, i) in notes" :key="i" :class="note.tone"><AppIcon :name="note.tone === 'error' ? 'alert' : 'info'" :size="13" />{{ note.text }}</li>
    </ul>

    <p v-if="!definition.fonts.length" class="empty">No font files yet: the quote uses the built-in typeface.</p>
    <div v-for="group in groups" v-show="group.faces.length" :key="group.value" class="group">
      <div class="group-head">
        <h4>{{ group.label }} <span>{{ group.faces[0]?.face.family }}</span></h4>
        <button v-if="group.faces.length" type="button" class="btn sm ghost" :disabled="disabled" @click="setRole(group.faces[0]!.index, group.value === 'body' ? 'display' : 'body')">
          {{ group.value === 'body' ? 'Use for headings' : 'Use for body text' }}
        </button>
      </div>
      <ul class="faces">
        <li v-for="{ face, index } in group.faces" :key="face.asset_id + index" class="face">
          <span
            class="specimen" :class="specimens.get(face.asset_id)" aria-hidden="true"
            :style="specimens.get(face.asset_id) === 'ready' ? { fontFamily: `'${specimenFamily(face.asset_id)}'` } : undefined"
          >Ag</span>
          <div class="face-fields">
            <label class="sr-only" :for="`${id}-family-${index}`">Family of font {{ index + 1 }}</label>
            <input :id="`${id}-family-${index}`" v-model="face.family" class="field family" :class="{ bad: familyBad(face) }" maxlength="80" :disabled="disabled" :aria-invalid="familyBad(face) || undefined" @change="emit('changed')" />
            <div class="face-row">
              <label class="sr-only" :for="`${id}-weight-${index}`">Weight of {{ face.family }}</label>
              <select :id="`${id}-weight-${index}`" v-model.number="face.weight" class="field" :disabled="disabled" @change="emit('changed')">
                <option v-for="(name, weight) in WEIGHT_NAMES" :key="weight" :value="Number(weight)">{{ name }} · {{ weight }}</option>
              </select>
              <label class="sr-only" :for="`${id}-style-${index}`">Style of {{ face.family }} {{ face.weight }}</label>
              <select :id="`${id}-style-${index}`" v-model="face.style" class="field" :disabled="disabled" @change="emit('changed')">
                <option value="normal">Upright</option><option value="italic">Italic</option>
              </select>
            </div>
            <p v-if="familyBad(face)" class="bad-note">Start with a letter or digit; letters, digits, spaces, dots and dashes.</p>
            <p v-else-if="specimens.get(face.asset_id) === 'failed'" class="bad-note">This file could not be shown here; the PDF may not use it.</p>
          </div>
          <button type="button" class="icon-btn sm flat" :disabled="disabled" :aria-label="`Remove ${face.family} ${WEIGHT_NAMES[face.weight] ?? face.weight}${face.style === 'italic' ? ' italic' : ''}`" data-tip="Remove" @click="remove(index)"><AppIcon name="trash" :size="14" /></button>
        </li>
      </ul>
    </div>
    <!-- The type as the quote sets it: a heading and a line of body text. -->
    <template v-if="definition.fonts.length">
      <div class="specimen-card" role="img" :aria-label="`Type specimen: ${specimenNote}`" :style="{ background: definition.colors.paper, color: definition.colors.ink }">
        <p class="spec-heading" :style="{ ...familyStyle('display'), color: definition.colors.accent }">Leistungsumfang</p>
        <p class="spec-body" :style="{ ...familyStyle('body'), color: definition.colors.ink }">Vielen Dank für Ihre Anfrage. Äpfel, Öfen, Übergröße: 5.560,00 € ist netto.</p>
      </div>
      <p class="spec-note">{{ specimenNote }}</p>
    </template>
  </div>
</template>

<style scoped>
.fonts { display: grid; gap: 12px; }
.drop { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 12px; padding: 14px; border-radius: 12px; background: var(--surface-2); outline: 1.5px dashed var(--line-2); outline-offset: -1.5px; transition: background .15s ease; }
.drop.over { background: var(--chip-teal-bg); outline-color: var(--teal); }
.drop.disabled { opacity: .6; }
.drop-icon { display: grid; place-items: center; width: 34px; height: 34px; border-radius: 10px; background: var(--surface-raised-2); box-shadow: inset 0 0 0 1px var(--line); color: var(--teal-ink); }
.drop-title { font-size: 13.5px; font-weight: 600; color: var(--ink); }
.drop-sub { margin-top: 2px; font-size: 12px; line-height: 1.45; color: var(--ink-2); }
.notes { display: grid; gap: 4px; margin: 0; padding: 0; list-style: none; }
.notes li { display: flex; align-items: flex-start; gap: 7px; font-size: 12.5px; line-height: 1.45; color: var(--ink-2); }
.notes li svg { flex-shrink: 0; margin-top: 2px; color: var(--ink-3); }
.notes li.error { color: var(--danger); }
.notes li.error svg { color: var(--danger); }
.empty { font-size: 12.5px; color: var(--ink-2); }
.group { display: grid; gap: 6px; }
.group h4 { font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.group h4 span { margin-left: 6px; font-family: var(--font); letter-spacing: 0; text-transform: none; font-size: 12px; font-weight: 600; color: var(--ink-2); }
.faces { display: grid; gap: 6px; margin: 0; padding: 0; list-style: none; }
.face { display: grid; grid-template-columns: 52px minmax(0, 1fr) auto; align-items: start; gap: 10px; padding: 8px; border-radius: 12px; background: var(--surface-raised-2); box-shadow: inset 0 0 0 1px var(--line); }
.specimen { display: grid; place-items: center; width: 52px; height: 52px; border-radius: 9px; background: var(--surface-2); color: var(--ink); font-size: 26px; line-height: 1; }
.specimen.loading { color: var(--ink-3); }
.specimen.failed { color: var(--ink-3); text-decoration: line-through; }
.face-fields { display: grid; gap: 6px; min-width: 0; }
.face-fields .field { height: 30px; font-size: 13px; }
.family { font-weight: 600; }
.family.bad { box-shadow: var(--field-inset), 0 0 0 1px var(--danger-line); }
.face-row { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr); gap: 6px; }
.group-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-height: 30px; }
.group-head .btn { color: var(--ink-2); }
.specimen-card { display: grid; gap: 6px; padding: 14px 16px; border-radius: 12px; box-shadow: inset 0 0 0 1px var(--line-2), 0 1px 3px color-mix(in srgb, var(--shadow-color) 10%, transparent); }
.spec-heading { font-size: 20px; line-height: 1.2; letter-spacing: .04em; }
.spec-body { font-size: 13.5px; line-height: 1.5; }
.spec-note { margin-top: -6px; font-size: 12px; color: var(--ink-2); }
.face-row .field { min-width: 0; padding-left: 8px; padding-right: 22px; }
.bad-note { font-size: 12px; color: var(--danger); }
@media (max-width: 520px) {
  .drop { grid-template-columns: auto minmax(0, 1fr); }
  .drop .btn { grid-column: 1 / -1; justify-self: start; }
  .face { grid-template-columns: 44px minmax(0, 1fr) auto; }
  .specimen { width: 44px; height: 44px; font-size: 22px; }
}
</style>
