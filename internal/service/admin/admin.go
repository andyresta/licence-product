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

// UpdateProduct changes a product's display name/description — not its product_code,
// which every consuming product's build already has baked in as a literal constant (see
// README's integration checklist), so changing it out from under them would silently
// break activation for anyone already shipped.
func (s *Service) UpdateProduct(ctx context.Context, productID, nama, keterangan string) *apperror.Error {
	var ket any
	if keterangan != "" {
		ket = keterangan
	}
	res, err := s.db.ExecContext(ctx, `UPDATE products SET nama = $1, keterangan = $2, update_at = $3 WHERE product_id = $4`,
		nama, ket, time.Now().UTC(), productID)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal memperbarui produk")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.New(apperror.NotFound, "produk tidak ditemukan")
	}
	return nil
}

// SetProductStatus activates/deactivates a product — the soft-delete path for a product
// that already has customers (RecordPurchase already refuses new purchases against an
// inactive product; see licenseCustomerSelect's WHERE p.status_aktif for activation).
func (s *Service) SetProductStatus(ctx context.Context, productID string, aktif bool) *apperror.Error {
	res, err := s.db.ExecContext(ctx, `UPDATE products SET status_aktif = $1, update_at = $2 WHERE product_id = $3`,
		aktif, time.Now().UTC(), productID)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal mengubah status produk")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.New(apperror.NotFound, "produk tidak ditemukan")
	}
	return nil
}

// DeleteProduct removes a product row outright — only when it has never had a customer
// recorded against it (license_customers.product_id references it). A product that's
// already sold should be deactivated with SetProductStatus instead; deleting it would
// orphan real customer history.
func (s *Service) DeleteProduct(ctx context.Context, productID string) *apperror.Error {
	var customerCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM license_customers WHERE product_id = $1`, productID).Scan(&customerCount); err != nil {
		return apperror.New(apperror.Internal, "gagal memeriksa data produk")
	}
	if customerCount > 0 {
		return apperror.New(apperror.Validation, "produk sudah punya customer — nonaktifkan saja, tidak bisa dihapus")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM products WHERE product_id = $1`, productID)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal menghapus produk")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.New(apperror.NotFound, "produk tidak ditemukan")
	}
	return nil
}

// RecordPurchaseInput is RecordPurchase's parameter struct. LicenseType/SubscriptionMonths
// only take effect the first time a (email, product) license is created — a repeat
// purchase against an existing license just adds seats; to change an existing license's
// type/expiry, use ExtendSubscription instead.
type RecordPurchaseInput struct {
	Email              string
	ProductCode        string
	Seats              int
	Catatan            *string
	RecordedBy         string
	LicenseType        string // model.LicenseTypeLifetime (default) or model.LicenseTypeSubscription
	SubscriptionMonths int    // only used when LicenseType == SUBSCRIPTION and the license is new
}

