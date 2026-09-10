# licence-product

A license activation server shared across multiple products (FixUnit is the first
consumer) — not one server per product. Every license is scoped by `product_code`, so
a new product can start selling licenses through this same server without any schema
or API change.

## How it works

1. **Provisioning (manual, via the admin panel).** After a sale, the vendor logs into
   `/admin` and records a purchase: email, product_code, and how many seats (machine
   activations) that purchase grants. Buying the same product again just adds more
   seats to the same customer record — it does not create a duplicate.
2. **Activation.** The product being licensed (e.g. FixUnit's install wizard) asks the
   customer for their email, computes a fingerprint of the current machine's hardware,
   and calls `POST /api/v1/activate`. If the email+product_code pair exists and has a
   free seat, the server returns a signed `license.lic`. The product saves that file
   and continues installing.
3. **Runtime validation is entirely offline.** The product verifies `license.lic`'s
   signature and checks its embedded machine fingerprint against the current machine —
   both client-side, with no further contact to this server. Copying the whole
   installed app + `license.lic` to a different machine fails this check immediately.
4. **Reinstalling on the same machine is free.** If the exact same hardware fingerprint
   activates again (a fresh OS install after a format, or just re-running the
   installer), the server recognizes it and reissues `license.lic` without consuming a
   second seat.
5. **Deactivation frees a seat.** Either the product itself calls
   `POST /api/v1/deactivate` (self-service, e.g. before decommissioning a machine — see
   "Deactivation proof" below), or the vendor force-deactivates from the admin panel
   for the case where the old machine is already gone.

## Data model

- `products` — one row per product_code.
- `license_customers` — one row per (email, product_code) pair. `purchase_count` and
  `max_activations` are cumulative running totals, updated every time a purchase is
  recorded.
- `purchases` — an audit trail of every purchase recorded against a
  `license_customers` row (seats can vary per purchase — a 5-seat pack is one row with
  `seats = 5`).
- `activations` — one row per machine that has ever activated for a customer. Only
  `status = 'ACTIVE'` rows count against `max_activations`. See
  `internal/service/license/license.go` for the exact activate/reactivate/deactivate
  state machine and why re-activating the same fingerprint never double-counts.

## API contract

### `POST /api/v1/activate`

```json
{
  "email": "customer@example.com",
  "product_code": "FIXUNIT",
  "machine_fingerprint": "<see Fingerprint below>",
  "machine_label": "Kasir Depan (optional, for the admin panel's display only)"
}
```

Success (`200`):

```json
{
  "success": true,
  "data": {
    "license_lic": "<opaque signed string — save this to license.lic verbatim>",
    "activation_id": "ACT...",
    "reused": false
  }
}
```

`reused: true` means this exact machine was already active and no new seat was
consumed — informational only, the product doesn't need to branch on it.

Failure (`404`/`409`/...):

```json
{ "success": false, "code": "QUOTA_EXCEEDED", "message": "kuota aktivasi sudah penuh (2/2)" }
```

Codes: `NOT_REGISTERED` (email+product_code never provisioned), `QUOTA_EXCEEDED`,
`VALIDATION` (malformed request).

### `POST /api/v1/deactivate`

```json
{ "license_lic": "<the exact content this machine received from /activate>" }
```

Success: `{"success": true}`. The caller proves it owns the activation being freed by
presenting a validly-signed `license.lic` for it — not just knowledge of the
email/product_code, which anyone could guess or overhear. Signature validity is
checked; **expiry is deliberately not checked** — freeing a seat must work even after
`license.lic` has expired. Failure codes: `INVALID_LICENSE` (bad/forged signature),
`NOT_FOUND` (already deactivated).

## license.lic format

A JSON payload plus an Ed25519 signature, encoded as
`base64url(payload_json) + "." + base64url(signature)` (JWT-shaped, but with exactly
one fixed signing scheme — there is nothing to negotiate and nothing to downgrade).

Payload:

```json
{
  "email": "customer@example.com",
  "product_code": "FIXUNIT",
  "machine_fingerprint": "...",
  "activation_id": "ACT...",
  "issued_at": "2026-01-01T00:00:00Z",
  "expires_at": "2100-08-23T00:00:00Z"
}
```

Every activation/reactivation gets `expires_at` set ~74 years out
(`license.DefaultLicenseTerm`) — "lifetime" products are just issued a very-far expiry
rather than no expiry field at all, so a future subscription-style product can use the
exact same mechanism with a real expiry instead of a special case.

**Verifying client-side (in the product, not this server):** decode the two
base64url segments, verify the signature over the payload bytes using the server's
public key (from `go run ./cmd/genkey`, embedded in the product at build time), then
check `expires_at` and that `machine_fingerprint` matches the current machine. See
`internal/crypto/license.go`'s `Verify` for the reference implementation (Ed25519,
stdlib `crypto/ed25519` — trivially portable to any language with an Ed25519
verify primitive).

