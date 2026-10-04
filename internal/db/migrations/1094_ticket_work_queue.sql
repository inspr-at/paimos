-- SPDX-License-Identifier: AGPL-3.0-only
-- Expand only: legacy run contracts and their NOT NULL agent remain intact.
-- An untargeted ticket run is held by an inert, keyless tenant agent until
-- coordinator routing sets its real agent. It is never exposed to daemon poll.
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_runs
    ADD COLUMN queue_node_id uuid,
    ADD COLUMN queue_by_principal_id uuid,
    ADD COLUMN queue_at timestamptz,
    ADD COLUMN queue_target_agent_id uuid,
    ADD COLUMN queue_rank numeric,
    ADD COLUMN queue_routed_at timestamptz,
    ADD COLUMN queue_security_review_required boolean,
    ADD CONSTRAINT agent_runs_queue_node_fk FOREIGN KEY (tenant_id, queue_node_id) REFERENCES nodes(tenant_id, id) NOT VALID,
    ADD CONSTRAINT agent_runs_queue_by_fk FOREIGN KEY (tenant_id, queue_by_principal_id) REFERENCES principals(tenant_id, id) NOT VALID,
    ADD CONSTRAINT agent_runs_queue_target_fk FOREIGN KEY (tenant_id, queue_target_agent_id) REFERENCES principals(tenant_id, id) NOT VALID;
CREATE UNIQUE INDEX agent_runs_ticket_queue_live_idx ON agent_runs(tenant_id, queue_node_id)
    WHERE queue_node_id IS NOT NULL AND status IN ('queued','starting','running','waiting');
CREATE INDEX agent_runs_ticket_queue_order_idx ON agent_runs(tenant_id, queue_target_agent_id, queue_at, id)
    WHERE queue_node_id IS NOT NULL AND status='queued';

ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_ticket_queue_metadata CHECK (
 (queue_node_id IS NULL AND queue_by_principal_id IS NULL AND queue_at IS NULL
  AND queue_target_agent_id IS NULL AND queue_rank IS NULL AND queue_routed_at IS NULL
  AND queue_security_review_required IS NULL)
 OR (queue_node_id IS NOT NULL AND queue_by_principal_id IS NOT NULL AND queue_at IS NOT NULL
  AND queue_security_review_required IS NOT NULL
  AND (queue_target_agent_id IS NULL OR queue_target_agent_id=agent_principal_id)
  AND (queue_rank IS NULL OR queue_rank>0))
) NOT VALID;

ALTER TABLE agent_runs VALIDATE CONSTRAINT agent_runs_queue_node_fk;
ALTER TABLE agent_runs VALIDATE CONSTRAINT agent_runs_queue_by_fk;
ALTER TABLE agent_runs VALIDATE CONSTRAINT agent_runs_queue_target_fk;
ALTER TABLE agent_runs VALIDATE CONSTRAINT agent_runs_ticket_queue_metadata;
