// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
)

// structuredKnowledgeShortBodyLimitBytes is the production-pinned reviewed
// Compact body limit. Proposal candidates may be larger (up to 64 KiB), but a
// durable structured entry cannot cross this UTF-8 byte boundary.
const structuredKnowledgeShortBodyLimitBytes = 1200

// ApplyStructuredKnowledgeMigrationForTest applies M167 explicitly to an
// isolated database stopped at M166. Production registration lives in db.go;
// this helper remains only for focused migration fixtures.
func ApplyStructuredKnowledgeMigrationForTest(ctx context.Context, database *sql.DB, shortBodyLimit int) error {
	if os.Getenv("PAIMOS_TEST_MODE") != "1" {
		return fmt.Errorf("structured knowledge test migration requires PAIMOS_TEST_MODE=1")
	}
	if database == nil || shortBodyLimit <= 0 {
		return fmt.Errorf("structured knowledge test migration requires a database and positive body limit")
	}
	conn, err := database.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := checkM167SchemaIsUnapplied(ctx, conn); err != nil {
		return err
	}
	return applyMigrationAtomic(ctx, conn, migration{version: 167, steps: migration167StructuredKnowledgeSteps(shortBodyLimit)})
}

// migration167StructuredKnowledgeSteps is parameterized so migration tests can
// prove byte-bound behavior independently. Production always passes the pinned
// structuredKnowledgeShortBodyLimitBytes constant.
func migration167StructuredKnowledgeSteps(shortBodyLimit int) []string {
	return []string{
		`CREATE TABLE knowledge_compact_sessions (
		 project_id                 INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
		 product_session_id         TEXT NOT NULL UNIQUE REFERENCES product_sessions(product_session_id) ON DELETE RESTRICT,
		 revision                   INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
		 created_by_user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 updated_by_user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at                 TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("created_at") + `),
		 updated_at                 TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("updated_at") + `)
		)`,
		`CREATE TRIGGER trg_knowledge_compact_session_insert BEFORE INSERT ON knowledge_compact_sessions
		 WHEN NOT EXISTS(SELECT 1 FROM product_sessions ps WHERE ps.product_session_id=NEW.product_session_id
		  AND ps.project_id=NEW.project_id AND ps.target_kind='paimos' AND ps.target_project_agent_id IS NULL)
		 BEGIN SELECT RAISE(ABORT,'Compact must be a same-project Paimos product session'); END`,
		`CREATE TRIGGER trg_knowledge_compact_session_update BEFORE UPDATE ON knowledge_compact_sessions
		 WHEN NEW.project_id<>OLD.project_id OR NEW.revision<>OLD.revision+1 OR NEW.updated_by_user_id IS NULL OR
		  NOT EXISTS(SELECT 1 FROM product_sessions ps WHERE ps.product_session_id=NEW.product_session_id
		   AND ps.project_id=NEW.project_id AND ps.target_kind='paimos' AND ps.target_project_agent_id IS NULL) OR
		  (NEW.product_session_id<>OLD.product_session_id AND (
		   EXISTS(SELECT 1 FROM structured_knowledge_entries sk WHERE sk.origin_project_id=OLD.project_id) OR
		   EXISTS(SELECT 1 FROM structured_knowledge_proposals sp WHERE sp.project_id=OLD.project_id)
		  ))
		 BEGIN SELECT RAISE(ABORT,'Compact binding requires a fresh revision and cannot move after use'); END`,

		`CREATE TABLE structured_knowledge_entries (
		 knowledge_id                 INTEGER PRIMARY KEY REFERENCES issues(id) ON DELETE CASCADE,
		 level                        TEXT NOT NULL CHECK(level IN ('project','instance','kernel','vision')),
		 origin_project_id            INTEGER NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
		 purpose                      TEXT NOT NULL CHECK(length(CAST(purpose AS BLOB)) BETWEEN 1 AND 400
		  AND purpose=trim(purpose) AND instr(purpose,char(0))=0
		  AND purpose NOT GLOB ('*['||char(1)||'-'||char(31)||char(127)||']*')),
		 authored_product_session_id  TEXT NOT NULL REFERENCES product_sessions(product_session_id) ON DELETE RESTRICT,
		 revision                     INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
		 created_by_user_id           INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 updated_by_user_id           INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at                   TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("created_at") + `),
		 updated_at                   TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("updated_at") + `)
		)`,
		`CREATE INDEX idx_structured_knowledge_level ON structured_knowledge_entries(level,origin_project_id,knowledge_id)`,
		`CREATE TRIGGER trg_structured_knowledge_entry_insert BEFORE INSERT ON structured_knowledge_entries
		 WHEN NOT EXISTS(
		  SELECT 1 FROM issues i JOIN knowledge_compact_sessions kc ON kc.project_id=NEW.origin_project_id
		  WHERE i.id=NEW.knowledge_id AND i.deleted_at IS NULL
		   AND i.type IN ('memory','runbook','external_system','related_project','guideline')
		   AND length(CAST(i.slug AS BLOB)) BETWEEN 1 AND 64 AND i.slug GLOB '[a-z]*'
		   AND i.slug NOT GLOB '*[^a-z0-9_-]*'
		   AND length(CAST(i.title AS BLOB)) BETWEEN 1 AND 240 AND i.title=trim(i.title)
		   AND instr(i.title,char(0))=0 AND i.title NOT GLOB ('*['||char(1)||'-'||char(31)||char(127)||']*')
		   AND length(CAST(COALESCE(i.description,'') AS BLOB)) BETWEEN 1 AND ` + fmt.Sprint(shortBodyLimit) + `
		   AND trim(COALESCE(i.description,''))<>'' AND instr(COALESCE(i.description,''),char(0))=0
		   AND COALESCE(i.category_metadata,'') IN ('','{}')
		   AND i.updated_at=NEW.updated_at
		   AND kc.product_session_id=NEW.authored_product_session_id
		   AND ((NEW.level='project' AND i.project_id=NEW.origin_project_id AND i.user_id IS NULL) OR
		        (NEW.level IN ('instance','kernel','vision') AND i.project_id IS NULL AND i.user_id IS NULL))
		   AND (NEW.level='project' OR EXISTS(
		    SELECT 1 FROM structured_knowledge_promotions promotion
		    JOIN structured_knowledge_entries source_scope ON source_scope.knowledge_id=promotion.source_knowledge_id
		    WHERE promotion.destination_knowledge_id=NEW.knowledge_id AND promotion.to_level=NEW.level
		     AND promotion.from_level=source_scope.level
		     AND NEW.origin_project_id=source_scope.origin_project_id
		     AND NEW.purpose=source_scope.purpose
		     AND NEW.authored_product_session_id=source_scope.authored_product_session_id))
		 ) BEGIN SELECT RAISE(ABORT,'structured knowledge entry violates content, scope, or Compact session contract'); END`,
		`CREATE TRIGGER trg_structured_knowledge_entry_update BEFORE UPDATE ON structured_knowledge_entries
		 WHEN NEW.knowledge_id<>OLD.knowledge_id OR NEW.level<>OLD.level OR NEW.origin_project_id<>OLD.origin_project_id OR
		  NEW.authored_product_session_id<>OLD.authored_product_session_id OR NEW.created_by_user_id<>OLD.created_by_user_id OR
		  NEW.created_at<>OLD.created_at OR NEW.revision<>OLD.revision+1 OR NEW.updated_by_user_id IS NULL
		 BEGIN SELECT RAISE(ABORT,'structured knowledge identity is immutable and updates require a fresh revision'); END`,
		`CREATE TRIGGER trg_structured_knowledge_issue_update BEFORE UPDATE OF
		 project_id,user_id,type,slug,title,description,category_metadata,deleted_at ON issues
		 WHEN EXISTS(SELECT 1 FROM structured_knowledge_entries sk WHERE sk.knowledge_id=OLD.id)
		  AND NOT EXISTS(
		   SELECT 1 FROM structured_knowledge_entries sk WHERE sk.knowledge_id=OLD.id
		    AND NEW.type IN ('memory','runbook','external_system','related_project','guideline')
		    AND length(CAST(NEW.slug AS BLOB)) BETWEEN 1 AND 64 AND NEW.slug GLOB '[a-z]*'
		    AND NEW.slug NOT GLOB '*[^a-z0-9_-]*'
		    AND ((sk.level='project' AND NEW.project_id=sk.origin_project_id AND NEW.user_id IS NULL) OR
		         (sk.level IN ('instance','kernel','vision') AND NEW.project_id IS NULL AND NEW.user_id IS NULL))
		    AND ((NEW.title IS OLD.title AND NEW.description IS OLD.description AND NEW.slug IS OLD.slug
		          AND NEW.category_metadata IS OLD.category_metadata AND NEW.type IS OLD.type) OR
		         (sk.updated_at=NEW.updated_at AND NEW.updated_at<>OLD.updated_at))
		    AND (NEW.deleted_at IS NOT NULL OR (
		     length(CAST(NEW.title AS BLOB)) BETWEEN 1 AND 240 AND NEW.title=trim(NEW.title)
		     AND instr(NEW.title,char(0))=0 AND NEW.title NOT GLOB ('*['||char(1)||'-'||char(31)||char(127)||']*')
		     AND length(CAST(COALESCE(NEW.description,'') AS BLOB)) BETWEEN 1 AND ` + fmt.Sprint(shortBodyLimit) + `
		     AND trim(COALESCE(NEW.description,''))<>'' AND instr(COALESCE(NEW.description,''),char(0))=0
		     AND COALESCE(NEW.category_metadata,'') IN ('','{}')
		    ))
		  ) BEGIN SELECT RAISE(ABORT,'structured knowledge issue violates content or scope contract'); END`,

		`CREATE TABLE structured_knowledge_proposals (
		 proposal_id               TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("proposal_id") + `),
		 project_id                INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		 product_session_id        TEXT NOT NULL REFERENCES product_sessions(product_session_id) ON DELETE RESTRICT,
		 source_kind               TEXT NOT NULL CHECK(source_kind='remember'),
		 proposed_type             TEXT NOT NULL CHECK(proposed_type IN ('memory','runbook','external_system','related_project','guideline')),
		 slug                      TEXT NOT NULL CHECK(length(CAST(slug AS BLOB)) BETWEEN 1 AND 64
		  AND slug GLOB '[a-z]*' AND slug NOT GLOB '*[^a-z0-9_-]*'),
		 title                     TEXT NOT NULL CHECK(length(CAST(title AS BLOB)) BETWEEN 1 AND 240 AND title=trim(title)
		  AND instr(title,char(0))=0 AND title NOT GLOB ('*['||char(1)||'-'||char(31)||char(127)||']*')),
		 purpose                   TEXT NOT NULL CHECK(length(CAST(purpose AS BLOB)) BETWEEN 1 AND 400 AND purpose=trim(purpose)
		  AND instr(purpose,char(0))=0 AND purpose NOT GLOB ('*['||char(1)||'-'||char(31)||char(127)||']*')),
		 candidate_body            TEXT NOT NULL CHECK(length(CAST(candidate_body AS BLOB)) BETWEEN 1 AND 65536
		  AND trim(candidate_body)<>'' AND instr(candidate_body,char(0))=0),
		 state                     TEXT NOT NULL DEFAULT 'proposed' CHECK(state IN ('proposed','dismissed','promoted')),
		 promoted_knowledge_id     INTEGER REFERENCES structured_knowledge_entries(knowledge_id) ON DELETE RESTRICT,
		 created_by_user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at                TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("created_at") + `),
		 updated_at                TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("updated_at") + `),
		 CHECK((state='promoted')=(promoted_knowledge_id IS NOT NULL))
		)`,
		`CREATE INDEX idx_structured_knowledge_proposals_project ON structured_knowledge_proposals(project_id,state,updated_at,proposal_id)`,
		`CREATE TRIGGER trg_structured_knowledge_proposal_insert BEFORE INSERT ON structured_knowledge_proposals
		 WHEN NOT EXISTS(SELECT 1 FROM knowledge_compact_sessions kc JOIN product_sessions ps
		  ON ps.product_session_id=kc.product_session_id AND ps.project_id=kc.project_id
		  WHERE kc.project_id=NEW.project_id AND kc.product_session_id=NEW.product_session_id)
		 BEGIN SELECT RAISE(ABORT,'remember proposal requires the project Compact product session'); END`,
		`CREATE TRIGGER trg_structured_knowledge_proposal_update BEFORE UPDATE ON structured_knowledge_proposals
		 WHEN NEW.proposal_id<>OLD.proposal_id OR NEW.project_id<>OLD.project_id OR NEW.product_session_id<>OLD.product_session_id OR
		  NEW.source_kind<>OLD.source_kind OR NEW.proposed_type<>OLD.proposed_type OR NEW.slug<>OLD.slug OR NEW.title<>OLD.title OR
		  NEW.purpose<>OLD.purpose OR NEW.candidate_body<>OLD.candidate_body OR NEW.created_by_user_id<>OLD.created_by_user_id OR
		  NEW.created_at<>OLD.created_at OR OLD.state<>'proposed' OR NEW.state NOT IN ('dismissed','promoted') OR
		  (NEW.state='promoted' AND NOT EXISTS(
		   SELECT 1 FROM structured_knowledge_entries sk JOIN issues i ON i.id=sk.knowledge_id
		   WHERE sk.knowledge_id=NEW.promoted_knowledge_id AND sk.level='project'
		    AND sk.origin_project_id=NEW.project_id AND i.project_id=NEW.project_id AND i.deleted_at IS NULL
		    AND i.type=NEW.proposed_type AND i.slug=NEW.slug AND i.title=NEW.title
		    AND COALESCE(i.description,'')=NEW.candidate_body AND sk.purpose=NEW.purpose
		    AND sk.authored_product_session_id=NEW.product_session_id))
		 BEGIN SELECT RAISE(ABORT,'remember proposal transition is invalid'); END`,

		`CREATE TABLE structured_knowledge_links (
		 link_id                    INTEGER PRIMARY KEY AUTOINCREMENT,
		 source_knowledge_id        INTEGER NOT NULL REFERENCES structured_knowledge_entries(knowledge_id) ON DELETE CASCADE,
		 target_issue_id            INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
		 canonical_kind             TEXT NOT NULL CHECK(canonical_kind IN ('parent','about','see_also','supersedes')),
		 created_by_user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at                 TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')) CHECK(` + sqlControlTimestampCheck("created_at") + `),
		 UNIQUE(source_knowledge_id,target_issue_id,canonical_kind),
		 CHECK(source_knowledge_id<>target_issue_id)
		)`,
		`CREATE UNIQUE INDEX idx_structured_knowledge_one_parent ON structured_knowledge_links(source_knowledge_id) WHERE canonical_kind='parent'`,
		`CREATE INDEX idx_structured_knowledge_links_target ON structured_knowledge_links(target_issue_id,canonical_kind,source_knowledge_id)`,
		`CREATE TRIGGER trg_structured_knowledge_link_insert BEFORE INSERT ON structured_knowledge_links
		 WHEN (NEW.canonical_kind='see_also' AND NEW.source_knowledge_id>NEW.target_issue_id) OR NOT EXISTS(
		  SELECT 1 FROM structured_knowledge_entries source_scope
		  JOIN issues source_issue ON source_issue.id=source_scope.knowledge_id AND source_issue.deleted_at IS NULL
		  JOIN issues target_issue ON target_issue.id=NEW.target_issue_id AND target_issue.deleted_at IS NULL
		  LEFT JOIN structured_knowledge_entries target_scope ON target_scope.knowledge_id=target_issue.id
		  WHERE source_scope.knowledge_id=NEW.source_knowledge_id
		   AND (NEW.canonical_kind='about' OR target_scope.knowledge_id IS NOT NULL)
		   AND ((source_scope.level='project' AND source_issue.project_id=source_scope.origin_project_id
		         AND target_issue.project_id=source_scope.origin_project_id
		         AND (NEW.canonical_kind='about' OR
		              (target_scope.level='project' AND target_scope.origin_project_id=source_scope.origin_project_id))) OR
		        (source_scope.level IN ('instance','kernel','vision') AND source_issue.project_id IS NULL
		         AND target_issue.project_id IS NULL AND target_scope.level=source_scope.level))
		 ) BEGIN SELECT RAISE(ABORT,'structured knowledge link violates canonical form or scope'); END`,
		`CREATE TRIGGER trg_structured_knowledge_parent_cycle BEFORE INSERT ON structured_knowledge_links
		 WHEN NEW.canonical_kind='parent' AND EXISTS(
		  WITH RECURSIVE ancestors(knowledge_id) AS (
		   SELECT NEW.target_issue_id
		   UNION
		   SELECT link.target_issue_id FROM structured_knowledge_links link
		   JOIN ancestors parent ON link.source_knowledge_id=parent.knowledge_id
		   WHERE link.canonical_kind='parent'
		  ) SELECT 1 FROM ancestors WHERE knowledge_id=NEW.source_knowledge_id
		 ) BEGIN SELECT RAISE(ABORT,'structured knowledge parent cycle'); END`,
		`CREATE TRIGGER trg_structured_knowledge_link_no_update BEFORE UPDATE ON structured_knowledge_links
		 BEGIN SELECT RAISE(ABORT,'structured knowledge links are immutable; replace explicitly'); END`,
		`CREATE TRIGGER trg_structured_knowledge_linked_soft_delete BEFORE UPDATE OF deleted_at ON issues
		 WHEN NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL AND EXISTS(
		  SELECT 1 FROM structured_knowledge_links l WHERE l.source_knowledge_id=OLD.id OR l.target_issue_id=OLD.id)
		 BEGIN SELECT RAISE(ABORT,'structured knowledge links must be remapped or dropped before delete'); END`,
		`CREATE TRIGGER trg_issue_relations_structured_knowledge_insert BEFORE INSERT ON issue_relations
		 WHEN NEW.type IN ('parent','applies_to_memory','depends_on','impacts','follows_from','blocks','related')
		  AND (EXISTS(SELECT 1 FROM structured_knowledge_entries sk WHERE sk.knowledge_id=NEW.source_id)
		       OR EXISTS(SELECT 1 FROM structured_knowledge_entries sk WHERE sk.knowledge_id=NEW.target_id))
		 BEGIN SELECT RAISE(ABORT,'structured knowledge links require the canonical link table'); END`,
		`CREATE TRIGGER trg_issue_relations_structured_knowledge_update BEFORE UPDATE OF source_id,target_id,type ON issue_relations
		 WHEN NEW.type IN ('parent','applies_to_memory','depends_on','impacts','follows_from','blocks','related')
		  AND (EXISTS(SELECT 1 FROM structured_knowledge_entries sk WHERE sk.knowledge_id=NEW.source_id)
		       OR EXISTS(SELECT 1 FROM structured_knowledge_entries sk WHERE sk.knowledge_id=NEW.target_id))
		 BEGIN SELECT RAISE(ABORT,'structured knowledge links require the canonical link table'); END`,

		`CREATE TABLE structured_knowledge_promotions (
		 promotion_id            TEXT PRIMARY KEY CHECK(` + sqlUUIDCheck("promotion_id") + `),
		 source_knowledge_id     INTEGER NOT NULL UNIQUE REFERENCES issues(id) ON DELETE RESTRICT,
		 destination_knowledge_id INTEGER NOT NULL UNIQUE REFERENCES issues(id) ON DELETE RESTRICT,
		 from_level              TEXT NOT NULL CHECK(from_level IN ('project','instance')),
		 to_level                TEXT NOT NULL CHECK(to_level IN ('instance','kernel','vision')),
		 actor_user_id           INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
		 created_at              TEXT NOT NULL CHECK(` + sqlControlTimestampCheck("created_at") + `),
		 CHECK((from_level='project' AND to_level='instance') OR
		       (from_level='instance' AND to_level IN ('kernel','vision'))),
		 CHECK(source_knowledge_id<>destination_knowledge_id)
		)`,
		`CREATE TRIGGER trg_structured_knowledge_promotion_insert BEFORE INSERT ON structured_knowledge_promotions
		 WHEN NOT EXISTS(
		  SELECT 1 FROM structured_knowledge_entries source_scope
		  JOIN issues source_issue ON source_issue.id=source_scope.knowledge_id AND source_issue.deleted_at IS NOT NULL
		  JOIN issues destination_issue ON destination_issue.id=NEW.destination_knowledge_id
		   AND destination_issue.deleted_at IS NULL AND destination_issue.project_id IS NULL AND destination_issue.user_id IS NULL
		  WHERE source_scope.knowledge_id=NEW.source_knowledge_id AND source_scope.level=NEW.from_level
		   AND source_issue.type=destination_issue.type AND source_issue.slug=destination_issue.slug
		   AND source_issue.title=destination_issue.title
		   AND source_issue.description=destination_issue.description
		   AND source_issue.status=destination_issue.status
		   AND source_issue.priority=destination_issue.priority
		   AND COALESCE(source_issue.category_metadata,'')=COALESCE(destination_issue.category_metadata,'')
		 ) BEGIN SELECT RAISE(ABORT,'structured knowledge promotion evidence does not match source and destination'); END`,
		`CREATE TABLE structured_knowledge_promotion_links (
		 promotion_id       TEXT NOT NULL REFERENCES structured_knowledge_promotions(promotion_id) ON DELETE CASCADE,
		 original_link_id   INTEGER NOT NULL,
		 outcome            TEXT NOT NULL CHECK(outcome IN ('remapped','dropped')),
		 resulting_link_id  INTEGER,
		 reason             TEXT NOT NULL CHECK(reason IN ('same_scope_identity','target_missing_at_destination','node_target_dropped','incoming_scope_edge_dropped','destination_graph_conflict')),
		 PRIMARY KEY(promotion_id,original_link_id),
		 CHECK((outcome='remapped')=(resulting_link_id IS NOT NULL))
		)`,
		`CREATE TRIGGER trg_structured_knowledge_promotions_no_update BEFORE UPDATE ON structured_knowledge_promotions
		 BEGIN SELECT RAISE(ABORT,'structured knowledge promotion evidence is append-only'); END`,
		`CREATE TRIGGER trg_structured_knowledge_promotions_no_delete BEFORE DELETE ON structured_knowledge_promotions
		 BEGIN SELECT RAISE(ABORT,'structured knowledge promotion evidence is append-only'); END`,
		`CREATE TRIGGER trg_structured_knowledge_promotion_links_no_update BEFORE UPDATE ON structured_knowledge_promotion_links
		 BEGIN SELECT RAISE(ABORT,'structured knowledge promotion link evidence is append-only'); END`,
		`CREATE TRIGGER trg_structured_knowledge_promotion_links_no_delete BEFORE DELETE ON structured_knowledge_promotion_links
		 BEGIN SELECT RAISE(ABORT,'structured knowledge promotion link evidence is append-only'); END`,
		`CREATE TRIGGER trg_structured_knowledge_promoted_source_issue_update BEFORE UPDATE OF
		 project_id,user_id,issue_number,type,slug,title,description,status,priority,category_metadata,deleted_at,deleted_by ON issues
		 WHEN EXISTS(SELECT 1 FROM structured_knowledge_promotions promotion WHERE promotion.source_knowledge_id=OLD.id)
		  AND (NEW.project_id IS NOT OLD.project_id OR NEW.user_id IS NOT OLD.user_id OR NEW.issue_number IS NOT OLD.issue_number OR
		       NEW.type IS NOT OLD.type OR NEW.slug IS NOT OLD.slug OR NEW.title IS NOT OLD.title OR
		       NEW.description IS NOT OLD.description OR NEW.status IS NOT OLD.status OR NEW.priority IS NOT OLD.priority OR
		       NEW.category_metadata IS NOT OLD.category_metadata OR NEW.deleted_at IS NOT OLD.deleted_at OR NEW.deleted_by IS NOT OLD.deleted_by)
		 BEGIN SELECT RAISE(ABORT,'promoted structured knowledge source is immutable'); END`,
		`CREATE TRIGGER trg_structured_knowledge_promoted_source_entry_update BEFORE UPDATE ON structured_knowledge_entries
		 WHEN EXISTS(SELECT 1 FROM structured_knowledge_promotions promotion WHERE promotion.source_knowledge_id=OLD.knowledge_id)
		 BEGIN SELECT RAISE(ABORT,'promoted structured knowledge source overlay is immutable'); END`,
		`CREATE TRIGGER trg_structured_knowledge_promoted_source_entry_delete BEFORE DELETE ON structured_knowledge_entries
		 WHEN EXISTS(SELECT 1 FROM structured_knowledge_promotions promotion WHERE promotion.source_knowledge_id=OLD.knowledge_id)
		 BEGIN SELECT RAISE(ABORT,'promoted structured knowledge source overlay is immutable'); END`,
	}
}

func checkM167SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	return checkSchemaObjectsAbsent(ctx, conn, 167, []string{
		"knowledge_compact_sessions", "trg_knowledge_compact_session_insert", "trg_knowledge_compact_session_update",
		"structured_knowledge_entries", "idx_structured_knowledge_level", "trg_structured_knowledge_entry_insert",
		"trg_structured_knowledge_entry_update", "trg_structured_knowledge_issue_update",
		"structured_knowledge_proposals", "idx_structured_knowledge_proposals_project",
		"trg_structured_knowledge_proposal_insert", "trg_structured_knowledge_proposal_update",
		"structured_knowledge_links", "idx_structured_knowledge_one_parent", "idx_structured_knowledge_links_target",
		"trg_structured_knowledge_link_insert", "trg_structured_knowledge_parent_cycle", "trg_structured_knowledge_link_no_update",
		"trg_structured_knowledge_linked_soft_delete", "trg_issue_relations_structured_knowledge_insert",
		"trg_issue_relations_structured_knowledge_update", "structured_knowledge_promotions",
		"structured_knowledge_promotion_links", "trg_structured_knowledge_promotions_no_update",
		"trg_structured_knowledge_promotions_no_delete", "trg_structured_knowledge_promotion_links_no_update",
		"trg_structured_knowledge_promotion_links_no_delete",
		"trg_structured_knowledge_promotion_insert",
		"trg_structured_knowledge_promoted_source_issue_update",
		"trg_structured_knowledge_promoted_source_entry_update",
		"trg_structured_knowledge_promoted_source_entry_delete",
	})
}
