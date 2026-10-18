// SPDX-License-Identifier: AGPL-3.0-only
export function preferenceFailure(status: number, german = false) {
  if (status === 409) return german ? 'Andernorts geändert. Die Änderung wurde nicht gespeichert. Neu laden und erneut versuchen.' : 'Changed elsewhere. The change was not saved. Reload and try again.'
  if (status === 428) return german ? 'Die Person konnte nicht bestätigt werden. Die Änderung wurde nicht gespeichert. Neu laden und erneut versuchen.' : 'Your identity could not be confirmed. The change was not saved. Reload and try again.'
  if (status === 403) return german ? 'Dafür fehlt die Berechtigung. Die Änderung wurde nicht gespeichert.' : 'You do not have permission to do this. The change was not saved.'
  return german ? 'Konnte nicht gespeichert werden. Erneut versuchen.' : 'Could not save. Try again.'
}
/**
 * Entry points (a ticket's model, the Agents header, a project's live agents) open the one Models page. They keep
 * naming a project, kind and ticket; the minimal page ignores those and only `why` still acts: it opens the
 * "Why?" trace for the next queued ticket.
 */
export function modelsSettingsLink(context: { project?: { id: string }; level?: string; kind?: string; ticket?: string; why?: boolean } = {}) {
  const query = new URLSearchParams()
  if (context.project) query.set('project_id', context.project.id)
  query.set('layer', context.level === 'project' ? 'rules' : context.level === 'default' ? 'default' : 'mine')
  if (context.kind) query.set('kind', context.kind)
  if (context.ticket) query.set('ticket', context.ticket)
  if (context.why) query.set('why', '1')
  return `/settings/models?${query}`
}