// RecordPurchase finds-or-creates the Customer (by email), then finds-or-creates the
// (customer, product) license_customers row, adds seats to its cumulative
// max_activations, bumps purchase_count, and inserts an audit row in purchases. All in
// one transaction so a crash never leaves purchase_count and max_activations out of
// sync with the purchases table.
func (s *Service) RecordPurchase(ctx context.Context, in RecordPurchaseInput) *apperror.Error {
	if in.Seats <= 0 {
		return apperror.New(apperror.Validation, "jumlah seat harus lebih dari 0")
	}
	licenseType := in.LicenseType
	if licenseType == "" {
		licenseType = model.LicenseTypeLifetime
	}
	if licenseType != model.LicenseTypeLifetime && licenseType != model.LicenseTypeSubscription {
		return apperror.New(apperror.Validation, "jenis lisensi tidak dikenal")
	}
	if licenseType == model.LicenseTypeSubscription && in.SubscriptionMonths <= 0 {
		return apperror.New(apperror.Validation, "jumlah bulan langganan harus lebih dari 0")
	}

	var productID string
	if err := s.db.QueryRowContext(ctx, `SELECT product_id FROM products WHERE product_code = $1 AND status_aktif`, in.ProductCode).Scan(&productID); err != nil {
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

	customerID, aerr := findOrCreateCustomer(ctx, tx, in.Email)
	if aerr != nil {
		return aerr
	}

	var licenseCustomerID string
	lookupErr := tx.QueryRowContext(ctx, `
		SELECT license_customer_id FROM license_customers WHERE email = $1 AND product_id = $2 FOR UPDATE`,
		in.Email, productID).Scan(&licenseCustomerID)
	if lookupErr != nil && lookupErr != sql.ErrNoRows {
		return apperror.New(apperror.Internal, "gagal memeriksa data lisensi")
	}

	if lookupErr == sql.ErrNoRows {
		licenseCustomerID, err = idgen.Generate("LCU")
		if err != nil {
			return apperror.New(apperror.Internal, "gagal membuat license_customer_id")
		}
		var subscriptionExpiresAt any
		if licenseType == model.LicenseTypeSubscription {
			subscriptionExpiresAt = time.Now().UTC().AddDate(0, in.SubscriptionMonths, 0)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO license_customers (license_customer_id, customer_id, email, product_id, purchase_count, max_activations, license_type, subscription_expires_at)
			VALUES ($1, $2, $3, $4, 1, $5, $6, $7)`,
			licenseCustomerID, customerID, in.Email, productID, in.Seats, licenseType, subscriptionExpiresAt); err != nil {
			return apperror.New(apperror.Internal, "gagal membuat data lisensi")
		}
		if licenseType == model.LicenseTypeSubscription {
			extensionID, err := idgen.Generate("SUB")
			if err != nil {
				return apperror.New(apperror.Internal, "gagal membuat extension_id")
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO subscription_extensions (extension_id, license_customer_id, months, new_expires_at, catatan, recorded_by)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				extensionID, licenseCustomerID, in.SubscriptionMonths, subscriptionExpiresAt, in.Catatan, in.RecordedBy); err != nil {
				return apperror.New(apperror.Internal, "gagal mencatat masa langganan awal")
			}
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE license_customers SET purchase_count = purchase_count + 1, max_activations = max_activations + $1, update_at = $2
			WHERE license_customer_id = $3`,
			in.Seats, time.Now().UTC(), licenseCustomerID); err != nil {
			return apperror.New(apperror.Internal, "gagal memperbarui kuota lisensi")
		}
	}

	purchaseID, err := idgen.Generate("PUR")
	if err != nil {
		return apperror.New(apperror.Internal, "gagal membuat purchase_id")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO purchases (purchase_id, license_customer_id, seats, catatan, recorded_by) VALUES ($1, $2, $3, $4, $5)`,
		purchaseID, licenseCustomerID, in.Seats, in.Catatan, in.RecordedBy); err != nil {
		return apperror.New(apperror.Internal, "gagal mencatat pembelian")
	}

	if err := tx.Commit(); err != nil {
		return apperror.New(apperror.Internal, "gagal menyimpan pembelian")
	}
	return nil
}

// findOrCreateCustomer looks up a customers row by email within tx, creating one if it
// doesn't exist yet. Used by RecordPurchase so every license_customers row always has a
// valid customer_id, and by CreateCustomer's own uniqueness check.
func findOrCreateCustomer(ctx context.Context, tx *sql.Tx, email string) (string, *apperror.Error) {
	var customerID string
	err := tx.QueryRowContext(ctx, `SELECT customer_id FROM customers WHERE email = $1 FOR UPDATE`, email).Scan(&customerID)
	if err == nil {
		return customerID, nil
	}
	if err != sql.ErrNoRows {
		return "", apperror.New(apperror.Internal, "gagal memeriksa data customer")
	}
	customerID, genErr := idgen.Generate("CUS")
	if genErr != nil {
		return "", apperror.New(apperror.Internal, "gagal membuat customer_id")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO customers (customer_id, email) VALUES ($1, $2)`, customerID, email); err != nil {
		return "", apperror.New(apperror.Internal, "gagal membuat data customer")
	}
	return customerID, nil
}

// ListLicenses returns license_customers rows (with an active-activation count
// computed alongside) optionally filtered by an email substring — the admin panel's
// main license search view. Despite the SQL alias "lc" (kept from the underlying table
// name), each row is a License, not a Customer — see Customer/ListCustomerDirectory for
// the person/org side of things.
const licenseCustomerSelect = `
	SELECT lc.license_customer_id, lc.customer_id, lc.email, cu.nama, lc.product_id, p.product_code, p.nama,
		lc.purchase_count, lc.max_activations,
		(SELECT COUNT(*) FROM activations a WHERE a.license_customer_id = lc.license_customer_id AND a.status = 'ACTIVE'),
		lc.catatan, lc.create_at, lc.max_branches,
		(SELECT COUNT(*) FROM branches b WHERE b.license_customer_id = lc.license_customer_id AND b.status = 'ACTIVE'),
		lc.license_type, lc.subscription_expires_at
	FROM license_customers lc
	JOIN products p ON p.product_id = lc.product_id
	JOIN customers cu ON cu.customer_id = lc.customer_id`

func scanLicenseCustomer(row interface{ Scan(dest ...any) error }) (model.LicenseCustomer, error) {
	var c model.LicenseCustomer
	var customerNama sql.NullString
	var catatan sql.NullString
	var subscriptionExpiresAt sql.NullTime
	if err := row.Scan(&c.LicenseCustomerID, &c.CustomerID, &c.Email, &customerNama, &c.ProductID, &c.ProductCode, &c.ProductNama,
		&c.PurchaseCount, &c.MaxActivations, &c.ActiveCount, &catatan, &c.CreateAt, &c.MaxBranches, &c.ActiveBranchCount,
		&c.LicenseType, &subscriptionExpiresAt); err != nil {
		return model.LicenseCustomer{}, err
	}
	if customerNama.Valid {
		c.CustomerNama = &customerNama.String
	}
	if catatan.Valid {
		c.Catatan = &catatan.String
	}
	if subscriptionExpiresAt.Valid {
		c.SubscriptionExpiresAt = &subscriptionExpiresAt.Time
	}
	return c, nil
}

func (s *Service) ListLicenses(ctx context.Context, emailSearch string) ([]model.LicenseCustomer, error) {
	rows, err := s.db.QueryContext(ctx, licenseCustomerSelect+`
		WHERE $1 = '' OR lc.email ILIKE '%' || $1 || '%'
		ORDER BY lc.create_at DESC`, emailSearch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var licenses []model.LicenseCustomer
	for rows.Next() {
		c, err := scanLicenseCustomer(rows)
		if err != nil {
			return nil, err
		}
		licenses = append(licenses, c)
	}
	return licenses, rows.Err()
}

func (s *Service) GetLicense(ctx context.Context, licenseCustomerID string) (model.LicenseCustomer, *apperror.Error) {
	c, err := scanLicenseCustomer(s.db.QueryRowContext(ctx, licenseCustomerSelect+` WHERE lc.license_customer_id = $1`, licenseCustomerID))
	if err == sql.ErrNoRows {
		return model.LicenseCustomer{}, apperror.New(apperror.NotFound, "lisensi tidak ditemukan")
	}
	if err != nil {
		return model.LicenseCustomer{}, apperror.New(apperror.Internal, "gagal mengambil data lisensi")
	}
	return c, nil
}

// DeleteLicense permanently removes one license_customers row plus everything scoped to
// it alone (its activations, branches, purchases, subscription_extensions) — none of
// those child tables carry ON DELETE CASCADE, so this deletes them explicitly, in one
// transaction, before the parent row. Unlike DeleteCustomer (which refuses while any
// license still exists), this is a genuine hard delete: it's for clearing out a license
// and its own history entirely (e.g. test/mistaken data), not something with a safer
// soft-delete alternative like a product's SetProductStatus.
func (s *Service) DeleteLicense(ctx context.Context, licenseCustomerID string) *apperror.Error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal memulai transaksi")
	}
	defer tx.Rollback() //nolint:errcheck

	for _, table := range []string{"subscription_extensions", "branches", "activations", "purchases"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE license_customer_id = $1", licenseCustomerID); err != nil {
			return apperror.New(apperror.Internal, "gagal menghapus data terkait lisensi")
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM license_customers WHERE license_customer_id = $1`, licenseCustomerID)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal menghapus lisensi")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.New(apperror.NotFound, "lisensi tidak ditemukan")
	}
	if err := tx.Commit(); err != nil {
		return apperror.New(apperror.Internal, "gagal menyimpan penghapusan lisensi")
	}
	return nil
}

func (s *Service) ListLicensesByCustomer(ctx context.Context, customerID string) ([]model.LicenseCustomer, error) {
	rows, err := s.db.QueryContext(ctx, licenseCustomerSelect+`
		WHERE lc.customer_id = $1 ORDER BY lc.create_at DESC`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var licenses []model.LicenseCustomer
	for rows.Next() {
		c, err := scanLicenseCustomer(rows)
		if err != nil {
			return nil, err
		}
		licenses = append(licenses, c)
	}
	return licenses, rows.Err()
}

func scanCustomer(row interface{ Scan(dest ...any) error }) (model.Customer, error) {
	var c model.Customer
	var nama, telp, catatan sql.NullString
	if err := row.Scan(&c.CustomerID, &c.Email, &nama, &telp, &catatan, &c.CreateAt, &c.UpdateAt); err != nil {
		return model.Customer{}, err
	}
	if nama.Valid {
		c.Nama = &nama.String
	}
	if telp.Valid {
		c.Telp = &telp.String
	}
	if catatan.Valid {
		c.Catatan = &catatan.String
	}
	return c, nil
}

const customerSelect = `SELECT customer_id, email, nama, telp, catatan, create_at, update_at FROM customers`

// ListCustomerDirectory is the admin panel's Customer list — every person/org on file,
// independent of which (or how many) products they've licensed.
func (s *Service) ListCustomerDirectory(ctx context.Context, search string) ([]model.Customer, error) {
	rows, err := s.db.QueryContext(ctx, customerSelect+`
		WHERE $1 = '' OR email ILIKE '%' || $1 || '%' OR nama ILIKE '%' || $1 || '%'
		ORDER BY create_at DESC`, search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var customers []model.Customer
	for rows.Next() {
		c, err := scanCustomer(rows)
		if err != nil {
			return nil, err
		}
		customers = append(customers, c)
	}
	return customers, rows.Err()
}

func (s *Service) GetCustomerProfile(ctx context.Context, customerID string) (model.Customer, *apperror.Error) {
	c, err := scanCustomer(s.db.QueryRowContext(ctx, customerSelect+` WHERE customer_id = $1`, customerID))
	if err == sql.ErrNoRows {
		return model.Customer{}, apperror.New(apperror.NotFound, "customer tidak ditemukan")
	}
	if err != nil {
		return model.Customer{}, apperror.New(apperror.Internal, "gagal mengambil data customer")
	}
	return c, nil
}

// CreateCustomer adds a customer directly from the admin panel (no purchase yet) — the
// email just needs to be unique; RecordPurchase's own findOrCreateCustomer will pick
// this row up later once a license is recorded against the same email.
func (s *Service) CreateCustomer(ctx context.Context, email, nama, telp, catatan string) (model.Customer, *apperror.Error) {
	id, err := idgen.Generate("CUS")
	if err != nil {
		return model.Customer{}, apperror.New(apperror.Internal, "gagal membuat customer_id")
	}
	var namaVal, telpVal, catatanVal any
	if nama != "" {
		namaVal = nama
	}
	if telp != "" {
		telpVal = telp
	}
	if catatan != "" {
		catatanVal = catatan
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO customers (customer_id, email, nama, telp, catatan) VALUES ($1, $2, $3, $4, $5)`,
		id, email, namaVal, telpVal, catatanVal); err != nil {
		return model.Customer{}, apperror.New(apperror.Validation, "gagal membuat customer — pastikan email belum dipakai")
	}
	return s.GetCustomerProfile(ctx, id)
}

