// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, getSession, sessionEnded, type Identity } from '../lib/api'
import { restoreTheme } from '../lib/theme'
import { accessChanged, clearPermissions, refreshPermissions, revokePermissions } from '../lib/authz'
import { clearSignInReturn } from '../lib/signInReturn'
import { resetPositions } from '../lib/position'

export class SignInError extends Error {
  readonly reason: 'not_member' | 'disabled' | 'invalid' | 'network' | 'failed'
  constructor(reason: SignInError['reason']) { super(reason); this.reason = reason }
}

export const useSession = defineStore('session', () => {
  const identity = ref<Identity | null>(null)
  const devMode = ref(false)
  const error = ref('')
  const requiresSignIn = ref(false)
  let epoch = 0

  function invalidate() {
    epoch++
    identity.value = null
    requiresSignIn.value = true
    error.value = ''
    revokePermissions()
    resetPositions()
  }

  async function refresh() {
    const started = epoch
    error.value = ''
    try {
      const session = await getSession()
      // A late /me answer cannot restore a session after a 401 or sign-out.
      if (started !== epoch && session.identity) return
      if (requiresSignIn.value) { devMode.value = session.devMode; return }
      const before = identity.value
      identity.value = session.identity
      // Every navigation refreshes the session. The same person keeps the answers
      // on screen while they are asked again (a failed answer still grants
      // nothing); a different person or workspace starts from nothing.
      const same = !!before && !!session.identity && before.principal.id === session.identity.principal.id && before.tenant.id === session.identity.tenant.id
      if (same) void accessChanged()
      else {
        clearPermissions()
        resetPositions()
        if (session.identity) void refreshPermissions()
      }
      devMode.value = session.devMode
      if (session.identity) void restoreTheme()
    } catch {
      if (started !== epoch) return
      identity.value = null
      clearPermissions()
      devMode.value = false
      error.value = 'We couldn’t reach your workspace. Please try again.'
    }
  }

  async function signOut() {
    const response = await api('/auth/logout', { method: 'POST' })
    if (!response.ok) throw new Error('Sign out failed')
    invalidate()
    clearPermissions()
    clearSignInReturn()
  }

  // Errors carry a reason the sign-in page can explain: not_member, disabled, invalid, network, failed.
  async function devLogin(email: string) {
    if (!devMode.value) throw new SignInError('disabled')
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
    sessionEnded.blocked = false
    requiresSignIn.value = false
  }

  return { identity, devMode, error, requiresSignIn, refresh, invalidate, signOut, devLogin }
})
