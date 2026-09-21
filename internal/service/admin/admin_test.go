package admin

import (
	"context"
	"database/sql"
	"testing"
	"time"

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

	appErr = svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 3, RecordedBy: adminID})
	require.Nil(t, appErr)

	customers, err := svc.ListLicenses(context.Background(), "budi")
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

	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 2, RecordedBy: adminID}))
	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 5, RecordedBy: adminID}))

	customers, err := svc.ListLicenses(context.Background(), "budi")
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

	appErr := svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "NOPE", Seats: 1, RecordedBy: adminID})
	require.NotNil(t, appErr)
	assert.Equal(t, "NOT_FOUND", string(appErr.Code))
}

func TestForceDeactivate_FreesSeatForNextActivation(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	_, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)
	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 1, RecordedBy: adminID}))

	customers, err := svc.ListLicenses(context.Background(), "budi")
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

func TestUpdateProduct_ChangesNameAndDescription(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	product, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)

	appErr = svc.UpdateProduct(context.Background(), product.ProductID, "FixUnit POS", "Aplikasi kasir")
	require.Nil(t, appErr)

	products, err := svc.ListProducts(context.Background())
	require.NoError(t, err)
	require.Len(t, products, 1)
	assert.Equal(t, "FixUnit POS", products[0].Nama)
	require.NotNil(t, products[0].Keterangan)
	assert.Equal(t, "Aplikasi kasir", *products[0].Keterangan)
}

func TestSetProductStatus_DeactivatedProductRejectsNewPurchases(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	product, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)

	require.Nil(t, svc.SetProductStatus(context.Background(), product.ProductID, false))

	appErr = svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 1, RecordedBy: adminID})
	require.NotNil(t, appErr, "an inactive product must refuse new purchases")
	assert.Equal(t, "NOT_FOUND", string(appErr.Code))

	require.Nil(t, svc.SetProductStatus(context.Background(), product.ProductID, true))
	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 1, RecordedBy: adminID}))
}

func TestDeleteProduct_RefusesWhenCustomerExists(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	product, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)
	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 1, RecordedBy: adminID}))

	appErr = svc.DeleteProduct(context.Background(), product.ProductID)
	require.NotNil(t, appErr, "a product with a recorded customer must not be deletable")
	assert.Equal(t, "VALIDATION", string(appErr.Code))

	products, err := svc.ListProducts(context.Background())
	require.NoError(t, err)
	assert.Len(t, products, 1, "the product must still exist")
}

func TestDeleteProduct_RemovesUnusedProduct(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	product, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)

	require.Nil(t, svc.DeleteProduct(context.Background(), product.ProductID))

	products, err := svc.ListProducts(context.Background())
	require.NoError(t, err)
	assert.Empty(t, products)
}

func TestSetMaxBranches_UpdatesQuotaDirectly(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	_, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)
	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 1, RecordedBy: adminID}))

	customers, err := svc.ListLicenses(context.Background(), "budi")
	require.NoError(t, err)
	require.Len(t, customers, 1)
	assert.Equal(t, 5, customers[0].MaxBranches, "schema default is 5")

	require.Nil(t, svc.SetMaxBranches(context.Background(), customers[0].LicenseCustomerID, 10))

	customer, appErr := svc.GetLicense(context.Background(), customers[0].LicenseCustomerID)
	require.Nil(t, appErr)
	assert.Equal(t, 10, customer.MaxBranches)
}

func TestForceDeactivateBranch_FreesSlotForNextRegistration(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	_, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)
	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 1, RecordedBy: adminID}))

	customers, err := svc.ListLicenses(context.Background(), "budi")
	require.NoError(t, err)
	customerID := customers[0].LicenseCustomerID

	_, err = db.Exec(`INSERT INTO branches (branch_id, license_customer_id, branch_code, status)
		VALUES ('BRC000001', $1, 'CABANG-JKT', 'ACTIVE')`, customerID)
	require.NoError(t, err)

	appErr = svc.ForceDeactivateBranch(context.Background(), "BRC000001", adminID)
	require.Nil(t, appErr)

	branches, err := svc.ListBranches(context.Background(), customerID)
	require.NoError(t, err)
	require.Len(t, branches, 1)
	assert.Equal(t, "DEACTIVATED", branches[0].Status)

	appErr = svc.ForceDeactivateBranch(context.Background(), "BRC000001", adminID)
	require.NotNil(t, appErr)
	assert.Equal(t, "NOT_FOUND", string(appErr.Code))
}

func TestExtendSubscription_FromLifetime_SetsExpiryFromNow(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	_, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)
	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 1, RecordedBy: adminID}))

	customers, err := svc.ListLicenses(context.Background(), "budi")
	require.NoError(t, err)
	require.Len(t, customers, 1)
	assert.Nil(t, customers[0].SubscriptionExpiresAt, "a freshly-purchased customer starts as lifetime")
	customerID := customers[0].LicenseCustomerID

	require.Nil(t, svc.ExtendSubscription(context.Background(), customerID, 1, nil, adminID))

	customer, appErr := svc.GetLicense(context.Background(), customerID)
	require.Nil(t, appErr)
	require.NotNil(t, customer.SubscriptionExpiresAt)
	assert.WithinDuration(t, time.Now().AddDate(0, 1, 0), *customer.SubscriptionExpiresAt, time.Minute)

	extensions, err := svc.ListSubscriptionExtensions(context.Background(), customerID)
	require.NoError(t, err)
	require.Len(t, extensions, 1)
	assert.Equal(t, 1, extensions[0].Months)
}

