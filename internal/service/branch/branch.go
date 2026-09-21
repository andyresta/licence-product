// Package branch implements the optional branch-quota mechanism: a second, independent
// quota axis alongside internal/service/license's activations (machines). A product
// that sells to customers with multiple physical outlets can call Register once per
// branch to enforce a limit on how many branches one license covers; a product that
// doesn't care about this never calls it — every license_customers row still carries a
// max_branches value (default 5, adjustable per-customer from the admin panel), so
// nothing needs to change here for a product to start using it later.
//
// Register mirrors license.Service.Activate's exact three-case state machine (see that
// file's doc comment) with branch_code standing in for machine_fingerprint: no existing
// row needs a free slot, an existing ACTIVE row is reused for free, and a DEACTIVATED
// row is reclaimed through the same quota check as a brand new branch.
package branch

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/andyresta/licence-product/internal/apperror"
	"github.com/andyresta/licence-product/internal/crypto"
	"github.com/andyresta/licence-product/internal/idgen"
	"github.com/andyresta/licence-product/internal/model"
)

type Service struct {
	db     *sql.DB
	signer *crypto.Signer
}

func New(db *sql.DB, signer *crypto.Signer) *Service {
	return &Service{db: db, signer: signer}
}

type CheckResult struct {
	Exist int // current count of ACTIVE branches
	Kuota int // max_branches for this license
}

// Check is a read-only peek at a license's branch quota, identified by its license.lic
// content — it registers nothing. Useful for a product to show "3/5 cabang terpakai"
// without side effects.
func (s *Service) Check(ctx context.Context, licenseLic string) (CheckResult, *apperror.Error) {
	payload, err := crypto.Verify(licenseLic, s.signer.PublicKeyHex())
	if err != nil {
		return CheckResult{}, apperror.New(apperror.InvalidLicense, "license.lic tidak valid")
	}

	licenseCustomerID, maxBranches, appErr := s.lookupCustomer(ctx, payload.Email, payload.ProductCode)
	if appErr != nil {
		return CheckResult{}, appErr
	}

	exist, err := countActiveBranches(ctx, s.db, licenseCustomerID)
	if err != nil {
		return CheckResult{}, apperror.New(apperror.Internal, "gagal menghitung branch")
	}
	return CheckResult{Exist: exist, Kuota: maxBranches}, nil
}

type RegisterInput struct {
	LicenseLic  string
	BranchCode  string
	BranchLabel *string
}

type RegisterResult struct {
	Exist int
	Kuota int
}

