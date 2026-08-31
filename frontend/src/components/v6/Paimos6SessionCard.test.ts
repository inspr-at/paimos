import { createApp, defineComponent, h } from 'vue'
import { afterEach, describe, expect, it } from 'vitest'

import Paimos6SessionCard from './Paimos6SessionCard.vue'
import type { Paimos6SessionViewModel } from '@/v6/sessionHome'

const mounted: Array<() => void> = []
afterEach(() => {
  while (mounted.length) mounted.pop()?.()
})

function session(
  id: string,
  patch: Partial<Paimos6SessionViewModel> = {},
): Paimos6SessionViewModel {
  return {
    id,
    revision: 1,
    title: id,
    summary: 'Live summary',
    agent: 'codex:amy',
    status: 'working',
    statusLabel: 'Working · active harness',
    attention: false,
    attentionReason: null,
    exceptionCount: 0,
    actionRequestCount: 0,
    node: { id: 854, key: 'PAI-854', label: 'PAI-854 · Paimos 6.0 cut' },
    unread: 1,
    latestUnreadAt: '2026-08-30T11:58:00Z',
    mode: 'managed',
    harnessName: 'codex',
    advertisedCapabilities: { inbox: true, status: true, steer: true, interrupt: true, stop: true },
    capabilities: { directSteer: true, interrupt: true, stop: true, paimosSteer: false },
    ...patch,
  }
}

function mountCards(rows: Paimos6SessionViewModel[]) {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(defineComponent({
    setup: () => () => h('div', rows.map((row) => h(Paimos6SessionCard, {
      key: row.id,
      session: row,
      selected: false,
    }))),
  }))
  app.mount(root)
  mounted.push(() => { app.unmount(); root.remove() })
  return root
}

describe('Paimos6SessionCard live truth rendering (PAI-861)', () => {
  it('renders loose sessions and multiple distinct sessions on one node', () => {
    const root = mountCards([
      session('session-a'),
      session('session-b'),
      session('session-loose', {
        node: null,
        mode: 'paimos',
        agent: 'Paimos',
        harnessName: null,
        advertisedCapabilities: null,
        capabilities: { directSteer: false, interrupt: false, stop: false, paimosSteer: true },
      }),
    ])
    expect(root.querySelectorAll('.p6-session-card')).toHaveLength(3)
    expect(root.textContent?.match(/PAI-854 · Paimos 6.0 cut/g)).toHaveLength(2)
    expect(root.querySelector('.p6-loose')?.textContent).toContain('Loose session')
  })

  it('shows owned controls only for a managed row with returned capability truth', () => {
    const root = mountCards([session('managed')])
    const actions = root.querySelector('.p6-card-actions')?.textContent ?? ''
    expect(actions).toContain('Steer')
    expect(actions).toContain('Interrupt')
    expect(actions).toContain('Stop')
    expect(actions).not.toContain('Ask Paimos')
  })

  it('never renders interrupt/stop/direct steer for unmanaged rows and shows the nudge', () => {
    const root = mountCards([session('unmanaged', {
      mode: 'unmanaged',
      harnessName: 'claude',
      advertisedCapabilities: { inbox: true, status: true, steer: false, interrupt: false, stop: false },
      capabilities: { directSteer: false, interrupt: false, stop: false, paimosSteer: true },
    })])
    const actions = root.querySelector('.p6-card-actions')?.textContent ?? ''
    expect(actions).toContain('Ask Paimos to steer')
    expect(actions).not.toContain('Interrupt')
    expect(actions).not.toContain('Stop')
    expect(actions).not.toMatch(/^\s*Steer\s*$/)
    expect(root.textContent).toContain('Paimos does not own this process')
  })
})
