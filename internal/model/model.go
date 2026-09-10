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

// LicenseCustomer is one (email, product) pair — the unit that purchase_count and
// max_activations are tracked against. A customer who buys the same product twice
// gets a second row in Purchases, not a second LicenseCustomer.
type LicenseCustomer struct {
	LicenseCustomerID string    `json:"license_customer_id"`
	Email             string    `json:"email"`
	ProductID         string    `json:"product_id"`
	ProductCode       string    `json:"product_code"` // joined in for display/API convenience
	PurchaseCount     int       `json:"purchase_count"`
	MaxActivations    int       `json:"max_activations"`
	ActiveCount       int       `json:"active_count"` // computed: COUNT(activations WHERE status='ACTIVE')
	Catatan           *string   `json:"catatan,omitempty"`
	CreateAt          time.Time `json:"create_at"`
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

type AdminUser struct {
	AdminUserID  string `json:"admin_user_id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	StatusAktif  bool   `json:"status_aktif"`
}
