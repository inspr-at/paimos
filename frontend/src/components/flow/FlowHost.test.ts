/*
 * PAIMOS — Your Professional & Personal AI Project OS
 * Copyright (C) 2026 Markus Barta <markus@barta.com>
 */

import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineComponent, h, nextTick } from 'vue'

import { permissionsEpoch, sessionExpired } from '@/api/client'
import { mountComponent } from '@/components/ai/testMount'
import { msUntilNextTenMinuteBoundary } from '@/composables/useFlowHost'

const routerPush = vi.fn().mockResolvedValue(undefined)
const getFlowHostState = vi.fn()
const postFlowHostIntent = vi.fn()

vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/projects/9', query: { tab: 'overview' } }),
  useRouter: () => ({ push: routerPush }),
}))

vi.mock('@/services/flowHost', async () => {
  const actual = await vi.importActual<typeof import('@/services/flowHost')>('@/services/flowHost')
  return {
    ...actual,
    getFlowHostState: (...args: unknown[]) => getFlowHostState(...args),
    postFlowHostIntent: (...args: unknown[]) => postFlowHostIntent(...args),
  }
})

vi.mock('@inspr/flow-shell', () => ({}))
vi.mock('@inspr/flow-shell/assets/inspr-logo.svg', () => ({ default: '/flow-logo.svg' }))

import FlowHost from '@/components/flow/FlowHost.vue'

function usableState() {
  return {
    evaluatedAt: '2026-09-08T08:41:00.000Z',
    header: { appName: 'Paimos', version: 'ignore-me', projectName: 'Stream' },
    delivery: { status: 'draft', stageEvidence: ['unknown', 'unknown', 'unknown', 'unknown'] },
    prerequisites: {},
    progress: {},
    identityContext: {
      contract_version: 'inspr.flow-identity/0.1-draft',
      host_id: 'paimos',
      principal_kind: 'local_host',
      principal_ref: 'paimos:prin-abc',
      binding_ref: 'paimos:bind-abc',
      organization_ref: null,
      project_ref: 'paimos:proj-abc',
      actor_kind: 'human',
      issued_at: '2026-09-08T08:41:00.000Z',
      expires_at: '2026-09-08T16:41:00.000Z',
      fresh_until: '2026-09-08T08:50:00.000Z',
      context_revision: 'paimos:ctxrev-abc',
      display: { fixture_label: 'Host-issued Paimos session context. Not live identity.' },
    },
  }
}

