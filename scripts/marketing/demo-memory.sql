-- Give PAI-1 one relevant, deterministic memory so the flagship workbench
-- demonstrates retrieved project knowledge instead of an empty panel. The
-- insert and relation are re-run safe.
INSERT INTO issues
  (project_id, issue_number, type, title, description, status, priority, created_by, slug)
SELECT p.id, COALESCE(MAX(i.issue_number), 0) + 1, 'memory',
       'Public evidence follows shipped releases',
       'Website claims and screenshots come from a verified release. Captions retain the version of the pixels actually shown.',
       'backlog', 'medium', 9001, 'public-evidence-follows-shipped-releases'
  FROM projects p
  LEFT JOIN issues i ON i.project_id = p.id
 WHERE p.key = 'PAI'
   AND NOT EXISTS (
     SELECT 1 FROM issues existing
      WHERE existing.project_id = p.id
        AND existing.type = 'memory'
        AND existing.slug = 'public-evidence-follows-shipped-releases'
   )
 GROUP BY p.id;

INSERT OR IGNORE INTO issue_relations (source_id, target_id, type)
SELECT ticket.id, memory.id, 'applies_to_memory'
  FROM issues ticket
  JOIN projects project ON project.id = ticket.project_id AND project.key = 'PAI'
  JOIN issues memory ON memory.project_id = project.id
                    AND memory.type = 'memory'
                    AND memory.slug = 'public-evidence-follows-shipped-releases'
 WHERE ticket.issue_number = 1;