## Fingerprint

Not computed by this server — the product computes it and sends it as an opaque
string. Recommended approach (what FixUnit's client integration should use): hash a
firmware/DMI-level hardware UUID, which survives a normal OS reinstall on the same
physical machine (unlike a disk volume serial, which a fresh format can change):

- **Windows:** `Win32_ComputerSystemProduct.UUID` via WMI (`wmic csproduct get uuid`).
- **Linux:** `/sys/class/dmi/id/product_uuid` (falls back to `/etc/machine-id` only if
  unreadable — note `/etc/machine-id` is regenerated on a fresh OS install, so it's a
  weaker signal, not a substitute).
- **macOS:** the Hardware UUID from `ioreg -rd1 -c IOPlatformExpertDevice`.

Hash whatever raw value is read (SHA-256) before sending it — the server only ever
sees and stores the hash, never the raw hardware ID.

## Admin panel

`/admin` (session cookie, HMAC-signed, 12h TTL — see `internal/handler/session.go`).
First admin account is created from `BOOTSTRAP_ADMIN` on first startup (never
overwrites an existing account on later restarts). From the dashboard: search
customers by email, add a product, record a purchase. From a customer's detail page:
view every activation (with a "Lepas" button to force-deactivate) and the full
purchase history.

## Configuration (environment variables)

| Var | Required | Notes |
|---|---|---|
| `DATABASE_URL` | yes | Postgres DSN, e.g. `postgres://user:pass@host:5432/db?sslmode=disable` |
| `LICENSE_SIGNING_SEED` | yes | 64 hex chars (32-byte Ed25519 seed). Generate with `go run ./cmd/genkey`. **Never commit this.** |
| `SESSION_SECRET` | yes | Any random string — signs admin session cookies. |
| `BOOTSTRAP_ADMIN` | first run only | `username:password` — creates the first admin account if `admin_users` is empty. Safe to leave set across restarts. |
| `APP_PORT` | no (default `8090`) | |

## Running locally

```sh
createdb licence_dev   # Postgres must already be running
go run ./cmd/genkey    # prints a seed + public key — copy the seed below

DATABASE_URL="postgres://localhost:5432/licence_dev?sslmode=disable" \
LICENSE_SIGNING_SEED="<paste seed here>" \
SESSION_SECRET="dev-secret" \
BOOTSTRAP_ADMIN="vendor:change-me" \
go run ./cmd/licenseserver
```

Migrations in `migrations/*.sql` are applied automatically at startup.

## Running tests

Tests run against a real local Postgres (no mocking the DB layer):

```sh
createdb licence_test
psql licence_test -c "CREATE USER licence_test WITH PASSWORD 'licence_test' SUPERUSER;"  # or adjust LICENSE_TEST_DATABASE_URL below

LICENSE_TEST_DATABASE_URL="postgres://licence_test:licence_test@localhost:5432/licence_test?sslmode=disable" \
go test ./... -p 1
```

**`-p 1` is required** — every package's tests share the same live database
(`internal/testutil.OpenTestDB` truncates tables between tests, not between packages),
so letting Go run multiple packages' test binaries concurrently (its default) causes
one package's truncate to race another's still-running test.

## What's NOT in this repo

Client-side integration (computing the fingerprint, calling `/activate` from an
install wizard, verifying `license.lic` at runtime, a "Lepas Aktivasi" UI action) lives
in each consuming product's own repo — this server only owns the shared contract
above.
