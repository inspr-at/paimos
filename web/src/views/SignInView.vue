<!-- SPDX-License-Identifier: AGPL-3.0-only -->
<script setup lang="ts">
import { brand } from '../lib/brand'
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import hero from '../assets/brand/paimos-hero.jpg'
import mark from '../assets/brand/aeon-mark.svg'
import { SignInError, useSession } from '../stores/session'
import { clearSignInReturn, pendingSignInReturn, rememberSignInReturn, safeReturnPath } from '../lib/signInReturn'
import { dark, toggleTheme } from '../lib/theme'
import AppIcon from '../components/AppIcon.vue'
import VersionDisplay from '../components/VersionDisplay.vue'

// First impression: the agora in cloud light behind one clear way in. Problems are
// explained inline where they happen, each with a way to try again.
const session = useSession()
const route = useRoute()
const router = useRouter()
const email = ref('')
const busy = ref(false)
const retrying = ref(false)
const devError = ref('')
const returnPath = computed(() => {
  const value = route.query.return
  return typeof value === 'string' ? safeReturnPath(value) : pendingSignInReturn()
})
function startOIDC() { rememberSignInReturn(returnPath.value); session.beginSignIn() }

// Codes the sign-in flow can send back as ?error=.
const FLOW_ERRORS: Record<string, { title: string; body: string }> = {
  failed: { title: 'Sign-in didn’t finish', body: 'INSPR ID answered, but the sign-in could not be completed. Please try once more.' },
  denied: { title: 'Sign-in was cancelled', body: 'Access was declined at INSPR ID. Sign in again whenever you are ready.' },
  expired: { title: 'Your session ended', body: 'For your security you were signed out after a while. Sign in again to pick up where you left off.' },
  not_member: { title: 'Not a member of this workspace yet', body: 'Your INSPR ID works, but this workspace has not added you. Ask its owner for an invitation.' },
  unavailable: { title: 'Sign-in is not available right now', body: 'The workspace is not ready to accept sign-ins. Please try again in a moment.' },
}
const flowError = computed(() => {
  const code = typeof route.query.error === 'string' ? route.query.error : ''
  return code ? FLOW_ERRORS[code] ?? FLOW_ERRORS.failed : null
})
const DEV_ERRORS: Record<SignInError['reason'], string> = {
  not_member: 'This email is not a member of this workspace yet.',
  disabled: 'Development sign-in is switched off on this server.',
  invalid: 'That does not look like an email address.',
  network: 'We could not reach the server. Check your connection and try again.',
  failed: 'Sign in didn’t complete. Check your email and try again.',
}

async function signIn() {
  busy.value = true
  devError.value = ''
  try {
    await session.devLogin(email.value.trim())
    await router.replace(returnPath.value)
    if (session.identity) clearSignInReturn()
    if (!session.identity && !session.error) devError.value = 'Your session wasn’t established. Please try again.'
  } catch (e) { devError.value = DEV_ERRORS[e instanceof SignInError ? e.reason : 'failed'] }
  finally { busy.value = false }
}
async function reconnect() {
  retrying.value = true
  await session.refresh()
  retrying.value = false
  if (session.identity) await router.replace('/')
}
function dismiss() { const { error: _error, ...rest } = route.query; void router.replace({ query: rest }) }
</script>

<template>
  <section class="signin-page" aria-labelledby="signin-title">
    <div class="art" aria-hidden="true"><img :src="hero" alt="" /></div>
    <div class="signin-card" :class="{ dev: session.devMode }">
      <div class="brand">
        <span class="mark-backing"><img :src="mark" width="34" height="34" alt="" /></span>
        <span class="wordmark">{{ brand.product }}<sup>{{ brand.release_name }}</sup></span>
      </div>
      <h1 id="signin-title">Sign in</h1>
      <p class="intro">Your projects, tickets and agents in one calm place.</p>

      <div v-if="session.error" class="notice problem" role="alert">
        <AppIcon name="alert" :size="16" />
        <div>
          <p class="notice-title">We can’t reach the server</p>
          <p>Check your connection. Nothing is lost; sign-in works again as soon as the server answers.</p>
          <button type="button" class="btn sm" :disabled="retrying" @click="reconnect"><AppIcon name="refresh" :size="13" />{{ retrying ? 'Trying…' : 'Try again' }}</button>
        </div>
      </div>
      <div v-else-if="flowError" class="notice" :class="{ problem: route.query.error !== 'expired' }" role="alert">
        <AppIcon :name="route.query.error === 'expired' ? 'clock' : 'alert'" :size="16" />
        <div>
          <p class="notice-title">{{ flowError.title }}</p>
          <p>{{ flowError.body }}</p>
          <div class="notice-actions">
            <a class="btn sm" href="/api/auth/login" @click="startOIDC"><AppIcon name="refresh" :size="13" />Try again</a>
            <button type="button" class="btn sm ghost" @click="dismiss">Dismiss</button>
          </div>
        </div>
      </div>

      <a class="btn primary login-button" href="/api/auth/login" @click="startOIDC"><AppIcon name="key" :size="16" />Sign in with INSPR ID<AppIcon name="arrow" :size="15" class="go" /></a>
      <p class="fine">One account for everything INSPR. You are sent to INSPR ID and back.</p>

      <form v-if="session.devMode" class="dev-form" @submit.prevent="signIn">
        <p class="dev-divider"><span class="eyebrow">Development only</span></p>
        <label for="email">Email address</label>
        <div class="dev-row">
          <input id="email" v-model="email" class="field" name="email" type="email" autocomplete="email" placeholder="you@example.com" required :disabled="busy" />
          <button class="btn" type="submit" :disabled="busy">{{ busy ? 'Signing in…' : 'Continue with email' }}</button>
        </div>
        <p v-if="devError" class="dev-error" role="alert"><AppIcon name="alert" :size="13" />{{ devError }}</p>
      </form>

      <footer class="card-foot">
        <span class="foot-name">{{ brand.wordmark }}</span>
        <VersionDisplay />
        <button class="icon-btn sm flat theme" type="button" :aria-label="dark ? 'Switch to light theme' : 'Switch to dark theme'" :data-tip="dark ? 'Light theme' : 'Dark theme'" @click="toggleTheme(false)"><AppIcon :name="dark ? 'sun' : 'moon'" :size="14" /></button>
      </footer>
    </div>
  </section>
