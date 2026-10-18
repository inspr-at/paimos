-- aeon:no-transaction
-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-1315: activity pages include entry_checks.refused. The published
-- events_activity_page predicate cannot change in place, so this index covers
-- the candidate list in internal/activity/timeline.go timelineCandidatesSQL.
-- The concurrent migration runner bounds lock acquisition to 5s and restores it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS events_activity_page_v2 ON events(tenant_id,node_id,aeon_activity_at(type,after,at) DESC,id DESC)
 WHERE type IN ('import.comment','import.history','import.node_created','node.created','node.updated','entry_checks.refused','node.moved','node.kind_changed','comment.created','status_autopilot.changed','status_autopilot.undone','status_autopilot.skipped','status_autopilot.derived','status_autopilot.retained','status_autopilot.causal_undo');
