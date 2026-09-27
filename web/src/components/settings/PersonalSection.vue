<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { brand } from '../../lib/brand'
import { run } from '../../lib/commands'
import { setTheme, themeChoice, type ThemeChoice } from '../../lib/theme'
import { toast } from '../../lib/toast'
import { useProfile } from '../../stores/profile'
import AppIcon from '../AppIcon.vue'
import KeyCap from '../KeyCap.vue'
import ProfileCard from './ProfileCard.vue'
import SettingsCard from './SettingsCard.vue'
import AgentIndicatorSettings from './AgentIndicatorSettings.vue'
import GraphMotionSettings from './GraphMotionSettings.vue'

// Everyone's own settings: the profile (photo, names, handle, time zone, language),
// the theme, the greeting, and the keys.
const store = useProfile()

const themes: { value: ThemeChoice; label: string; icon: 'sun' | 'moon' | 'monitor' }[] = [
  { value: 'light', label: 'Light', icon: 'sun' }, { value: 'dark', label: 'Dark', icon: 'moon' }, { value: 'system', label: 'System', icon: 'monitor' },
]
const themeGroup = ref<HTMLElement>()
function themeKeys(event: KeyboardEvent) {
  if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
  event.preventDefault()
  const index = themes.findIndex(theme => theme.value === themeChoice.value)
  setTheme(themes[(index + (event.key === 'ArrowRight' ? 1 : -1) + themes.length) % themes.length].value)
  void nextTick(() => themeGroup.value?.querySelector<HTMLElement>('[aria-checked="true"]')?.focus())
}

// ---------- Greeting (profile.greeting_enabled) ----------
const profile = computed(() => store.profile)
const profileError = computed(() => store.error ? 'Your profile could not be loaded.' : '')
const saving = ref(false)
// The switch shows the new state at once; a failed save puts it back.
const pending = ref<boolean | null>(null)
const greetingOn = computed(() => pending.value ?? profile.value?.greeting_enabled ?? false)
function load() { void store.load(true) }
async function setGreeting(on: boolean) {
  if (!profile.value || saving.value) return
  pending.value = on
  saving.value = true
  try {
    await store.save({ greeting_enabled: on })
    toast(on ? 'The greeting is on.' : 'The greeting is off.', { action: { label: 'Undo', run: () => void setGreeting(!on) } })
  } catch {
    toast('Your greeting setting could not be saved. Please try again.', { tone: 'error' })
  } finally { pending.value = null; saving.value = false }
}
onMounted(() => { void store.load() })

const KEYS: { keys: string[][]; label: string }[] = [
  { keys: [['mod', 'K']], label: 'Search everything' },
  { keys: [['g', 'p'], ['g', 'a'], ['g', 'b']], label: 'Go to Projects, Agents or Business' },
  { keys: [['?']], label: 'All shortcuts on the page you are on' },
]
</script>

<template>
  <div class="section">
    <SettingsCard title="Profile" icon="user" anchor="profile">
      <template #lead>How you appear to people and agents in this workspace.</template>
      <p v-if="profileError" class="set-note error" role="alert"><AppIcon name="alert" :size="14" />{{ profileError }}<button type="button" class="btn sm" @click="load">Try again</button></p>
      <ProfileCard v-else />
    </SettingsCard>

    <SettingsCard v-if="profile || profileError" title="Appearance" icon="sun" anchor="appearance">
      <template #lead>Light, dark, or whatever your system uses. Saved to your account, so it stays after a reload and on your other devices.</template>
      <template #aside>
        <div ref="themeGroup" class="seg" role="radiogroup" aria-label="Theme" @keydown="themeKeys">
          <button v-for="option in themes" :key="option.value" type="button" role="radio" :aria-checked="themeChoice === option.value" :tabindex="themeChoice === option.value ? 0 : -1" @click="setTheme(option.value)">
            <AppIcon :name="option.icon" :size="13" />{{ option.label }}
          </button>
        </div>
      </template>
      <GraphMotionSettings />
    </SettingsCard>

    <SettingsCard v-if="profile || profileError" title="Agents" icon="agent" anchor="agents">
      <template #lead>Your indicator style, state palette and heartbeat warnings.</template>
      <AgentIndicatorSettings />
    </SettingsCard>

    <SettingsCard v-if="profile || profileError" title="Greeting" icon="sparkle" anchor="greeting">
      <template #lead>A short, personal line when you open {{ brand.short_name }}. Only you see it.</template>
      <template #aside>
        <span v-if="!profile && !profileError" class="skeleton switch-skeleton" role="status" aria-label="Loading" />
        <button v-else-if="profileError" type="button" class="btn sm" @click="load"><AppIcon name="refresh" :size="12" />Try again</button>
        <label v-else class="switch">
          <input type="checkbox" :checked="greetingOn" :disabled="saving" aria-labelledby="greeting-title greeting-state" @change="setGreeting(($event.target as HTMLInputElement).checked)" />
          <span id="greeting-state">{{ greetingOn ? 'On' : 'Off' }}</span>
        </label>
      </template>
      <template v-if="profileError" #default><p class="error-line" role="alert"><AppIcon name="alert" :size="13" />{{ profileError }}</p></template>
    </SettingsCard>

    <SettingsCard v-if="profile || profileError" title="Keyboard" icon="keyboard" anchor="keys">
      <template #lead>Most of {{ brand.short_name }} works from the keyboard.</template>
      <template #aside><button type="button" class="btn sm" aria-keyshortcuts="?" @click="run({ name: 'shortcuts' })">All shortcuts<kbd class="keycap" aria-hidden="true">?</kbd></button></template>
      <dl class="keys">
        <div v-for="row in KEYS" :key="row.label">
          <dt><template v-for="(combo, i) in row.keys" :key="i"><span v-if="i" class="or" aria-hidden="true">·</span><span class="combo"><KeyCap v-for="k in combo" :key="k" :k="k" /></span></template></dt>
          <dd>{{ row.label }}</dd>
        </div>
      </dl>
    </SettingsCard>
  </div>
</template>

<style scoped>
.section { display: grid; gap: 14px; }
.seg button { height: 30px; }
.switch-skeleton { width: 72px; height: 20px; }
.error-line { display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--danger); }
.keys { display: grid; margin: 0; }
.keys > div { display: grid; grid-template-columns: minmax(150px, max-content) 1fr; align-items: center; gap: 16px; min-height: 38px; border-top: 1px solid var(--line); }
.keys dt { display: flex; flex-wrap: wrap; align-items: center; gap: 4px; }
.combo { display: inline-flex; gap: 3px; }
.or { color: var(--ink-3); padding: 0 2px; }
.keys dd { margin: 0; font-size: 13px; color: var(--ink-2); }
.btn .keycap { margin-right: -4px; }
@media (max-width: 600px) {
  .keys > div { grid-template-columns: minmax(0, 1fr); gap: 4px; padding: 8px 0; }
  .seg { width: 100%; display: grid; grid-template-columns: repeat(3, 1fr); }
  .seg button { height: 40px; }
}
</style>
