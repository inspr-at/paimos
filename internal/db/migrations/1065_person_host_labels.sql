-- SPDX-License-Identifier: AGPL-3.0-only
-- AEON-501: labels belong to the viewing person, never the session or device.
SET LOCAL lock_timeout = '5s';
CREATE TABLE person_host_labels (
    tenant_id uuid NOT NULL REFERENCES tenants(id),
    person_id uuid NOT NULL,
    host text NOT NULL CHECK (char_length(host) BETWEEN 1 AND 128 AND host = btrim(host) AND host !~ '[[:cntrl:]]'),
    label text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 128 AND label = btrim(label) AND label !~ '[[:cntrl:]]'),
    PRIMARY KEY (tenant_id, person_id, host),
    FOREIGN KEY (tenant_id, person_id) REFERENCES principals(tenant_id, id)
);
ALTER TABLE person_host_labels ENABLE ROW LEVEL SECURITY;
ALTER TABLE person_host_labels FORCE ROW LEVEL SECURITY;
CREATE POLICY person_host_labels_tenant ON person_host_labels
    USING (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)
    WITH CHECK (tenant_id = NULLIF(current_setting('aeon.tenant_id', true), '')::uuid);
CREATE POLICY person_host_labels_owner ON person_host_labels AS RESTRICTIVE
    USING ((SELECT session_read_marker_owned(tenant_id, person_id)))
    WITH CHECK ((SELECT session_read_marker_owned(tenant_id, person_id)));
