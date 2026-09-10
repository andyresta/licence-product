// Package admin implements the back-office operations the admin panel exposes:
// authentication, recording purchases, and viewing/managing customers & activations.
// This is intentionally separate from internal/service/license — that package is the
// public activation-quota contract every product depends on being correct; this one is
// vendor-only tooling built on top of it.
package admin

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/andyresta/licence-product/internal/apperror"
	"github.com/andyresta/licence-product/internal/idgen"
	"github.com/andyresta/licence-product/internal/model"
)

type Service struct {
	db *sql.DB
}

func New(db *sql.DB) *Service {
	return &Service{db: db}
}

// Authenticate returns the admin_user_id for a valid username/password, or
// Unauthorized for any failure (unknown username, wrong password, deactivated
// account) — deliberately the same error/message for all three so a login form can't
// be used to enumerate valid usernames.
func (s *Service) Authenticate(ctx context.Context, username, password string) (string, *apperror.Error) {
	var adminUserID, passwordHash string
	var statusAktif bool
	err := s.db.QueryRowContext(ctx, `SELECT admin_user_id, password_hash, status_aktif FROM admin_users WHERE username = $1`, username).
		Scan(&adminUserID, &passwordHash, &statusAktif)
	if err == sql.ErrNoRows {
		return "", apperror.New(apperror.Unauthorized, "username atau password salah")
	}
	if err != nil {
		return "", apperror.New(apperror.Internal, "gagal memeriksa akun admin")
	}
	if !statusAktif || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil {
		return "", apperror.New(apperror.Unauthorized, "username atau password salah")
	}
	return adminUserID, nil
}

// EnsureBootstrapAdmin creates the given admin account if (and only if) admin_users is
// currently empty — run once at startup from BOOTSTRAP_ADMIN, never overwrites an
// existing account (so it's safe to leave that env var set across restarts).
func EnsureBootstrapAdmin(ctx context.Context, db *sql.DB, username, password string) error {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&count); err != nil {
		return fmt.Errorf("admin: count admin_users: %w", err)
	}
	if count > 0 {
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("admin: hash bootstrap password: %w", err)
	}
	id, err := idgen.Generate("ADM")
	if err != nil {
		return fmt.Errorf("admin: generate id: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_users (admin_user_id, username, password_hash) VALUES ($1, $2, $3)`,
		id, username, string(hash)); err != nil {
		return fmt.Errorf("admin: insert bootstrap admin: %w", err)
	}
	return nil
}

func (s *Service) ListProducts(ctx context.Context) ([]model.Product, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT product_id, product_code, nama, keterangan, status_aktif FROM products ORDER BY nama`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var products []model.Product
	for rows.Next() {
		var p model.Product
		var keterangan sql.NullString
		if err := rows.Scan(&p.ProductID, &p.ProductCode, &p.Nama, &keterangan, &p.StatusAktif); err != nil {
			return nil, err
		}
		if keterangan.Valid {
			p.Keterangan = &keterangan.String
		}
		products = append(products, p)
	}
	return products, rows.Err()
}

func (s *Service) CreateProduct(ctx context.Context, code, nama string) (model.Product, *apperror.Error) {
	id, err := idgen.Generate("PRD")
	if err != nil {
		return model.Product{}, apperror.New(apperror.Internal, "gagal membuat product_id")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO products (product_id, product_code, nama) VALUES ($1, $2, $3)`, id, code, nama); err != nil {
		return model.Product{}, apperror.New(apperror.Validation, "gagal membuat produk — pastikan product_code belum dipakai")
	}
	return model.Product{ProductID: id, ProductCode: code, Nama: nama, StatusAktif: true}, nil
}