func TestExtendSubscription_BeforeExpiry_StacksOnTopInsteadOfFromNow(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	_, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)
	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 1, RecordedBy: adminID}))
	customers, err := svc.ListLicenses(context.Background(), "budi")
	require.NoError(t, err)
	customerID := customers[0].LicenseCustomerID

	require.Nil(t, svc.ExtendSubscription(context.Background(), customerID, 12, nil, adminID))
	first, appErr := svc.GetLicense(context.Background(), customerID)
	require.Nil(t, appErr)
	firstExpiry := *first.SubscriptionExpiresAt

	// Renewing again well before the first term ends must stack on top of it, not
	// restart from "now" — a customer renewing early should never lose time.
	require.Nil(t, svc.ExtendSubscription(context.Background(), customerID, 1, nil, adminID))
	second, appErr := svc.GetLicense(context.Background(), customerID)
	require.Nil(t, appErr)
	assert.WithinDuration(t, firstExpiry.AddDate(0, 1, 0), *second.SubscriptionExpiresAt, time.Minute)

	extensions, err := svc.ListSubscriptionExtensions(context.Background(), customerID)
	require.NoError(t, err)
	assert.Len(t, extensions, 2, "both extensions must be recorded in the audit trail")
}

func TestDeleteLicense_RemovesLicenseAndAllItsHistory(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	_, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)
	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 5, RecordedBy: adminID}))
	licenses, err := svc.ListLicenses(context.Background(), "budi")
	require.NoError(t, err)
	licenseID := licenses[0].LicenseCustomerID

	// Give the license some history in every dependent table, to prove DeleteLicense
	// clears all of it (none of these tables carry ON DELETE CASCADE).
	_, err = db.Exec(`INSERT INTO activations (activation_id, license_customer_id, machine_fingerprint, status, expires_at)
		VALUES ('ACT000001', $1, 'fp-1', 'ACTIVE', now() + interval '1 year')`, licenseID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO branches (branch_id, license_customer_id, branch_code, status)
		VALUES ('BRC000001', $1, 'CABANG-JKT', 'ACTIVE')`, licenseID)
	require.NoError(t, err)
	require.Nil(t, svc.ExtendSubscription(context.Background(), licenseID, 1, nil, adminID))

	appErr = svc.DeleteLicense(context.Background(), licenseID)
	require.Nil(t, appErr)

	_, appErr = svc.GetLicense(context.Background(), licenseID)
	require.NotNil(t, appErr)
	assert.Equal(t, "NOT_FOUND", string(appErr.Code))

	var count int
	for _, table := range []string{"activations", "branches", "purchases", "subscription_extensions"} {
		require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE license_customer_id = $1", licenseID).Scan(&count))
		assert.Zero(t, count, "%s must have no rows left for the deleted license", table)
	}
}

func TestDeleteLicense_UnknownID_ReturnsNotFound(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)

	appErr := svc.DeleteLicense(context.Background(), "LCU999999")
	require.NotNil(t, appErr)
	assert.Equal(t, "NOT_FOUND", string(appErr.Code))
}

func TestDeleteCustomer_RefusesWhenLicenseExists(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	_, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)
	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 1, RecordedBy: adminID}))
	licenses, err := svc.ListLicenses(context.Background(), "budi")
	require.NoError(t, err)
	customerID := licenses[0].CustomerID

	appErr = svc.DeleteCustomer(context.Background(), customerID)
	require.NotNil(t, appErr, "a customer with an existing license must not be deletable")
	assert.Equal(t, "VALIDATION", string(appErr.Code))

	_, appErr = svc.GetCustomerProfile(context.Background(), customerID)
	assert.Nil(t, appErr, "the customer must still exist")
}

func TestDeleteCustomer_RemovesCustomerOnceLicensesAreGone(t *testing.T) {
	db := testutil.OpenTestDB(t)
	svc := New(db)
	adminID := seedAdmin(t, db)
	_, appErr := svc.CreateProduct(context.Background(), "FIXUNIT", "FixUnit")
	require.Nil(t, appErr)
	require.Nil(t, svc.RecordPurchase(context.Background(), RecordPurchaseInput{Email: "budi@example.com", ProductCode: "FIXUNIT", Seats: 1, RecordedBy: adminID}))
	licenses, err := svc.ListLicenses(context.Background(), "budi")
	require.NoError(t, err)
	customerID := licenses[0].CustomerID

	require.Nil(t, svc.DeleteLicense(context.Background(), licenses[0].LicenseCustomerID))
	require.Nil(t, svc.DeleteCustomer(context.Background(), customerID))

	_, appErr = svc.GetCustomerProfile(context.Background(), customerID)
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
