-- PAI-746: enrich the LOCAL dev demo workspace for marketing captures.
-- Runs against the gitignored data/paimos.db only. Nothing here is
-- committed, and nothing is copied from a live instance.

-- 1. Converge fixture databases created before PAI-697. New dev seeds already
--    carry this synthetic identity; keeping the update here makes old local
--    capture workspaces deterministic too.
UPDATE users
   SET first_name = 'Mara', last_name = 'Ellis', nickname = 'Mara Ellis'
 WHERE username = 'dev_admin';

-- 2. Kill the seven identical "Reporting CSV export" rows that dominate
--    Recent Issues and read as broken fixture data.
UPDATE issues SET title = 'Export retainer report as CSV'            WHERE project_id = 2 AND issue_number = 34;
UPDATE issues SET title = 'Portal: saved filters per customer'        WHERE project_id = 2 AND issue_number = 35;
UPDATE issues SET title = 'Invoice run misses partial-month entries'  WHERE project_id = 2 AND issue_number = 36;
UPDATE issues SET title = 'Acceptance mail should attach the PDF'     WHERE project_id = 2 AND issue_number = 37;
UPDATE issues SET title = 'Time entries: bulk re-assign to cost unit' WHERE project_id = 2 AND issue_number = 38;
UPDATE issues SET title = 'Budget burn-down chart for Q3 retainer'    WHERE project_id = 2 AND issue_number = 39;
UPDATE issues SET title = 'Portal login should honour SSO domains'    WHERE project_id = 2 AND issue_number = 40;
