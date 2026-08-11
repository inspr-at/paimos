<script setup lang="ts">
import { ref } from 'vue'
import { RouterLink } from 'vue-router'
import { api, ApiError } from '@/api/client'
import { useBranding } from '@/composables/useBranding'
import { useSidebarColors } from '@/composables/useSidebarColors'
import { formatDisplayVersion } from '@/utils/version'
import AppIcon from '@/components/AppIcon.vue'

const { branding } = useBranding()
const { bgColor, patternImage } = useSidebarColors()
const version = formatDisplayVersion(__APP_VERSION__)

const email = ref('')
const loading = ref(false)
const submitted = ref(false)
const error = ref('')

async function submit() {
  error.value = ''
  loading.value = true
  try {
    await api.post('/auth/forgot', { email: email.value.trim() })
    // Always show the same confirmation regardless of whether the email
    // was actually on file — the backend returns 202 either way to
    // prevent user enumeration, so the UI matches.
    submitted.value = true
  } catch (e) {
    // Rate limit or server error — still don't leak which.
    if (e instanceof ApiError && e.status === 429) {
      error.value = 'Too many requests. Please wait a few minutes and try again.'
    } else {
      error.value = 'Something went wrong. Please try again.'
    }
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-page" :style="{ background: bgColor }">
    <div class="pattern-bg" :style="{ backgroundImage: patternImage }" aria-hidden="true"></div>

    <div class="login-card">
      <div class="login-header">
        <img :src="branding.logo" :alt="branding.company" class="login-logo" />
        <h1 class="login-title">Reset password</h1>
        <p class="login-sub">We'll email you a link to choose a new one.</p>
      </div>

      <!-- Before submission: email input -->
      <form v-if="!submitted" @submit.prevent="submit" class="login-form">
        <div class="field">
          <label for="email">Email</label>
          <input
            id="email"
            v-model="email"
            type="email"
            autocomplete="email"
            placeholder="you@example.com"
            required
          />
        </div>
        <div v-if="error" class="login-error">{{ error }}</div>
        <button type="submit" class="btn btn-primary login-btn" :disabled="loading || !email">
          {{ loading ? 'Sending…' : 'Send reset link' }}
        </button>
        <RouterLink to="/login" class="login-forgot-link">← Back to sign in</RouterLink>
      </form>

      <!-- After submission: confirmation (same text whether email exists or not) -->
      <div v-else class="submitted-box">
        <AppIcon name="check" :size="32" class="submitted-icon" />
        <p class="submitted-title">Check your inbox</p>
        <p class="submitted-sub">
          If an account with <strong>{{ email }}</strong> exists, we've sent a password-reset link.
          It expires in 60 minutes.
        </p>
        <p class="submitted-hint">
          Didn't get the email? Check spam, or
          <a href="#" @click.prevent="submitted = false">try again</a>.
        </p>
        <RouterLink to="/login" class="btn btn-ghost login-btn-back">← Back to sign in</RouterLink>
      </div>

      <footer class="login-footer">
        <img :src="branding.logo" alt="" class="footer-logo" aria-hidden="true" />
        <span>{{ branding.company }}</span>
        <span class="footer-sep">·</span>
        <span>v{{ version }}</span>
      </footer>
    </div>
  </div>
</template>

<style scoped>
/* Reuses the same visual shell as LoginView — intentional duplication
   since these pages live side-by-side and should look identical. */

.login-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  position: relative;
  overflow: hidden;
}
.pattern-bg { position: absolute; inset: 0; opacity: 1; }
.login-card {
  position: relative;
  background: var(--bg-card);
  border-radius: 10px;
  box-shadow: var(--shadow-md), 0 0 0 1px rgba(0,0,0,.08);
  padding: 2.5rem 2.25rem 2rem;
  width: 100%;
  max-width: 360px;
}
.login-header { text-align: center; margin-bottom: 2rem; }
.login-logo { width: 52px; height: 52px; object-fit: contain; margin-bottom: .75rem; }
.login-title { font-size: 22px; font-weight: 700; color: var(--text); letter-spacing: -.02em; }
.login-sub   { font-size: 13px; color: var(--text-muted); margin-top: .2rem; }

.login-form { display: flex; flex-direction: column; gap: 1rem; }
.field { display: flex; flex-direction: column; gap: .35rem; }
.field label { font-size: 12px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: .05em; }

.login-error { background: #fde8e8; color: #c0392b; border-radius: var(--radius); padding: .5rem .75rem; font-size: 13px; }

.login-btn { width: 100%; justify-content: center; padding: .65rem; font-size: 14px; margin-top: .25rem; }
.login-btn-back { width: 100%; justify-content: center; font-size: 12px; color: var(--text-muted); margin-top: 1rem; text-decoration: none; }
.login-forgot-link {
  align-self: center; font-size: 12px; color: var(--text-muted);
  text-decoration: none; margin-top: .1rem;
}
.login-forgot-link:hover { color: var(--brand-blue); text-decoration: underline; }

.submitted-box { text-align: center; padding: .5rem 0; }
.submitted-icon { color: #2ecc71; margin-bottom: .75rem; }
.submitted-title { font-size: 17px; font-weight: 700; color: var(--text); margin-bottom: .5rem; }
.submitted-sub   { font-size: 13px; color: var(--text-muted); line-height: 1.5; margin-bottom: 1rem; }
.submitted-hint  { font-size: 12px; color: var(--text-muted); }
.submitted-hint a { color: var(--brand-blue); text-decoration: none; }
.submitted-hint a:hover { text-decoration: underline; }

.login-footer {
  display: flex; align-items: center; justify-content: center; gap: .4rem;
  margin-top: 1.75rem; color: #b0bec8; font-size: 11px; font-weight: 600;
  letter-spacing: .08em; text-transform: uppercase;
}
.footer-logo { width: 16px; height: 16px; object-fit: contain; opacity: .35; }
.footer-sep { opacity: .4; }
</style>
