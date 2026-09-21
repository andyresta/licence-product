-- =====================================================================================
-- licence-product — Migration 0004: Customer as its own entity + explicit license_type
-- =====================================================================================
-- Previously "customer" was just an email string embedded in each license_customers row
-- — there was no way to see one customer's licenses across multiple products in one
-- place, and no explicit record of whether a license was sold lifetime or subscription
-- (it was only inferrable from subscription_expires_at being NULL or not).
--
-- This migration is purely additive and backfills from data that already exists:
--   - customers: one row per DISTINCT email already in license_customers. customer_id
--     is derived from that email's own oldest license_customer_id (swap the "LCU" prefix
--     for "CUS") so no app code needs to run during the migration.
--   - license_customers.customer_id: backfilled from the new customers table, then made
--     NOT NULL — every existing license row keeps pointing at the exact same customer.
--   - license_customers.license_type: backfilled to SUBSCRIPTION wherever
--     subscription_expires_at was already set, LIFETIME (the column default) everywhere
--     else — so every existing row's effective behavior is completely unchanged.
-- =====================================================================================

CREATE TABLE customers (
    customer_id  VARCHAR(20)   PRIMARY KEY,
    create_at    TIMESTAMPTZ   NOT NULL DEFAULT now(),
    update_at    TIMESTAMPTZ   NOT NULL DEFAULT now(),
    email        VARCHAR(255)  NOT NULL UNIQUE,
    nama         VARCHAR(150)  NULL,
    telp         VARCHAR(50)   NULL,
    catatan      TEXT          NULL
);

INSERT INTO customers (customer_id, email, create_at)
SELECT 'CUS' || substring(min(license_customer_id) from 4), email, min(create_at)
FROM license_customers
GROUP BY email;

ALTER TABLE license_customers ADD COLUMN customer_id VARCHAR(20) NULL REFERENCES customers (customer_id);
UPDATE license_customers lc SET customer_id = c.customer_id FROM customers c WHERE c.email = lc.email;
ALTER TABLE license_customers ALTER COLUMN customer_id SET NOT NULL;
CREATE INDEX ix_license_customers_customer ON license_customers (customer_id);

ALTER TABLE license_customers ADD COLUMN license_type VARCHAR(20) NOT NULL DEFAULT 'LIFETIME'
    CHECK (license_type IN ('LIFETIME', 'SUBSCRIPTION'));
UPDATE license_customers SET license_type = 'SUBSCRIPTION' WHERE subscription_expires_at IS NOT NULL;
