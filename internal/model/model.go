// Package model holds licence-product's domain structs.
package model

import "time"

type Product struct {
	ProductID   string  `json:"product_id"`
	ProductCode string  `json:"product_code"`
	Nama        string  `json:"nama"`
	Keterangan  *string `json:"keterangan,omitempty"`
	StatusAktif bool    `json:"status_aktif"`
}

// Customer is a person/organization identified by email, independent of any single
// product. One Customer can hold many License rows (LicenseCustomer), one per product
// they've purchased — this is what lets the admin panel show "everything Budi owns"
// in one place instead of scattering it across per-product rows.
type Customer struct {
	CustomerID string    `json:"customer_id"`
	Email      string    `json:"email"`
	Nama       *string   `json:"nama,omitempty"`
	Telp       *string   `json:"telp,omitempty"`
	Catatan    *string   `json:"catatan,omitempty"`
	CreateAt   time.Time `json:"create_at"`
	UpdateAt   time.Time `json:"update_at"`
}

const (
	LicenseTypeLifetime     = "LIFETIME"
	LicenseTypeSubscription = "SUBSCRIPTION"
)

// LicenseCustomer is one (customer, product) pair — the unit that purchase_count and
// max_activations are tracked against. A customer who buys the same product twice
// gets a second row in Purchases, not a second LicenseCustomer. Despite its name (kept
// for backward compatibility with existing DB columns/API shapes) this struct
// represents a License, not a Customer — see Customer above for the actual person/org.
type LicenseCustomer struct {
	LicenseCustomerID string    `json:"license_customer_id"`
	CustomerID        string    `json:"customer_id"`
	Email             string    `json:"email"`
	CustomerNama      *string   `json:"customer_nama,omitempty"` // joined in for display convenience
	ProductID         string    `json:"product_id"`
	ProductCode       string    `json:"product_code"` // joined in for display/API convenience
	ProductNama       string    `json:"product_nama,omitempty"`
	PurchaseCount     int       `json:"purchase_count"`
	MaxActivations    int       `json:"max_activations"`
	ActiveCount       int       `json:"active_count"` // computed: COUNT(activations WHERE status='ACTIVE')
	Catatan           *string   `json:"catatan,omitempty"`
	CreateAt          time.Time `json:"create_at"`
	MaxBranches       int       `json:"max_branches"`
	ActiveBranchCount int       `json:"active_branch_count"` // computed: COUNT(branches WHERE status='ACTIVE')
	// LicenseType is explicit and authoritative — LIFETIME or SUBSCRIPTION — set at
	// creation/extension time rather than inferred from SubscriptionExpiresAt nullity.
	LicenseType string `json:"license_type"`
	// SubscriptionExpiresAt is nil for a lifetime license (the default — every existing
	// customer stays nil, unaffected). When set, internal/service/license.Activate
	// embeds this date (plus its grace period) as license.lic's expires_at instead of
	// the ~74-year DefaultLicenseTerm.
	SubscriptionExpiresAt *time.Time `json:"subscription_expires_at,omitempty"`
}

type Purchase struct {
	PurchaseID        string    `json:"purchase_id"`
	LicenseCustomerID string    `json:"license_customer_id"`
	Seats             int       `json:"seats"`
	Catatan           *string   `json:"catatan,omitempty"`
	RecordedBy        string    `json:"recorded_by"`
	CreateAt          time.Time `json:"create_at"`
}

const (
	ActivationStatusActive      = "ACTIVE"
	ActivationStatusDeactivated = "DEACTIVATED"
)

// Activation is one machine's activation record for a LicenseCustomer. Only ACTIVE
// rows count against MaxActivations — see internal/service/license for the exact
// rules around when a new row is created vs. an existing one is reused/reinstated.
type Activation struct {
	ActivationID       string     `json:"activation_id"`
	LicenseCustomerID  string     `json:"license_customer_id"`
	MachineFingerprint string     `json:"machine_fingerprint"`
	MachineLabel       *string    `json:"machine_label,omitempty"`
	Status             string     `json:"status"`
	ActivatedAt        time.Time  `json:"activated_at"`
	LastSeenAt         time.Time  `json:"last_seen_at"`
	DeactivatedAt      *time.Time `json:"deactivated_at,omitempty"`
	ExpiresAt          time.Time  `json:"expires_at"`
}

const (
	BranchStatusActive      = "ACTIVE"
	BranchStatusDeactivated = "DEACTIVATED"
)

// Branch is one registered branch/outlet under a LicenseCustomer — an optional second
// quota axis alongside Activation (machine installs). Only products that choose to call
// the /api/v1/branches/* endpoints ever populate this table; every LicenseCustomer still
// carries a MaxBranches value regardless of whether the product uses it. See
// internal/service/branch for the register/reuse/reclaim state machine (mirrors
// Activation's exactly).
type Branch struct {
	BranchID          string     `json:"branch_id"`
	LicenseCustomerID string     `json:"license_customer_id"`
	BranchCode        string     `json:"branch_code"`
	BranchLabel       *string    `json:"branch_label,omitempty"`
	Status            string     `json:"status"`
	RegisteredAt      time.Time  `json:"registered_at"`
	DeactivatedAt     *time.Time `json:"deactivated_at,omitempty"`
}

// SubscriptionExtension is the audit trail behind LicenseCustomer.SubscriptionExpiresAt
// — same relationship Purchase has to MaxActivations: the parent row's field is a
// fast-lookup denormalization, this table is the record of why it is what it is.
type SubscriptionExtension struct {
	ExtensionID       string    `json:"extension_id"`
	LicenseCustomerID string    `json:"license_customer_id"`
	Months            int       `json:"months"`
	NewExpiresAt      time.Time `json:"new_expires_at"`
	Catatan           *string   `json:"catatan,omitempty"`
	RecordedBy        string    `json:"recorded_by"`
	CreateAt          time.Time `json:"create_at"`
}

type AdminUser struct {
	AdminUserID  string `json:"admin_user_id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	StatusAktif  bool   `json:"status_aktif"`
}
