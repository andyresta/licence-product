-- =====================================================================================
-- licence-product — Migration 0001: initial schema (PostgreSQL only)
-- =====================================================================================
-- One license server meant to serve multiple products (hence product_code, not a single
-- hardcoded product) — FixUnit is the first consumer, more products can register their
-- own product_code later without any schema change.
--
-- Design summary (see README.md for the full API contract):
--   - license_customers: one row per (email, product_id) pair. purchase_count and
--     max_activations are cumulative — every recorded purchase adds to both, so a repeat
--     buyer of the same product just gets more activation seats on the same row rather
--     than a new one.
--   - purchases: an audit trail of every purchase recorded (manually, via the admin
--     panel, by the vendor) against a license_customers row — seats granted can vary
--     per purchase (a 5-seat pack in one transaction is a valid purchase).
--   - activations: one row per machine that has ever activated. A machine is identified
--     by machine_fingerprint (a hash of hardware-level identifiers computed client-side
--     by the product — see README's "Fingerprint" section). Re-activating from a
--     fingerprint that already has an ACTIVE row (e.g. the same PC after an OS
--     reinstall) never consumes a new seat — see internal/service/license.
--   - admin_users: a small number of back-office operators (expected: just the vendor)
--     who can log into the admin panel to record purchases and manage activations.
-- =====================================================================================

CREATE TABLE admin_users (
    admin_user_id  VARCHAR(20)   PRIMARY KEY,
    create_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    update_at      TIMESTAMPTZ   NOT NULL DEFAULT now(),
    username       VARCHAR(100)  NOT NULL UNIQUE,
    password_hash  VARCHAR(255)  NOT NULL,
    status_aktif   BOOLEAN       NOT NULL DEFAULT TRUE
);

CREATE TABLE products (
    product_id    VARCHAR(20)   PRIMARY KEY,
    create_at     TIMESTAMPTZ   NOT NULL DEFAULT now(),
    update_at     TIMESTAMPTZ   NOT NULL DEFAULT now(),
    product_code  VARCHAR(50)   NOT NULL UNIQUE,
    nama          VARCHAR(150)  NOT NULL,
    keterangan    TEXT          NULL,
    status_aktif  BOOLEAN       NOT NULL DEFAULT TRUE
);

CREATE TABLE license_customers (
    license_customer_id  VARCHAR(20)   PRIMARY KEY,
    create_at            TIMESTAMPTZ   NOT NULL DEFAULT now(),
    update_at            TIMESTAMPTZ   NOT NULL DEFAULT now(),
    email                VARCHAR(255)  NOT NULL,
    product_id           VARCHAR(20)   NOT NULL REFERENCES products (product_id),
    purchase_count       INTEGER       NOT NULL DEFAULT 0,
    max_activations      INTEGER       NOT NULL DEFAULT 0,
    catatan              TEXT          NULL,
    CONSTRAINT uq_license_customers_email_product UNIQUE (email, product_id)
);

CREATE INDEX ix_license_customers_email ON license_customers (email);

-- One row per purchase recorded against a license_customers pair — the audit trail
-- behind purchase_count/max_activations (which are denormalized running totals on the
-- parent row for fast lookup at activation time; this table is the source of truth for
-- "why" those totals are what they are).
CREATE TABLE purchases (
    purchase_id          VARCHAR(20)   PRIMARY KEY,
    create_at            TIMESTAMPTZ   NOT NULL DEFAULT now(),
    license_customer_id  VARCHAR(20)   NOT NULL REFERENCES license_customers (license_customer_id),
    seats                INTEGER       NOT NULL CHECK (seats > 0),
    catatan              TEXT          NULL,
    recorded_by          VARCHAR(20)   NOT NULL REFERENCES admin_users (admin_user_id)
);

CREATE INDEX ix_purchases_license_customer ON purchases (license_customer_id);

-- One row per machine that has ever activated for a given license_customers pair.
-- status ACTIVE counts against max_activations; DEACTIVATED does not (the seat is
-- freed the moment status flips, whether the customer self-served it from inside the
-- product or the vendor force-deactivated it from the admin panel).
CREATE TABLE activations (
    activation_id         VARCHAR(20)   PRIMARY KEY,
    create_at             TIMESTAMPTZ   NOT NULL DEFAULT now(),
    update_at             TIMESTAMPTZ   NOT NULL DEFAULT now(),
    license_customer_id   VARCHAR(20)   NOT NULL REFERENCES license_customers (license_customer_id),
    machine_fingerprint   VARCHAR(128)  NOT NULL,
    machine_label         VARCHAR(150)  NULL,
    status                VARCHAR(15)   NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DEACTIVATED')),
    activated_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    last_seen_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    deactivated_at        TIMESTAMPTZ   NULL,
    deactivated_by        VARCHAR(20)   NULL REFERENCES admin_users (admin_user_id),
    expires_at            TIMESTAMPTZ   NOT NULL,
    CONSTRAINT uq_activations_customer_fingerprint UNIQUE (license_customer_id, machine_fingerprint)
);

CREATE INDEX ix_activations_license_customer ON activations (license_customer_id);
-- Partial index: only ACTIVE rows are ever counted against max_activations, and that
-- count is on the hot path of every /activate call.
CREATE INDEX ix_activations_active ON activations (license_customer_id) WHERE status = 'ACTIVE';
