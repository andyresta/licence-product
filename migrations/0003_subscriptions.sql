-- =====================================================================================
-- licence-product — Migration 0003: subscription licenses (optional, alongside lifetime)
-- =====================================================================================
-- Every license_customers row keeps working exactly as before by default: NULL
-- subscription_expires_at means "lifetime" (internal/service/license.Activate issues
-- license.lic with the existing ~74-year DefaultLicenseTerm, unchanged). A product sold
-- as a monthly/yearly subscription sets this column instead (via the admin panel's
-- "Perpanjang Langganan" action), and every /activate call from then on embeds THAT
-- date (plus SubscriptionGracePeriod) as the license.lic's expires_at — no new API
-- endpoint needed, since expiry is already a field every product's offline check reads.
-- =====================================================================================

ALTER TABLE license_customers ADD COLUMN subscription_expires_at TIMESTAMPTZ NULL;

-- Audit trail of every subscription extension recorded (manually, via the admin panel,
-- by the vendor) — same reasoning as `purchases` being the source of truth behind
-- max_activations: subscription_expires_at on the parent row is a fast-lookup
-- denormalization, this table is "why" it is what it is.
CREATE TABLE subscription_extensions (
    extension_id         VARCHAR(20)   PRIMARY KEY,
    create_at            TIMESTAMPTZ   NOT NULL DEFAULT now(),
    license_customer_id  VARCHAR(20)   NOT NULL REFERENCES license_customers (license_customer_id),
    months               INTEGER       NOT NULL CHECK (months > 0),
    new_expires_at       TIMESTAMPTZ   NOT NULL,
    catatan              TEXT          NULL,
    recorded_by          VARCHAR(20)   NOT NULL REFERENCES admin_users (admin_user_id)
);

CREATE INDEX ix_subscription_extensions_customer ON subscription_extensions (license_customer_id);
