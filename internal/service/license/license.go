// Package license implements the activation/deactivation rules — the part of this
// server every other product depends on being correct. The core invariant: the number
// of ACTIVE activation rows for a license_customers pair must never exceed its
// max_activations, and a machine re-activating (same fingerprint, still ACTIVE) must
// never consume a second seat — that's what makes an OS reinstall on the same physical
// machine free.
package license

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

// DefaultLicenseTerm is how far in the future ExpiresAt is set for a normal
// activation/reactivation when the caller doesn't request a specific term — the vendor
// treats "lifetime" products as a very-far expiry (see README) rather than no expiry
// at all, so every license.lic can be validated the same way regardless of whether the
// underlying product is sold as perpetual or subscription.
const DefaultLicenseTerm = 74 * 365 * 24 * time.Hour // ~74 years — comfortably "lifetime"

type Service struct {
	db     *sql.DB
	signer *crypto.Signer
}

func New(db *sql.DB, signer *crypto.Signer) *Service {
	return &Service{db: db, signer: signer}
}

type ActivateInput struct {
	Email              string
	ProductCode        string
	MachineFingerprint string
	MachineLabel       *string
}

type ActivateResult struct {
	LicenseLic   string
	ActivationID string
	Reused       bool // true when an existing ACTIVE row for this exact machine was refreshed rather than a new seat consumed
}

// Activate resolves (email, product_code) to a license_customers row, then applies
// exactly one of three cases against machine_fingerprint:
//  1. No existing activation row for this machine -> must have a free seat.
//  2. An existing ACTIVE row for this machine -> reuse, no seat consumed (the OS
//     reinstall / re-run-the-installer case).
//  3. An existing DEACTIVATED row for this machine -> reclaiming a previously freed
//     seat, so it goes through the same seat check as a brand new machine.
func (s *Service) Activate(ctx context.Context, input ActivateInput) (ActivateResult, *apperror.Error) {
	var licenseCustomerID string
	var maxActivations int
	err := s.db.QueryRowContext(ctx, `
		SELECT lc.license_customer_id
		FROM license_customers lc
		JOIN products p ON p.product_id = lc.product_id
		WHERE lc.email = $1 AND p.product_code = $2 AND p.status_aktif`,
		input.Email, input.ProductCode).Scan(&licenseCustomerID)
	if err == sql.ErrNoRows {
		return ActivateResult{}, apperror.New(apperror.NotRegistered, "Email dan product_code ini tidak terdaftar")
	}
	if err != nil {
		return ActivateResult{}, apperror.New(apperror.Internal, "gagal memeriksa data lisensi")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ActivateResult{}, apperror.New(apperror.Internal, "gagal memulai transaksi")
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// Lock the customer row itself for the rest of this transaction — Postgres
	// disallows FOR UPDATE directly on an aggregate (COUNT) query below, and this
	// also serializes any two concurrent activation attempts for the same customer,
	// which is what actually needs to be serialized (not the activations rows
	// individually).
	if err := tx.QueryRowContext(ctx, `
		SELECT max_activations FROM license_customers WHERE license_customer_id = $1 FOR UPDATE`,
		licenseCustomerID).Scan(&maxActivations); err != nil {
		return ActivateResult{}, apperror.New(apperror.Internal, "gagal mengunci data lisensi")
	}

	var existingID, existingStatus string
	lookupErr := tx.QueryRowContext(ctx, `
		SELECT activation_id, status FROM activations
		WHERE license_customer_id = $1 AND machine_fingerprint = $2
		FOR UPDATE`,
		licenseCustomerID, input.MachineFingerprint).Scan(&existingID, &existingStatus)
	if lookupErr != nil && lookupErr != sql.ErrNoRows {
		return ActivateResult{}, apperror.New(apperror.Internal, "gagal memeriksa aktivasi")
	}
	hasExistingRow := lookupErr == nil

	now := time.Now().UTC()
	expiresAt := now.Add(DefaultLicenseTerm)

	if hasExistingRow && existingStatus == model.ActivationStatusActive {
		// Case 2: same machine, already active — refresh and reuse, no seat consumed.
		if _, err := tx.ExecContext(ctx, `
			UPDATE activations SET last_seen_at = $1, expires_at = $2, update_at = $1, machine_label = COALESCE($3, machine_label)
			WHERE activation_id = $4`,
			now, expiresAt, input.MachineLabel, existingID); err != nil {
			return ActivateResult{}, apperror.New(apperror.Internal, "gagal memperbarui aktivasi")
		}
		if err := tx.Commit(); err != nil {
			return ActivateResult{}, apperror.New(apperror.Internal, "gagal menyimpan aktivasi")
		}
		lic, sigErr := s.sign(input, existingID, now, expiresAt)
		if sigErr != nil {
			return ActivateResult{}, sigErr
		}
		return ActivateResult{LicenseLic: lic, ActivationID: existingID, Reused: true}, nil
	}

	// Case 1 (no row) or case 3 (DEACTIVATED row, reclaiming a seat) — both need a
	// free seat. The license_customers row lock taken above already serializes any
	// concurrent activation attempts for this same customer, so this plain count
	// (Postgres disallows FOR UPDATE on an aggregate query) is race-free.
	var activeCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM activations WHERE license_customer_id = $1 AND status = $2`,
		licenseCustomerID, model.ActivationStatusActive).Scan(&activeCount); err != nil {
		return ActivateResult{}, apperror.New(apperror.Internal, "gagal menghitung aktivasi")
	}
	if activeCount >= maxActivations {
		return ActivateResult{}, apperror.New(apperror.QuotaExceeded, fmt.Sprintf("kuota aktivasi sudah penuh (%d/%d)", activeCount, maxActivations))
	}

	var activationID string
	if !hasExistingRow {
		activationID, err = idgen.Generate("ACT")
		if err != nil {
			return ActivateResult{}, apperror.New(apperror.Internal, "gagal membuat activation_id")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO activations (activation_id, license_customer_id, machine_fingerprint, machine_label, status, activated_at, last_seen_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6, $7)`,
			activationID, licenseCustomerID, input.MachineFingerprint, input.MachineLabel, model.ActivationStatusActive, now, expiresAt); err != nil {
			return ActivateResult{}, apperror.New(apperror.Internal, "gagal mencatat aktivasi baru")
		}
	} else {
		// Reclaiming a DEACTIVATED row for this exact fingerprint.
		activationID = existingID
		if _, err := tx.ExecContext(ctx, `
			UPDATE activations SET status = $1, activated_at = $2, last_seen_at = $2, expires_at = $3,
				deactivated_at = NULL, deactivated_by = NULL, update_at = $2, machine_label = COALESCE($4, machine_label)
			WHERE activation_id = $5`,
			model.ActivationStatusActive, now, expiresAt, input.MachineLabel, activationID); err != nil {
			return ActivateResult{}, apperror.New(apperror.Internal, "gagal mengaktifkan ulang aktivasi")
		}
	}

	if err := tx.Commit(); err != nil {
		return ActivateResult{}, apperror.New(apperror.Internal, "gagal menyimpan aktivasi")
	}
	lic, sigErr := s.sign(input, activationID, now, expiresAt)
	if sigErr != nil {
		return ActivateResult{}, sigErr
	}
	return ActivateResult{LicenseLic: lic, ActivationID: activationID, Reused: false}, nil
}

