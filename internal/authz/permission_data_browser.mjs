// SPDX-License-Identifier: AGPL-3.0-only
import { composePermissionData } from './permission_data_shared.mjs'

// The glob is a build input, never a committed fragment index or aggregate.
export const { permissionWords, builtinAgentExclusions, projectSelfPermissions } =
  composePermissionData(import.meta.glob('./permission_data/*.json', { eager: true, import: 'default' }))