// UpdateCustomerProfile edits a customer's contact info. It deliberately does not touch
// email — every license_customers row still keys its own activation lookups off
// lc.email directly (see internal/service/license.Activate), so changing it here would
// silently orphan that customer's existing licenses from their real email address.
func (s *Service) UpdateCustomerProfile(ctx context.Context, customerID, nama, telp, catatan string) *apperror.Error {
	var namaVal, telpVal, catatanVal any
	if nama != "" {
		namaVal = nama
	}
	if telp != "" {
		telpVal = telp
	}
	if catatan != "" {
		catatanVal = catatan
	}
	res, err := s.db.ExecContext(ctx, `UPDATE customers SET nama = $1, telp = $2, catatan = $3, update_at = $4 WHERE customer_id = $5`,
		namaVal, telpVal, catatanVal, time.Now().UTC(), customerID)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal memperbarui data customer")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.New(apperror.NotFound, "customer tidak ditemukan")
	}
	return nil
}

// DeleteCustomer removes a customer outright — only when they have no license rows left
// (mirrors DeleteProduct's own refuse-while-referenced rule). A customer with existing
// licenses should have those deleted first via DeleteLicense (which also clears that
// license's own activations/branches/purchases history) rather than being cascaded away
// silently here.
func (s *Service) DeleteCustomer(ctx context.Context, customerID string) *apperror.Error {
	var licenseCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM license_customers WHERE customer_id = $1`, customerID).Scan(&licenseCount); err != nil {
		return apperror.New(apperror.Internal, "gagal memeriksa data customer")
	}
	if licenseCount > 0 {
		return apperror.New(apperror.Validation, "customer masih punya lisensi — hapus lisensinya dulu sebelum menghapus customer")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM customers WHERE customer_id = $1`, customerID)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal menghapus customer")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.New(apperror.NotFound, "customer tidak ditemukan")
	}
	return nil
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