func (s *Service) sign(input ActivateInput, activationID string, issuedAt, expiresAt time.Time) (string, *apperror.Error) {
	lic, err := s.signer.Sign(crypto.LicensePayload{
		Email:              input.Email,
		ProductCode:        input.ProductCode,
		MachineFingerprint: input.MachineFingerprint,
		ActivationID:       activationID,
		IssuedAt:           issuedAt,
		ExpiresAt:          expiresAt,
	})
	if err != nil {
		return "", apperror.New(apperror.Internal, "gagal menandatangani license.lic")
	}
	return lic, nil
}

// Deactivate frees the seat identified by a currently-signed license.lic (proof that
// the caller actually holds a genuine activation, not just knowledge of someone else's
// email/product_code). Signature validity is checked; ExpiresAt is deliberately NOT
// checked here — freeing a seat should work even past expiry.
func (s *Service) Deactivate(ctx context.Context, licenseLic string) *apperror.Error {
	payload, err := crypto.Verify(licenseLic, s.signer.PublicKeyHex())
	if err != nil {
		return apperror.New(apperror.InvalidLicense, "license.lic tidak valid")
	}
	res, err2 := s.db.ExecContext(ctx, `
		UPDATE activations SET status = $1, deactivated_at = $2, update_at = $2
		WHERE activation_id = $3 AND status = $4`,
		model.ActivationStatusDeactivated, time.Now().UTC(), payload.ActivationID, model.ActivationStatusActive)
	if err2 != nil {
		return apperror.New(apperror.Internal, "gagal melepas aktivasi")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.New(apperror.NotFound, "aktivasi tidak ditemukan atau sudah dilepas")
	}
	return nil
}
