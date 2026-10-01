// SPDX-License-Identifier: AGPL-3.0-only
import { stripAttachCode } from './attachLink.ts'

const key = 'aeon.signInReturn'

// Every return path, stored or built into a sign-in address, passes here: an
// attach code never travels in one (it would reach the server when the page reloads).
export function safeReturnPath(value: unknown): string {
  return typeof value === 'string' && value.startsWith('/') && !value.startsWith('//') && !value.startsWith('/\\') && !value.startsWith('/signin') ? stripAttachCode(value) : '/'
}

// Where a person who lost their session goes; the page they were on comes back after sign-in.
export const expiredSignIn = (fullPath: string) => ({ path: '/signin', query: { error: 'expired', return: safeReturnPath(fullPath) } })

export function pendingSignInReturn(): string {
  try { return safeReturnPath(sessionStorage.getItem(key)) } catch { return '/' }
}

export function rememberSignInReturn(path: string): void {
  try { sessionStorage.setItem(key, safeReturnPath(path)) } catch { /* Storage may be disabled. */ }
}

export function clearSignInReturn(): void {
  try { sessionStorage.removeItem(key) } catch { /* Storage may be disabled. */ }
}

export function takeSignInReturn(): string {
  const path = pendingSignInReturn()
  clearSignInReturn()
  return path
}
