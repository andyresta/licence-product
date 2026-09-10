// licenseserver is the entrypoint: loads config, connects to Postgres, applies
// migrations, bootstraps the first admin account if needed, and serves the public
// activation API plus the admin panel.
package main

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/andyresta/licence-product/internal/config"
	"github.com/andyresta/licence-product/internal/crypto"
	"github.com/andyresta/licence-product/internal/handler"
	"github.com/andyresta/licence-product/internal/repository"
	"github.com/andyresta/licence-product/internal/service/admin"
	"github.com/andyresta/licence-product/internal/service/license"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := repository.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	if err := repository.Migrate(db, "migrations"); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	if cfg.BootstrapAdmin != "" {
		parts := strings.SplitN(cfg.BootstrapAdmin, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			log.Fatalf("config: BOOTSTRAP_ADMIN must be \"username:password\"")
		}
		if err := admin.EnsureBootstrapAdmin(context.Background(), db, parts[0], parts[1]); err != nil {
			log.Fatalf("bootstrap admin: %v", err)
		}
	}

	signer, err := crypto.NewSigner(cfg.SigningSeedHex)
	if err != nil {
		log.Fatalf("signer: %v", err)
	}
	log.Printf("license public key (embed this in every consuming product): %s", signer.PublicKeyHex())

	licenseSvc := license.New(db, signer)
	adminSvc := admin.New(db)

	apiHandler := handler.NewAPIHandler(licenseSvc)
	adminHandler := handler.NewAdminHandler(adminSvc, cfg.SessionSecret)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/activate", apiHandler.Activate)
	mux.HandleFunc("POST /api/v1/deactivate", apiHandler.Deactivate)

	mux.HandleFunc("GET /admin/login", adminHandler.LoginPage)
	mux.HandleFunc("POST /admin/login", adminHandler.LoginSubmit)
	mux.HandleFunc("POST /admin/logout", adminHandler.Logout)
	mux.HandleFunc("GET /admin", adminHandler.RequireAdmin(adminHandler.Dashboard))
	mux.HandleFunc("GET /admin/customers/{id}", adminHandler.RequireAdmin(adminHandler.CustomerDetail))
	mux.HandleFunc("POST /admin/products", adminHandler.RequireAdmin(adminHandler.CreateProduct))
	mux.HandleFunc("POST /admin/purchases", adminHandler.RequireAdmin(adminHandler.RecordPurchase))
	mux.HandleFunc("POST /admin/activations/{id}/deactivate", adminHandler.RequireAdmin(adminHandler.ForceDeactivate))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint:errcheck
	})

	addr := ":" + cfg.AppPort
	log.Printf("licenseserver listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}
