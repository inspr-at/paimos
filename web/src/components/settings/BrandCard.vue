<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api } from '../../lib/api'
import { brand } from '../../lib/brand'
import { toast } from '../../lib/toast'
import { fitLogo, headerBrand, logoProblem, logoType, MAX_SHORT_NAME, publicBrand, type BrandSettings } from '../../lib/tenantBrand'
import { useSession } from '../../stores/session'
import mark from '../../assets/brand/aeon-mark.svg'
import AppIcon from '../AppIcon.vue'
import SettingsCard from './SettingsCard.vue'

// AEON-431: the workspace's logo and short name in the top left, instead of the
// product mark. Two previews show the header as it will look, light and dark;
// each carries its own upload. Person-only and settings.manage on the server.
type Variant = 'light' | 'dark'
const session = useSession()
const settings = ref<BrandSettings | null>(null)
const name = ref('')
const busy = ref<Variant | 'name' | ''>('')
const problem = ref('')
const inputs = { light: ref<HTMLInputElement>(), dark: ref<HTMLInputElement>() }
const lightInput = inputs.light
const darkInput = inputs.dark

async function message(response: Response, fallback: string) {
  if (response.status === 403) return 'Only a workspace admin can change the brand.'
  const body = await response.json().catch(() => null) as { error?: string } | null
  const text = typeof body?.error === 'string' ? body.error : ''
  // A 413 from a proxy has no body; the server's says which limit was hit.
  if (response.status === 413 && !text) return 'The file is larger than 256 KB.'
  return text ? text[0]!.toUpperCase() + text.slice(1) + (/[.!?]$/.test(text) ? '' : '.') : fallback
}

function apply(next: BrandSettings) {
  settings.value = next
  name.value = next.short_name
  // The header follows at once, without another session round trip.
  if (session.identity) session.identity.tenant.brand = publicBrand(next)
}

onMounted(async () => {
  try {
    const response = await api('/settings/brand')
    if (response.ok) apply(await response.json() as BrandSettings)
  } catch { /* the card stays hidden */ }
})

async function saveName() {
  if (!settings.value) return
  const next = name.value.replace(/\s+/g, ' ').trim()
  if (next === settings.value.short_name) { name.value = next; return }
  busy.value = 'name'
  problem.value = ''
  try {
    const response = await api('/settings/brand', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ short_name: next }) })
    if (!response.ok) { problem.value = await message(response, 'The short name could not be saved.'); return }
    apply(await response.json() as BrandSettings)
  } catch { problem.value = 'The short name could not be saved. Check the connection and try again.' } finally { busy.value = '' }
}

async function upload(variant: Variant, file: File | undefined) {
  if (!file) return
  problem.value = logoProblem(file)
  if (problem.value) return
  busy.value = variant
  try {
    const response = await api(`/settings/brand/logo/${variant}`, { method: 'PUT', headers: { 'Content-Type': logoType(file) }, body: file })
    if (!response.ok) { problem.value = await message(response, 'The logo could not be saved.'); return }
    const next = await response.json() as BrandSettings
    apply(next)
    if (next.cleaned) toast('Saved. Parts of the SVG that could run code or load other files were removed.')
  } catch { problem.value = 'The logo could not be saved. Check the connection and try again.' } finally {
    busy.value = ''
    const input = inputs[variant].value
    if (input) input.value = ''
  }
}

async function remove(variant: Variant) {
  busy.value = variant
  problem.value = ''
  try {
    const response = await api(`/settings/brand/logo/${variant}`, { method: 'DELETE' })
    if (!response.ok) { problem.value = await message(response, 'The logo could not be removed.'); return }
    apply(await response.json() as BrandSettings)
  } catch { problem.value = 'The logo could not be removed. Check the connection and try again.' } finally { busy.value = '' }
}

function dropped(variant: Variant, event: DragEvent) { void upload(variant, event.dataTransfer?.files[0]) }

// What each preview draws: exactly the header's rule, with the name as typed.
const draft = computed(() => settings.value ? { ...publicBrand(settings.value), short_name: name.value } : undefined)
const previews = computed(() => (['light', 'dark'] as const).map(variant => ({ variant, mark: headerBrand(draft.value, variant === 'dark') })))
const hasLogo = computed(() => !!settings.value?.logo)
const hasDark = computed(() => !!settings.value?.logo_dark)
</script>

