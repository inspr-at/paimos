// SPDX-License-Identifier: AGPL-3.0-only
import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'
import { getKinds, listNodes, type Kind, type ListItem } from '../lib/api'
import { listPrincipals, listRates, type CostRate, type Principal, type Unit } from '../lib/business'
import { availability, configurePlugin, installationWrite, listPlugins, type BusinessPlugin } from '../components/business/catalog'
import { can } from '../lib/authz'
import { businessSections } from '../components/business/areas'
import { api } from '../lib/api'
import { useSession } from './session'

export type AreaId = 'costs' | 'crm' | 'quotes' | 'hours'
export interface CostUnit { node: ListItem; rates: CostRate[] }

// Tenant kinds the business screens create when an admin sets Business up.
// Kinds are tenant configuration: contacts get email, phone and role besides
// the plugin's note; the screens show whatever fields the live kind declares.
const KIND_SETUP: Record<string, { label: string; short_prefix: string; icon: string; field_schema: Record<string, unknown> }> = {
  organisation: {
    label: 'Organisation', short_prefix: 'ORG', icon: 'organisation',
    field_schema: { type: 'object', additionalProperties: false, properties: { legal_name: { type: 'string', maxLength: 200 }, website: { type: 'string', maxLength: 500 } } },
  },
  contact: {
    label: 'Contact', short_prefix: 'CON', icon: 'contact',
    field_schema: { type: 'object', additionalProperties: false, properties: { email: { type: 'string', maxLength: 320 }, phone: { type: 'string', maxLength: 60 }, role: { type: 'string', maxLength: 120 }, note: { type: 'string', maxLength: 500 } } },
  },
  quote: { label: 'Quote', short_prefix: 'QUO', icon: 'quote', field_schema: { type: 'object', properties: {} } },
  cost_unit: { label: 'Cost unit', short_prefix: 'CU', icon: 'cost_unit', field_schema: { type: 'object' } },
}
const AREA_PLUGIN: Record<AreaId, string> = { costs: 'business_costs', crm: 'business_crm', quotes: 'business_quotes', hours: 'business_hours' }
const AREA_KINDS: Record<AreaId, string[]> = { costs: ['cost_unit'], crm: ['organisation', 'contact'], quotes: ['quote'], hours: [] }

