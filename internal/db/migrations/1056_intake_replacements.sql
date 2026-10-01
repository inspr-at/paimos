-- SPDX-License-Identifier: AGPL-3.0-only
SET LOCAL lock_timeout = '5s';

ALTER TABLE intake_drafts ADD COLUMN requester_principal_id uuid,
    ADD CONSTRAINT intake_draft_requester_fk FOREIGN KEY (tenant_id, requester_principal_id)
        REFERENCES principals(tenant_id, id);

-- Drafts stay immutable. Supersession is an immutable edge and a byte-exact
-- operation receipt, in the transaction that inserts the replacement draft.
CREATE TABLE intake_draft_replacements (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    project_node_id uuid NOT NULL,
    old_draft_id uuid NOT NULL,
    new_draft_id uuid NOT NULL,
    operation_key text NOT NULL CHECK (length(operation_key) BETWEEN 1 AND 128),
    plugin_principal_id uuid NOT NULL,
    requester_principal_id uuid,
    session_id uuid,
    request_bytes bytea NOT NULL,
    result_bytes bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, project_node_id, old_draft_id),
    UNIQUE (tenant_id, project_node_id, new_draft_id),
    UNIQUE (tenant_id, project_node_id, operation_key),
    CHECK (old_draft_id <> new_draft_id),
    FOREIGN KEY (tenant_id, project_node_id, old_draft_id)
        REFERENCES intake_drafts(tenant_id, project_node_id, id),
    FOREIGN KEY (tenant_id, project_node_id, new_draft_id)
        REFERENCES intake_drafts(tenant_id, project_node_id, id),
    FOREIGN KEY (tenant_id, plugin_principal_id) REFERENCES principals(tenant_id, id),
    FOREIGN KEY (tenant_id, requester_principal_id) REFERENCES principals(tenant_id, id)
);
ALTER TABLE intake_draft_replacements ENABLE ROW LEVEL SECURITY;
ALTER TABLE intake_draft_replacements FORCE ROW LEVEL SECURITY;
CREATE POLICY intake_draft_replacements_tenant ON intake_draft_replacements
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY intake_draft_replacements_project ON intake_draft_replacements AS RESTRICTIVE
    USING ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]))
    WITH CHECK ((SELECT aeon_visible_all()) OR project_node_id = ANY ((SELECT aeon_visible_projects())::uuid[]));
CREATE TRIGGER intake_replacements_immutable BEFORE UPDATE OR DELETE ON intake_draft_replacements
    FOR EACH ROW EXECUTE FUNCTION aeon_events_append_only();

-- Cross-table arbitration also protects trusted in-process callers. All intake
-- mutation paths acquire this project lock before node locks or draft writes.
CREATE FUNCTION aeon_intake_terminal_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_id uuid;
BEGIN
    PERFORM 1 FROM journey_projects
        WHERE tenant_id = NEW.tenant_id AND project_node_id = NEW.project_node_id FOR UPDATE;
    IF TG_TABLE_NAME = 'intake_draft_replacements' THEN
        old_id := NEW.old_draft_id;
        IF EXISTS (SELECT 1 FROM intake_draft_acceptances
            WHERE tenant_id = NEW.tenant_id AND draft_id = old_id) THEN
            RAISE EXCEPTION 'already_accepted' USING ERRCODE = 'P0001';
        END IF;
    ELSE
        old_id := NEW.draft_id;
    END IF;
    IF EXISTS (SELECT 1 FROM intake_draft_replacements
        WHERE tenant_id = NEW.tenant_id AND old_draft_id = old_id) THEN
        RAISE EXCEPTION 'draft_superseded' USING ERRCODE = 'P0001';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER intake_replacement_terminal BEFORE INSERT ON intake_draft_replacements
    FOR EACH ROW EXECUTE FUNCTION aeon_intake_terminal_guard();
CREATE TRIGGER intake_acceptance_terminal BEFORE INSERT ON intake_draft_acceptances
    FOR EACH ROW EXECUTE FUNCTION aeon_intake_terminal_guard();