// Register applies the same three-case logic as license.Service.Activate, keyed on
// branch_code instead of machine_fingerprint:
//  1. No existing row for this branch_code -> must have a free slot.
//  2. An existing ACTIVE row for this branch_code -> reuse, no slot consumed
//     (calling Register again for a branch that's already registered is a no-op).
//  3. An existing DEACTIVATED row -> reclaiming a previously freed slot, so it goes
//     through the same quota check as a brand new branch.
//
// Unlike Activate, there is no signed token to issue here — the caller already holds
// the license.lic; Register only reports whether it succeeded plus the resulting
// exist/kuota counts (returned even on QUOTA_EXCEEDED, so the caller can show the
// customer why it was refused).
func (s *Service) Register(ctx context.Context, input RegisterInput) (RegisterResult, *apperror.Error) {
	payload, err := crypto.Verify(input.LicenseLic, s.signer.PublicKeyHex())
	if err != nil {
		return RegisterResult{}, apperror.New(apperror.InvalidLicense, "license.lic tidak valid")
	}

	licenseCustomerID, maxBranches, appErr := s.lookupCustomer(ctx, payload.Email, payload.ProductCode)
	if appErr != nil {
		return RegisterResult{}, appErr
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RegisterResult{}, apperror.New(apperror.Internal, "gagal memulai transaksi")
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// Lock the customer row for the rest of this transaction — same reasoning as
	// license.Service.Activate: serializes concurrent branch registrations for the
	// same customer, which is what actually needs to be serialized.
	if _, err := tx.ExecContext(ctx, `SELECT max_branches FROM license_customers WHERE license_customer_id = $1 FOR UPDATE`, licenseCustomerID); err != nil {
		return RegisterResult{}, apperror.New(apperror.Internal, "gagal mengunci data lisensi")
	}

	var existingID, existingStatus string
	lookupErr := tx.QueryRowContext(ctx, `
		SELECT branch_id, status FROM branches
		WHERE license_customer_id = $1 AND branch_code = $2
		FOR UPDATE`,
		licenseCustomerID, input.BranchCode).Scan(&existingID, &existingStatus)
	if lookupErr != nil && lookupErr != sql.ErrNoRows {
		return RegisterResult{}, apperror.New(apperror.Internal, "gagal memeriksa branch")
	}
	hasExistingRow := lookupErr == nil
	now := time.Now().UTC()

	if hasExistingRow && existingStatus == model.BranchStatusActive {
		// Case 2: already registered and active — reuse, no slot consumed.
		if _, err := tx.ExecContext(ctx, `
			UPDATE branches SET update_at = $1, branch_label = COALESCE($2, branch_label)
			WHERE branch_id = $3`,
			now, input.BranchLabel, existingID); err != nil {
			return RegisterResult{}, apperror.New(apperror.Internal, "gagal memperbarui branch")
		}
		if err := tx.Commit(); err != nil {
			return RegisterResult{}, apperror.New(apperror.Internal, "gagal menyimpan branch")
		}
		exist, err := countActiveBranches(ctx, s.db, licenseCustomerID)
		if err != nil {
			return RegisterResult{Kuota: maxBranches}, apperror.New(apperror.Internal, "gagal menghitung branch")
		}
		return RegisterResult{Exist: exist, Kuota: maxBranches}, nil
	}

	// Case 1 (no row) or case 3 (DEACTIVATED row, reclaiming a slot) — both need a
	// free slot. The customer row lock above already serializes concurrent
	// registrations for this customer, so this plain count is race-free.
	var activeCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM branches WHERE license_customer_id = $1 AND status = $2`,
		licenseCustomerID, model.BranchStatusActive).Scan(&activeCount); err != nil {
		return RegisterResult{}, apperror.New(apperror.Internal, "gagal menghitung branch")
	}
	if activeCount >= maxBranches {
		return RegisterResult{Exist: activeCount, Kuota: maxBranches},
			apperror.New(apperror.QuotaExceeded, fmt.Sprintf("kuota branch sudah penuh (%d/%d)", activeCount, maxBranches))
	}

	if !hasExistingRow {
		branchID, err := idgen.Generate("BRC")
		if err != nil {
			return RegisterResult{}, apperror.New(apperror.Internal, "gagal membuat branch_id")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO branches (branch_id, license_customer_id, branch_code, branch_label, status, registered_at)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			branchID, licenseCustomerID, input.BranchCode, input.BranchLabel, model.BranchStatusActive, now); err != nil {
			return RegisterResult{}, apperror.New(apperror.Internal, "gagal mendaftarkan branch baru")
		}
	} else {
		// Reclaiming a DEACTIVATED row for this exact branch_code.
		if _, err := tx.ExecContext(ctx, `
			UPDATE branches SET status = $1, registered_at = $2, update_at = $2,
				deactivated_at = NULL, deactivated_by = NULL, branch_label = COALESCE($3, branch_label)
			WHERE branch_id = $4`,
			model.BranchStatusActive, now, input.BranchLabel, existingID); err != nil {
			return RegisterResult{}, apperror.New(apperror.Internal, "gagal mengaktifkan ulang branch")
		}
	}

	if err := tx.Commit(); err != nil {
		return RegisterResult{}, apperror.New(apperror.Internal, "gagal menyimpan branch")
	}
	return RegisterResult{Exist: activeCount + 1, Kuota: maxBranches}, nil
}

type ReleaseResult struct {
	Exist int
	Kuota int
}

// Release is the self-service counterpart to Register — a product calls this when one
// of its branches/outlets closes down, freeing that slot on its own instead of making
// the customer wait on the vendor to step in from the admin panel (see
// admin.Service.ForceDeactivateBranch for that path). Authenticated the same way as
// license.Service.Deactivate: by presenting a currently valid, signed license.lic
// instead of an admin session — no separate credential needed.
func (s *Service) Release(ctx context.Context, licenseLic, branchCode string) (ReleaseResult, *apperror.Error) {
	payload, err := crypto.Verify(licenseLic, s.signer.PublicKeyHex())
	if err != nil {
		return ReleaseResult{}, apperror.New(apperror.InvalidLicense, "license.lic tidak valid")
	}

	licenseCustomerID, maxBranches, appErr := s.lookupCustomer(ctx, payload.Email, payload.ProductCode)
	if appErr != nil {
		return ReleaseResult{}, appErr
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE branches SET status = $1, deactivated_at = $2, update_at = $2
		WHERE license_customer_id = $3 AND branch_code = $4 AND status = $5`,
		model.BranchStatusDeactivated, time.Now().UTC(), licenseCustomerID, branchCode, model.BranchStatusActive)
	if err != nil {
		return ReleaseResult{}, apperror.New(apperror.Internal, "gagal melepas branch")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ReleaseResult{}, apperror.New(apperror.NotFound, "branch tidak ditemukan atau sudah dilepas")
	}

	exist, err := countActiveBranches(ctx, s.db, licenseCustomerID)
	if err != nil {
		return ReleaseResult{Kuota: maxBranches}, apperror.New(apperror.Internal, "gagal menghitung branch")
	}
	return ReleaseResult{Exist: exist, Kuota: maxBranches}, nil
}

// lookupCustomer resolves (email, product_code) from a verified license.lic payload to
// its license_customer_id and current max_branches — the same lookup shape as
// license.Service.Activate uses for max_activations.
func (s *Service) lookupCustomer(ctx context.Context, email, productCode string) (licenseCustomerID string, maxBranches int, appErr *apperror.Error) {
	err := s.db.QueryRowContext(ctx, `
		SELECT lc.license_customer_id, lc.max_branches
		FROM license_customers lc
		JOIN products p ON p.product_id = lc.product_id
		WHERE lc.email = $1 AND p.product_code = $2 AND p.status_aktif`,
		email, productCode).Scan(&licenseCustomerID, &maxBranches)
	if err == sql.ErrNoRows {
		return "", 0, apperror.New(apperror.NotRegistered, "Email dan product_code ini tidak terdaftar")
	}
	if err != nil {
		return "", 0, apperror.New(apperror.Internal, "gagal memeriksa data lisensi")
	}
	return licenseCustomerID, maxBranches, nil
}

func countActiveBranches(ctx context.Context, db *sql.DB, licenseCustomerID string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM branches WHERE license_customer_id = $1 AND status = $2`,
		licenseCustomerID, model.BranchStatusActive).Scan(&count)
	return count, err
}
