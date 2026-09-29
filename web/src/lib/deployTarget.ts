// SPDX-License-Identifier: AGPL-3.0-only
// Informational deployment metadata. Authority remains server-owned.
export interface DeployTarget {
  hosts?: string[]
  environment?: string
  service?: string
  image?: string
  change?: string
}
export interface DeployApproval {
  scope: string
  target?: DeployTarget | null
  target_digest_sha256?: string | null
}
export function isDeployScope(scope: string) {
  return scope === 'journey.deploy' || scope === 'stage.deploy'
}
const text = (value: unknown) => typeof value === 'string' ? value.trim() : ''
export function readDeployTarget(approval: DeployApproval | null | undefined) {
  const raw = approval?.target
  const hosts = Array.isArray(raw?.hosts) ? raw.hosts.map(text).filter(Boolean) : []
  const environment = text(raw?.environment)
  const where = [hosts.join(', '), environment].filter(Boolean).join(' · ')
  const service = text(raw?.service), image = text(raw?.image), change = text(raw?.change)
  return {
    applicable: !!approval && isDeployScope(approval.scope),
    standing: where ? 'named' : 'unknown',
    hosts, environment, service, image, change, digest: text(approval?.target_digest_sha256),
    where: where || 'Target not named',
    aria: where ? `Deploy target: ${[where, service, image, change].filter(Boolean).join(', ')}` : 'Target not named',
  }
}
export function deployTargetSentence(approval: DeployApproval | null | undefined) {
  const view = readDeployTarget(approval)
  if (view.standing !== 'named') return 'Target not named.'
  return [`Server: ${view.where}.`, view.service && `Service: ${view.service}.`, view.image && `Image: ${view.image}.`, view.change && `Change: ${view.change}.`].filter(Boolean).join(' ')
}