<template>
  <SettingsCard v-if="settings" title="Brand" icon="image" anchor="brand">
    <template #lead>Your logo and short name in the top left, in place of the {{ brand.wordmark }} mark.</template>
    <div class="brand-form">
      <label class="name-field">
        <span class="label">Short name</span>
        <input v-model="name" class="field" type="text" :maxlength="MAX_SHORT_NAME" autocomplete="organization" placeholder="Shown next to the logo" :disabled="busy === 'name'" @change="saveName" @keydown.enter.prevent="($event.target as HTMLInputElement).blur()" />
      </label>
      <div class="previews">
        <figure v-for="p in previews" :key="p.variant" class="preview" :class="p.variant" @dragover.prevent @drop.prevent="dropped(p.variant, $event)">
          <div class="bar" :aria-label="`Header preview, ${p.variant === 'light' ? 'light' : 'dark'} mode`" role="img">
            <template v-if="p.mark">
              <span v-if="p.mark.logo" class="logo" :class="{ plate: p.mark.plate }"><img :src="p.mark.logo.url" v-bind="fitLogo(p.mark.logo, p.mark.plate ? 24 : 28, p.mark.plate ? 116 : 132)" alt="" /></span>
              <span v-if="p.mark.name" class="name">{{ p.mark.name }}</span>
            </template>
            <template v-else>
              <span class="product"><img :src="mark" width="22" height="22" alt="" /></span>
              <span class="wordmark">{{ brand.product }}<sup>{{ brand.release_name }}</sup></span>
            </template>
          </div>
          <figcaption>
            <span class="mode">{{ p.variant === 'light' ? 'Light' : 'Dark' }}</span>
            <span v-if="p.variant === 'dark' && hasLogo && !hasDark" class="auto">Auto: your logo on a light plate</span>
            <span class="actions">
              <template v-if="p.variant === 'light'">
                <button type="button" class="btn sm" :class="{ primary: !hasLogo }" :disabled="!!busy" @click="lightInput?.click()">
                  <AppIcon name="upload" :size="13" />{{ hasLogo ? 'Replace' : 'Upload logo' }}
                </button>
                <button v-if="hasLogo" type="button" class="icon-btn sm flat" aria-label="Remove logo" data-tip="Remove logo" :disabled="!!busy" @click="remove('light')"><AppIcon name="trash" :size="14" /></button>
              </template>
              <template v-else-if="hasLogo || hasDark">
                <button type="button" class="btn sm" :disabled="!!busy" @click="darkInput?.click()">
                  <AppIcon name="upload" :size="13" />{{ hasDark ? 'Replace' : 'Upload dark logo' }}
                </button>
                <button v-if="hasDark" type="button" class="icon-btn sm flat" aria-label="Remove dark logo" data-tip="Remove dark logo" :disabled="!!busy" @click="remove('dark')"><AppIcon name="trash" :size="14" /></button>
              </template>
            </span>
          </figcaption>
        </figure>
      </div>
      <p class="hint">PNG, WebP or SVG up to 256 KB, at least 32 px on each side; fitted, never cropped.</p>
      <p v-if="problem" class="set-note error" role="alert"><AppIcon name="alert" :size="14" /><span>{{ problem }}</span></p>
      <input ref="lightInput" class="file" type="file" accept="image/png,image/webp,image/svg+xml,.png,.webp,.svg" tabindex="-1" aria-label="Logo file" @change="upload('light', ($event.target as HTMLInputElement).files?.[0])" />
      <input ref="darkInput" class="file" type="file" accept="image/png,image/webp,image/svg+xml,.png,.webp,.svg" tabindex="-1" aria-label="Dark logo file" @change="upload('dark', ($event.target as HTMLInputElement).files?.[0])" />
    </div>
  </SettingsCard>
</template>

<style scoped>
.brand-form { display: grid; gap: 14px; }
.name-field { display: grid; gap: 6px; max-width: 320px; }
.label { font-size: 13px; color: var(--ink-2); }
.previews { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.preview { display: grid; gap: 8px; margin: 0; min-width: 0; }
/* Each preview is a slice of the header in its theme, whatever theme the page is in. */
.bar { display: flex; align-items: center; gap: 10px; height: 56px; padding: 0 16px; border-radius: 12px; overflow: hidden; }
.light .bar { background: #fbfaf6; box-shadow: inset 0 0 0 1px rgba(32, 60, 61, .1); color: #203c3d; }
.dark .bar { background: #183034; box-shadow: inset 0 0 0 1px rgba(237, 244, 240, .12); color: #edf4f0; }
.logo { display: grid; place-items: center; flex-shrink: 0; height: 32px; }
.logo img { display: block; object-fit: contain; }
.logo.plate { padding: 0 8px; border-radius: 9px; background: #f7f6f2; box-shadow: 0 0 0 1px rgba(164, 229, 223, .18); }
.name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: 600 14px/1.2 var(--font); }
.product { display: grid; place-items: center; width: 30px; height: 30px; border-radius: 8px; background: #f7f6f2; box-shadow: 0 0 0 1px rgba(164, 229, 223, .45); }
.wordmark { font: 600 12px/1 var(--mono); letter-spacing: .28em; white-space: nowrap; font-variant-ligatures: none; }
.wordmark sup { margin-left: 2px; font: 600 7.5px/1 var(--mono); letter-spacing: .16em; color: #0b5c59; }
.dark .wordmark sup { color: #bff0eb; }
figcaption { display: flex; align-items: center; gap: 8px; min-height: 28px; min-width: 0; }
.mode { font: 500 10.5px/1.4 var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-3); font-variant-ligatures: none; }
.auto { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; color: var(--ink-3); }
.actions { display: inline-flex; align-items: center; gap: 4px; margin-left: auto; flex-shrink: 0; }
.actions .btn { gap: 6px; }
.hint { margin: 0; font-size: 12.5px; color: var(--ink-3); }
.set-note { margin: 0; }
.file { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; opacity: 0; pointer-events: none; }
@media (max-width: 600px) {
  .previews { grid-template-columns: 1fr; }
  .name-field { max-width: none; }
}
</style>
