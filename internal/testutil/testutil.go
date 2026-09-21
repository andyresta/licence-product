// Package testutil provides a shared Postgres test-database helper for every
// package's tests — a plain (non-_test.go) file so it can be imported across package
// boundaries (internal/service/license, internal/service/admin, internal/handler, ...).
package testutil

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/andyresta/licence-product/internal/repository"
)

// OpenTestDB connects to a real local Postgres test database (see README's "Running
// tests" section for setup), applies every migration, then truncates every
// non-migration table so each test starts from a clean slate — cheaper than a fresh
// database per test and Postgres has no SQLite-style throwaway-tempfile equivalent.
func OpenTestDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("LICENSE_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://licence_test:licence_test@localhost:5432/licence_test?sslmode=disable"
	}
	db, err := repository.Open(url)
	if err != nil {
		t.Fatalf("testutil: open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "migrations")
	if err := repository.Migrate(db, dir); err != nil {
		t.Fatalf("testutil: migrate: %v", err)
	}

	// TRUNCATE ... CASCADE and reset in FK-safe order isn't needed with CASCADE, but
	// listed explicitly (rather than a dynamic catalog query) so a newly added table
	// is a deliberate one-line addition here, not a silent gap in test isolation.
	for _, table := range []string{"subscription_extensions", "branches", "activations", "purchases", "license_customers", "customers", "products", "admin_users"} {
		if _, err := db.Exec("TRUNCATE TABLE " + table + " CASCADE"); err != nil {
			t.Fatalf("testutil: truncate %s: %v", table, err)
		}
	}
	return db
}
