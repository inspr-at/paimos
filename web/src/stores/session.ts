// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { onScopeDispose, ref, watch } from 'vue'
import { api, getSession, sessionEnded, type Identity } from '../lib/api'
import { resetAgentTheme, restoreAgentTheme } from '../lib/agentTheme'
import { restoreTheme } from '../lib/theme'
import { accessChanged, clearPermissions, refreshPermissions, revokePermissions } from '../lib/authz'
import { clearSignInReturn } from '../lib/signInReturn'
import { dropAttachCode } from '../lib/attachLink'
import { followAuthentication, OIDC_PENDING_KEY } from '../lib/authTabs'
import { resetToasts } from '../lib/toast'
import { settleConfirm } from '../lib/confirm'
import { setPreferenceOwner } from '../lib/preferences'
import { resetPositions } from '../lib/position'
import { resetReleaseOpen } from '../lib/releaseMembership'

export class SignInError extends Error {
  readonly reason: 'not_member' | 'disabled' | 'invalid' | 'network' | 'failed'
  constructor(reason: SignInError['reason']) { super(reason); this.reason = reason }
}

export const useSession = defineStore('session', () => {
  const identity = ref<Identity | null>(null)
  const devMode = ref(false)
  const oidcDisplayName = ref('')
  const error = ref('')
  const requiresSignIn = ref(false)
  let epoch = 0
  const tabs = followAuthentication(() => {
    // Keep drafts in a revoked tab, as with a 401. Another tab signing in does
    // not authorize this page's old view as the newly signed-in person.
    sessionEnded.blocked = true
    invalidate()
  })
  onScopeDispose(tabs.stop)

  function authenticationCurrent() { return tabs.current() }
  watch(identity, (who, before) => {
    const same = !!who && !!before && who.tenant.id === before.tenant.id && who.principal.id === before.principal.id
    if (!same) {
      resetReleaseOpen()
      resetToasts()
      settleConfirm(false)
    }
    setPreferenceOwner(who, authenticationCurrent)
  }, { immediate: true, flush: 'sync' })

  function beginSignIn() {
    invalidate()
    const generation = tabs.publish()
    // The OIDC round trip returns to a fresh document in this same tab.
    try { sessionStorage.setItem(OIDC_PENDING_KEY, generation) } catch { /* storage may be disabled */ }
  }

  function invalidate() {
    epoch++
    identity.value = null
    resetAgentTheme()
    requiresSignIn.value = true
    error.value = ''
    dropAttachCode()
    revokePermissions()
    resetPositions()
  }

  function readSignInConfig(session: Awaited<ReturnType<typeof getSession>>) {
    devMode.value = session.devMode
    // A blocked tab can receive a local, bodyless 401. Keep the last public
    // provider name; a server-provided empty string explicitly clears it.
    if (session.oidcDisplayName !== undefined) oidcDisplayName.value = session.oidcDisplayName
  }

  async function refresh() {
    const started = epoch
    error.value = ''
    try {
      const session = await getSession()
      // An initial 401 invalidates this tab inside api() before it returns. Its
      // public sign-in configuration is still needed to render sign-in.
      if (requiresSignIn.value && !session.identity && tabs.current()) readSignInConfig(session)
      // A late /me answer cannot restore a session after a 401 or sign-out.
      if (started !== epoch || !tabs.current()) return
      if (requiresSignIn.value) { readSignInConfig(session); return }
      const before = identity.value
      identity.value = session.identity
      if (session.identity) {
        let pending = false
        try { pending = tabs.owns(sessionStorage.getItem(OIDC_PENDING_KEY)); sessionStorage.removeItem(OIDC_PENDING_KEY) } catch { /* storage may be disabled */ }
        if (pending) tabs.publish()
      }
      // Every navigation refreshes the session. The same person keeps the answers
      // on screen while they are asked again (a failed answer still grants
      // nothing); a different person or workspace starts from nothing.
      const same = !!before && !!session.identity && before.principal.id === session.identity.principal.id && before.tenant.id === session.identity.tenant.id
      if (same) void accessChanged()
      else {
        clearPermissions()
        resetPositions()
        if (session.identity) {
          resetAgentTheme(`${session.identity.tenant.id}/${session.identity.principal.id}`)
          void refreshPermissions()
          await restoreAgentTheme(`${session.identity.tenant.id}/${session.identity.principal.id}`)
          if (started !== epoch || !tabs.current()) return
        } else resetAgentTheme()
      }
      readSignInConfig(session)
      if (session.identity) void restoreTheme()
    } catch {
      if (started !== epoch || !tabs.current()) return
      identity.value = null
      resetAgentTheme()
      clearPermissions()
      devMode.value = false
      error.value = 'We couldn’t reach your workspace. Please try again.'
    }
  }

  async function signOut() {
    // End outstanding work before the cookie can change. Keep the account menu
    // until the server answers, so a failed sign-out still offers its retry.
    const started = ++epoch
    setPreferenceOwner(null, authenticationCurrent)
    resetToasts()
    settleConfirm(false)
    dropAttachCode()
    revokePermissions()
    tabs.publish()
    try {
      const response = await api('/auth/logout', { method: 'POST' })
      if (!response.ok) throw new Error('Sign out failed')
    } catch (error) {
      if (started === epoch && tabs.current() && identity.value) { setPreferenceOwner(identity.value, authenticationCurrent); clearPermissions(); void refreshPermissions() }
      throw error
    }
    invalidate()
    sessionEnded.blocked = true
    tabs.publish()
    clearPermissions()
    clearSignInReturn()
  }

  // Errors carry a reason the sign-in page can explain: not_member, disabled, invalid, network, failed.
  async function devLogin(email: string) {
    if (!devMode.value) throw new SignInError('disabled')
    invalidate()
    tabs.publish()
    let response: Response
    try {
      response = await api('/auth/dev-login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email }),
      })
    } catch { throw new SignInError('network') }
    if (response.status === 403) throw new SignInError('not_member')
    if (response.status === 404) throw new SignInError('disabled')
    if (response.status === 400) throw new SignInError('invalid')
    if (!response.ok) throw new SignInError('failed')
    tabs.publish()
    sessionEnded.blocked = false
    requiresSignIn.value = false
  }

  return { identity, devMode, oidcDisplayName, error, requiresSignIn, refresh, invalidate, signOut, devLogin, beginSignIn, authenticationCurrent }
})
