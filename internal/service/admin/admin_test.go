package admin

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/andyresta/licence-product/internal/idgen"
	"github.com/andyresta/licence-product/internal/testutil"
)

func seedAdminWithPassword(t *testing.T, db *sql.DB, username, password string) string {
	t.Helper()
	id, err := idgen.Generate("ADM")
	require.NoError(t, err)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO admin_users (admin_user_id, username, password_hash) VALUES ($1, $2, $3)`, id, username, string(hash))
	require.NoError(t, err)
	return id
}

func seedAdmin(t *testing.T, db *sql.DB) string {
	t.Helper()
	return seedAdminWithPassword(t, db, "vendor-"+t.Name(), "test-password")
}

func TestRecordPurchase_FirstPurchase_CreatesCustomer(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	_, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)

	appErr = svc.RecordPurchase(context.Background(), "budi@example.com", "FIXUNIT", 3, nil, adminID)
	require.Nil(t, appErr)

	customers, err := svc.ListCustomers(context.Background(), "budi")
	require.NoError(t, err)
	require.Len(t, customers, 1)
	assert.Equal(t, 1, customers[0].PurchaseCount)
	assert.Equal(t, 3, customers[0].MaxActivations)
}

func TestRecordPurchase_RepeatPurchase_AccumulatesSeatsAndCount(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	_, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)

	require.Nil(t, svc.RecordPurchase(context.Background(), "budi@example.com", "FIXUNIT", 2, nil, adminID))
	require.Nil(t, svc.RecordPurchase(context.Background(), "budi@example.com", "FIXUNIT", 5, nil, adminID))

	customers, err := svc.ListCustomers(context.Background(), "budi")
	require.NoError(t, err)
	require.Len(t, customers, 1, "a repeat purchase of the same product must accumulate onto the same row, not create a second one")
	assert.Equal(t, 2, customers[0].PurchaseCount)
	assert.Equal(t, 7, customers[0].MaxActivations)

	purchases, err := svc.ListPurchases(context.Background(), customers[0].LicenseCustomerID)
	require.NoError(t, err)
	require.Len(t, purchases, 2, "both purchases must still be individually recorded in the audit trail")
}

func TestRecordPurchase_UnknownProductCode_Rejected(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)

	appErr := svc.RecordPurchase(context.Background(), "budi@example.com", "NOPE", 1, nil, adminID)
	require.NotNil(t, appErr)
	assert.Equal(t, "NOT_FOUND", string(appErr.Code))
}

func TestForceDeactivate_FreesSeatForNextActivation(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	_, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)
	require.Nil(t, svc.RecordPurchase(context.Background(), "budi@example.com", "FIXUNIT", 1, nil, adminID))

	customers, err := svc.ListCustomers(context.Background(), "budi")
	require.NoError(t, err)
	customerID := customers[0].LicenseCustomerID

	_, err = db.Exec(`INSERT INTO activations (activation_id, license_customer_id, machine_fingerprint, status, expires_at)
		VALUES ('ACT000001', $1, 'fp-1', 'ACTIVE', now() + interval '1 year')`, customerID)
	require.NoError(t, err)

	appErr = svc.ForceDeactivate(context.Background(), "ACT000001", adminID)
	require.Nil(t, appErr)

	activations, err := svc.ListActivations(context.Background(), customerID)
	require.NoError(t, err)
	require.Len(t, activations, 1)
	assert.Equal(t, "DEACTIVATED", activations[0].Status)

	// Re-deactivating the same (already-deactivated) row must fail, not silently
	// succeed — mirrors license.Service.Deactivate's own idempotency guard.
	appErr = svc.ForceDeactivate(context.Background(), "ACT000001", adminID)
	require.NotNil(t, appErr)
	assert.Equal(t, "NOT_FOUND", string(appErr.Code))
}

func TestAuthenticate_WrongPassword_Rejected(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	seedAdminWithPassword(t, db, "vendor", "correct-horse")

	_, appErr := svc.Authenticate(context.Background(), "vendor", "wrong-password")
	require.NotNil(t, appErr)
	assert.Equal(t, "UNAUTHORIZED", string(appErr.Code))

	id, appErr := svc.Authenticate(context.Background(), "vendor", "correct-horse")
	require.Nil(t, appErr)
	assert.NotEmpty(t, id)
}

func TestEnsureBootstrapAdmin_DoesNotOverwriteExisting(t *testing.T) {
	db := testutil.OpenTestDB(t)
	seedAdminWithPassword(t, db, "vendor", "original-password")

	require.NoError(t, EnsureBootstrapAdmin(context.Background(), db, "vendor", "different-password"))

	svc := New(db)
	_, appErr := svc.Authenticate(context.Background(), "vendor", "original-password")
	assert.Nil(t, appErr, "bootstrap must never overwrite an admin account that already exists")
}
