-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-444: the doctrine inbox. An agent-proposed rule change waits here until
-- a person sends it to git as a PR, or dismisses it. This table holds its text
-- while it waits, bounded like a proposal; the row is deleted when the
-- proposal leaves the inbox. doctrine_proposals keeps references only.
SET LOCAL lock_timeout = '5s';

CREATE TABLE doctrine_proposal_drafts (
    tenant_id uuid NOT NULL,
    proposal_id uuid NOT NULL,
    source text NOT NULL CHECK (octet_length(source) BETWEEN 1 AND 8000),
    tldr_en text NOT NULL CHECK (octet_length(tldr_en) BETWEEN 1 AND 300),
    tldr_de text NOT NULL DEFAULT '' CHECK (octet_length(tldr_de) <= 300),
    why text NOT NULL CHECK (octet_length(why) BETWEEN 1 AND 2000),
    PRIMARY KEY (tenant_id, proposal_id),
    FOREIGN KEY (tenant_id, proposal_id) REFERENCES doctrine_proposals(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE doctrine_proposal_drafts ENABLE ROW LEVEL SECURITY;
ALTER TABLE doctrine_proposal_drafts FORCE ROW LEVEL SECURITY;
CREATE POLICY doctrine_proposal_drafts_tenant ON doctrine_proposal_drafts
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);

CREATE INDEX doctrine_proposals_inbox_pending ON doctrine_proposals (tenant_id, created_at DESC)
    WHERE data->>'inbox' = 'true' AND data->>'state' = 'pending';

-- One toast per person and proposal, across tabs and devices: the first
-- claim inserts the row, every later claim finds it. Rows go with the
-- proposal and are swept once it leaves the inbox.
CREATE TABLE doctrine_inbox_notified (
    tenant_id uuid NOT NULL,
    principal_id uuid NOT NULL,
    proposal_id uuid NOT NULL,
    notified_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, principal_id, proposal_id),
    FOREIGN KEY (tenant_id, proposal_id) REFERENCES doctrine_proposals(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, principal_id) REFERENCES principals(tenant_id, id) ON DELETE CASCADE
);
ALTER TABLE doctrine_inbox_notified ENABLE ROW LEVEL SECURITY;
ALTER TABLE doctrine_inbox_notified FORCE ROW LEVEL SECURITY;
CREATE POLICY doctrine_inbox_notified_tenant ON doctrine_inbox_notified
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
