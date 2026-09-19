package license

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andyresta/licence-product/internal/crypto"
	"github.com/andyresta/licence-product/internal/testutil"
)

// testSeedHex is a fixed 64-hex-char (32-byte) Ed25519 seed — deterministic so test
// failures are reproducible, never used outside tests.
var testSeedHex = strings.Repeat("ab", 32)

func newTestService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	signer, err := crypto.NewSigner(testSeedHex)
	require.NoError(t, err)
	return New(db, signer), db
}

var seedSeq int

// seedCustomer inserts one product + one license_customers row with the given
// max_activations, returning its license_customer_id. IDs are kept short (the schema
// caps id columns at VARCHAR(20)) via a per-process counter rather than embedding the
// email/product_code.
func seedCustomer(t *testing.T, db *sql.DB, email, productCode string, maxActivations int) string {
	t.Helper()
	seedSeq++
	productID := fmt.Sprintf("PRD%06d", seedSeq)
	_, err := db.Exec(`INSERT INTO products (product_id, product_code, nama) VALUES ($1, $2, $2)
		ON CONFLICT (product_code) DO UPDATE SET nama = EXCLUDED.nama`, productID, productCode)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT product_id FROM products WHERE product_code = $1`, productCode).Scan(&productID))

	customerID := fmt.Sprintf("LCU%06d", seedSeq)
	_, err = db.Exec(`INSERT INTO license_customers (license_customer_id, email, product_id, purchase_count, max_activations)
		VALUES ($1, $2, $3, 1, $4)`, customerID, email, productID, maxActivations)
	require.NoError(t, err)
	return customerID
}

func TestActivate_NewMachine_ConsumesOneSeat(t *testing.T) {
	svc, db := newTestService(t)
	seedCustomer(t, db, "budi@example.com", "FIXUNIT", 2)

	result, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.Nil(t, appErr)
	assert.False(t, result.Reused)
	assert.NotEmpty(t, result.LicenseLic)

	var activeCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM activations WHERE status = 'ACTIVE'`).Scan(&activeCount))
	assert.Equal(t, 1, activeCount)
}

func TestActivate_SameMachineAgain_DoesNotConsumeSecondSeat(t *testing.T) {
	svc, db := newTestService(t)
	seedCustomer(t, db, "budi@example.com", "FIXUNIT", 1)

	first, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.Nil(t, appErr)

	// Simulates the OS-reinstall case: same physical machine (same fingerprint)
	// re-activates. max_activations is 1, so if this consumed a second seat, the
	// second call itself (or a genuinely different machine afterward) would fail.
	second, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.Nil(t, appErr)
	assert.True(t, second.Reused)
	assert.Equal(t, first.ActivationID, second.ActivationID)

	var activeCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM activations WHERE status = 'ACTIVE'`).Scan(&activeCount))
	assert.Equal(t, 1, activeCount, "reactivating the same fingerprint must never create a second row")
}

func TestActivate_DifferentMachine_RejectedOnceQuotaExhausted(t *testing.T) {
	svc, db := newTestService(t)
	seedCustomer(t, db, "budi@example.com", "FIXUNIT", 1)

	_, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.Nil(t, appErr)

	_, appErr = svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-2",
	})
	require.NotNil(t, appErr)
	assert.Equal(t, "QUOTA_EXCEEDED", string(appErr.Code))
}

func TestActivate_UnregisteredEmail_Rejected(t *testing.T) {
	svc, _ := newTestService(t)

	_, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "unknown@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.NotNil(t, appErr)
	assert.Equal(t, "NOT_REGISTERED", string(appErr.Code))
}

func TestDeactivate_FreesSeatForReactivation(t *testing.T) {
	svc, db := newTestService(t)
	seedCustomer(t, db, "budi@example.com", "FIXUNIT", 1)

	first, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.Nil(t, appErr)

	// Quota is full — a genuinely different machine must be rejected.
	_, appErr = svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-2",
	})
	require.NotNil(t, appErr)

	require.Nil(t, svc.Deactivate(context.Background(), first.LicenseLic))

	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM activations WHERE activation_id = $1`, first.ActivationID).Scan(&status))
	assert.Equal(t, "DEACTIVATED", status)

	// The freed seat can now go to a different machine.
	second, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-2",
	})
	require.Nil(t, appErr)
	assert.False(t, second.Reused)
}

func TestDeactivate_ThenReactivateSameMachine_GoesThroughQuotaAgain(t *testing.T) {
	svc, db := newTestService(t)
	seedCustomer(t, db, "budi@example.com", "FIXUNIT", 1)

	first, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.Nil(t, appErr)
	require.Nil(t, svc.Deactivate(context.Background(), first.LicenseLic))

	// Someone else takes the freed seat first.
	_, appErr = svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-2",
	})
	require.Nil(t, appErr)

	// The original machine trying to reclaim its now-deactivated row must be
	// rejected — the seat legitimately belongs to fp-pc-2 now.
	_, appErr = svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.NotNil(t, appErr)
	assert.Equal(t, "QUOTA_EXCEEDED", string(appErr.Code))
}