// Business setup, people and rates shared by the current business screens.
// Every list is a read model; the server checks every change again.
export const useBusiness = defineStore('business', () => {
  const session = useSession()
  const plugins = ref<BusinessPlugin[] | null>(null)
  const pluginsError = ref('')
  const kinds = ref<Kind[]>([])
  const principals = ref<Principal[]>([])
  const costUnits = ref<CostUnit[]>([])
  const costUnitsLoaded = ref(false)
  const admin = computed(() => can('plugins.manage'))
  const staff = computed(() => can('crm.write'))

  // ---------- Plugins ----------
  const open = computed<Record<AreaId, boolean>>(() => {
    const out = { costs: false, crm: false, quotes: false, hours: false }
    if (!plugins.value) return out
    for (const area of businessSections) out[area.id as AreaId] = availability(area, plugins.value).open
    return out
  })
  const anyOpen = computed(() => Object.values(open.value).some(Boolean))
  // The header's Business place from the first paint: until the plugins are read,
  // this browser's last answer for the workspace stands in, so the header does not
  // shift sideways a moment after every load.
  const hintKey = computed(() => session.identity ? `aeon.business-place.${session.identity.tenant.id}` : '')
  const remembered = computed<boolean | null>(() => { if (!hintKey.value) return null; try { const v = localStorage.getItem(hintKey.value); return v === '1' ? true : v === '0' ? false : null } catch { return null } })
  const placeOpen = computed(() => plugins.value || pluginsError.value ? anyOpen.value : remembered.value === true)
  // Whether the header can already say if Business is one of the places: read, failed
  // (it then stays out), or remembered from this browser's last visit.
  const placeKnown = computed(() => !!plugins.value || !!pluginsError.value || remembered.value !== null)
  watch([plugins, anyOpen], () => {
    if (!plugins.value || !hintKey.value) return
    try { localStorage.setItem(hintKey.value, anyOpen.value ? '1' : '0') } catch { /* private window: the place waits for the plugins */ }
  })
  // The overview's parts status stays until hours and rates are on; Customers
  // and Quotes are added in Settings › Business when a workspace wants them.
  const allOpen = computed(() => open.value.costs && open.value.hours)
  let pluginRequest: Promise<void> | null = null
  function loadPlugins(force = false): Promise<void> {
    if (pluginRequest) return pluginRequest
    if (plugins.value && !force) return Promise.resolve()
    pluginRequest = (async () => {
      try { plugins.value = await listPlugins(); pluginsError.value = '' }
      catch (e) { pluginsError.value = e instanceof Error ? e.message : 'Business settings could not be loaded.'; if (!plugins.value) plugins.value = null }
      finally { pluginRequest = null }
    })()
    return pluginRequest
  }
  async function loadKinds(force = false) {
    if (kinds.value.length && !force) return
    kinds.value = (await getKinds()).items
  }
  const kindBySlug = (slug: string) => kinds.value.find(kind => kind.slug === slug)
  function fieldsOf(slug: string): string[] {
    const properties = kindBySlug(slug)?.field_schema?.properties
    return properties && typeof properties === 'object' ? Object.keys(properties) : []
  }

  // Enable plugins (compiled digest, manifest permissions) and add missing kinds.
  async function enable(areas: AreaId[]) {
    const catalog = plugins.value ?? await listPlugins()
    const order: AreaId[] = ['costs', 'crm', 'quotes', 'hours']
    for (const area of order.filter(a => areas.includes(a))) {
      const plugin = catalog.find(item => item.id === AREA_PLUGIN[area])
      if (!plugin) throw new Error(`${area} is not part of this build.`)
      await configurePlugin(plugin.id, installationWrite(plugin, true))
    }
    await loadKinds(true)
    for (const slug of new Set(areas.flatMap(area => AREA_KINDS[area]))) {
      if (kindBySlug(slug)) continue
      // A plugin's own field schema wins, so its records fit the kind (CRM fields,
      // addresses, rates); the setup here is the fallback for older catalogs.
      const declared = catalog.flatMap(item => item.node_kinds).find(kind => kind.slug === slug)?.field_schema
      const setup = { ...KIND_SETUP[slug], ...(declared ? { field_schema: declared } : {}) }
      const response = await api('/kinds', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ slug, allowed_child_kinds: [], ...setup }) })
      if (!response.ok && response.status !== 409) throw new Error(`The ${setup.label.toLowerCase()} type could not be added.`)
    }
    await Promise.all([loadPlugins(true), loadKinds(true)])
  }
  async function disable(area: AreaId) {
    const plugin = plugins.value?.find(item => item.id === AREA_PLUGIN[area])
    if (!plugin) return
    await configurePlugin(plugin.id, installationWrite(plugin, false))
    await loadPlugins(true)
  }

  // ---------- People and agents ----------
  let principalsRequest: Promise<void> | null = null
  function loadPrincipals(force = false) {
    if (principalsRequest) return principalsRequest
    if (principals.value.length && !force) return Promise.resolve()
    principalsRequest = listPrincipals().then(items => { principals.value = items }).catch(() => undefined).finally(() => { principalsRequest = null })
    return principalsRequest
  }
  function nameOf(principalId: string | null | undefined): string {
    if (!principalId) return '—'
    if (principalId === session.identity?.principal.id) return session.identity.identity?.display_name?.trim() || session.identity.principal.name
    return principals.value.find(p => p.id === principalId)?.name ?? 'Someone'
  }
  const people = computed(() => principals.value.filter(p => p.kind === 'person'))
  // Staff who log time, and agents that are not system accounts.
  const timeKeepers = computed(() => principals.value.filter(p => p.kind === 'person' ? !p.roles.includes('customer') : p.kind === 'agent' && !p.roles.includes('system')))

  // ---------- Cost units and rates ----------
  let costRequest: Promise<void> | null = null
  function loadCostUnits(force = false) {
    if (costRequest) return costRequest
    if (costUnitsLoaded.value && !force) return Promise.resolve()
    costRequest = (async () => {
      try {
        const page = await listNodes({ kind: ['cost_unit'], sort: 'key', limit: 200 })
        const withRates = await Promise.all(page.items.map(async node => ({ node, rates: await listRates(node.id).catch(() => [] as CostRate[]) })))
        costUnits.value = withRates
        costUnitsLoaded.value = true
      } finally { costRequest = null }
    })()
    return costRequest
  }
  function setRates(costUnitId: string, rates: CostRate[]) {
    costUnits.value = costUnits.value.map(unit => unit.node.id === costUnitId ? { ...unit, rates } : unit)
  }
  function addCostUnit(node: ListItem) { costUnits.value = [...costUnits.value, { node, rates: [] }] }
  const costUnit = (id: string) => costUnits.value.find(unit => unit.node.id === id)
  // The bill rate in force on a UTC date (YYYY-MM-DD): half-open [from, until).
  function rateOn(costUnitId: string, unit: Unit, currency: string, day = new Date().toISOString().slice(0, 10)): CostRate | null {
    const rates = costUnit(costUnitId)?.rates ?? []
    return rates.filter(rate => rate.unit === unit && rate.currency === currency && rate.effective_from <= day && (!rate.effective_until || rate.effective_until > day))
      .sort((a, b) => b.effective_from.localeCompare(a.effective_from))[0] ?? null
  }
  const currencies = computed(() => [...new Set(costUnits.value.flatMap(unit => unit.rates.map(rate => rate.currency)))].sort())
  // Cost units usable today for a unit and currency (live, not cancelled, with a rate).
  function usable(unit: Unit | null, currency: string | null, day?: string) {
    return costUnits.value.filter(entry => !['cancelled', 'archived', 'done'].includes(entry.node.state)
      && entry.rates.some(rate => (!unit || rate.unit === unit) && (!currency || rate.currency === currency) && rate.effective_from <= (day ?? new Date().toISOString().slice(0, 10)) && (!rate.effective_until || rate.effective_until > (day ?? new Date().toISOString().slice(0, 10)))))
  }

  function reset() {
    plugins.value = null; kinds.value = []; principals.value = []; costUnits.value = []; costUnitsLoaded.value = false
  }

  return {
    plugins, pluginsError, open, anyOpen, placeOpen, placeKnown, allOpen, admin, staff, loadPlugins, enable, disable,
    kinds, loadKinds, kindBySlug, fieldsOf,
    principals, loadPrincipals, nameOf, people, timeKeepers,
    costUnits, costUnitsLoaded, loadCostUnits, setRates, addCostUnit, costUnit, rateOn, currencies, usable,
    reset,
  }
})