func (s *Service) ListBranches(ctx context.Context, licenseCustomerID string) ([]model.Branch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT branch_id, license_customer_id, branch_code, branch_label, status, registered_at, deactivated_at
		FROM branches WHERE license_customer_id = $1 ORDER BY registered_at DESC`, licenseCustomerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var branches []model.Branch
	for rows.Next() {
		var b model.Branch
		var label sql.NullString
		var deactivatedAt sql.NullTime
		if err := rows.Scan(&b.BranchID, &b.LicenseCustomerID, &b.BranchCode, &label, &b.Status, &b.RegisteredAt, &deactivatedAt); err != nil {
			return nil, err
		}
		if label.Valid {
			b.BranchLabel = &label.String
		}
		if deactivatedAt.Valid {
			b.DeactivatedAt = &deactivatedAt.Time
		}
		branches = append(branches, b)
	}
	return branches, rows.Err()
}

// SetMaxBranches directly overwrites a customer's branch quota. Unlike max_activations
// (a running total that only grows, purchase by purchase), max_branches is a plain
// adjustable setting — the vendor sets it once (default 5, from the schema) and changes
// it directly as needed, no audit trail required.
func (s *Service) SetMaxBranches(ctx context.Context, licenseCustomerID string, maxBranches int) *apperror.Error {
	if maxBranches < 0 {
		return apperror.New(apperror.Validation, "kuota branch tidak boleh negatif")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE license_customers SET max_branches = $1, update_at = $2 WHERE license_customer_id = $3`,
		maxBranches, time.Now().UTC(), licenseCustomerID)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal memperbarui kuota branch")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.New(apperror.NotFound, "customer tidak ditemukan")
	}
	return nil
}

