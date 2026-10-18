-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-611: cover project-scoped newest-answer keyset reads and timestamp ties.
CREATE INDEX desk_answers_newest
 ON desk_answers(tenant_id, project_id, created_at DESC, question_id DESC);