</template>

<style scoped>
.signin-page { position: relative; display: grid; place-items: center; min-height: 100%; padding: 40px 20px; overflow: hidden; isolation: isolate; }
/* The agora in cloud light: faded into the canvas, never louder than the card. */
.art { position: absolute; inset: 0; z-index: -1; pointer-events: none; }
.art img {
  width: 100%; height: 100%; object-fit: cover; object-position: center 40%; opacity: var(--signin-art-opacity); filter: var(--signin-art-filter);
  -webkit-mask-image: radial-gradient(75% 70% at 50% 42%, #000 35%, transparent 100%); mask-image: radial-gradient(75% 70% at 50% 42%, #000 35%, transparent 100%);
}
.signin-card {
  position: relative; width: min(420px, 100%); padding: 30px 32px 12px; border-radius: 22px; border: 1px solid var(--glass-edge);
  background: linear-gradient(165deg, var(--surface-raised-2), var(--glass) 70%); box-shadow: var(--shadow-pop), var(--shadow);
  -webkit-backdrop-filter: blur(22px) saturate(1.15); backdrop-filter: blur(22px) saturate(1.15);
}
.signin-card::after { content: ''; position: absolute; left: 12%; right: 12%; top: 0; height: 1px; background: linear-gradient(90deg, transparent, var(--glass-edge), var(--aqua), var(--glass-edge), transparent); }
.brand { display: flex; align-items: center; gap: 11px; }
.mark-backing { display: grid; place-items: center; width: 44px; height: 44px; border-radius: 12px; background: #f7f6f2; box-shadow: 0 0 0 1px var(--glass-rim), 0 8px 20px -12px rgba(14, 111, 108, .6); }
.wordmark { font: 600 13px/1 var(--mono); letter-spacing: .28em; color: var(--ink); font-variant-ligatures: none; }
.wordmark sup { position: relative; top: -.15em; margin-left: 2px; font: 600 8px/1 var(--mono); letter-spacing: .16em; color: var(--teal-ink); }
h1 { margin-top: 26px; font-size: 34px; }
.intro { margin-top: 8px; font-size: 14.5px; }
.notice { display: grid; grid-template-columns: 16px minmax(0, 1fr); gap: 10px; margin-top: 20px; padding: 12px 14px; border-radius: 12px; background: var(--chip-teal-bg); box-shadow: inset 0 0 0 1px var(--chip-teal-line); font-size: 13px; }
.notice > svg { margin-top: 1px; color: var(--teal-ink); }
.notice.problem { background: var(--danger-bg); box-shadow: inset 0 0 0 1px var(--danger-line); }
.notice.problem > svg { color: var(--danger); }
.notice p { color: var(--ink-2); }
.notice .notice-title { color: var(--ink); font-weight: 650; margin-bottom: 2px; }
.notice .btn, .notice-actions { margin-top: 10px; }
.notice-actions { display: flex; gap: 6px; }
.notice-actions .btn { margin: 0; }
.login-button { display: flex; width: 100%; height: 48px; margin-top: 24px; font-size: 15px; gap: 10px; }
.login-button .go { margin-left: auto; }
.fine { margin-top: 10px; font-size: 12.5px; color: var(--ink-3); text-align: center; }
.dev-form { display: grid; gap: 8px; margin-top: 20px; }
.dev-divider { display: flex; align-items: center; gap: 10px; margin-bottom: 4px; }
.dev-divider::before, .dev-divider::after { content: ''; flex: 1; height: 1px; background: var(--line); }
.dev-divider .eyebrow { margin: 0; }
label { font-size: 12.5px; color: var(--ink-2); }
.dev-row { display: flex; gap: 8px; }
.dev-row .field { height: 38px; }
.dev-row .btn { height: 38px; flex-shrink: 0; }
.dev-error { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--danger); }
.card-foot { display: flex; align-items: center; gap: 10px; margin-top: 22px; padding-top: 2px; border-top: 1px solid var(--line); }
.foot-name { font: 600 10px/1 var(--mono); letter-spacing: .22em; color: var(--ink-3); font-variant-ligatures: none; }
.card-foot :deep(.version-display) { margin-left: auto; font-size: 12px; }
.theme { color: var(--ink-2); }
@media (prefers-reduced-motion: no-preference) {
  .signin-card { animation: rise .5s cubic-bezier(.2, .7, .2, 1); }
  @keyframes rise { from { opacity: 0; transform: translateY(12px); } to { opacity: 1; transform: none; } }
}
@media (max-width: 480px) {
  .signin-page { padding: 20px 12px; place-items: start center; padding-top: 8vh; }
  .signin-card { padding: 24px 20px 8px; }
  h1 { margin-top: 20px; font-size: 30px; }
  .dev-row { flex-direction: column; }
  .dev-row .field, .dev-row .btn { height: 44px; }
  .login-button { height: 50px; }
  .notice .btn { height: 44px; }
  .theme { width: 44px; height: 44px; }
}
@media (max-height: 740px) and (min-width: 481px) { .signin-page { padding-block: 16px; } h1 { margin-top: 18px; } .login-button { margin-top: 18px; } }
</style>
