// SPDX-License-Identifier: AGPL-3.0-only
// Vite and native Node consumers assemble the same domain inputs. Conditional
// imports keep filesystem access entirely out of the production browser.
export { permissionWords, builtinAgentExclusions, projectSelfPermissions } from '#permission-data'
