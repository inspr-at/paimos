// SPDX-License-Identifier: AGPL-3.0-only
export function composePermissionData(fragments) {
  const permissionWords = { resources: {}, actions: {}, special: {} }
  const excluded = new Set(), self = new Set()
  const merge = (target, values, source, kind) => {
    for (const [key, value] of Object.entries(values ?? {})) {
      if (Object.hasOwn(target, key)) throw new Error(`authz: duplicate ${kind} ${key} in ${source}`)
      Object.defineProperty(target, key, { value, enumerable: true, writable: true, configurable: true })
    }
  }
  const mergeSet = (target, values, source, kind) => {
    for (const key of values ?? []) {
      if (target.has(key)) throw new Error(`authz: duplicate ${kind} ${key} in ${source}`)
      target.add(key)
    }
  }
  for (const [source, part] of Object.entries(fragments).sort(([a], [b]) => a.localeCompare(b))) {
    if (part._license !== 'SPDX-License-Identifier: AGPL-3.0-only') throw new Error(`authz: invalid permission data ${source}`)
    const fields = ['_license', 'words', 'builtin_agent_exclusions', 'project_self_permissions']
    if (Object.keys(part).some(key => !fields.includes(key)) ||
        Object.keys(part.words ?? {}).some(key => !Object.hasOwn(permissionWords, key))) {
      throw new Error(`authz: invalid permission data ${source}`)
    }
    for (const kind of ['resources', 'actions', 'special']) merge(permissionWords[kind], part.words?.[kind], source, kind)
    mergeSet(excluded, part.builtin_agent_exclusions, source, 'built-in agent exclusion')
    mergeSet(self, part.project_self_permissions, source, 'project self permission')
  }
  return { permissionWords, builtinAgentExclusions: { permissions: [...excluded].sort() }, projectSelfPermissions: { permissions: [...self].sort() } }
}
