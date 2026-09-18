-- =====================================================================================
-- licence-product — Migration 0002: branch quota (optional second axis alongside
-- activations)
-- =====================================================================================
-- Branch tracks how many physical branches/outlets a license covers — independent from
-- activations (which track machines). Not every product cares about this: a product
-- that never calls /api/v1/branches/* simply never has rows here, while every
-- license_customers row still carries max_branches (default 5) regardless, so a product
-- can start enforcing branch limits later without any schema change.
--
-- branches mirrors activations' ACTIVE/DEACTIVATED state machine on purpose — see
-- internal/service/branch for the exact register/reuse/reclaim rules, which are the
-- same three cases as internal/service/license's activate.
-- =====================================================================================

ALTER TABLE license_customers ADD COLUMN max_branches INTEGER NOT NULL DEFAULT 5;

CREATE TABLE branches (
    branch_id            VARCHAR(20)   PRIMARY KEY,
    create_at            TIMESTAMPTZ   NOT NULL DEFAULT now(),
    update_at            TIMESTAMPTZ   NOT NULL DEFAULT now(),
    license_customer_id  VARCHAR(20)   NOT NULL REFERENCES license_customers (license_customer_id),
    branch_code          VARCHAR(150)  NOT NULL,
    branch_label         VARCHAR(150)  NULL,
    status               VARCHAR(15)   NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DEACTIVATED')),
    registered_at        TIMESTAMPTZ   NOT NULL DEFAULT now(),
    deactivated_at       TIMESTAMPTZ   NULL,
    deactivated_by       VARCHAR(20)   NULL REFERENCES admin_users (admin_user_id),
    CONSTRAINT uq_branches_customer_code UNIQUE (license_customer_id, branch_code)
);

CREATE INDEX ix_branches_license_customer ON branches (license_customer_id);
-- Partial index: only ACTIVE rows count against max_branches, and that count is on the
-- hot path of every branch check/register call — same reasoning as ix_activations_active.
CREATE INDEX ix_branches_active ON branches (license_customer_id) WHERE status = 'ACTIVE';
