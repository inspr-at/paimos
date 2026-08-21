/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, version 3.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public
 * License along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

// Security nag shared by every authenticated shell (AppLayout and the
// PAI-805 AgentModeLayout) so the rule cannot drift between them.
//
// PAI-742: SSO-minted sessions never see the local-2FA nag — their second
// factor is the IdP's policy, and a wrong nag trains users to ignore
// security banners.

import { computed } from 'vue'
import { useAuthStore } from '@/stores/auth'

export function useTotpNag() {
  const auth = useAuthStore()
  const show2FAWarning = computed(
    () =>
      auth.checked &&
      !!auth.user &&
      auth.totpChecked &&
      !auth.totpEnabled &&
      !auth.suppressSecurityNags &&
      !auth.ssoSession,
  )
  return { show2FAWarning }
}