describe('Flow host UI', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    getFlowHostState.mockReset()
    postFlowHostIntent.mockReset()
    routerPush.mockReset().mockResolvedValue(undefined)
    sessionExpired.value = false
    permissionsEpoch.value = null
  })

  it('keeps search toolbar and routes review without mutating start', async () => {
    getFlowHostState.mockResolvedValue(usableState())
    postFlowHostIntent.mockResolvedValue({
      executed: false,
      routed: 'project-overview-baseline',
      location: '/projects/9?tab=overview#baseline-batch',
      notice: 'Review stays on the existing project overview baseline controls.',
    })
    const HostHarness = defineComponent({
      setup() {
        return () =>
          h(
            FlowHost,
            { projectId: 9 },
            {
              toolbar: () => h('div', { 'data-testid': 'host-search' }, 'Search'),
              default: () => h('div', { id: 'baseline-batch' }, 'Baseline controls'),
            },
          )
      },
    })
    const mounted = await mountComponent(HostHarness)
    await vi.waitFor(() => expect(getFlowHostState).toHaveBeenCalledWith(9))
    await nextTick()
    expect(mounted.el.querySelector('[data-testid="host-search"]')?.textContent).toBe('Search')
    expect(mounted.el.querySelector('#baseline-batch')?.textContent).toContain('Baseline controls')
    const shell = mounted.el.querySelector('inspr-flow-shell')
    expect(shell).not.toBeNull()
    expect(shell?.getAttribute('layout-mode')).toBe('bounded')
    expect(shell?.getAttribute('content-layout')).toBeNull()
    shell?.dispatchEvent(
      new CustomEvent('flow-intent', {
        bubbles: true,
        composed: true,
        detail: { type: 'flow:review-batch' },
      }),
    )
    await vi.waitFor(() => expect(postFlowHostIntent).toHaveBeenCalled())
    expect(postFlowHostIntent.mock.calls[0][1]).toBe('flow:review-batch')
    await vi.waitFor(() =>
      expect(routerPush).toHaveBeenCalledWith('/projects/9?tab=overview#baseline-batch'),
    )
    shell?.dispatchEvent(
      new CustomEvent('flow-intent', {
        bubbles: true,
        composed: true,
        detail: { type: 'flow:start-intent' },
      }),
    )
    await vi.waitFor(() =>
      expect(postFlowHostIntent.mock.calls.some((call) => call[1] === 'flow:start-intent')).toBe(true),
    )
    await mounted.unmount()
  })

  it('drops Flow chrome when the session expires', async () => {
    getFlowHostState.mockResolvedValue(usableState())
    const mounted = await mountComponent(FlowHost, { projectId: 4 })
    await vi.waitFor(() => expect(getFlowHostState).toHaveBeenCalled())
    sessionExpired.value = true
    await vi.waitFor(() => expect(mounted.el.querySelector('[data-testid="paimos-flow-host"]')).toBeNull())
    await mounted.unmount()
  })

  it('aligns refresh to full ten-minute boundaries', () => {
    expect(msUntilNextTenMinuteBoundary(Date.parse('2026-09-08T08:41:00.000Z'))).toBe(9 * 60 * 1000)
    expect(msUntilNextTenMinuteBoundary(Date.parse('2026-09-08T08:50:00.000Z'))).toBe(10 * 60 * 1000)
  })

  it('does not mount Flow without a selected project', async () => {
    const mounted = await mountComponent(FlowHost, { projectId: null })
    await nextTick()
    expect(getFlowHostState).not.toHaveBeenCalled()
    expect(mounted.el.querySelector('inspr-flow-shell')).toBeNull()
    await mounted.unmount()
  })

  it('keeps bounded footer reservation in host integration CSS', () => {
    const source = readFileSync(
      join(dirname(fileURLToPath(import.meta.url)), 'FlowHost.vue'),
      'utf8',
    )
    expect(source).toMatch(/\.paimos-flow-host[\s\S]*padding-bottom:\s*var\(--shell-footer-space\)/)
  })

  it('keeps host footer slots mounted under bounded shell with App-level padding reset', async () => {
    const reset = document.createElement('style')
    reset.textContent = '*, *::before, *::after { margin: 0; padding: 0; }'
    document.head.appendChild(reset)

    getFlowHostState.mockResolvedValue(usableState())
    const mounted = await mountComponent(
      FlowHost,
      { projectId: 9 },
      {
        default: () =>
          h('div', [
            h('div', { id: 'project-footer-slot', class: 'project-footer-slot' }),
            h('footer', { class: 'habitat-footer' }, [
              h('a', { href: '/legacy' }, 'Classic workspace'),
              h('a', { href: '/settings' }, 'Settings'),
              h('a', { href: '/sessions' }, 'Product sessions'),
            ]),
          ]),
      },
    )
    await vi.waitFor(() => expect(getFlowHostState).toHaveBeenCalledWith(9))

    const shell = mounted.el.querySelector('inspr-flow-shell')
    const flowBody = mounted.el.querySelector('.paimos-flow-body')
    expect(shell).not.toBeNull()
    expect(shell?.classList.contains('paimos-flow-host')).toBe(true)
    expect(shell?.getAttribute('layout-mode')).toBe('bounded')
    expect(shell?.getAttribute('content-layout')).toBeNull()
    expect(flowBody?.querySelector('#project-footer-slot')).not.toBeNull()
    expect(flowBody?.querySelector('footer.habitat-footer')).not.toBeNull()

    reset.remove()
    await mounted.unmount()
  })

  it('marks classic fill hosts with region slots for bounded footer measurement', async () => {
    getFlowHostState.mockResolvedValue(usableState())
    const mounted = await mountComponent(
      FlowHost,
      { projectId: 9, contentLayout: 'fill' },
      {
        toolbar: () => h('header', { 'data-testid': 'classic-toolbar' }, 'Toolbar'),
        default: () =>
          h('div', [
            h('div', { class: 'main-content' }, 'Scroll body'),
            h('div', { id: 'project-footer-slot', class: 'project-footer-slot' }),
          ]),
      },
    )
    await vi.waitFor(() => expect(getFlowHostState).toHaveBeenCalledWith(9))

    const shell = mounted.el.querySelector('inspr-flow-shell')
    const toolbar = mounted.el.querySelector('.paimos-flow-toolbar')
    const body = mounted.el.querySelector('.paimos-flow-body')
    expect(shell?.getAttribute('content-layout')).toBe('fill')
    expect(toolbar?.getAttribute('data-flow-host-region')).toBe('toolbar')
    expect(body?.getAttribute('data-flow-host-region')).toBe('body')
    expect(body?.querySelector('#project-footer-slot')).not.toBeNull()

    await mounted.unmount()
  })
})
