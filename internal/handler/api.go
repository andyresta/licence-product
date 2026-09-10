// Package handler wires internal/service onto HTTP — the public activation API and
// the admin panel.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/andyresta/licence-product/internal/apperror"
	"github.com/andyresta/licence-product/internal/service/license"
)

type APIHandler struct {
	license *license.Service
}

func NewAPIHandler(license *license.Service) *APIHandler {
	return &APIHandler{license: license}
}

type envelope struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body) //nolint:errcheck // response already committed; nothing to do if the client disconnected
}

func writeAppError(w http.ResponseWriter, err *apperror.Error) {
	writeJSON(w, err.HTTPStatus(), envelope{Success: false, Code: string(err.Code), Message: err.Message})
}

type activateRequest struct {
	Email              string  `json:"email"`
	ProductCode        string  `json:"product_code"`
	MachineFingerprint string  `json:"machine_fingerprint"`
	MachineLabel       *string `json:"machine_label"`
}

// Activate handles POST /api/v1/activate. See README.md for the full request/response
// contract every consuming product builds against.
func (h *APIHandler) Activate(w http.ResponseWriter, r *http.Request) {
	var req activateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAppError(w, apperror.New(apperror.Validation, "body permintaan tidak valid"))
		return
	}
	if req.Email == "" || req.ProductCode == "" || req.MachineFingerprint == "" {
		writeAppError(w, apperror.New(apperror.Validation, "email, product_code, dan machine_fingerprint wajib diisi"))
		return
	}

	result, appErr := h.license.Activate(r.Context(), license.ActivateInput{
		Email:              req.Email,
		ProductCode:        req.ProductCode,
		MachineFingerprint: req.MachineFingerprint,
		MachineLabel:       req.MachineLabel,
	})
	if appErr != nil {
		writeAppError(w, appErr)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Success: true, Data: map[string]any{
		"license_lic":   result.LicenseLic,
		"activation_id": result.ActivationID,
		"reused":        result.Reused,
	}})
}

type deactivateRequest struct {
	LicenseLic string `json:"license_lic"`
}

// Deactivate handles POST /api/v1/deactivate. The caller proves ownership of the
// activation being freed by presenting its own currently-signed license.lic content —
// see license.Service.Deactivate's doc comment for why.
func (h *APIHandler) Deactivate(w http.ResponseWriter, r *http.Request) {
	var req deactivateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAppError(w, apperror.New(apperror.Validation, "body permintaan tidak valid"))
		return
	}
	if req.LicenseLic == "" {
		writeAppError(w, apperror.New(apperror.Validation, "license_lic wajib diisi"))
		return
	}
	if appErr := h.license.Deactivate(r.Context(), req.LicenseLic); appErr != nil {
		writeAppError(w, appErr)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Success: true})
}
