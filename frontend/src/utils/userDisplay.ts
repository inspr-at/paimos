export interface UserDisplayIdentity {
  username?: string
  nickname?: string
  first_name?: string
  last_name?: string
}

export function userDisplayName(user: UserDisplayIdentity | null | undefined): string {
  if (!user) return ''
  const fullName = [user.first_name?.trim(), user.last_name?.trim()].filter(Boolean).join(' ')
  return user.nickname?.trim() || fullName || user.username?.trim() || ''
}

/** At most two readable letters, derived from nicknames before login handles. */
export function userInitials(user: UserDisplayIdentity | null | undefined, fallback = '?'): string {
  if (!user) return fallback
  // Preserve the historical avatar source order: a deliberately chosen
  // nickname wins, otherwise the login handle distinguishes fixture users.
  // Full names remain a fallback for identities without a username.
  const source =
    user.nickname?.trim() ||
    user.username?.trim() ||
    [user.first_name?.trim(), user.last_name?.trim()].filter(Boolean).join(' ')
  if (!source) return fallback

  const words = source.split(/[\s_-]+/u).filter(Boolean)
  if (words.length > 1) {
    return `${words[0][0]}${words[words.length - 1][0]}`.toUpperCase()
  }
  return source.slice(0, 2).toUpperCase()
}
