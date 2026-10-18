// SPDX-License-Identifier: AGPL-3.0-only
import { readdirSync, readFileSync } from 'node:fs'
import { composePermissionData } from './permission_data_shared.mjs'

const directory = new URL('./permission_data/', import.meta.url)
const fragments = Object.fromEntries(readdirSync(directory).filter(name => name.endsWith('.json')).sort()
  .map(name => [name, JSON.parse(readFileSync(new URL(name, directory), 'utf8'))]))

export const { permissionWords, builtinAgentExclusions, projectSelfPermissions } = composePermissionData(fragments)
