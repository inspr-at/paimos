// SPDX-License-Identifier: AGPL-3.0-only
export const permissionWords: {
  resources: Record<string, string>
  actions: Record<string, string>
  special: Record<string, string>
}
export const builtinAgentExclusions: { permissions: string[] }
export const projectSelfPermissions: { permissions: string[] }
