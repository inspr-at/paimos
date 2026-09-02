import { describe, expect, it } from 'vitest'

import {
  orchestratorSetupCommand,
  setupAgents,
  setupDisplayLabelError,
  setupExpectedDeployment,
  validSetupDisplayLabel,
} from './orchestratorSetup'

describe('PAI-884 orchestrator setup handoff', () => {
  it('accepts only an enforced matching health-derived instance name', () => {
    expect(
      setupExpectedDeployment({
        agent_bus_identity_enforced: true,
        agent_bus_instance: 'ppm',
        deployment_instance: 'ppm',
      }),
    ).toBe('ppm')
    expect(
      setupExpectedDeployment({
        agent_bus_identity_enforced: true,
        agent_bus_instance: 'pma',
        deployment_instance: 'ppm',
      }),
    ).toBeNull()
    expect(
      setupExpectedDeployment({
        agent_bus_identity_enforced: true,
        agent_bus_instance: '$(touch bad)',
        deployment_instance: '$(touch bad)',
      }),
    ).toBeNull()
  })

  it('accepts only unique canonical agents from the selected project', () => {
    expect(
      setupAgents(
        [
          { project_id: 42, name: 'amy', body: 'ignored' },
          { project_id: 42, name: 'reviewer' },
        ],
        42,
      ),
    ).toEqual([
      { projectId: 42, key: 'amy' },
      { projectId: 42, key: 'reviewer' },
    ])
    expect(setupAgents([{ project_id: 42, name: 'Amy' }], 42)).toBeNull()
    expect(setupAgents([{ project_id: 99, name: 'amy' }], 42)).toBeNull()
    expect(
      setupAgents(
        [
          { project_id: 42, name: 'amy' },
          { project_id: 42, name: 'amy' },
        ],
        42,
      ),
    ).toBeNull()
  })

  it('renders the full secret-free command with inert POSIX shell quoting', () => {
    const command = orchestratorSetupCommand({
      cliInstance: 'my-local-ppm',
      expectedDeployment: 'ppm',
      project: 'PAI',
      agent: 'amy',
      displayLabel: `Amy $(touch nope); O'Brien`,
    })
    expect(command).toBe(
      `paimos --instance 'my-local-ppm' orchestrator set --expect-deployment-instance 'ppm' --project 'PAI' --agent 'amy' --display-label 'Amy $(touch nope); O'"'"'Brien'`,
    )
    expect(command).not.toContain('api_key')
    expect(command).not.toContain('token')
    expect(command).not.toContain('curl')
  })

  it('enforces the API display-label boundary', () => {
    expect(validSetupDisplayLabel('Amy')).toBe(true)
    expect(validSetupDisplayLabel(' Amy')).toBe(false)
    expect(validSetupDisplayLabel('Amy\nOps')).toBe(false)
    expect(validSetupDisplayLabel('é'.repeat(33))).toBe(false)
    expect(setupDisplayLabelError('é'.repeat(33))).toBe(
      'Display label must be at most 64 UTF-8 bytes.',
    )
    expect(validSetupDisplayLabel('\ud800')).toBe(false)
  })
})
