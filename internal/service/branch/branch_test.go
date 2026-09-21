package branch

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andyresta/licence-product/internal/crypto"
	"github.com/andyresta/licence-product/internal/testutil"
)

// testSeedHex is a fixed 64-hex-char (32-byte) Ed25519 seed — deterministic so test
// failures are reproducible, never used outside tests.
var testSeedHex = strings.Repeat("cd", 32)

func newTestService(t *testing.T) (*Service, *sql.DB, *crypto.Signer) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	signer, err := crypto.NewSigner(testSeedHex)
	require.NoError(t, err)
	return New(db, signer), db, signer
}

var seedSeq int

// seedCustomer inserts one product + one license_customers row with the given
// max_branches, returning its license_customer_id.
func seedCustomer(t *testing.T, db *sql.DB, email, productCode string, maxBranches int) string {
	t.Helper()
	seedSeq++
	productID := fmt.Sprintf("PRD%06d", seedSeq)
	_, err := db.Exec(`INSERT INTO products (product_id, product_code, nama) VALUES ($1, $2, $2)
		ON CONFLICT (product_code) DO UPDATE SET nama = EXCLUDED.nama`, productID, productCode)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT product_id FROM products WHERE product_code = $1`, productCode).Scan(&productID))

	custID := fmt.Sprintf("CUS%06d", seedSeq)
	_, err = db.Exec(`INSERT INTO customers (customer_id, email) VALUES ($1, $2)
		ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email`, custID, email)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT customer_id FROM customers WHERE email = $1`, email).Scan(&custID))

	licenseCustomerID := fmt.Sprintf("LCU%06d", seedSeq)
	_, err = db.Exec(`INSERT INTO license_customers (license_customer_id, customer_id, email, product_id, purchase_count, max_activations, max_branches)
		VALUES ($1, $2, $3, $4, 1, 1, $5)`, licenseCustomerID, custID, email, productID, maxBranches)
	require.NoError(t, err)
	return licenseCustomerID
}

// signLicense builds a license.lic for (email, productCode) the same shape
// license.Service.Activate would issue — branch endpoints only need email/product_code
// out of it, so the other fields are filler.
func signLicense(t *testing.T, signer *crypto.Signer, email, productCode string) string {
	t.Helper()
	lic, err := signer.Sign(crypto.LicensePayload{
		Email:              email,
		ProductCode:        productCode,
		MachineFingerprint: "fp-irrelevant-to-branches",
		ActivationID:       "ACT-IRRELEVANT",
	})
	require.NoError(t, err)
	return lic
}

func TestRegister_NewBranch_ConsumesOneSlot(t *testing.T) {
	svc, db, signer := newTestService(t)
	seedCustomer(t, db, "budi@example.com", "FIXUNIT", 2)
	lic := signLicense(t, signer, "budi@example.com", "FIXUNIT")

	result, appErr := svc.Register(context.Background(), RegisterInput{LicenseLic: lic, BranchCode: "CABANG-JKT"})
	require.Nil(t, appErr)
	assert.Equal(t, 1, result.Exist)
	assert.Equal(t, 2, result.Kuota)

	var activeCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM branches WHERE status = 'ACTIVE'`).Scan(&activeCount))
	assert.Equal(t, 1, activeCount)
}

func TestRegister_SameBranchCodeAgain_DoesNotConsumeSecondSlot(t *testing.T) {
	svc, db, signer := newTestService(t)
	seedCustomer(t, db, "budi@example.com", "FIXUNIT", 1)
	lic := signLicense(t, signer, "budi@example.com", "FIXUNIT")

	first, appErr := svc.Register(context.Background(), RegisterInput{LicenseLic: lic, BranchCode: "CABANG-JKT"})
	require.Nil(t, appErr)
	assert.Equal(t, 1, first.Exist)

	second, appErr := svc.Register(context.Background(), RegisterInput{LicenseLic: lic, BranchCode: "CABANG-JKT"})
	require.Nil(t, appErr)
	assert.Equal(t, 1, second.Exist, "registering the same branch_code again must not consume a second slot")

	var activeCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM branches WHERE status = 'ACTIVE'`).Scan(&activeCount))
	assert.Equal(t, 1, activeCount)
}

