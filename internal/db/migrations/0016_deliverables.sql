-- Deliverables: anything an agent (or a person) produces that waits for a human's approval and is
-- then acted on by an action connector. Posts keep their own table; this is the general form.
CREATE TABLE deliverables (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID NOT NULL REFERENCES orgs(id),
    task_id             UUID REFERENCES tasks(id) ON DELETE SET NULL,
    kind                VARCHAR(64)  NOT NULL,           -- registered deliverable kind, e.g. document
    title               VARCHAR(255) NOT NULL DEFAULT '',
    payload             JSONB        NOT NULL DEFAULT '{}',
    status              VARCHAR(32)  NOT NULL DEFAULT 'DRAFT' CHECK (status IN
                          ('DRAFT','PENDING_REVIEW','APPROVED','REJECTED','QUEUED','DONE','FAILED')),
    review_note         TEXT         NOT NULL DEFAULT '',
    error_message       TEXT         NOT NULL DEFAULT '',
    action_connector_id UUID REFERENCES connectors(id) ON DELETE SET NULL,
    external_ids        JSONB        NOT NULL DEFAULT '{}',  -- what the action created, for retries
    result              JSONB        NOT NULL DEFAULT '{}',  -- what the action reported
    attempts            INT          NOT NULL DEFAULT 0,
    lease_until         TIMESTAMPTZ,                         -- worker lease; expired = worker died mid-action
    version             INT          NOT NULL DEFAULT 1,
    created_by          VARCHAR(64)  NOT NULL DEFAULT '',    -- actor: system_agent | human_reviewer
    acted_at            TIMESTAMPTZ,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_deliverables_org_status ON deliverables(org_id, status);
CREATE INDEX idx_deliverables_task ON deliverables(task_id);
CREATE INDEX idx_deliverables_created ON deliverables(created_at DESC);

CREATE TABLE deliverable_audit_logs (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    seq            BIGSERIAL NOT NULL,
    deliverable_id UUID NOT NULL REFERENCES deliverables(id) ON DELETE CASCADE,
    action         VARCHAR(32) NOT NULL,
    actor          VARCHAR(64) NOT NULL,
    from_status    VARCHAR(32) NOT NULL DEFAULT '',
    to_status      VARCHAR(32) NOT NULL DEFAULT '',
    note           TEXT NOT NULL DEFAULT '',
    payload_diff   JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_deliverable_audit_logs_item ON deliverable_audit_logs(deliverable_id, seq);