// RecordPurchase finds-or-creates the (email, productCode) license_customers row, adds
// seats to its cumulative max_activations, bumps purchase_count, and inserts an audit
// row in purchases. All in one transaction so a crash never leaves purchase_count and
// max_activations out of sync with the purchases table.
func (s *Service) RecordPurchase(ctx context.Context, email, productCode string, seats int, catatan *string, recordedBy string) *apperror.Error {
	if seats <= 0 {
		return apperror.New(apperror.Validation, "jumlah seat harus lebih dari 0")
	}

	var productID string
	if err := s.db.QueryRowContext(ctx, `SELECT product_id FROM products WHERE product_code = $1 AND status_aktif`, productCode).Scan(&productID); err != nil {
		if err == sql.ErrNoRows {
			return apperror.New(apperror.NotFound, "product_code tidak ditemukan atau nonaktif")
		}
		return apperror.New(apperror.Internal, "gagal memeriksa produk")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal memulai transaksi")
	}
	defer tx.Rollback() //nolint:errcheck

	var licenseCustomerID string
	lookupErr := tx.QueryRowContext(ctx, `
		SELECT license_customer_id FROM license_customers WHERE email = $1 AND product_id = $2 FOR UPDATE`,
		email, productID).Scan(&licenseCustomerID)
	if lookupErr != nil && lookupErr != sql.ErrNoRows {
		return apperror.New(apperror.Internal, "gagal memeriksa data customer")
	}

	if lookupErr == sql.ErrNoRows {
		licenseCustomerID, err = idgen.Generate("LCU")
		if err != nil {
			return apperror.New(apperror.Internal, "gagal membuat license_customer_id")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO license_customers (license_customer_id, email, product_id, purchase_count, max_activations)
			VALUES ($1, $2, $3, 1, $4)`,
			licenseCustomerID, email, productID, seats); err != nil {
			return apperror.New(apperror.Internal, "gagal membuat data customer")
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE license_customers SET purchase_count = purchase_count + 1, max_activations = max_activations + $1, update_at = $2
			WHERE license_customer_id = $3`,
			seats, time.Now().UTC(), licenseCustomerID); err != nil {
			return apperror.New(apperror.Internal, "gagal memperbarui kuota customer")
		}
	}

	purchaseID, err := idgen.Generate("PUR")
	if err != nil {
		return apperror.New(apperror.Internal, "gagal membuat purchase_id")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO purchases (purchase_id, license_customer_id, seats, catatan, recorded_by) VALUES ($1, $2, $3, $4, $5)`,
		purchaseID, licenseCustomerID, seats, catatan, recordedBy); err != nil {
		return apperror.New(apperror.Internal, "gagal mencatat pembelian")
	}

	if err := tx.Commit(); err != nil {
		return apperror.New(apperror.Internal, "gagal menyimpan pembelian")
	}
	return nil
}

// ListCustomers returns license_customers rows (with an active-activation count
// computed alongside) optionally filtered by an email substring — the admin panel's
// main search view.
const licenseCustomerSelect = `
	SELECT lc.license_customer_id, lc.email, lc.product_id, p.product_code, lc.purchase_count, lc.max_activations,
		(SELECT COUNT(*) FROM activations a WHERE a.license_customer_id = lc.license_customer_id AND a.status = 'ACTIVE'),
		lc.catatan, lc.create_at
	FROM license_customers lc
	JOIN products p ON p.product_id = lc.product_id`

func scanLicenseCustomer(row interface{ Scan(dest ...any) error }) (model.LicenseCustomer, error) {
	var c model.LicenseCustomer
	var catatan sql.NullString
	if err := row.Scan(&c.LicenseCustomerID, &c.Email, &c.ProductID, &c.ProductCode, &c.PurchaseCount, &c.MaxActivations,
		&c.ActiveCount, &catatan, &c.CreateAt); err != nil {
		return model.LicenseCustomer{}, err
	}
	if catatan.Valid {
		c.Catatan = &catatan.String
	}
	return c, nil
}

func (s *Service) ListCustomers(ctx context.Context, emailSearch string) ([]model.LicenseCustomer, error) {
	rows, err := s.db.QueryContext(ctx, licenseCustomerSelect+`
		WHERE $1 = '' OR lc.email ILIKE '%' || $1 || '%'
		ORDER BY lc.create_at DESC`, emailSearch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var customers []model.LicenseCustomer
	for rows.Next() {
		c, err := scanLicenseCustomer(rows)
		if err != nil {
			return nil, err
		}
		customers = append(customers, c)
	}
	return customers, rows.Err()
}

func (s *Service) GetCustomer(ctx context.Context, licenseCustomerID string) (model.LicenseCustomer, *apperror.Error) {
	c, err := scanLicenseCustomer(s.db.QueryRowContext(ctx, licenseCustomerSelect+` WHERE lc.license_customer_id = $1`, licenseCustomerID))
	if err == sql.ErrNoRows {
		return model.LicenseCustomer{}, apperror.New(apperror.NotFound, "customer tidak ditemukan")
	}
	if err != nil {
		return model.LicenseCustomer{}, apperror.New(apperror.Internal, "gagal mengambil data customer")
	}
	return c, nil
}

func (s *Service) ListActivations(ctx context.Context, licenseCustomerID string) ([]model.Activation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT activation_id, license_customer_id, machine_fingerprint, machine_label, status, activated_at, last_seen_at, deactivated_at, expires_at
		FROM activations WHERE license_customer_id = $1 ORDER BY activated_at DESC`, licenseCustomerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var activations []model.Activation
	for rows.Next() {
		var a model.Activation
		var machineLabel sql.NullString
		var deactivatedAt sql.NullTime
		if err := rows.Scan(&a.ActivationID, &a.LicenseCustomerID, &a.MachineFingerprint, &machineLabel, &a.Status,
			&a.ActivatedAt, &a.LastSeenAt, &deactivatedAt, &a.ExpiresAt); err != nil {
			return nil, err
		}
		if machineLabel.Valid {
			a.MachineLabel = &machineLabel.String
		}
		if deactivatedAt.Valid {
			a.DeactivatedAt = &deactivatedAt.Time
		}
		activations = append(activations, a)
	}
	return activations, rows.Err()
}

func (s *Service) ListPurchases(ctx context.Context, licenseCustomerID string) ([]model.Purchase, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT purchase_id, license_customer_id, seats, catatan, recorded_by, create_at
		FROM purchases WHERE license_customer_id = $1 ORDER BY create_at DESC`, licenseCustomerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var purchases []model.Purchase
	for rows.Next() {
		var p model.Purchase
		var catatan sql.NullString
		if err := rows.Scan(&p.PurchaseID, &p.LicenseCustomerID, &p.Seats, &catatan, &p.RecordedBy, &p.CreateAt); err != nil {
			return nil, err
		}
		if catatan.Valid {
			p.Catatan = &catatan.String
		}
		purchases = append(purchases, p)
	}
	return purchases, rows.Err()
}

// ForceDeactivate is the admin-panel emergency path — used when a customer's machine
// is gone (crashed, stolen, wiped) before they could self-service deactivate through
// the product itself. Unlike license.Service.Deactivate, this trusts the admin's
// identity (an authenticated session) instead of requiring a signed license.lic, since
// the whole point is to handle the case where that license.lic is unrecoverable.
func (s *Service) ForceDeactivate(ctx context.Context, activationID, adminUserID string) *apperror.Error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE activations SET status = $1, deactivated_at = $2, deactivated_by = $3, update_at = $2
		WHERE activation_id = $4 AND status = $5`,
		model.ActivationStatusDeactivated, time.Now().UTC(), adminUserID, activationID, model.ActivationStatusActive)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal melepas aktivasi")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.New(apperror.NotFound, "aktivasi tidak ditemukan atau sudah dilepas")
	}
	return nil
}