func TestDeactivate_ForgedLicenseRejected(t *testing.T) {
	svc, _ := newTestService(t)
	appErr := svc.Deactivate(context.Background(), "not-a-real-license")
	require.NotNil(t, appErr)
	assert.Equal(t, "INVALID_LICENSE", string(appErr.Code))
}

func TestDeactivate_AlreadyDeactivated_ReturnsNotFound(t *testing.T) {
	svc, db := newTestService(t)
	seedCustomer(t, db, "budi@example.com", "FIXUNIT", 1)

	first, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.Nil(t, appErr)
	require.Nil(t, svc.Deactivate(context.Background(), first.LicenseLic))

	appErr = svc.Deactivate(context.Background(), first.LicenseLic)
	require.NotNil(t, appErr)
	assert.Equal(t, "NOT_FOUND", string(appErr.Code))
}

func TestActivate_SubscriptionCustomer_ExpiresAtReflectsSubscriptionPlusGrace(t *testing.T) {
	svc, db := newTestService(t)
	customerID := seedCustomer(t, db, "budi@example.com", "FIXUNIT", 1)
	subscriptionExpiresAt := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second)
	_, err := db.Exec(`UPDATE license_customers SET subscription_expires_at = $1 WHERE license_customer_id = $2`,
		subscriptionExpiresAt, customerID)
	require.NoError(t, err)
	signer, err := crypto.NewSigner(testSeedHex)
	require.NoError(t, err)

	result, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.Nil(t, appErr)

	payload, err := crypto.Verify(result.LicenseLic, signer.PublicKeyHex())
	require.NoError(t, err)
	assert.WithinDuration(t, subscriptionExpiresAt.Add(SubscriptionGracePeriod), payload.ExpiresAt, time.Second,
		"a subscription customer's license.lic must expire at subscription_expires_at + grace, not DefaultLicenseTerm")
}

func TestActivate_SubscriptionCustomer_ReactivatingSameMachineRefreshesToNewSubscriptionExpiry(t *testing.T) {
	svc, db := newTestService(t)
	customerID := seedCustomer(t, db, "budi@example.com", "FIXUNIT", 1)
	signer, err := crypto.NewSigner(testSeedHex)
	require.NoError(t, err)

	first, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.Nil(t, appErr)
	firstPayload, err := crypto.Verify(first.LicenseLic, signer.PublicKeyHex())
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(DefaultLicenseTerm), firstPayload.ExpiresAt, time.Minute,
		"before any extension, a customer is lifetime")

	// Vendor sells this lifetime customer a subscription plan starting now.
	newExpiry := time.Now().UTC().Add(365 * 24 * time.Hour).Truncate(time.Second)
	_, err = db.Exec(`UPDATE license_customers SET subscription_expires_at = $1 WHERE license_customer_id = $2`, newExpiry, customerID)
	require.NoError(t, err)

	// Case 2 (reuse, same fingerprint) must also pick up the new subscription expiry —
	// the product just needs to call /activate again to refresh, no special code path.
	second, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.Nil(t, appErr)
	assert.True(t, second.Reused)
	secondPayload, err := crypto.Verify(second.LicenseLic, signer.PublicKeyHex())
	require.NoError(t, err)
	assert.WithinDuration(t, newExpiry.Add(SubscriptionGracePeriod), secondPayload.ExpiresAt, time.Second)
}

func TestActivate_LicenseLicVerifiesAndMatchesInput(t *testing.T) {
	svc, db := newTestService(t)
	seedCustomer(t, db, "budi@example.com", "FIXUNIT", 1)
	signer, err := crypto.NewSigner(testSeedHex)
	require.NoError(t, err)

	result, appErr := svc.Activate(context.Background(), ActivateInput{
		Email: "budi@example.com", ProductCode: "FIXUNIT", MachineFingerprint: "fp-pc-1",
	})
	require.Nil(t, appErr)

	payload, err := crypto.Verify(result.LicenseLic, signer.PublicKeyHex())
	require.NoError(t, err)
	assert.Equal(t, "budi@example.com", payload.Email)
	assert.Equal(t, "FIXUNIT", payload.ProductCode)
	assert.Equal(t, "fp-pc-1", payload.MachineFingerprint)
	assert.Equal(t, result.ActivationID, payload.ActivationID)
	assert.WithinDuration(t, time.Now().Add(DefaultLicenseTerm), payload.ExpiresAt, time.Minute)
}
