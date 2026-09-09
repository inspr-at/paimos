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
/**
 * PAI-980. The customer views are reachable from two shells: `/customers`
 * (5.x AppLayout) and `/crm` (Paimos 6 door). Every list ↔ detail link
 * derives its base from the current route so a user who entered through
 * the 6.0 shell stays in it.
 */
import { computed } from 'vue'
import { useRoute } from 'vue-router'

export function useCustomerBase() {
  const route = useRoute()
  const base = computed(() => (route.path === '/crm' || route.path.startsWith('/crm/') ? '/crm' : '/customers'))
  const detailPath = (id: number | string) => `${base.value}/${id}`
  return { base, detailPath }
}