// ForceDeactivateBranch is the admin-panel counterpart to ForceDeactivate (activations)
// — frees a branch slot without a signed license.lic, for when the vendor needs to
// correct a customer's branch list directly (e.g. a branch closed down).
func (s *Service) ForceDeactivateBranch(ctx context.Context, branchID, adminUserID string) *apperror.Error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE branches SET status = $1, deactivated_at = $2, deactivated_by = $3, update_at = $2
		WHERE branch_id = $4 AND status = $5`,
		model.BranchStatusDeactivated, time.Now().UTC(), adminUserID, branchID, model.BranchStatusActive)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal melepas branch")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperror.New(apperror.NotFound, "branch tidak ditemukan atau sudah dilepas")
	}
	return nil
}

// ExtendSubscription adds months to a customer's subscription, extending from
// MAX(now, current subscription_expires_at) rather than from "now" — renewing a few
// days early (or from a NULL/lifetime row, which starts counting from now) never costs
// the customer anything. Every extension is recorded in subscription_extensions, the
// audit trail behind the parent row's denormalized field (same relationship purchases
// has to max_activations).
func (s *Service) ExtendSubscription(ctx context.Context, licenseCustomerID string, months int, catatan *string, recordedBy string) *apperror.Error {
	if months <= 0 {
		return apperror.New(apperror.Validation, "jumlah bulan harus lebih dari 0")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apperror.New(apperror.Internal, "gagal memulai transaksi")
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	var currentExpiresAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT subscription_expires_at FROM license_customers WHERE license_customer_id = $1 FOR UPDATE`,
		licenseCustomerID).Scan(&currentExpiresAt); err != nil {
		if err == sql.ErrNoRows {
			return apperror.New(apperror.NotFound, "customer tidak ditemukan")
		}
		return apperror.New(apperror.Internal, "gagal memeriksa data langganan")
	}

	now := time.Now().UTC()
	base := now
	if currentExpiresAt.Valid && currentExpiresAt.Time.After(now) {
		base = currentExpiresAt.Time
	}
	newExpiresAt := base.AddDate(0, months, 0)

	if _, err := tx.ExecContext(ctx, `
		UPDATE license_customers SET subscription_expires_at = $1, license_type = $2, update_at = $3 WHERE license_customer_id = $4`,
		newExpiresAt, model.LicenseTypeSubscription, now, licenseCustomerID); err != nil {
		return apperror.New(apperror.Internal, "gagal memperbarui masa langganan")
	}

	extensionID, err := idgen.Generate("SUB")
	if err != nil {
		return apperror.New(apperror.Internal, "gagal membuat extension_id")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO subscription_extensions (extension_id, license_customer_id, months, new_expires_at, catatan, recorded_by)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		extensionID, licenseCustomerID, months, newExpiresAt, catatan, recordedBy); err != nil {
		return apperror.New(apperror.Internal, "gagal mencatat perpanjangan langganan")
	}

	if err := tx.Commit(); err != nil {
		return apperror.New(apperror.Internal, "gagal menyimpan perpanjangan langganan")
	}
	return nil
}

func (s *Service) ListSubscriptionExtensions(ctx context.Context, licenseCustomerID string) ([]model.SubscriptionExtension, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT extension_id, license_customer_id, months, new_expires_at, catatan, recorded_by, create_at
		FROM subscription_extensions WHERE license_customer_id = $1 ORDER BY create_at DESC`, licenseCustomerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var extensions []model.SubscriptionExtension
	for rows.Next() {
		var e model.SubscriptionExtension
		var catatan sql.NullString
		if err := rows.Scan(&e.ExtensionID, &e.LicenseCustomerID, &e.Months, &e.NewExpiresAt, &catatan, &e.RecordedBy, &e.CreateAt); err != nil {
			return nil, err
		}
		if catatan.Valid {
			e.Catatan = &catatan.String
		}
		extensions = append(extensions, e)
	}
	return extensions, rows.Err()
}