func TestRegister_DifferentBranch_RejectedOnceQuotaExhausted(t *testing.T) {
	svc, db, signer := newTestService(t)
	seedCustomer(t, db, "budi@example.com", "FIXUNIT", 1)
	lic := signLicense(t, signer, "budi@example.com", "FIXUNIT")

	_, appErr := svc.Register(context.Background(), RegisterInput{LicenseLic: lic, BranchCode: "CABANG-JKT"})
	require.Nil(t, appErr)

	result, appErr := svc.Register(context.Background(), RegisterInput{LicenseLic: lic, BranchCode: "CABANG-BDG"})
	require.NotNil(t, appErr)
	assert.Equal(t, "QUOTA_EXCEEDED", string(appErr.Code))
	// exist/kuota must still be populated on a quota-exceeded failure, per the API
	// contract — the caller needs these numbers even when registration is refused.
	assert.Equal(t, 1, result.Exist)
	assert.Equal(t, 1, result.Kuota)
}

func TestRegister_UnregisteredEmail_Rejected(t *testing.T) {
	svc, _, signer := newTestService(t)
	lic := signLicense(t, signer, "unknown@example.com", "FIXUNIT")

	_, appErr := svc.Register(context.Background(), RegisterInput{LicenseLic: lic, BranchCode: "CABANG-JKT"})
	require.NotNil(t, appErr)
	assert.Equal(t, "NOT_REGISTERED", string(appErr.Code))
}

func TestRegister_ForgedLicenseRejected(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, appErr := svc.Register(context.Background(), RegisterInput{LicenseLic: "not-a-real-license", BranchCode: "CABANG-JKT"})
	require.NotNil(t, appErr)
	assert.Equal(t, "INVALID_LICENSE", string(appErr.Code))
}

func TestCheck_ReflectsRegisteredBranchesWithoutMutating(t *testing.T) {
	svc, db, signer := newTestService(t)
	seedCustomer(t, db, "budi@example.com", "FIXUNIT", 5)
	lic := signLicense(t, signer, "budi@example.com", "FIXUNIT")

	before, appErr := svc.Check(context.Background(), lic)
	require.Nil(t, appErr)
	assert.Equal(t, 0, before.Exist)
	assert.Equal(t, 5, before.Kuota)

	_, appErr = svc.Register(context.Background(), RegisterInput{LicenseLic: lic, BranchCode: "CABANG-JKT"})
	require.Nil(t, appErr)

	after, appErr := svc.Check(context.Background(), lic)
	require.Nil(t, appErr)
	assert.Equal(t, 1, after.Exist)
	assert.Equal(t, 5, after.Kuota)

	var branchCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM branches`).Scan(&branchCount))
	assert.Equal(t, 1, branchCount, "Check must never create a branch row itself")
}

func TestRegister_ReclaimsDeactivatedBranch_GoesThroughQuotaAgain(t *testing.T) {
	svc, db, signer := newTestService(t)
	customerID := seedCustomer(t, db, "budi@example.com", "FIXUNIT", 1)
	lic := signLicense(t, signer, "budi@example.com", "FIXUNIT")

	first, appErr := svc.Register(context.Background(), RegisterInput{LicenseLic: lic, BranchCode: "CABANG-JKT"})
	require.Nil(t, appErr)
	assert.Equal(t, 1, first.Exist)

	_, err := db.Exec(`UPDATE branches SET status = 'DEACTIVATED' WHERE license_customer_id = $1 AND branch_code = $2`,
		customerID, "CABANG-JKT")
	require.NoError(t, err)

	// The freed slot can go to a different branch_code.
	second, appErr := svc.Register(context.Background(), RegisterInput{LicenseLic: lic, BranchCode: "CABANG-BDG"})
	require.Nil(t, appErr)
	assert.Equal(t, 1, second.Exist)

	// The original branch_code trying to reclaim now finds the quota taken by BDG.
	result, appErr := svc.Register(context.Background(), RegisterInput{LicenseLic: lic, BranchCode: "CABANG-JKT"})
	require.NotNil(t, appErr)
	assert.Equal(t, "QUOTA_EXCEEDED", string(appErr.Code))
	assert.Equal(t, 1, result.Exist)
}
